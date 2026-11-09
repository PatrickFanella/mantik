import json
from pathlib import Path
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from rehearsal_model_proxy import RESPONSE_LIMIT, create_proxy, private_origin


class RehearsalProxyTests(unittest.TestCase):
    def test_forward_outage_recovery_and_route_isolation(self):
        seen = []
        response = b'{"choices":[{"message":{"content":"actual upstream bytes"}}]}'

        class Upstream(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                seen.append((self.path, self.headers.get("Authorization"),
                             self.rfile.read(int(self.headers["Content-Length"]))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(response)

        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        self.addCleanup(upstream.server_close)
        self.addCleanup(upstream.shutdown)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "proxy"
            proxy = create_proxy(f"http://127.0.0.1:{upstream.server_port}", root)
            threading.Thread(target=proxy.serve_forever, daemon=True).start()
            try:
                origin = f"http://127.0.0.1:{proxy.server_port}"
                body = b'{"model":"ollama/local","messages":[]}'

                def call(path="/v1/chat/completions"):
                    request = Request(origin + path, data=body,
                                      headers={"Authorization": "Bearer must-not-forward"})
                    try:
                        result = urlopen(request, timeout=3)
                    except HTTPError as error:
                        result = error
                    with result:
                        return result.code, result.read()

                self.assertEqual(call(), (200, response))
                self.assertEqual(seen, [("/v1/chat/completions", None, body)])
                (root / "outage").touch()
                self.assertEqual(call()[0], 503)
                self.assertEqual(len(seen), 1)
                (root / "outage").unlink()
                self.assertEqual(call(), (200, response))
                self.assertEqual(len(seen), 2)
                self.assertEqual(call("/api/runs")[0], 404)
                self.assertEqual(call("/v1/chat/completions?override=1")[0], 404)
                self.assertEqual(len(seen), 2)
                events = [json.loads(line) for line in (root / "events.jsonl").read_text().splitlines()]
                self.assertEqual([e["status"] for e in events if e["phase"] == "result"], [200, 503, 200])
                self.assertNotIn("must-not-forward", (root / "events.jsonl").read_text())
                self.assertEqual(root.stat().st_mode & 0o777, 0o700)
                self.assertEqual((root / "events.jsonl").stat().st_mode & 0o777, 0o600)
                with self.assertRaises(FileExistsError):
                    create_proxy(f"http://127.0.0.1:{upstream.server_port}", root)
            finally:
                proxy.shutdown()
                proxy.server_close()

    def test_reject_oversized_upstream_without_replacing_it_with_success(self):
        class Upstream(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"x" * (RESPONSE_LIMIT + 1))

        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        self.addCleanup(upstream.server_close)
        self.addCleanup(upstream.shutdown)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "proxy"
            proxy = create_proxy(f"http://127.0.0.1:{upstream.server_port}", root)
            threading.Thread(target=proxy.serve_forever, daemon=True).start()
            try:
                request = Request(f"http://127.0.0.1:{proxy.server_port}/v1/chat/completions", data=b"{}")
                with self.assertRaises(HTTPError) as raised:
                    urlopen(request, timeout=3)
                self.assertEqual(raised.exception.code, 502)
                raised.exception.close()
                events = [json.loads(line) for line in (root / "events.jsonl").read_text().splitlines()]
                self.assertTrue(events[-1]["upstream_oversized"])
            finally:
                proxy.shutdown()
                proxy.server_close()

    def test_reject_ambiguous_or_public_upstream(self):
        for origin in ["https://example.com", "http://8.8.8.8:80", "http://127.0.0.1:80/x",
                       "http://user:secret@127.0.0.1:80", "http://0.0.0.0:80",
                       "http://127.0.0.1:80?redirect=elsewhere"]:
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                private_origin(origin)


if __name__ == "__main__":
    unittest.main()
