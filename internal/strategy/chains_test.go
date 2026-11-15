package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type chainSource struct {
	memorySource
	chain options.Chain
	reads int
}

func (s *chainSource) OptionChain(context.Context, string, time.Time) (options.Chain, error) {
	s.reads++
	return s.chain, nil
}

func TestOptionChainViewLoadsDeclaredDataAndCopiesEntries(t *testing.T) {
	ctx := context.Background()
	m := store(t)
	at := day(7, 22)
	r := storage.OptionRecord{Kind: "contract", Symbol: "SPY260116C00100000", Underlying: "SPY", Key: "id", Feed: "reference", OccurredAt: at, ObservedAt: at, AvailableAt: at, Data: json.RawMessage(`{"id":"id","symbol":"SPY260116C00100000","underlying_symbol":"SPY","expiration_date":"2026-01-16","strike_price":"100","type":"call","size":"100"}`)}
	b := r
	b.Kind = "bar"
	b.Key = "2026-01-07"
	b.OccurredAt = day(7, 5)
	b.Feed = "indicative"
	b.FeedSelection = "provider_default_unverified"
	b.Data = json.RawMessage(`{"c":2}`)
	if _, err := m.RecordOptionData(ctx, []storage.OptionRecord{r, b}); err != nil {
		t.Fatal(err)
	}
	c, err := options.Read(ctx, m, options.Spot{AssetID: "asset-spy", Underlying: "SPY", Date: "2026-01-07", Price: 100, At: day(7, 21), ObservedAt: at, AvailableAt: at}, at, options.Assumptions{Rate: .03, MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	src := &chainSource{memorySource: memorySource{m}, chain: c}
	stockOnly, err := Build(ctx, spec, src, at, []string{"asset-spy"}, Book{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stockOnly.OptionChain("asset-spy"); ok || src.reads != 0 {
		t.Fatal("undeclared chains loaded")
	}
	s := spec
	s.Data = []string{KindDailyBars, KindOptionChains}
	v, err := Build(ctx, s, src, at, []string{"asset-spy"}, Book{})
	if err != nil {
		t.Fatal(err)
	}
	chain, ok := v.OptionChain("asset-spy")
	if !ok || len(chain.Entries) != 1 || chain.Entries[0].EstimateStatus != "ok" || len(v.Bars("asset-spy")) != 3 {
		t.Fatal("view", chain)
	}
	src.chain.Entries[0].Close = 999
	chain.Entries[0].Close = 998
	got, _ := v.OptionChain("asset-spy")
	if got.Entries[0].Close != 2 {
		t.Fatal("mutable chain")
	}
	s.Data = []string{KindOptionChains}
	if _, err := Build(ctx, s, memorySource{m}, at, []string{"asset-spy"}, Book{}); err == nil {
		t.Fatal("missing chain capability admitted")
	}
	for name, change := range map[string]func(*options.Chain){
		"wrong clock":      func(c *options.Chain) { c.AsOf = c.AsOf.Add(time.Second) },
		"wrong asset":      func(c *options.Chain) { c.AssetID = "asset-qqq" },
		"wrong feed":       func(c *options.Chain) { c.Feed = "opra" },
		"future reference": func(c *options.Chain) { c.Entries[0].ReferenceAvailableAt = at.Add(time.Second) },
		"future price":     func(c *options.Chain) { c.Entries[0].PriceAvailableAt = at.Add(time.Second) },
		"future spot":      func(c *options.Chain) { c.Spot.AvailableAt = at.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := got
			bad.Entries = append([]options.Entry(nil), got.Entries...)
			change(&bad)
			src.chain = bad
			if _, err := Build(ctx, s, src, at, []string{"asset-spy"}, Book{}); !errors.Is(err, ErrFutureData) {
				t.Fatal("leaky source admitted", err)
			}
		})
	}
	stockID, _ := spec.ID()
	chainID, err := s.ID()
	if err != nil || stockID == chainID {
		t.Fatal("chain kind missing from identity", err)
	}
}
