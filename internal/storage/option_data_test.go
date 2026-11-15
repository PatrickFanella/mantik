package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type optionBackend interface {
	RecordOptionData(context.Context, []OptionRecord) (OptionReceipt, error)
	OptionDataAsOf(context.Context, string, time.Time) ([]OptionRecord, error)
	OptionChainAsOf(context.Context, string, time.Time) ([]OptionRecord, error)
}

func TestMemoryOptionData(t *testing.T) { optionContracts(t, NewMemory()) }
func TestPostgresContractsOptionData(t *testing.T) {
	p := timerPostgres(t, "options")
	optionContracts(t, p)
	for range 2 {
		if _, err := p.pool.Exec(context.Background(), migrations.OptionData); err != nil {
			t.Fatal("migration replay", err)
		}
	}
	for _, sql := range []string{"UPDATE mantik_option_data SET feed='opra'", "DELETE FROM mantik_option_data", "TRUNCATE mantik_option_data"} {
		if _, err := p.pool.Exec(context.Background(), sql); err == nil {
			t.Fatal("immutable observation changed", sql)
		}
	}
}

func optionContracts(t *testing.T, s optionBackend) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2024, 2, 3, 0, 0, 0, 123456000, time.UTC)
	r := OptionRecord{Kind: "bar", Symbol: "SPY240202C00490000", Underlying: "SPY", Key: "2024-02-01", Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: at.Add(-48 * time.Hour), ObservedAt: at, AvailableAt: at, Data: json.RawMessage(`{"c":2.123}`)}
	got, err := s.RecordOptionData(ctx, []OptionRecord{r})
	if err != nil || got.Inserted != 1 {
		t.Fatal(got, err)
	}
	got, err = s.RecordOptionData(ctx, []OptionRecord{r})
	if err != nil || got.Unchanged != 1 || got.Inserted != 0 {
		t.Fatal(got, err)
	}
	old, err := s.OptionDataAsOf(ctx, r.Symbol, at.Add(-time.Microsecond))
	if err != nil || len(old) != 0 {
		t.Fatal(old, err)
	}
	changed := r
	changed.AvailableAt = at.Add(time.Hour)
	changed.ObservedAt = changed.AvailableAt
	changed.Data = json.RawMessage(`{"c":1.5}`)
	if _, err := s.RecordOptionData(ctx, []OptionRecord{changed}); err != nil {
		t.Fatal(err)
	}
	old, err = s.OptionDataAsOf(ctx, r.Symbol, at)
	if err != nil || len(old) != 1 || string(old[0].Data) != `{"c":2.123}` {
		t.Fatal(old, err)
	}
	latest, err := s.OptionDataAsOf(ctx, r.Symbol, changed.AvailableAt)
	if err != nil || len(latest) != 1 || string(latest[0].Data) != `{"c":1.5}` {
		t.Fatal(latest, err)
	}
	conflict := r
	conflict.Data = json.RawMessage(`{"c":99}`)
	fresh := r
	fresh.Key = "2024-02-02"
	if _, err := s.RecordOptionData(ctx, []OptionRecord{fresh, conflict}); !errors.Is(err, ErrOptionData) {
		t.Fatal("conflict admitted", err)
	}
	latest, err = s.OptionDataAsOf(ctx, r.Symbol, changed.AvailableAt)
	if err != nil || len(latest) != 1 {
		t.Fatal("failed page partly stored", latest, err)
	}
	invalid := r
	invalid.Feed = "opra"
	if _, err := s.RecordOptionData(ctx, []OptionRecord{invalid}); err == nil {
		t.Fatal("OPRA label accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { _, err := s.RecordOptionData(ctx, []OptionRecord{fresh}); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	latest, err = s.OptionDataAsOf(ctx, r.Symbol, changed.AvailableAt)
	if err != nil || len(latest) != 2 {
		t.Fatal("concurrent replay duplicated", latest, err)
	}
	// Discovery must choose price chronology before correction chronology and
	// filter availability before selecting a reference or market revision.
	chain, err := s.OptionChainAsOf(ctx, "SPY", at.Add(-time.Microsecond))
	if err != nil || len(chain) != 0 {
		t.Fatal("future chain", chain, err)
	}
	chain, err = s.OptionChainAsOf(ctx, "SPY", changed.AvailableAt)
	if err != nil || len(chain) != 1 || string(chain[0].Data) != `{"c":1.5}` {
		t.Fatal("chain correction", chain, err)
	}
	next := r
	next.Key = "2024-02-03"
	next.OccurredAt = r.OccurredAt.Add(24 * time.Hour)
	next.Data = json.RawMessage(`{"c":3}`)
	if _, err := s.RecordOptionData(ctx, []OptionRecord{next}); err != nil {
		t.Fatal(err)
	}
	lateOld := changed
	lateOld.AvailableAt = at.Add(2 * time.Hour)
	lateOld.ObservedAt = lateOld.AvailableAt
	lateOld.Data = json.RawMessage(`{"c":99}`)
	ref := r
	ref.Kind = "contract"
	ref.Feed = "reference"
	ref.FeedSelection = ""
	ref.Key = "id"
	ref.OccurredAt = at
	ref.Data = json.RawMessage(`{"id":"id"}`)
	other := r
	other.Symbol = "QQQ240202C00490000"
	other.Underlying = "QQQ"
	if _, err := s.RecordOptionData(ctx, []OptionRecord{lateOld, ref, other}); err != nil {
		t.Fatal(err)
	}
	chain, err = s.OptionChainAsOf(ctx, "SPY", lateOld.AvailableAt)
	if err != nil || len(chain) != 2 || chain[0].Kind != "bar" || string(chain[0].Data) != `{"c":3}` || chain[1].Kind != "contract" {
		t.Fatal("chain chronology", chain, err)
	}
	chain[0].Data[0] = 'x'
	again, err := s.OptionChainAsOf(ctx, "SPY", lateOld.AvailableAt)
	if err != nil || string(again[0].Data) != `{"c":3}` {
		t.Fatal("mutable chain storage", again, err)
	}
}
