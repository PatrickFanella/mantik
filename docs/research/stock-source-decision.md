# Wave 7 stock source decision

Decision date: 2026-11-15. Delivery: one combined PR for the remaining Wave 7
issues, as requested by the owner.

Use configured-source ports built from pinned MIT bt/ffn algorithms. The
[stock-library reference](../reference/stock-library.md) records the exact
recipes, revisions, source algorithms, research papers and adaptations. The
retained executable Python reference calls those upstream algorithms; it does
not call the Go implementation. Seven cases bind the full config/hash, library
version, input hash and source revisions, covering all shipped defaults and
separate two-name cross-sectional selection cases.

The issues require same-input source-trade reproduction. The implementation
interprets that as reproduction of each explicitly configured source recipe
and its declared next-session execution adapter. It does **not** claim the
trades match an unchanged complete external strategy application, nor that it
reproduces a paper's empirical results. That stronger requirement would call
for different source strategies and, in several cases, different trading rules.
The owner explicitly selected “Use the reviewed recipe ports and their pinned
reference tests” during Wave 7 implementation. This records that acceptance
choice; it does not approve the stronger external-application equivalence claim.

## Sources considered

| Source | Revision/license | Finding |
| --- | --- | --- |
| pmorissette/bt | `763ae01047fce2d09411faeba436855ecf8b679c`, MIT | Executable total/multi-period return, selection, inverse-volatility and equal-weight algorithms; selected as the configured reference. |
| pmorissette/ffn | `eb9354c5c82dbada5645569dc34e5861d156e471`, MIT | Return/risk primitives used by bt; selected and pinned in the reference environment. |
| QuantConnect/Tutorials | `4a341890296f7e79e095508f06170c72ccaa629c`, Apache-2.0 | Useful methodological examples, but algorithm pages embed remote backtests instead of committing their complete executable code. The 12-month futures trend, long/short reversal and low-vol examples differ from the requested recipes. |
| lukefreyman-ai/tsmom-etf | `a169685adcc32cbf3d33b8b82ed75908f13da815`, MIT | Complete strategy and backtester, but long-only 12-1 ETF logic, SMA filter, volatility target and position caps produce a different strategy. |
| kieranjwood/slow-momentum-fast-reversion | `b22006066c274cd5953446baf24b99a2726068e4`, MIT | Research/factor helpers do not provide complete runnable versions of all four requested strategies. |

Full-strategy alternatives were reviewed for fidelity, not selected on their
published performance. Replacing the requested rules with one of those
applications would not establish parity for the roadmap's original recipes.

## Evidence boundary

Reference generation uses the retained adjusted three-ETF input, synthetic
membership/clocks, zero costs and a fractional-share next-open adapter. It
checks every decision and trade under those declared conditions. The normal
engine keeps its own cost/account tests; gates, minimum viable book size,
forward qualification and strategy activation remain later issues. The
large-cap reversal universe still requires a reviewed large-cap input source;
the ETF numerical fixture does not establish that data coverage.
