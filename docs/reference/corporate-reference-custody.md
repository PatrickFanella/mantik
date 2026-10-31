# Corporate reference source custody

[ADR-0003](../adr/0003-issuer-distribution-coverage.md) step 3 moves retained
[Polygon references](polygon-corporate-actions.md) and
[issuer documents](issuer-distribution-documents.md) from their private
journals into service custody. A custody record is retained source evidence. It
does not reconcile sources, establish coverage or apply an economic effect;
every authority flag on it is false.

## Configuration

`serve` opens each configured journal read-only for the service lifetime. Its
exclusive lock keeps collectors out while the service runs. A path and its
expected journal identity are both required:

```sh
./bin/mantik serve \
  --polygon-reference-journal /absolute/private/polygon-journal \
  --polygon-reference-journal-id polygon-journal-... \
  --issuer-document-journal /absolute/private/issuer-journal \
  --issuer-document-journal-id issuer-journal-...
```

Either kind may be configured alone. An identity mismatch stops startup. The
service never creates collections, fetches provider data or changes journal
artifacts.

## Import

`POST /v1/corporate-reference-imports` names a retained collection and pins its
binding hash:

```json
{
  "id": "polygon-spy-dividends",
  "receiver_id": "receiver-...",
  "kind": "polygon_reference",
  "collection_id": "spy-dividends-2026",
  "acquisition_sha256": "..."
}
```

`kind` is `polygon_reference` or `issuer_document`. The binding hash is the
`sha256` of the acquisition derived from the collection (see below). The request
cannot carry bytes, rows, a snapshot or a coverage flag; unknown fields return
422.

The service reads the collection from the journal configured for that kind and
accepts only a `retained` one. Interrupted, failed or inconsistent collections
return 409, a missing collection 404 and an unconfigured kind 422. A changed
pin, a reused identity with another binding, or one binding imported under two
identities returns 409. An exact retry returns the retained record with 200
without reading the journal, so it still works after the journal is closed.

Custody is bounded to 32 sources and 8 MiB of original bytes per source.
Records are hash-chained in retention order and share the source observation
clock with corporate-action sources: a retention time cannot precede the
collection's completion or an already retained decision, outcome or assessment
cut.

## What is retained

Each record holds:

- the acquisition binding: journal identity, spec, request and completion
  times, the intent and completion receipt hashes, and for Polygon every
  page's request URL, receipt time, body hash and receipt hash;
- the exact original bytes, stored beside the record;
- the parse rederived from those bytes at retention: a Polygon snapshot or an
  issuer document, or `parse_status: "unparseable"` with the parse error.

Every journal receipt hash is recomputable from the binding. Custody verifies
the chain and the original bytes without the journal, including that each
Polygon continuation is the URL the previous body named. Unparseable bytes are
retained for inspection, never partially used.

## Inspection

`GET /v1/corporate-reference-sources` lists identities, kinds, binding hashes,
parse status, byte counts and retention order, without parsed rows.

`GET /v1/corporate-reference-sources/{id}` rereads the stored originals,
reverifies them against the binding and rederives the parse. It returns the
record only when the result equals the retained record, with
`originals_verified: true`. Altered bytes or an edited record return 422. The
originals themselves are never returned: sponsor documents stay private.
