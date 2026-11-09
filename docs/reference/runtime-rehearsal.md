# Isolated live-feed rehearsal

`scripts/runtime_rehearsal.py` prepares a new PostgreSQL 17 database, starts a
Mantik binary against it, and registers two SPY/IEX threshold configurations and
one declared evaluation window. It uses the existing private Alpaca credential
file only through Mantik's read-only acquisition and collector commands.

The live run starts the collector and advisory research loops. Decisions,
outcomes, comparisons, evaluation assessments and execution are left to the
service workers; the script never calls their write endpoints to sequence a
positive result. One configuration permits an entry proposal around the seed
quote, and the other has a threshold at half that seed price. Both are fixed
before the observation window. Price movement can still change which actions
occur. The declared execution assumptions and paper account are retained through
the same owner APIs as ordinary operation.

## Preparation and scheduling

On Kvant, the 2026-11-09 run uses
`/home/onnwee/.local/state/mantik/alpaca-access-20261014/credentials.json` and
Switchyard's existing `http://10.0.0.50:11434` model listener with
`ollama/gemma3:12b`. The owner authorized this model use. No Switchyard service,
workflow, configuration or private historical data is changed. No raw Ollama
listener is used.

The seed quote is retained in its own `seed-journal`. The registered continuous
collector uses a separate, initially empty `journal`, so the seed's unacknowledged
input cannot violate the collector's fenced-binding precondition. Both original
captures remain in the private rehearsal directory.

Build the intended exact source with the pinned toolchain before invoking the
runner. Supply its full 40-character commit as `--source-commit`, the absolute
binary path as `--binary`, a new absolute private directory as `--root`, the
credential path as `--credentials-file`, and the explicit model origin and route
as `--upstream` and `--model`. Keep the build/source evidence beside the receipt;
the supplied commit identifies intended source and is not itself build attestation.
The runner additionally records the binary and harness hashes.

`--preflight` starts and configures the isolated service, obtains one actual
quote to choose the declarations, verifies storage/feed/decision read routes,
restarts into the explicit receiver-bound feed readiness policy, and checks a
small real model request through the forwarding proxy. It then saves a dump
and tears down. This does not start continuous collection or a model review and
does not establish the market-session, outage or post-decision restart criteria.
It can be performed outside regular market hours.

Without `--preflight`, the runner observes for 1,200 seconds by default;
`--duration-seconds` permits 1,200–4,500 seconds. The regular-session collector
uses eight quotes per finite session at 30-second intervals, with verified local
journal rollover enabled. The advisory loop permits at most one model attempt
per cycle and three attempts per decision under its existing contract.

The T3 one-shot rehearsal is scheduled for 2026-11-09 at 14:40 UTC / 09:40
America/New_York. Check the actual scheduled task and exact merged source before
running it; this document does not prove that a scheduler is enabled or a run
has occurred.

## Outages, restart and custody

`rehearsal_model_proxy.py` binds only loopback and forwards the exact bounded
request/response bytes to the fixed private model origin. It exposes only the
chat-completion route and health. It disables ambient proxies and redirects,
drops authorization headers, and never forwards Mantik's service token. This
rehearsal uses a model listener that needs no separate bearer token. A model
gateway requiring credentials needs a separately reviewed transport change.

Creating the proxy's private `outage` marker returns HTTP 503 for this rehearsal
without contacting the upstream. Removing it restores forwarding. Intent and
transport results retain only hashes, counts, times and status. Mantik's own
draft capture directories retain actual model request/response artifacts. Invalid
JSON, unsupported claims and model errors remain failures; the proxy never
replaces them with a successful draft.

The live runner follows these phases, relative to collector startup:

| Elapsed | Action |
| --- | --- |
| 0–240 seconds | Observe normal collection and autonomous workers. |
| 240 seconds | Inject model-connection outage for the rehearsal. |
| 360 seconds | Restore model forwarding. |
| 480 seconds | Gracefully stop this runner's collector. |
| 630 seconds | Restart collection after 150 seconds without it. |
| 750 seconds | Stop its collector, capture owner records, restart its Mantik process, compare all earlier decision/outcome/assessment records, then restart collection. |
| End | Capture final state, stop owned processes, retain a private PostgreSQL dump, remove the exact newly created container, and write a receipt. |

Every snapshot includes current feed coverage, worker readiness, decisions,
outcomes, assessments, comparisons, attribution and service state. Readiness
degradation during a declared outage is captured rather than discarded. Failure
of an owner read stops the run visibly. The runner records handles before using
them and stops only its own `Popen` objects and exact database container ID.
Unexpected service, research-loop or active collector exits also stop the run;
the deliberately stopped collector is exempt during the feed-outage phase.
Database dump failure does not skip container cleanup. Credentials, dumps,
receipts and model captures stay outside Git in mode-0700 directories with
mode-0600 files.

`receipt.json` names phases and artifact/build identities and records cleanup.
The snapshots and model attempt captures must still be inspected to establish
which activity actually occurred. Completion of the runner means its sequence
finished; it does not assert a successful review, financial lifecycle, provider
entitlement, restart qualification or profitability. A backup dump hash does not
establish restore proof.

## Acceptance limits

A fresh short run cannot manufacture the multi-day sample and corporate-action
coverage needed for an eligible funded strategy. An entry proposal followed by
deferred allocation is an expected observable result. Record missing positive
funded execution and established-position protection as uncovered unless they
actually occur; never weaken gates or import a prior pilot to fill the gap.
Synthetic positive lifecycle tests supply separate implementation evidence.

No model availability is needed for decisions or established protection. This
rehearsal can test that independence only for the states it actually reaches.
Inspect that decisions and health continue across the injected outage, that real
failed model attempts remain present, and that later real model requests recover.
An HTTP 200 smoke request alone proves none of those behaviors.

The complete archive and #24 attribution work are tracked in the
[continuous runtime plan](../plans/continuous-paper-runtime.md).
