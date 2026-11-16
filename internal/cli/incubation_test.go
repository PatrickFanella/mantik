package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func TestIncubationCLIExplicitBoundary(t *testing.T) {
	var out, errOut bytes.Buffer
	if e := Run(context.Background(), []string{"incubation", "--help"}, &out, &errOut); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MANTIK_EMPTY_INCUBATION_DB", "")
	if e := Run(context.Background(), []string{"incubation", "--id", "book", "--database-env", "MANTIK_EMPTY_INCUBATION_DB"}, &out, &errOut); e == nil {
		t.Fatal("implicit database accepted")
	}
}
func TestIncubationInputPinsRecipeAndSource(t *testing.T) {
	cfg := library.Default(library.Trend)
	in := incubationInput{ID: "cycle", Strategies: []incubationStrategyInput{{SleeveID: "trend", Library: &cfg, Assets: []string{"SPY"}}}}
	before, e := incubationRequest(in)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Gross = 0.5
	after, e := incubationRequest(in)
	if e != nil || before.InputSHA256 == after.InputSHA256 {
		t.Fatal("changed recipe not pinned", e)
	}
	oldID, _ := before.Strategies[0].Strategy.Spec().ID()
	newID, _ := after.Strategies[0].Strategy.Spec().ID()
	if oldID == newID {
		t.Fatal("recipe identity unchanged")
	}
	in.Strategies[0].Library = nil
	if _, e = incubationRequest(in); e == nil {
		t.Fatal("missing compiled strategy accepted")
	}
}
func TestIncubationSourceCannotBackdateObservedBars(t *testing.T) {
	at := time.Now().UTC()
	b := storage.DailyBar{AssetID: "SPY", Symbol: "SPY", Date: at.AddDate(0, 0, -1).Format(time.DateOnly), Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 1, PublishedAt: at.Add(-time.Hour), ObservedAt: at, AvailableAt: at.Add(-time.Second)}
	src := incubationSource{incubationStrategyInput{Bars: map[string][]storage.DailyBar{"SPY": {b}}}}
	if _, e := src.AdjustedBars(context.Background(), "SPY", at); e == nil {
		t.Fatal("backdated observed bar accepted")
	}
	b.AvailableAt = at
	src.in.Bars["SPY"] = []storage.DailyBar{b}
	if _, e := src.AdjustedBars(context.Background(), "SPY", at); e != nil {
		t.Fatal(e)
	}
}
