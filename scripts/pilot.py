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


def prepare(root, port, image=None, source=None, gate=None):
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
    if gate:
        gate_dir = root / 'gate'
        gate_dir.mkdir()
        gate_dir.chmod(0o777)  # Container UID writes markers; parent evidence directory stays 0700.
        config['services']['mantik']['volumes'] = [f'{gate_dir}:/pilot-gate']
        config['services']['mantik']['environment'].update({
            'MANTIK_FIXTURE_GATE_DIR': '/pilot-gate', 'MANTIK_FIXTURE_ACTION_GATE': gate})
    save(root / 'compose.json', config)
    save(root / 'manifest.json', {'config_sha256': hashlib.sha256((root / 'compose.json').read_bytes()).hexdigest(),
         'root': str(root), 'project': project, 'port': port,
         'launcher_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
         'fixture_gate': gate, 'source_sha': source, 'image': image, 'postgres_image': postgres,
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


def check_routing_scenario(status):
    routing = status['state']['routing']
    jobs = routing['timers']['jobs']
    for name, expected in [('pilot-missing-v1', 'matched'), ('pilot-observed-v1', 'event_observed')]:
        job = jobs[name]
        if job['status'] != 'completed' or job['receipt']['status'] != expected:
            raise RuntimeError(f'{name}: unexpected timer completion')
        matches = job['receipt']['routing']['matches'] or []
        if len(matches) != (1 if expected == 'matched' else 0):
            raise RuntimeError(f'{name}: unexpected routing matches')
    cases = routing['cases']
    if len(cases) != 1 or cases[0]['case_key'] != 'pilot-missing-case' or cases[0]['match_count'] != 1:
        raise RuntimeError('Missing-event delivery multiplied or incorrectly suppressed research work')
    if status['observation_count'] != 0 or status['state']['case']['status'] != 'waiting' or status['state']['actions']['items']:
        raise RuntimeError('Research routing produced an unintended economic action')


def routing_smoke(root):
    initial = api(root, '/v1/status')
    if initial['observation_count'] or initial['state']['routing']['definitions']:
        raise ValueError('Routing smoke requires a fresh stack; retain failed attempts separately')
    start = dt.datetime.now(dt.timezone.utc)
    due = start + dt.timedelta(seconds=20)
    save(root / 'routing-scenario-plan.json', {'declared_at': now(), 'due_at': due.isoformat(),
        'expected': {'pilot-missing-v1': 'one research case', 'pilot-observed-v1': 'suppressed by timely evidence'},
        'allowed_interruption': 'service restart after completed timer receipts',
        'economic_actions_expected': 0, 'continuous_window_started': False})
    for name, case, event in [('pilot-missing-v1', 'pilot-missing-case', 'absent'),
                             ('pilot-observed-v1', 'pilot-observed-case', 'present')]:
        definition = {'id': name, 'owner': 'operator', 'case_key': case, 'behavior': 'research',
            'condition': {'kind': 'expected_event_missing', 'event_name': event, 'due_at': due.isoformat()},
            'valid_from': (start - dt.timedelta(seconds=1)).isoformat(),
            'valid_until': (start + dt.timedelta(minutes=5)).isoformat(), 'repeat_policy': 'once',
            'requirements': {'source': 'fixture', 'symbol': 'DEMO',
                             'evidence_class': 'synthetic_unqualified', 'max_age_seconds': 60}}
        api(root, '/v1/triggers', definition)
        api(root, '/v1/triggers', definition)
        api(root, '/v1/triggers', dict(definition, case_key='changed-case'), 409)
    stamp = now()
    event = {'id': 'pilot-timely-event-v1', 'kind': 'event', 'source': 'fixture', 'symbol': 'DEMO',
             'evidence_class': 'synthetic_unqualified', 'occurred_at': stamp, 'received_at': stamp, 'event_name': 'present'}
    if dt.datetime.now(dt.timezone.utc) >= due:
        raise RuntimeError('Setup missed the declared timely-event deadline; retain this failed attempt')
    api(root, '/v1/routing-inputs', event)
    if not api(root, '/v1/routing-inputs', event)['duplicate']:
        raise RuntimeError('Routing input retry was not recognized')
    api(root, '/v1/routing-inputs', dict(event, event_name='changed'), 409)
    deadline = time.monotonic() + 35
    while time.monotonic() < deadline:
        status = api(root, '/v1/status')
        jobs = status['state']['routing']['timers']['jobs']
        if all(jobs[name]['status'] == 'completed' for name in ['pilot-missing-v1', 'pilot-observed-v1']):
            break
        time.sleep(1)
    else:
        raise RuntimeError('Expected-event timers did not complete')
    check_routing_scenario(status)
    retained_jobs = status['state']['routing']['timers']['jobs']
    compose(root, 'restart', 'mantik')
    wait_ready(root)
    time.sleep(3)  # Multiple additional worker cycles must not deliver completed timers again.
    after = snapshot(root)
    check_routing_scenario(after)
    if after['state']['routing']['timers']['jobs'] != retained_jobs:
        raise RuntimeError('Completed timer generations or receipts changed after restart')
    save(root / 'routing-smoke-result.json', {'at': now(), 'passed': True,
        'scenarios': ['missing event once', 'timely event suppression', 'definition and input retries/conflicts',
                      'completed timer restart retention'], 'qualification_credit': False})


def ledger(root):
    result = compose(root, 'exec', '-T', 'postgres', 'psql', '-U', 'mantik', '-d', 'mantik', '-At', '-c',
        'SELECT payload FROM mantik_fixture_effects ORDER BY idempotency_key;', capture_output=True, text=True)
    rows = [json.loads(line) for line in result.stdout.splitlines() if line]
    save(root / f'effect-ledger-{time.time_ns()}.json', {'at': now(), 'rows': rows})
    return rows


def delivery_smoke(root, scenario):
    expected_gate = 'after_submit' if scenario == 'post-crash' else 'before_submit'
    if load(root).get('fixture_gate') != expected_gate:
        raise ValueError(f'{scenario} requires a fresh stack prepared with --gate {expected_gate}')
    wait_ready(root)
    if api(root, '/v1/status')['observation_count']:
        raise ValueError('Delivery smoke requires a fresh aggregate')
    save(root / 'delivery-scenario-plan.json', {'declared_at': now(), 'scenario': scenario,
        'gate': expected_gate, 'expected': 'completed once' if scenario == 'post-crash' else 'no economic effect',
        'allowed_interruptions': ['SIGKILL and service start'] if 'crash' in scenario else [],
        'original_validity_and_lease_seconds': 30, 'continuous_window_started': False})
    stamp = now()
    quote = {'id': 'pilot-delivery-entry-v1', 'kind': 'quote', 'source': 'fixture', 'symbol': 'DEMO',
             'occurred_at': stamp, 'received_at': stamp, 'price_cents': 10000}
    api(root, '/v1/observations', quote, 201)
    deadline = time.monotonic() + 10
    marker_path = root / 'gate' / 'reached.json'
    while time.monotonic() < deadline:
        try:
            marker = json.loads(marker_path.read_text())
            break
        except (FileNotFoundError, json.JSONDecodeError):
            time.sleep(.05)
    else:
        raise RuntimeError('Worker did not reach the declared delivery boundary')
    before = snapshot(root)
    action = before['state']['actions']['items'][marker['action_id']]
    if marker['stage'] != expected_gate or action['status'] != 'unknown' or before['state']['case'].get('entry'):
        raise RuntimeError('Delivery was not paused before acknowledgment')
    effects = ledger(root)
    if len(effects) != (1 if scenario == 'post-crash' else 0):
        raise RuntimeError('Independent ledger does not match the declared crash boundary')
    if scenario in ['pre-crash', 'post-crash']:
        container = compose(root, 'ps', '-a', '-q', 'mantik', capture_output=True, text=True).stdout.strip()
        if not container or '\n' in container:
            raise RuntimeError('Expected exactly one owned application container')
        compose(root, 'kill', '-s', 'SIGKILL', 'mantik')
        stopped = run('docker', 'wait', container, capture_output=True, text=True, timeout=15)
        save(root / 'kill-receipt.json', {'at': now(), 'container': container, 'exit_status': stopped.stdout.strip()})
        if stopped.stdout.strip() != '137':
            raise RuntimeError('Application did not exit from the declared SIGKILL')
        compose(root, 'start', 'mantik')
    elif scenario == 'changed-authority':
        stamp = now()
        api(root, '/v1/observations', dict(quote, id='pilot-changed-price-v1', price_cents=10300,
            occurred_at=stamp, received_at=stamp), 201)
        (root / 'gate' / 'release').touch(exist_ok=False)
    else:
        valid_until = dt.datetime.fromisoformat(action['intent']['decision']['valid_until'].replace('Z', '+00:00'))
        while dt.datetime.now(dt.timezone.utc) <= valid_until:
            time.sleep(.5)
        (root / 'gate' / 'release').touch(exist_ok=False)
    expected = 'completed' if scenario == 'post-crash' else 'rejected' if scenario == 'changed-authority' else 'expired'
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            after = api(root, '/v1/status')
            if after['state']['actions']['items'][marker['action_id']]['status'] == expected:
                break
        except (OSError, RuntimeError):
            pass
        time.sleep(.5)
    else:
        raise RuntimeError(f'Delivery did not reach {expected} under original authority')
    if after['state']['actions']['items'][marker['action_id']]['intent'] != action['intent']:
        raise RuntimeError('Recovery rewrote original intent')
    outcomes = [e for e in after['events'] if e.get('outcome')]
    effects = ledger(root)
    economic_effects = [e for e in effects if e.get('outcome')]
    expected_effects = 1 if scenario == 'post-crash' else 0
    if len(outcomes) != expected_effects or len(economic_effects) != expected_effects:
        raise RuntimeError('Delivery lost or duplicated an economic effect')
    if (after['state']['case'].get('entry') is not None) != bool(expected_effects):
        raise RuntimeError('Aggregate position does not match the effect ledger')
    time.sleep(3)
    stable = snapshot(root)
    if stable['state']['actions']['items'][marker['action_id']] != after['state']['actions']['items'][marker['action_id']]:
        raise RuntimeError('Terminal action changed during later worker cycles')
    save(root / 'delivery-smoke-result.json', {'at': now(), 'passed': True, 'scenario': scenario,
        'terminal_status': expected, 'economic_effect_count': expected_effects,
        'intent_preserved': True, 'qualification_credit': False})


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
    if load(root).get('fixture_gate'):
        raise ValueError('A deliberately gated stack cannot run the continuous qualification window')
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
    parser.add_argument('command', choices=['prepare', 'start', 'smoke', 'routing-smoke', 'delivery-smoke', 'snapshot', 'backup', 'restore', 'observe', 'stop'])
    parser.add_argument('--dir', type=Path, required=True)
    parser.add_argument('--port', type=int, default=41880)
    parser.add_argument('--gate', choices=['before_submit', 'after_submit'])
    parser.add_argument('--scenario', choices=['pre-crash', 'post-crash', 'changed-authority', 'expiry'])
    parser.add_argument('--target', type=Path)
    parser.add_argument('--dump', type=Path)
    args = parser.parse_args()
    root = args.dir.expanduser().resolve()
    os.umask(0o077)
    if args.command == 'prepare':
        prepare(root, args.port, gate=args.gate)
    else:
        load(root)
        if args.command == 'start':
            compose(root, 'up', '-d', '--wait')
            wait_ready(root)
            snapshot(root)
        elif args.command == 'smoke':
            smoke(root)
        elif args.command == 'routing-smoke':
            routing_smoke(root)
        elif args.command == 'delivery-smoke':
            if not args.scenario:
                parser.error('delivery-smoke requires --scenario')
            delivery_smoke(root, args.scenario)
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
