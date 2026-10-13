# Augr corpus recovery and assessment

Assessed on 2026-10-13 for [Mantik issue #4](https://git.subcult.tv/subculture-collective/mantik/issues/4).
This inventory records source recovery and candidate reuse. It does not register
a strategy, import market observations, reproduce a paper, or qualify performance.

## Scope and ownership

The current source owner is
[`subculture-collective/augr`](https://git.subcult.tv/subculture-collective/augr).
The inspected remote-main revision is
[`45a6383b38b6f463a4e38bf06a31174899b068f5`](https://git.subcult.tv/subculture-collective/augr/commit/45a6383b38b6f463a4e38bf06a31174899b068f5).
It was fetched through `/home/onnwee/Work/Subcult/augr` without changing that
checkout's branch or files. Its working tree was clean and its HEAD remained
`a725135d3bd07dea709d44fe737b3b7db9709b19`. The handoff's separate clean checkout,
`/home/onnwee/.t3/worktrees/augr/t3-f7c89172`, remained at
`c86311b7dcf3e5b811913eafc882118fd50d418e`.

Augr's repository contains a GPL version 3 license. Check attribution and
per-file dependencies before porting code. Papers retain their authors' and
publishers' rights; a locally downloaded PDF is not redistribution permission.
SUBCULT owns product research curation; Archivum holds imported reference
snapshots. Neither vault is the editing owner of current executable Augr source.

Only tracked source, relevant Git history, and the identified research folders
were inspected. No Augr database, provider archive, credentials, service,
scheduler, trading account, or generated operational report was read or changed.
No Augr code or test command was executed. Mantik imports references and this
assessment, rather than copies of papers, reports, or runtime data.

## Recovered locations

| Source | Finding | Ownership and limits |
| --- | --- | --- |
| Current Augr Git source | Immutable catalog, datasets, replay runner, hypothesis/critic, constrained generated methods, and offline research components found | SUBCULT source; inspected at the revision above. Existing tests are implementation evidence, not a fresh reproduction |
| Historical `docs/research/` | 36 Markdown paths removed by commit `f2d478f69c5614efaf7e5636e4d7e9402a85fa68` on 2026-06-08 remain recoverable in its parent `c8b011692581672c7482c26ab1056df286eac299` | Source history; recover through exact Git objects rather than recreating the old directory in current Augr |
| SUBCULT `Products/Augr/research/` | Paper tracker, four original synthesis reports, and `papers/` found | Product research curation; report authorship and empirical claims remain unverified |
| SUBCULT `Products/Augr/notes/` and `trading-agents-notes/` | Concept, LLM limitation, and research-team notes found | Supporting source notes; assess a specific note before reuse |
| Archivum `Repositories/subculture-collective/augr/docs/research/` | Historical notes and four original reports found | Imported snapshot; does not override current source or establish freshness |

Open the curator's [paper tracker](obsidian://open?vault=SUBCULT&file=Products%2FAugr%2Fresearch%2Fpaper-tracker)
or the [historical citation list](https://git.subcult.tv/subculture-collective/augr/src/commit/c8b011692581672c7482c26ab1056df286eac299/docs/research/cited-papers.md)
for the full bibliography. The tracker says it was generated on 2026-03-20 and
lists 52 references: 51 papers and one book. Its 49 entries marked downloaded
have matching files in `Products/Augr/research/papers/`; all 49 have PDF headers.
Filename matching, byte hashes, and headers were checked. Document structure,
local manuscript versions, citation accuracy for every entry, and paper results
were not independently verified.

The local hash receipt is retained outside Git at
`/home/onnwee/.local/state/mantik/research-assessment-20261013/paper-files.json`.
It contains filenames, byte lengths, SHA-256 values, and header results for the
49 files. `git-sources.json` beside it records the inspected source-object hashes.
These receipts are dated observations; recheck them when using the corpus.

## Missing and unresolved material

- The tracker marks Do and Faff's pairs-trading paper and Filho and Gaspar's
  risk-parity paper as found but not downloadable. Neither has a downloaded
  file in the inspected folder. Their current access status was not resolved.
- Graham and Dodd's *Security Analysis* is listed as a book to obtain separately.
  No copy was recovered from the scoped paper folder.
- No notebook or bibliography file was found by the `.ipynb`/`.bib` filename
  search in the scoped product and repository mirror. This does not establish
  that no such files exist elsewhere.
- No licensed point-in-time dataset, experiment output, retained model response,
  or baseline performance was imported or reproduced. Operational stores were
  deliberately outside the search scope.
- Report titles use profitability language. That wording is a source claim,
  not an assessment result. The reports are topic indexes until their claims,
  citations, costs, selection rules, and test windows are independently checked.

## Literature-to-library candidates

The names below are proposed library work, not admitted Mantik entries.
Unless noted, citation identity comes from the recovered tracker and has not
been checked against the publisher. All reproduction states are `not_reproduced`.

| Literature group | Candidate | Assumptions and required datasets | Reproduction/admission gap |
| --- | --- | --- | --- |
| Time-series momentum; cross-sectional momentum | `price-return-v1` transform, `trend-sign-v1` and `rank-momentum-v1` signals | Declared lookback/skip/rebalance windows; survivorship-aware universe; point-in-time corporate actions and total-return series; calendars and costs | Keep a synthetic rule baseline separate from a paper reproduction; no historical adapter or baseline experiment is registered yet |
| Value, quality, and factor models | `fundamental-rank-v1`, `factor-exposure-v1` | Filing availability and revision times, point-in-time fundamentals, universe/delisting history, portfolio neutralization and turnover | Current cents quote fixture supplies none of the fundamental or multi-instrument contracts |
| Mean reversion and pairs/statistical arbitrage | `spread-normalization-v1`, `pairs-residual-v1` | Training-only pair selection and hedge ratio; synchronous prices; borrow/shorting and transaction-cost assumptions; no future fit | Requires two-leg position/safety contracts and held-out selection controls |
| Carry, volatility selling, and market making | Carry/option-risk transforms; bounded quote/fill models | Funding/roll curves, options chains/Greeks and settlement, executable bid/ask and depth, partial-fill and adverse-selection assumptions | Contract precision, tail-loss protections, and realistic execution are prerequisites; no entry is proposed for execution now |
| Low-volatility and risk parity | `realized-volatility-v1`, `risk-budget-v1` | Lagged covariance estimation, leverage/financing and rebalance costs, allocator-owned account constraints | Capital allocation belongs to issue #19; source labels do not authorize leverage or allocation |
| LLM sentiment and multi-agent investigation | Evidence extraction, hypothesis records, and a deterministic comparison recipe | Original headlines and revisions, public availability, pinned prompts/model responses, label timing, inference costs, knowledge-cutoff contamination controls | Requires issues #12/#15/#17 and comparison against a non-LLM baseline; no narrative-to-execution shortcut |

Three primary records were checked for citation identity on the assessment date:

- [TradingAgents](https://arxiv.org/abs/2412.20138): Xiao, Sun, Luo, and Wang;
  arXiv lists revisions through v7. It is an architecture reference for bounded
  investigation, not a Mantik return estimate.
- [Can ChatGPT Forecast Stock Price Movements?](https://arxiv.org/abs/2304.07619):
  Lopez-Lira and Tang; arXiv lists revisions through v6. A future reproduction
  must identify the exact manuscript version as well as the local PDF hash.
- [Sentiment trading with large language models](https://arxiv.org/abs/2412.19245):
  Kirtac and Germano. This confirms the tracker's correction of the earlier
  “Kim et al.” attribution. No accuracy or return claim is adopted here.

The NBER pairs-trading page returned HTTP 403 and the author's time-series
momentum PDF timed out during external retrieval. Their local tracker entries
and stored files remain references; those attempts did not verify their content.

## Source implementation candidates

All paths below are relative to the pinned current Augr revision. Source and
nearby test files exist; no test was run as part of this assessment. Names in the
candidate column describe possible Mantik work, not source-module imports.

| Augr source | Candidate reuse | Assumptions, dataset needs, and reproduction gap |
| --- | --- | --- |
| `internal/strategycatalog/version.go`, `legacy.go` | Version identity and explicit legacy-unvalidated mapping | Pins compiler/config/source/dataset kinds; avoid inheriting mutable legacy strategy authority. Adapt to Mantik's library contract before tests |
| `internal/dataset/manifest.go` | Richer observation/partition provenance | Effective/published/observed/available times, correction lineage, license and symbology. Extend Mantik fixture schema only with an explicit historical adapter |
| `internal/experimentrun/runner.go` | Pinned programs and append-only attempt/result evidence | Requires instrument, simulation, ledger, and capital graph. Reuse boundary design, not its repository/database bindings |
| `internal/researchworkflow/hypothesis.go` | Structured claims, source/search provenance, and refutation tests | Pinned parent evidence and model/prompt identity. Source examples use synthetic citations; do not treat them as recovered literature results |
| `internal/research/eventcalibration/evidence.go` | `forecast-calibration-v1` evaluation component | Forecast probabilities, observed outcomes, weights, and explicit timestamp; sanitizes inputs. Requires an explicit invalid-data policy and held-out labels |
| `internal/research/solverarb/model.go` | Offline complete-set cost/edge evaluator | Complete outcome set, ask prices, fees, size, and partial-fill haircut. Declared inputs are not execution proof; review outcome uniqueness and payout/settlement assumptions before porting |
| `internal/research/latency/simulator.go` | Latency sensitivity stress component | Assumed stale/reversal probabilities, resolution window, stake and loss cap. Heuristic sensitivity model; needs measured calibration before real-data inference |
| `internal/research/walletintel/score.go` | Attributed wallet-observation score | Realized ROI/trade count/calibration/recency/concentration with configurable weights and clock. Scoring assumptions and selection bias need tests; no wallet records imported |
| `internal/generativestrategy/spec.go` | Constrained candidate-specification design | Typed inputs/expressions, sizing, costs, capacity, tests and retirement. Needs worker isolation and mandatory evaluation; do not import execution/activation authority |
| `internal/optionsresearch/research.go` | Retrospective EOD provenance and limitations | Calendar plus licensed stock/options EOD records and session coverage. Its source declares retrospective results; no provider archives or reports were inspected |

Selected byte hashes make the inspection reproducible. Git-object hashes were
calculated from `git show REVISION:PATH`, independently of working-tree bytes.

| Source | SHA-256 |
| --- | --- |
| Current `LICENSE` | `3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986` |
| Current `internal/strategycatalog/version.go` | `d654b1024c5bca8bfd916fe1b6d5a1593ded7399e820bf4b83232bb70daa2f97` |
| Current `internal/dataset/manifest.go` | `8c41653cf59a8a16e009b02356cb20c8ebe252ffb8e02e048a5806ae21ee5dec` |
| Current `internal/research/eventcalibration/evidence.go` | `fe4c95ffb886d28429c1ebac243467edf558e88a46aeadd5c89928e0b1190786` |
| Current `internal/research/solverarb/model.go` | `ad089875b5a0a7ccbcc6fd34868bced247a2490ce08c95c69fd461c65a3c851a` |
| Historical `docs/research/cited-papers.md` at `c8b011692581672c7482c26ab1056df286eac299` | `7682e172931957f527c3a5708a267cf7ac75a5e30f38f70a6e8f90b555d3b2ab` |
| SUBCULT `Products/Augr/research/paper-tracker.md` | `2ef592f9457e5d1efebfcf9bc956b068a3c3fe70d791663cbd10329db9be7ac6` |
| SUBCULT `Products/Augr/research/deep-research-report.md` | `5b5acd33b5d66824fa6a850ebcb92212538b28f819404c91f411b70d15ca33c2` |

## Next implementation boundary

[Issue #6](https://git.subcult.tv/subculture-collective/mantik/issues/6) can use
this assessed inventory with the merged library and dataset contracts. Start by
declaring a small deterministic baseline recipe and its required tests. Record
its synthetic result as synthetic, preserve failed attempts, and compare exact
decisions across identical replays. A momentum-inspired rule is a candidate for
that baseline; it is not a reproduction of the cited momentum papers.

Any historical reproduction requires a selected licensed dataset, public
availability and revision contracts, exact instrument precision, declared
benchmarks/costs/windows, and the appropriate adapter. Preserve each original
paper/report and source hash outside Mantik Git. Import only reviewed references
or separately attributed source changes in their own bounded implementation.
