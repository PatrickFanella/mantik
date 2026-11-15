package strategy

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

type universeSource struct {
	memorySource
	report marketdata.UniverseReport
}

func (s universeSource) StockUniverse(context.Context, time.Time) (marketdata.UniverseReport, error) {
	return s.report, nil
}
func TestUniverseIsDeclaredPointInTimeAndBoundedByConfiguredAssets(t *testing.T) {
	at := day(7, 22)
	s := spec
	s.Data = []string{KindDailyBars, KindStockUniverse}
	r := marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: day(1, 0), Members: []marketdata.UniverseMember{{AssetID: "asset-spy", LastSession: "2026-01-06"}, {AssetID: "asset-unconfigured", LastSession: "2026-01-06"}}}
	src := universeSource{memorySource{store(t)}, r}
	v, e := Build(context.Background(), s, src, at, []string{"asset-spy", "asset-qqq"}, Book{})
	if e != nil {
		t.Fatal(e)
	}
	eligible := v.Universe()
	if len(eligible) != 1 || eligible[0] != "asset-spy" {
		t.Fatal(eligible)
	}
	eligible[0] = "edited"
	if v.Universe()[0] != "asset-spy" {
		t.Fatal("mutable universe")
	}
	stockOnly, e := Build(context.Background(), spec, src, at, []string{"asset-spy"}, Book{})
	if e != nil || len(stockOnly.Universe()) != 0 {
		t.Fatal("undeclared universe", e)
	}
	for name, change := range map[string]func(*marketdata.UniverseReport){
		"future reference": func(r *marketdata.UniverseReport) { r.ReferenceObservedAt = at.Add(time.Second) },
		"wrong clock":      func(r *marketdata.UniverseReport) { r.AsOf = at.Add(time.Second) },
		"duplicate":        func(r *marketdata.UniverseReport) { r.Members = append(r.Members, r.Members[0]) },
		"same session":     func(r *marketdata.UniverseReport) { r.Members[0].LastSession = "2026-01-07" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r
			bad.Members = append([]marketdata.UniverseMember(nil), r.Members...)
			change(&bad)
			if _, e := Build(context.Background(), s, universeSource{src.memorySource, bad}, at, []string{"asset-spy"}, Book{}); e == nil {
				t.Fatal("invalid universe accepted")
			}
		})
	}
	if _, e := Build(context.Background(), s, src.memorySource, at, []string{"asset-spy"}, Book{}); e == nil {
		t.Fatal("missing capability accepted")
	}
	if _, e := Build(context.Background(), s, universeSource{src.memorySource, marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: at.Add(time.Second)}}, at, []string{"asset-spy"}, Book{}); !errors.Is(e, ErrFutureData) {
		t.Fatal(e)
	}
}
