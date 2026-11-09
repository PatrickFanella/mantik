#!/usr/bin/env python3
"""Forward only a rehearsal's model calls; inject outage without stopping a service."""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


REQUEST_LIMIT = 128 * 1024
RESPONSE_LIMIT = 64 * 1024


def private_origin(value):
    parsed = urlsplit(value)
    try:
        address = ipaddress.ip_address(parsed.hostname)
        port = parsed.port
    except (ValueError, TypeError):
        raise ValueError("upstream requires a private literal IP origin") from None
    if (parsed.scheme not in {"http", "https"} or not address.is_private
            or address.is_unspecified or address.is_multicast
            or parsed.username or parsed.password or parsed.path not in {"", "/"}
            or parsed.query or parsed.fragment or not port):
        raise ValueError("upstream requires a private literal IP origin with a port")
    return value.rstrip("/")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def create_proxy(upstream, root):
    """Bind loopback with a fixed upstream; never copy Mantik service credentials."""
    upstream = private_origin(upstream)
    root = Path(root)
    if not root.is_absolute():
        raise ValueError("state directory must be absolute")
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    event_lock = threading.Lock()
    opener = build_opener(ProxyHandler({}), NoRedirect())

    def event(**fields):
        data = (json.dumps({"at_unix_ns": time.time_ns(), **fields}) + "\n").encode()
        with event_lock:
            fd = os.open(root / "events.jsonl", os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
            with os.fdopen(fd, "wb") as output:
                output.write(data)
                output.flush()
                os.fsync(output.fileno())

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def reply(self, status, body):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/health":
                self.reply(200, b'{"ok":true}')
            else:
                self.reply(404, b'{"error":"rehearsal route unavailable"}')

        def do_POST(self):
            if self.path != "/v1/chat/completions":
                self.reply(404, b'{"error":"rehearsal route unavailable"}')
                return
            try:
                size = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                size = 0
            if size < 1 or size > REQUEST_LIMIT or self.headers.get("Transfer-Encoding"):
                self.reply(413, b'{"error":"bounded content length required"}')
                return
            self.connection.settimeout(65)
            body = self.rfile.read(size)
            if len(body) != size:
                self.reply(400, b'{"error":"incomplete request"}')
                return
            request_sha = hashlib.sha256(body).hexdigest()
            # Persist intent before any upstream effect. Only hashes and transport
            # metadata belong here; Mantik retains the original model artifacts.
            event(phase="intent", request_sha256=request_sha, request_bytes=size)
            if (root / "outage").exists():
                event(phase="result", request_sha256=request_sha, status=503, injected_outage=True)
                self.reply(503, b'{"error":"rehearsal model connection outage"}')
                return
            request = Request(upstream + self.path, data=body,
                              headers={"Content-Type": "application/json"}, method="POST")
            try:
                response = opener.open(request, timeout=65)
            except HTTPError as error:
                response = error
            except (URLError, TimeoutError, OSError):
                event(phase="result", request_sha256=request_sha, status=502, upstream_unavailable=True)
                self.reply(502, b'{"error":"rehearsal model upstream unavailable"}')
                return
            with response:
                try:
                    result = response.read(RESPONSE_LIMIT + 1)
                except (TimeoutError, OSError):
                    event(phase="result", request_sha256=request_sha, status=502, upstream_incomplete=True)
                    self.reply(502, b'{"error":"rehearsal model response incomplete"}')
                    return
                if len(result) > RESPONSE_LIMIT:
                    event(phase="result", request_sha256=request_sha, status=502, upstream_oversized=True)
                    self.reply(502, b'{"error":"rehearsal model response too large"}')
                    return
                event(phase="result", request_sha256=request_sha, status=response.code,
                      response_bytes=len(result), response_sha256=hashlib.sha256(result).hexdigest())
                self.reply(response.code, result)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    return server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--state-dir", type=Path, required=True)
    args = parser.parse_args()
    if not args.model.startswith("ollama/") or "cloud" in args.model.lower():
        parser.error("select an explicit local ollama/ model")
    server = create_proxy(args.upstream, args.state_dir)
    config = {"endpoint": f"http://127.0.0.1:{server.server_port}",
              "model": args.model, "max_tokens": 1024}
    fd = os.open(args.state_dir / "model.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(config, output)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    print(json.dumps({"pid": os.getpid(), "model_config": str(args.state_dir / "model.json"),
                      "outage_marker": str(args.state_dir / "outage")}), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
