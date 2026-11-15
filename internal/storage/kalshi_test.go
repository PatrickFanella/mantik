package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type kalshiBackend interface {
	RecordKalshiData(context.Context, []KalshiMarket, []KalshiOrderBook) (KalshiReceipt, error)
	KalshiBooksAsOf(context.Context, string, time.Time) ([]KalshiOrderBook, error)
	KalshiMarketsAsOf(context.Context, time.Time) ([]KalshiMarket, error)
}

func TestMemoryKalshiData(t *testing.T) { kalshiContracts(t, NewMemory()) }
func TestPostgresContractsKalshiData(t *testing.T) {
	p := timerPostgres(t, "kalshi")
	kalshiContracts(t, p)
	// Upgrade/recovery contracts replay additive migrations over existing tables.
	// Reinstallation must retain observations and keep every guard active.
	before, err := p.KalshiMarketsAsOf(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.pool.Exec(context.Background(), migrations.KalshiData); err != nil {
			t.Fatal("Kalshi migration replay", err)
		}
	}
	after, err := p.KalshiMarketsAsOf(context.Background(), time.Now())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration replay changed observations", err)
	}
	for _, table := range []string{"mantik_kalshi_markets", "mantik_kalshi_order_books"} {
		for _, sql := range []string{"UPDATE " + table + " SET sha256='invalid'", "DELETE FROM " + table, "TRUNCATE " + table} {
			if _, err := p.pool.Exec(context.Background(), sql); err == nil {
				t.Fatal("immutable table operation admitted", sql)
			}
		}
	}
}
func kalshiContracts(t *testing.T, s kalshiBackend) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	sum := sha256.Sum256([]byte(`{"orderbook":{"yes":[[60,10]]}}`))
	hash := hex.EncodeToString(sum[:])
	book := KalshiOrderBook{MarketTicker: "KXHIGH", Original: `{"orderbook":{"yes":[[60,10]]}}`, SHA256: hash, Book: []byte(`{"no_dollars":[["0.4000","1.00"]],"yes_dollars":[["0.6000","1.00"]]}`), ObservedAt: at}
	mraw := `{"ticker":"KXHIGH"}`
	msum := sha256.Sum256([]byte(mraw))
	seriesRaw := `{"series":{"ticker":"KXHIGH"}}`
	seriesSum := sha256.Sum256([]byte(seriesRaw))
	market := KalshiMarket{Ticker: "KXHIGH", SeriesTicker: "KXHIGH", SeriesOriginal: seriesRaw, SeriesSHA256: hex.EncodeToString(seriesSum[:]), EventTicker: "KXHIGH-26NOV15", ResolutionSource: "Weather", ResolutionURL: "https://weather.test", Status: "open", Original: mraw, SHA256: hex.EncodeToString(msum[:]), ObservedAt: at}
	if got, err := s.RecordKalshiData(ctx, []KalshiMarket{market}, []KalshiOrderBook{book}); err != nil || got != (KalshiReceipt{Markets: 1, Books: 1}) {
		t.Fatal(got, err)
	}
	if got, err := s.RecordKalshiData(ctx, []KalshiMarket{market}, []KalshiOrderBook{book}); err != nil || got.Unchanged != 2 {
		t.Fatal(got, err)
	}
	if got, err := s.KalshiBooksAsOf(ctx, "KXHIGH", at.Add(-time.Microsecond)); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := s.KalshiBooksAsOf(ctx, "KXHIGH", at); err != nil || len(got) != 1 || string(got[0].Book) != string(book.Book) {
		t.Fatal(got, err)
	}
	market.Status = "settled"
	market.ObservedAt = at.Add(time.Second)
	raw := `{"ticker":"KXHIGH","status":"settled"}`
	sum = sha256.Sum256([]byte(raw))
	market.Original = raw
	market.SHA256 = hex.EncodeToString(sum[:])
	if _, err := s.RecordKalshiData(ctx, []KalshiMarket{market}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.KalshiMarketsAsOf(ctx, at); err != nil || len(got) != 1 || got[0].Status != "open" {
		t.Fatal(got, err)
	}
	if got, err := s.KalshiMarketsAsOf(ctx, market.ObservedAt); err != nil || len(got) != 1 || got[0].Status != "settled" {
		t.Fatal(got, err)
	}
	// A later insertion with an earlier observation must remain visible at its
	// own clock but cannot replace the newer known state.
	earlier := market
	earlier.Status = "active"
	earlier.ObservedAt = at.Add(500 * time.Millisecond)
	raw = `{"ticker":"KXHIGH","status":"active"}`
	sum = sha256.Sum256([]byte(raw))
	earlier.Original = raw
	earlier.SHA256 = hex.EncodeToString(sum[:])
	if _, err := s.RecordKalshiData(ctx, []KalshiMarket{earlier}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.KalshiMarketsAsOf(ctx, earlier.ObservedAt); err != nil || got[0].Status != "active" {
		t.Fatal(got, err)
	}
	// An identity can only be retried byte-for-byte.
	conflict := earlier
	conflict.Status = "changed"
	raw = `{"ticker":"KXHIGH","status":"changed"}`
	sum = sha256.Sum256([]byte(raw))
	conflict.Original = raw
	conflict.SHA256 = hex.EncodeToString(sum[:])
	if _, err := s.RecordKalshiData(ctx, []KalshiMarket{conflict}, nil); err == nil {
		t.Fatal("changed timestamp identity admitted")
	}
	// Invalid later input cannot leave the valid first record half-written.
	bad := market
	bad.Ticker = "bad/ticker"
	if _, err := s.RecordKalshiData(ctx, []KalshiMarket{market, bad}, nil); err == nil {
		t.Fatal("invalid batch admitted")
	}
	if got, err := s.KalshiMarketsAsOf(ctx, market.ObservedAt); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	// A valid fresh record preceding a retained-identity conflict must roll back.
	fresh := market
	fresh.Ticker = "KXNEW"
	if _, err := s.RecordKalshiData(ctx, []KalshiMarket{fresh, conflict}, nil); err == nil {
		t.Fatal("conflicting batch admitted")
	}
	if got, err := s.KalshiMarketsAsOf(ctx, market.ObservedAt); err != nil || len(got) != 1 {
		t.Fatal("partial batch committed", got, err)
	}
	// JSON spacing is canonicalized before PostgreSQL jsonb storage.
	spaced := book
	spaced.Book = []byte(`{ "yes_dollars": [["0.6000","1.00"]], "no_dollars": [["0.4000","1.00"]] }`)
	if _, err := s.RecordKalshiData(ctx, nil, []KalshiOrderBook{spaced}); err != nil {
		t.Fatal("formatted exact retry conflicts", err)
	}
	// Returned books must not alias retained data.
	got, err := s.KalshiBooksAsOf(ctx, "KXHIGH", at)
	if err != nil {
		t.Fatal(err)
	}
	got[0].Book[0] = 'X'
	again, err := s.KalshiBooksAsOf(ctx, "KXHIGH", at)
	if err != nil || again[0].Book[0] == 'X' {
		t.Fatal(again, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.RecordKalshiData(ctx, nil, []KalshiOrderBook{book}); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
