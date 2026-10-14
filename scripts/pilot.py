#!/usr/bin/env python3
"""Isolated Mantik fixture preparation. Evidence and credentials stay outside Git."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[1]


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def save(path, value):
    # Never replace an earlier evidence file, even after a failed attempt.
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')


def load(root):
    manifest = json.loads((root / 'manifest.json').read_text())
    if manifest['root'] != str(root) or not manifest['project'].startswith('mantik-pilot-'):
        raise ValueError('Manifest ownership/path mismatch')
    return manifest


def compose(root, *args, **kwargs):
    m = load(root)
    if hashlib.sha256((root / 'compose.json').read_bytes()).hexdigest() != m['config_sha256']:
        raise ValueError('Retained Compose configuration changed; refusing resource operation')
    return run('docker', 'compose', '--project-name', m['project'],
               '--file', str(root / 'compose.json'), *args, **kwargs)


def api(root, path, body=None, expected=200):
    m = load(root)
    token = (root / 'token').read_text().strip()
    req = urllib.request.Request(f"http://127.0.0.1:{m['port']}{path}",
        data=None if body is None else json.dumps(body).encode(),
        headers={'Authorization': f'Bearer {token}', 'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status = response.status
        value = json.loads(response.read())
    save(root / f'request-{time.time_ns()}.json',
         {'at': now(), 'path': path, 'input': body, 'status': status, 'response': value})
    if status != expected:
        raise RuntimeError(f'{path}: expected HTTP {expected}, got {status}: {value}')
    return value


def wait_ready(root):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if api(root, '/readyz?scope=fixture_operation')['ready']:
                return
        except (OSError, RuntimeError):
            pass
        time.sleep(1)
    raise RuntimeError('Fixture readiness did not recover within 90 seconds')


def prepare(root, port, image=None, source=None):
    if root == REPO or REPO in root.parents:
        raise ValueError('Evidence directory must be outside the repository')
    if not 1024 <= port <= 65535:
        raise ValueError('Use an unprivileged loopback port')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', port))
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    project = 'mantik-pilot-' + secrets.token_hex(8)
    # Inventory before resource creation. A unique project must own no resources yet.
    inventory = run('docker', 'ps', '-a', '--format', '{{json .}}', capture_output=True, text=True).stdout
    (root / 'before-containers.jsonl').write_text(inventory)
    for kind in ('container', 'volume', 'network'):
        existing = run('docker', kind, 'ls', '-q', '--filter',
                       f'label=com.docker.compose.project={project}', capture_output=True, text=True).stdout
        if existing.strip():
            raise RuntimeError('Generated project already owns resources; preparation stopped')
    if image is None:
        source = run('git', '-C', str(REPO), 'rev-parse', 'HEAD', capture_output=True, text=True).stdout.strip()
        # Build only committed content; no local credentials or evidence enter the context.
        with tempfile.TemporaryDirectory(prefix='mantik-source-') as temp:
            archive = Path(temp) / 'source.tar'
            run('git', '-C', str(REPO), 'archive', '--format=tar', '-o', str(archive), source)
            with archive.open('rb') as stream, (root / 'build.log').open('w') as log:
                run('docker', 'build', '--iidfile', str(root / 'image-id'), '-', stdin=stream,
                    stdout=log, stderr=subprocess.STDOUT)
        image = (root / 'image-id').read_text().strip()
    postgres = run('docker', 'image', 'inspect', 'postgres:17', '--format', '{{.Id}}', capture_output=True, text=True).stdout.strip()
    token, password = secrets.token_hex(32), secrets.token_hex(32)
    (root / 'token').write_text(token)
    config = {'services': {
        'postgres': {'image': postgres, 'environment': {'POSTGRES_DB': 'mantik',
            'POSTGRES_USER': 'mantik', 'POSTGRES_PASSWORD': password},
            'volumes': ['postgres-data:/var/lib/postgresql/data'],
            'healthcheck': {'test': ['CMD', 'pg_isready', '-U', 'mantik', '-d', 'mantik'],
                            'interval': '2s', 'timeout': '5s', 'retries': 30}},
        'mantik': {'image': image, 'command': ['serve', '--listen', '0.0.0.0:40880'],
            'environment': {'DATABASE_URL': f'postgres://mantik:{password}@postgres:5432/mantik?sslmode=disable',
                            'MANTIK_API_TOKEN': token, 'MANTIK_TIMER_INTERVAL': '1s'},
            'ports': [f'127.0.0.1:{port}:40880'], 'init': True, 'read_only': True,
            'security_opt': ['no-new-privileges:true'], 'cap_drop': ['ALL'],
            'depends_on': {'postgres': {'condition': 'service_healthy'}}}},
        'volumes': {'postgres-data': {}}}
    save(root / 'compose.json', config)
    save(root / 'manifest.json', {'config_sha256': hashlib.sha256((root / 'compose.json').read_bytes()).hexdigest(),
         'root': str(root), 'project': project, 'port': port,
         'launcher_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
         'source_sha': source, 'image': image, 'postgres_image': postgres,
         'prepared_at': now(), 'mode': 'synthetic_unqualified', 'window_started_during_preparation': False})
    compose(root, 'config', '--quiet')
    print(f'Prepared {root} ({project}); observation window has not started')


def snapshot(root):
    status = api(root, '/v1/status')
    api(root, '/v1/operations')
    api(root, '/v1/readiness/fixture_operation')
    with (root / f'schema-{time.time_ns()}.txt').open('w') as stream:
        compose(root, 'exec', '-T', 'postgres', 'psql', '-U', 'mantik', '-d', 'mantik', '-At', '-c',
                'SELECT version FROM mantik_schema ORDER BY version;', stdout=stream)
    with (root / f'logs-{time.time_ns()}.txt').open('w') as stream:
        compose(root, 'logs', '--no-color', '--timestamps', stdout=stream, stderr=subprocess.STDOUT)
    return status


def wait_case(root, expected):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        result = api(root, '/v1/status')
        if result['state']['case']['status'] == expected:
            return result
        time.sleep(.5)
    raise RuntimeError(f'Case did not become {expected}')


def smoke(root):
    if api(root, '/v1/status')['observation_count'] != 0:
        raise ValueError('Smoke requires a fresh aggregate; create another pilot directory')
    timestamp = now()
    quote = {'id': 'pilot-entry', 'kind': 'quote', 'source': 'fixture', 'symbol': 'DEMO',
             'occurred_at': timestamp, 'received_at': timestamp, 'price_cents': 10000}
    api(root, '/v1/observations', quote, 201)
    wait_case(root, 'open')
    retry = api(root, '/v1/observations', quote)
    if not retry['duplicate']:
        raise RuntimeError('Exact retry was not recognized')
    api(root, '/v1/observations', dict(quote, price_cents=10001), 409)
    timestamp = now()
    api(root, '/v1/observations', dict(quote, id='pilot-stop', price_cents=9400,
        occurred_at=timestamp, received_at=timestamp), 201)
    result = wait_case(root, 'closed')
    outcomes = [e for e in result['events'] if e.get('outcome')]
    if len(outcomes) != 2 or result['observation_count'] != 2 or result['state']['case']['exit']['realized_pnl_cents'] != -6000:
        raise RuntimeError('Lifecycle effect/count/P&L mismatch')
    save(root / 'smoke-result.json', {'at': now(), 'passed': True,
        'scenarios': ['normal lifecycle', 'exact retry', 'conflicting retry'],
        'fixture_pnl_cents': -6000, 'qualification_credit': False})
    snapshot(root)


def backup(root):
    path = root / f'backup-{time.time_ns()}.dump'
    # One pg_dump snapshot includes aggregate, audit, observations, migrations, effect ledger.
    with path.open('xb') as stream:
        compose(root, 'exec', '-T', 'postgres', 'pg_dump', '-U', 'mantik', '-d', 'mantik', '-Fc', stdout=stream)
    save(path.with_suffix('.json'), {'at': now(), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
         'source_manifest': load(root), 'dump': str(path)})
    print(path)
    return path


def restore(root, target, port, dump):
    receipt = json.loads(dump.with_suffix('.json').read_text())
    if receipt['source_manifest'] != load(root) or receipt['sha256'] != hashlib.sha256(dump.read_bytes()).hexdigest():
        raise ValueError('Backup source or checksum mismatch')
    m = load(root)
    prepare(target, port, m['image'], m['source_sha'])
    compose(target, 'up', '-d', '--wait', 'postgres')
    with dump.open('rb') as stream:
        compose(target, 'exec', '-T', 'postgres', 'pg_restore', '-U', 'mantik', '-d', 'mantik', '--exit-on-error', stdin=stream)
    # Capture restored database before workers can advance pending obligations.
    with (target / 'restored-before-workers.jsonl').open('w') as stream:
        compose(target, 'exec', '-T', 'postgres', 'psql', '-U', 'mantik', '-d', 'mantik', '-At', '-c',
                'SELECT payload FROM mantik_state;', stdout=stream)
    save(target / 'restore-receipt.json', {'at': now(), 'backup': str(dump),
        'sha256': receipt['sha256'], 'source_project': m['project'], 'target_project': load(target)['project']})
    compose(target, 'up', '-d', '--wait')
    wait_ready(target)
    snapshot(target)


def observe(root):
    start = dt.datetime.now(dt.timezone.utc)
    save(root / 'window.json', {'start': start.isoformat(), 'end': (start + dt.timedelta(hours=24)).isoformat(),
         'interval_seconds': 30, 'allowed_interruptions': [],
         'purpose': 'continuous fixture readiness; acceptance scenarios tracked separately'})
    # Foreground supervision: termination leaves an incomplete window, never a success claim.
    failures = 0
    previous_sample = time.monotonic()
    deadline = previous_sample + 86400
    while time.monotonic() < deadline:
        sample = time.monotonic()
        if sample - previous_sample > 45:
            failures += 1
            save(root / f'observation-gap-{time.time_ns()}.json',
                 {'at': now(), 'gap_seconds': sample - previous_sample})
        previous_sample = sample
        try:
            status = api(root, '/v1/readiness/fixture_operation')
            api(root, '/v1/operations')
            api(root, '/v1/status')
            if not status['ready']:
                failures += 1
        except (OSError, RuntimeError) as error:
            failures += 1
            save(root / f'observation-error-{time.time_ns()}.json', {'at': now(), 'error': str(error)})
        time.sleep(min(30, max(0, deadline - time.monotonic())))
    if time.monotonic() - previous_sample > 45:
        failures += 1
        save(root / f'observation-gap-{time.time_ns()}.json',
             {'at': now(), 'gap_seconds': time.monotonic() - previous_sample})
    snapshot(root)
    save(root / 'window-result.json', {'finished_at': now(), 'unhealthy_samples': failures,
        'continuous_readiness_passed': failures == 0, 'all_scenarios_qualified': False})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'start', 'smoke', 'snapshot', 'backup', 'restore', 'observe', 'stop'])
    parser.add_argument('--dir', type=Path, required=True)
    parser.add_argument('--port', type=int, default=41880)
    parser.add_argument('--target', type=Path)
    parser.add_argument('--dump', type=Path)
    args = parser.parse_args()
    root = args.dir.expanduser().resolve()
    os.umask(0o077)
    if args.command == 'prepare':
        prepare(root, args.port)
    else:
        load(root)
        if args.command == 'start':
            compose(root, 'up', '-d', '--wait')
            wait_ready(root)
            snapshot(root)
        elif args.command == 'smoke':
            smoke(root)
        elif args.command == 'snapshot':
            snapshot(root)
        elif args.command == 'backup':
            backup(root)
        elif args.command == 'restore':
            if not args.target or not args.dump:
                parser.error('restore requires --target and --dump')
            restore(root, args.target.expanduser().resolve(), args.port, args.dump.expanduser().resolve())
        elif args.command == 'observe':
            observe(root)
        elif args.command == 'stop':
            snapshot(root)
            compose(root, 'stop')  # Retain volumes and all evidence; no destructive cleanup.
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
