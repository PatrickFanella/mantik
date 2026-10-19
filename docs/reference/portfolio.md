# Interactive fixture portfolio supervision

```sh
./bin/mantik supervise --view portfolio --limit 10 \
  --api-url http://127.0.0.1:40880
```

This read-only view shows the account activated through the
[explicit fixture-capital binding](fixture-capital.md#explicit-public-binding-and-readback).
It displays available cash, reserved cash, position valued at entry cost,
realized fixture PnL, opening funding and the committed-capital ceiling, all in
integer USD cents. An open position includes its quantity, entry price, case,
strategy version and entry action identity. Text is quoted or JSON-escaped for
terminal display.

The heading identifies a synthetic fixture portfolio. Its zero-cost quote fills
and realized fixture PnL carry no strategy qualification or market-performance
credit. General allocation across competing proposals remains incomplete.

## Reads and validation

The viewer reads `GET /v1/fixture-capital` and the selected immutable account
receipt. It shares the standalone `fixture-capital` command's checks for account
pins, schema, authority flags, bounded amounts, conservation, ordered identities,
position/commitment consistency and digest. The capital response is limited to
256 KiB and the funding receipt to 16 KiB. At most 256 compact commitments are
retained locally; the viewer does not load the observation/audit ledger, private
checkpoint transitions or research files.

A 404 with the explicit `fixture_capital_unbound` code causes a bounded account
catalogue read to verify the receiver identity. The view then displays `UNBOUND`
without substituting opening funding for an operational balance. An unknown or
older endpoint's generic 404, failed authentication, unavailable store, invalid
capital or invalid funding receipt is a failed read, not an unbound portfolio.
The account catalogue is limited to its existing 16-account/256-KiB contract.

Only the initial load and `r` fetch data. `READBACK OK` identifies the last
successful on-demand read; it is not a continuous worker-health check. The worker
may continue trading while the display waits for input. Refresh to read later
capital changes.

## Navigation

Use these keys followed by Enter:

| Command | Behavior |
| --- | --- |
| `n` / `p` | Next / previous local commitment page |
| `r` | Read and validate fresh capital/funding, returning to the first page |
| `d ACTION_ID` | Print the selected displayed commitment as structured JSON |
| `q` or EOF | Close the terminal viewer |

`--limit` is 1 through 100. Details are limited to identities on the displayed
page and preserve the commitment's decision, intent/effect digests, amount,
status and reason code. Paging, detail reads and unknown commands make no HTTP
requests. The view has no binding, reset, allocation, execution or recovery
mutation command.

## Stale state, ownership and disconnects

A failed refresh keeps a detached copy of the last validated data, labels it
`STALE`, and blocks navigation/details until a valid refresh succeeds. An initial
failed read still permits refresh, quit and EOF. Error text from a remote server
is not rendered into the terminal. Successful refresh resets the page offset.

The first valid read pins the receiver for this viewer session. The first valid
bound read also pins the account ID, funding digest and durability declaration.
An initially unbound receiver can become explicitly bound elsewhere and appear
after refresh. A changed receiver, changed bound account/funding, removed binding
or changed durability marks the retained display stale. Quit and reconnect to
accept a different owner; refreshing can retry the original owner. Data from
different account owners is never combined into one displayed portfolio.

The viewer owns a separate Unix stdin handle and uses the shared supervision
cancellation path. Quit/EOF closes the viewer; SIGINT cancels its input/request
and returns cancellation. The service and its independent workers continue.
Reconnecting starts a fresh read and ownership pin. No background polling,
lease acquisition or worker shutdown occurs in the terminal process.

## Verification and scope

Tests exercise HTTP/command parity, local page/details without requests,
unbound-to-bound activation elsewhere, failed refresh/reconnect, retained-data
detachment, invalid responses, changed owner/account/durability, generic 404
handling and cancellation of blocked input. Run `make verify` and
`make test-race`, plus an isolated native terminal journey with the actual
service worker. Keep terminal transcripts and generated runtime evidence outside
Git.

This supplies a bounded fixture portfolio view for issue #22. It does not close
issue #19's competing-proposal allocation criteria or establish qualified,
realistic paper execution. Existing pilot accounts/services are not upgraded or
bound by opening the viewer.
