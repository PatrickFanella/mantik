# Research and design references

This library will connect literature, executable methods, assumptions, tests,
and retained experiment evidence. Implemented or published methods still need
qualification in Mantik's intended setting. Include unsuccessful experiments
and search history when assessing a candidate.

The October 13 rebuild discussion surveyed these upstream projects for design
ideas. These links are references, not adopted dependencies or verified trading
results:

- [LEAN](https://github.com/QuantConnect/Lean): interchangeable asset-selection,
  signal, portfolio, risk, and execution components.
- [NautilusTrader](https://github.com/nautechsystems/nautilus_trader): shared
  event-driven strategy semantics across replay and ongoing execution.
- [Freqtrade](https://github.com/freqtrade/freqtrade): strategy diagnostic tools,
  including lookahead and indicator warm-up analysis.
- [Qlib](https://github.com/microsoft/qlib): configurable research workflows,
  model examples, and separate signal/portfolio evaluation.
- [RD-Agent](https://github.com/microsoft/RD-Agent): research material to
  implementation, experimentation, and feedback.
- [Hummingbot](https://github.com/hummingbot/hummingbot): reusable controllers
  and terminal operation.
- [OpenBB](https://github.com/OpenBB-finance/OpenBB): shared data interfaces
  for applications and agent tools.
- [VectorBT](https://github.com/polakowo/vectorbt): fast candidate screening
  before more detailed execution evaluation.

The original Augr research corpus has not been recovered or imported. Preserve
its provenance and assess its relevance before adding library entries.
