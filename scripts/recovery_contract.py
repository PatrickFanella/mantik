"""Storage-only backup/restore contract on the integration runner's fresh server."""
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile
import time

from snapshot_identity import Snapshot, require_identity


def recovery_contract(container, database, replay_owner):
    # Names come only from the runner's freshly created UUID-owned resources.
    if not re.fullmatch('mantik-test-[0-9a-f]{12}', container) or not re.fullmatch('mantik_test_[0-9a-f]{12}', database):
        raise ValueError('Recovery requires disposable integration resources')
    psql = ['docker', 'exec', '-i', container, 'psql', '-U', 'mantik', '-d', database]
    def sql(command, target=database):
        args = ['docker', 'exec', '-i', container, 'psql', '-U', 'mantik', '-d', target,
                '--no-psqlrc', '-qAt', '-v', 'ON_ERROR_STOP=1']
        result = subprocess.run(args, input=command, text=True, capture_output=True, timeout=60)
        if result.returncode:
            raise RuntimeError(f'Disposable recovery SQL failed (exit {result.returncode}): {result.stderr.strip()}')
        return result.stdout
    sql("CREATE TABLE mantik_recovery_identity_contract(id int, payload jsonb, original bytea); INSERT INTO mantik_recovery_identity_contract VALUES (1, '{\"source\":\"snapshot fixture\"}', decode('00ff','hex')); ")
    configured = os.environ.get('MANTIK_RECOVERY_RECEIPT_DIR')
    with tempfile.TemporaryDirectory(prefix='mantik-recovery-') as temporary:
        root = Path(configured).resolve() if configured else Path(temporary)
        repo = Path(__file__).resolve().parents[1]
        if root == repo or repo in root.parents:
            raise ValueError('Recovery evidence must stay outside Git')
        root.mkdir(parents=True, exist_ok=True)
        dump = root / f'recovery-{time.time_ns()}.dump'
        with Snapshot(psql) as source:
            expected = source.identity
            # This committed write occurs after the exported snapshot. Neither
            # identity nor dump may include it, even though the live DB does.
            sql("INSERT INTO mantik_recovery_identity_contract VALUES (2, '{\"late\":true}', NULL)")
            with dump.open('xb') as stream:
                subprocess.run(['docker', 'exec', container, 'pg_dump', '-U', 'mantik', '-d', database,
                                '-Fc', '--snapshot=' + source.snapshot_id], stdout=stream, check=True, timeout=600)
        names = {row['table'] for row in expected['tables'] if row['rows'] > 0}
        if not {'public.mantik_social_watch_journal', 'public.mantik_owned_allocations',
                'public.mantik_owned_allocation_archive', 'public.mantik_owned_allocation_checkpoints',
                'public.mantik_owned_allocation_batches', 'public.mantik_owned_allocation_members',
                'public.mantik_owned_allocation_decision_refs',
                'public.mantik_paper_evaluation_declarations', 'public.mantik_paper_evaluation_assessments',
                'public.mantik_feed_archives', 'public.mantik_feed_archive_sources',
                'public.mantik_feed_archive_source_batches', 'public.mantik_feed_archive_prefixes',
                'public.mantik_feed_archive_prefix_batches', 'public.mantik_paper_feed_checkpoints',
                'public.mantik_feed_retention', 'public.mantik_paper_feed_source_seals',
                'public.mantik_paper_record_archives', 'public.mantik_paper_record_epochs',
                'public.mantik_paper_archive_members', 'public.mantik_paper_archive_batches'}.issubset(names):
            raise ValueError('Recovery seed did not retain source, allocation, evaluation and archive owners')
        target = database + '_restore'
        sql('CREATE DATABASE "' + target + '" TEMPLATE template0')
        with dump.open('rb') as stream:
            subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'mantik', '-d', target,
                            '--exit-on-error'], stdin=stream, check=True, timeout=600)
        restored_psql = ['docker', 'exec', '-i', container, 'psql', '-U', 'mantik', '-d', target]
        with Snapshot(restored_psql) as restored:
            actual = restored.identity
            require_identity(expected, actual)
        owner_replay = replay_owner(target, False)
        original_owner = sql("SELECT encode(payload, 'hex') FROM mantik_owned_allocations WHERE singleton", target).strip()
        if not original_owner:
            raise ValueError('Restored allocation owner payload missing')
        try:
            sql("UPDATE mantik_owned_allocations SET payload=convert_to(jsonb_set(convert_from(payload, 'UTF8')::jsonb, '{records,0,record,execution_safety,0,safety,stop_cents}', '0'::jsonb)::text, 'UTF8') WHERE singleton", target)
            if sql("SELECT encode(payload, 'hex') FROM mantik_owned_allocations WHERE singleton", target).strip() == original_owner:
                raise ValueError('Restored owner corruption fixture changed no bytes')
            rejection_replay = replay_owner(target, True)
        finally:
            encoded = original_owner
            if not re.fullmatch('[0-9a-f]+', encoded):
                raise ValueError('Restored owner payload encoding invalid')
            sql("UPDATE mantik_owned_allocations SET payload=decode('" + encoded + "','hex') WHERE singleton", target)
        with Snapshot(restored_psql) as repaired:
            require_identity(expected, repaired.identity)
        if sql('SELECT count(*) FROM mantik_recovery_identity_contract', target).strip() != '1':
            raise ValueError('Dump included a write outside its exported snapshot')
        with Snapshot(psql) as current:
            if current.identity == expected:
                raise ValueError('Concurrent source write was not retained')
        sql("UPDATE mantik_recovery_identity_contract SET original=decode('01ff','hex')", target)
        with Snapshot(restored_psql) as changed:
            try:
                require_identity(expected, changed.identity)
            except ValueError:
                pass
            else:
                raise ValueError('Changed restored bytes were accepted')
        receipt = {'at_ns': time.time_ns(), 'passed': True, 'container': container,
                   'source_database': database, 'restored_database': target,
                   'dump': str(dump), 'sha256': hashlib.sha256(dump.read_bytes()).hexdigest(),
                   'table_data_identity': expected, 'restored_identity': actual,
                   'concurrent_source_write_excluded': True, 'changed_bytes_rejected': True,
                   'restored_owner_replay': owner_replay, 'corrupt_owner_rejection': rejection_replay,
                   'owner_payload_restored_identity_verified': True,
                   'application_workers_started': False, 'qualification_credit': False}
        with dump.with_suffix('.json').open('x') as stream:
            json.dump(receipt, stream, indent=2)
            stream.write('\n')
        print('Snapshot-bound recovery contract passed; no application workers or provider requests', flush=True)
