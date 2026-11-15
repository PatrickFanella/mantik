# Alpaca stock paper adapter

`mantik alpaca-paper` provides a separate, fixed-paper-endpoint adapter for
simple stock day orders. The existing `alpaca.Client` remains market-read only.
This adapter is a manual integration path for #59. Capped-book allocation,
risk admission and automatic strategy execution still belong to #71–#77.

The credential file is current-user mode 0600 JSON containing either `key`
and `secret`, or `oauth_token`. The command requires the expected paper
`--account-id` and an absolute private `--journal` directory. Every operation
checks `/v2/account` against that account and the journal's immutable binding.
Blocked or inactive accounts fail closed. Redirects are refused. No live
origin or endpoint override is exposed.

## Submit and recover

`--action submit --input` takes a JSON order with `client_order_id`, `symbol`,
`qty`, `side`, `type`, `time_in_force` and, for limits, `limit_price`. Supported
types are market and limit, with day time in force and exact decimal strings.
The asset endpoint must identify a tradable US equity.

The journal owns a process lock. It fsyncs the order intent before the only
POST. Every later invocation with the same client ID does only a lookup using
`/v2/orders:by_client_order_id`, including after a process exit or HTTP 404.
Research-journal rollover retains the paper account binding and every order
intent in the active directory, including when cleanup resumes on reopen.
Raw response files are exported with the other working evidence. These
permanent submission fences cannot be retired by rollover.
An unavailable lookup leaves the submission unresolved. It never authorizes
another POST. A different order under that ID conflicts. Do not use a fresh
client ID to work around an unresolved submission.

Raw HTTP responses (including errors and partial bodies) are stored with
method, path, status, observation time, digest and original bytes before any
normalization. Retention failures stop the operation. Order responses must
match the retained intent. The journal keeps credentials out of its records;
account and order records remain private financial data outside Git.

`--action cancel --client-order-id` looks up a previously declared order and
requests cancellation by venue ID. Cancellation acknowledgement is not final
cancellation; read the order again with its retained submit input to confirm.

## Fills and reconciliation

`--action fills --after` takes an explicit RFC3339 lower bound, retains the raw
fill activities, and paginates up to 100 pages of 100 records. It returns the
original fill objects, including quantities, prices and any venue fee fields.
It does not infer fills from order status or assume missing fees are zero.

`--action reconcile --input` takes a JSON map from venue asset ID to signed
share quantity for the **whole account**. It returns every missing, unexpected
or unequal quantity, using exact rational comparison. An empty result means
the positions matched that snapshot. It does not establish ledger equity,
cash, pending-order or fee reconciliation. Other books in the same paper
account must be included explicitly; they are never canceled or adjusted.

## Evidence limits

Loopback tests exercise ambiguous transport/HTTP results, restart recovery,
immutable identity, raw-before-decode retention and signed share differences.
A manual placed/canceled paper order is adapter evidence, not protected
positions, a capped strategy book, profitability or forward qualification.

Provider contracts: [create order](https://docs.alpaca.markets/us/reference/postorder)
and [lookup by client ID](https://docs.alpaca.markets/us/reference/getorderbyclientorderid).

On 2026-11-15 on Kvant, the adapter used the existing paper OAuth profile to
place one SPY day buy limit for one share at $1, read back `accepted`, cancel
that exact declared order, and recover it by the original client ID with
status `canceled`. Its full-account position comparison returned zero
quantity differences against the preflight snapshot. The order did not fill.
Private original responses and the result receipt are retained under
`/tmp/mantik-wave1-paper-evidence/`; no credential or account response is in
Git. This is order-submission and reconciliation evidence only, not the
later protected-position exit check #107.
