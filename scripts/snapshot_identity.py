"""Snapshot-bound public-table data identity for isolated PostgreSQL backups."""
import json
import re
import subprocess

METHOD = 'postgres17-public-table-json-sha256-v1'
# Stable PostgreSQL 17 JSON rendering; this is data identity, not DDL, role,
# sequence, provider, economic or protective-recovery qualification.
SQL = r"""
SET TIME ZONE 'UTC';
SET DateStyle = 'ISO, YMD';
SET IntervalStyle = 'iso_8601';
SET bytea_output = 'hex';
SET extra_float_digits = 3;
SELECT format($identity$
 SELECT json_build_object('table', %L, 'rows', count(*), 'sha256',
 encode(sha256(convert_to(coalesce(string_agg(h, '' ORDER BY h), ''), 'UTF8')), 'hex'))
 FROM (SELECT encode(sha256(convert_to(to_jsonb(t)::text, 'UTF8')), 'hex') h FROM %I.%I t) row_data
$identity$, schemaname || '.' || tablename, schemaname, tablename)
FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename COLLATE "C"
\gexec
\echo MANTIK_SNAPSHOT_IDENTITY_READY
"""


def validate_identity(value):
    if not isinstance(value, dict) or set(value) != {'method', 'tables'} or value['method'] != METHOD or not isinstance(value['tables'], list):
        raise ValueError('Invalid backup table-data identity')
    previous = ''
    for row in value['tables']:
        if not isinstance(row, dict) or set(row) != {'table', 'rows', 'sha256'} or not isinstance(row['table'], str) or not row['table'].startswith('public.') or row['table'] <= previous or type(row['rows']) is not int or row['rows'] < 0 or not isinstance(row['sha256'], str) or not re.fullmatch('[0-9a-f]{64}', row['sha256']):
            raise ValueError('Invalid backup table-data row')
        previous = row['table']
    return value


def require_identity(expected, actual):
    validate_identity(expected)
    validate_identity(actual)
    if expected != actual:
        raise ValueError('Restored public-table data does not match backup snapshot')


class Snapshot:
    """Keep one read-only exported snapshot alive until the dump completes.

    psql_prefix is a trusted argv selected from a validated owned manifest or the
    newly created disposable integration container. No shell or new credentials.
    """
    def __init__(self, psql_prefix):
        self.command = [*psql_prefix, '--no-psqlrc', '-qAt', '-v', 'ON_ERROR_STOP=1']
        self.process = None

    def __enter__(self):
        self.process = subprocess.Popen(self.command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True)
        try:
            self.process.stdin.write("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\nSET statement_timeout = '60s';\nSET idle_in_transaction_session_timeout = '10min';\nSELECT current_setting('server_version_num');\nSELECT pg_export_snapshot();\n" + SQL)
            self.process.stdin.flush()
            version = self.process.stdout.readline(256).strip()
            if not version.isdigit() or not 170000 <= int(version) < 180000:
                raise ValueError('Snapshot table-data method requires PostgreSQL 17')
            self.snapshot_id = self.process.stdout.readline(256).strip()
            if not re.fullmatch('[0-9A-Fa-f]{8}-[0-9A-Fa-f]{8}-[0-9]+', self.snapshot_id):
                raise ValueError('PostgreSQL did not export a valid snapshot')
            rows = []
            while True:
                line = self.process.stdout.readline(8192)
                if line.strip() == 'MANTIK_SNAPSHOT_IDENTITY_READY':
                    break
                if not line or len(rows) >= 4096 or not line.endswith('\n'):
                    raise ValueError('Incomplete or oversized snapshot table-data identity')
                rows.append(json.loads(line))
            self.identity = validate_identity({'method': METHOD, 'tables': rows})
            return self
        except BaseException:
            self.process.kill()
            self.process.communicate(timeout=10)
            raise

    def __exit__(self, kind, value, traceback):
        try:
            _, stderr = self.process.communicate('ROLLBACK;\n', timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.communicate(timeout=10)
            if kind is None:
                raise
        else:
            if self.process.returncode and kind is None:
                raise RuntimeError('Backup snapshot session failed: ' + stderr[-2000:])
