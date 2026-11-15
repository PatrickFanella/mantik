# Subscription model adapter

`mantik subscription-model` invokes `claude -p` or `codex exec` through an existing subscription login. It removes inherited API-key, access-token and auth-token variables; it accepts no API credentials. Claude runs in restricted safe mode with tools and MCP configuration disabled. Codex runs read-only and ephemeral in a fresh temporary directory, ignoring user configuration and execution-policy rules while retaining the host subscription login. The adapter has no trading, allocation, or execution dependency, so model failure cannot block those paths.

The caller supplies a private prompt file, absolute mode-0700 journal, provider, UTC daily budget, and bounded timeout. A nonblocking exclusive journal lock admits one call across processes; contention returns busy. The fsynced, atomically replaced mode-0600 budget state stops additional calls for the day and rejects a backwards clock. A rate-limit response creates a five-minute persisted backoff before another process can launch. UTC midnight resets the daily count but preserves any unexpired backoff. `calls.jsonl` records a start before launch and a terminal record afterwards. Records retain only timestamps, provider, status and SHA-256 prompt/response hashes. Prompt text, response text, and provider diagnostics are never retained; each captured stdout/diagnostic stream is limited to 64 KiB and its process group is killed on cancellation.


The immediate Go caller receives the successful response in memory as
`Record.Response`; JSON output and persisted records omit that field.
Prompts travel through stdin, rather than process arguments. Claude uses
`--restricted --safe-mode --strict-mcp-config --no-session-persistence` with
all tools disabled. Codex uses `--ignore-user-config --ignore-rules`, a
read-only sandbox and ephemeral execution. Subscription auth files remain
under their existing owners; no API key or alternate provider is accepted.
Callers must use one stable journal and consistent budget for a research desk;
creating another journal is a separate budget, not global host accounting.
The budget counts attempts, including failures and interrupted starts; it is
a call limit rather than an estimate of money or tokens. Cancellation is
bounded and kills the launched process group. A hard process exit leaves a
start without a terminal record, consuming its reserved call allowance.

On 2026-11-15 on Kvant, a real Codex subscription call completed with a tiny
no-tool prompt. Claude's Max subscription authenticated, but refused the real
prompt because its weekly limit was reached (reported reset November 20 at
01:00 America/Chicago). The adapter recorded `rate_limited` and persisted its
backoff. A successful Claude response remains unverified until quota returns;
this is a provider-capacity limitation, not evidence of a successful response.
Hash-only call records remain outside Git under
`/tmp/mantik-wave1-model-evidence/`. No trading package depends on this adapter.
