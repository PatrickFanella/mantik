package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func TestIncubationQualificationInputUsesCompiledCustody(t *testing.T) {
	cfg := library.Default(library.Trend)
	in := incubationInput{ID: "qualification-cycle", Qualification: &forward.Collection{PlanID: "plan"}, Strategies: []incubationStrategyInput{{SleeveID: "trend", Library: &cfg, Assets: []string{"SPY"}}}}
	req, err := incubationRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil || !bytes.Equal(req.QualificationInput, raw) {
		t.Fatal("CLI qualification lost its canonical compiled input", err)
	}
	in.Strategies[0].AnalystCycleID = "other-authority"
	if _, err = incubationRequest(in); err == nil {
		t.Fatal("qualification accepted an analyst/library authority combination")
	}
	in.Strategies[0].AnalystCycleID = ""
	in.Qualification.DecisionSources = []json.RawMessage{json.RawMessage(`null`)}
	if _, err = incubationRequest(in); err == nil {
		t.Fatal("CLI qualification accepted caller decision evidence")
	}
}

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
	src := incubationSource{Input: incubationStrategyInput{Bars: map[string][]storage.DailyBar{"SPY": {b}}}}
	if _, e := src.AdjustedBars(context.Background(), "SPY", at); e == nil {
		t.Fatal("backdated observed bar accepted")
	}
	b.AvailableAt = at
	src.Input.Bars["SPY"] = []storage.DailyBar{b}
	if _, e := src.AdjustedBars(context.Background(), "SPY", at); e != nil {
		t.Fatal(e)
	}
}

func TestIncubationInputRetainsLegacyHashAndPinsForwardBinding(t *testing.T) {
	cfg := library.Default(library.Trend)
	in := incubationInput{ID: "legacy-cycle", Strategies: []incubationStrategyInput{{SleeveID: "trend", GateID: "legacy-gate", Risk: "0.02", Library: &cfg, Assets: []string{"SPY"}}}}
	legacy, e := incubationRequest(in)
	if e != nil {
		t.Fatal(e)
	}
	const hash = "a97776ecb526db1001163e1594db34dfcd498aa07175cdb4fded9f55ae1f07c6"
	if legacy.InputSHA256 != hash {
		t.Fatal("legacy retry hash changed", legacy.InputSHA256)
	}
	in.Strategies[0].ForwardDeclarationID = "forward-one"
	bound, e := incubationRequest(in)
	if e != nil || bound.InputSHA256 == hash || bound.Strategies[0].ForwardDeclarationID != "forward-one" {
		t.Fatal("binding not pinned", bound, e)
	}
	in.Strategies[0].ForwardDeclarationID = "forward-two"
	changed, e := incubationRequest(in)
	if e != nil || changed.InputSHA256 == bound.InputSHA256 {
		t.Fatal("changed binding not pinned", e)
	}
	in.Strategies[0].ForwardDeclarationID = ""
	retry, e := incubationRequest(in)
	if e != nil || retry.InputSHA256 != hash {
		t.Fatal("empty binding changed retry", retry, e)
	}
}
