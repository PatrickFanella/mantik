package trial

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
)

func fixture(t *testing.T) (Command, hypothesis.Publication, time.Time) {
	t.Helper()
	sealed, err := hypothesis.Seal(hypothesis.Fixture())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 17, 0, 0, 0, time.UTC)
	p := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-abcdefghijklmnop", PublishedAt: at, OriginBindingVerified: true, Sealed: sealed}
	catalog, err := experimentplan.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r, err := experimentplan.Seal(experimentplan.Recipe{Schema: experimentplan.RecipeSchema, ID: "trial-plan", Version: "v1", DatasetID: "data", DatasetVersion: "v1", DatasetSHA256: strings.Repeat("a", 64), WindowStart: at, WindowEnd: at.Add(time.Hour), Cutoffs: []time.Time{at, at.Add(time.Hour)}, RequiredKeys: []string{"first", "last"}, Methods: catalog.Methods[:1], Benchmarks: catalog.Methods[2:], CapitalCents: 100000, CostModel: experimentplan.CostModel, PlannedMetrics: experimentplan.Metrics()})
	if err != nil {
		t.Fatal(err)
	}
	return Command{ReceiverID: p.ReceiverID, RequestID: "trial", PublicationID: p.ID, Recipe: r}, p, at
}
func TestTrialBindingAndScope(t *testing.T) {
	c, p, at := fixture(t)
	r, err := Register(c, p, nil, at)
	if err != nil || r.Validate() != nil || r.PublicationSHA256 != Hash(p) {
		t.Fatal(r, err)
	}
	for _, name := range []string{"receiver", "publication", "declaration", "publication-hash", "clock", "horizon", "method", "benchmark", "recipe-hash", "missing-parent", "self-parent", "other-question-parent"} {
		t.Run(name, func(t *testing.T) {
			c, p, at := fixture(t)
			var parent *Registration
			switch name {
			case "receiver":
				c.ReceiverID = "receiver-other"
			case "publication":
				c.PublicationID = "other"
			case "declaration":
				p.Sealed.Declaration.HorizonSeconds++
			case "publication-hash":
				p.Sealed.DeclarationSHA256 = strings.Repeat("a", 64)
			case "clock":
				at = at.Add(-time.Second)
			case "horizon":
				c.Recipe.WindowEnd = c.Recipe.WindowEnd.Add(-time.Second)
				c.Recipe.Cutoffs[1] = c.Recipe.WindowEnd
				c.Recipe, _ = experimentplan.Seal(c.Recipe)
			case "method":
				catalog, _ := experimentplan.BuiltinCatalog()
				c.Recipe.Methods = catalog.Methods[1:2]
				c.Recipe, _ = experimentplan.Seal(c.Recipe)
			case "benchmark":
				c.Recipe.Benchmarks = c.Recipe.Benchmarks[:1]
			case "recipe-hash":
				c.Recipe.SHA256 = strings.Repeat("a", 64)
			case "missing-parent":
				c.ParentID = "parent"
			case "self-parent":
				c.ParentID = c.RequestID
			case "other-question-parent":
				base, _ := Register(c, p, nil, at)
				base.HypothesisID = "hypothesis-" + strings.Repeat("a", 64)
				base.SHA256 = ""
				base.SHA256 = Hash(base)
				parent = &base
				c.RequestID = "child"
				c.ParentID = base.ID
			}
			if _, err := Register(c, p, parent, at); err == nil {
				t.Fatal("invalid trial registered")
			}
		})
	}
}
func TestTrialStrictCommand(t *testing.T) {
	c, _, _ := fixture(t)
	raw, _ := json.Marshal(c)
	if _, err := Read(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(raw) + "{}", strings.Replace(string(raw), `"request_id":`, `"request_id":"forged","request_id":`, 1), strings.Replace(string(raw), `"recipe":`, `"qualification_credit":true,"recipe":`, 1), strings.Replace(string(raw), `"recipe":`, `"Recipe":`, 1), strings.Repeat(" ", MaxBytes+1)} {
		if _, err := Read(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
}
