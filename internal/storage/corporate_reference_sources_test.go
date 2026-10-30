package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/referencetest"
)

var referenceRequested = time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)

// Fixtures pass through real journals so custody binds exactly what a
// configured journal reader would supply.
func polygonReferenceFixture(t *testing.T, owner, id string) CorporateReferenceSourceCommand {
	t.Helper()
	j, err := polygonref.OpenJournal(filepath.Join(t.TempDir(), "polygon"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	spec := polygonref.Spec{ID: id, Method: polygonref.MethodDividends, Symbol: "SPY", Start: "2025-11-01", End: "2026-10-30", AccessRef: "manufactured-polygon-account"}
	body := []byte(`{"results":[{"cash_amount":1.888834,"currency":"USD","dividend_type":"CD","ex_dividend_date":"2026-09-18","id":"E` + strings.ReplaceAll(id, "-", "") + `","pay_date":"2026-10-30","ticker":"SPY"}],"status":"OK","request_id":"manufactured"}`)
	referencetest.PolygonCollection(t, j, spec, referenceRequested, body)
	_, a, bodies, err := j.Retained(id)
	if err != nil {
		t.Fatal(err)
	}
	return CorporateReferenceSourceCommand{ID: id, ReceiverID: owner, Kind: CorporateReferencePolygon, Polygon: &a, Originals: bodies}
}

func issuerReferenceFixture(t *testing.T, owner, id string, raw []byte) CorporateReferenceSourceCommand {
	t.Helper()
	j, err := issuerdoc.OpenJournal(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	spec := issuerdoc.Spec{ID: id, Method: issuerdoc.MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured-public-document"}
	referencetest.IssuerCollection(t, j, spec, referenceRequested, raw)
	_, a, original, err := j.Retained(id)
	if err != nil {
		t.Fatal(err)
	}
	return CorporateReferenceSourceCommand{ID: id, ReceiverID: owner, Kind: CorporateReferenceIssuer, Issuer: &a, Originals: [][]byte{original}}
}

type corporateReferenceBackend interface {
	CorporateReferenceSourceStore
	ResearchReceiverStore
	Store
}

func TestMemoryCorporateReferenceSources(t *testing.T) {
	m := NewMemory()
	corporateReferenceContracts(t, m)
	m.corporateReferenceOriginals["polygon-one"][0][0] ^= 1
	if _, err := m.CorporateReferenceSource(context.Background(), "polygon-one"); !errors.Is(err, ErrCorporateReferenceSourceInvalid) {
		t.Fatal("altered original accepted", err)
	}
}
func TestPostgresContractsCorporateReferenceSources(t *testing.T) {
	p := timerPostgres(t, "corporatereferences")
	corporateReferenceContracts(t, p)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_corporate_reference_sources SET originals=ARRAY[convert_to(replace(convert_from(originals[1],'UTF8'),'1.888834','1.888835'),'UTF8')] WHERE id='polygon-one'"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CorporateReferenceSource(ctx, "polygon-one"); !errors.Is(err, ErrCorporateReferenceSourceInvalid) {
		t.Fatal("altered original accepted", err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_corporate_reference_sources SET payload=jsonb_set(payload,'{coverage_complete}','true') WHERE id='issuer-one'"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CorporateReferenceSources(ctx); !errors.Is(err, ErrCorporateReferenceSourceInvalid) {
		t.Fatal("coverage claim injected into custody", err)
	}
}

func corporateReferenceContracts(t *testing.T, s corporateReferenceBackend) {
	t.Helper()
	ctx := context.Background()
	owner, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := referenceRequested.Add(time.Hour)
	clock := func() time.Time { return at }
	c := polygonReferenceFixture(t, owner.ID, "polygon-one")
	r, err := s.RetainCorporateReferenceSource(ctx, c, clock)
	if err != nil || r.Duplicate || r.Record.Sequence != 1 || r.Record.ParseStatus != "parsed" || r.Record.Snapshot == nil || len(r.Record.Snapshot.Dividends) != 1 || r.Record.Snapshot.Dividends[0].CashAmount != "1.888834" || r.Record.CoverageComplete || r.Record.QualificationCredit {
		t.Fatalf("%+v %v", r, err)
	}
	noClock := func() time.Time { t.Fatal("exact retry consulted the clock"); return time.Time{} }
	if retry, err := s.RetainCorporateReferenceSource(ctx, c, noClock); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r.Record) {
		t.Fatal("exact retry not idempotent", err)
	}
	other := polygonReferenceFixture(t, owner.ID, "polygon-two")
	changed := other
	changed.ID = c.ID
	if _, err := s.RetainCorporateReferenceSource(ctx, changed, noClock); !errors.Is(err, ErrCorporateReferenceSourceConflict) {
		t.Fatal("changed binding reused an identity", err)
	}
	alias := c
	alias.ID = "polygon-alias"
	if _, err := s.RetainCorporateReferenceSource(ctx, alias, noClock); !errors.Is(err, ErrCorporateReferenceSourceConflict) {
		t.Fatal("binding retained under a second identity", err)
	}
	wrong := other
	wrong.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err := s.RetainCorporateReferenceSource(ctx, wrong, noClock); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	tampered := other
	tampered.Originals = [][]byte{[]byte(strings.Replace(string(other.Originals[0]), "1.888834", "1.888835", 1))}
	early := func() time.Time { return referenceRequested }
	for name, tc := range map[string]struct {
		c     CorporateReferenceSourceCommand
		clock func() time.Time
	}{
		"altered bytes":     {tampered, clock},
		"missing originals": {CorporateReferenceSourceCommand{ID: other.ID, ReceiverID: other.ReceiverID, Kind: other.Kind, Polygon: other.Polygon}, clock},
		"mismatched kind":   {CorporateReferenceSourceCommand{ID: other.ID, ReceiverID: other.ReceiverID, Kind: CorporateReferenceIssuer, Polygon: other.Polygon, Originals: other.Originals}, clock},
		"before completion": {other, early},
	} {
		if _, err := s.RetainCorporateReferenceSource(ctx, tc.c, tc.clock); !errors.Is(err, ErrCorporateReferenceSourceInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
	issuer := issuerReferenceFixture(t, owner.ID, "issuer-one", []byte("not a workbook"))
	ir, err := s.RetainCorporateReferenceSource(ctx, issuer, clock)
	if err != nil || ir.Record.Sequence != 2 || ir.Record.PreviousSHA256 != r.Record.SHA256 || ir.Record.ParseStatus != "unparseable" || ir.Record.Document != nil || ir.Record.ParseError == "" {
		t.Fatalf("unparseable document not retained for inspection: %+v %v", ir, err)
	}
	cat, err := s.CorporateReferenceSources(ctx)
	if err != nil || cat.ReceiverID != owner.ID || len(cat.Records) != 2 || !reflect.DeepEqual(cat.Records[0], r.Record) {
		t.Fatal("catalog does not reproduce custody", err)
	}
	for _, id := range []string{"polygon-one", "issuer-one"} {
		if got, err := s.CorporateReferenceSource(ctx, id); err != nil || got.ID != id {
			t.Fatal("originals did not reverify", id, err)
		}
	}
	if _, err := s.CorporateReferenceSource(ctx, "missing"); !errors.Is(err, ErrCorporateReferenceSourceNotFound) {
		t.Fatal(err)
	}
}
