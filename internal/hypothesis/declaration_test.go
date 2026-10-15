package hypothesis

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestDeclarationIdentityAndExactQuestionMatching(t *testing.T) {
	d := Fixture()
	original, _ := json.Marshal(d)
	first, err := Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "hypothesis-"+first.QuestionSHA256 || first.QualificationCredit || first.OriginsVerified || first.Authority != "research_declaration_only" || len(first.Methods) != 1 {
		t.Fatal(first)
	}
	if first.Methods[0].Strategy != library.FixtureStrategy() {
		t.Fatal("wrong native config")
	}
	again, err := Seal(d)
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatal("nondeterministic declaration", err)
	}
	d.Label = "another-label"
	d.Origins[0].Reference = "retained:another-reference"
	another, err := Seal(d)
	if err != nil || another.QuestionSHA256 != first.QuestionSHA256 || another.DeclarationSHA256 == first.DeclarationSHA256 {
		t.Fatal("matching key lost or supporting declaration rewritten", err)
	}
	d.Mechanism.Statement = "A different proposed mechanism."
	different, err := Seal(d)
	if err != nil || different.QuestionSHA256 == first.QuestionSHA256 {
		t.Fatal("mechanism change reused identity", err)
	}
	var source Declaration
	_ = json.Unmarshal(original, &source)
	if source.Mechanism.Statement != Fixture().Mechanism.Statement {
		t.Fatal("source unexpectedly changed")
	}
	first.Methods[0].Entry.Inputs[0].Source = "mutated"
	first.Declaration.Methods[0].SHA256 = "mutated"
	actual, err := Seal(Fixture())
	if err != nil || actual.Methods[0].Entry.Inputs[0].Source != "fixture" || actual.Declaration.Methods[0].SHA256 == "mutated" {
		t.Fatal("caller mutated native pin", err)
	}
}
func TestCanonicalSetsAndConflictingSources(t *testing.T) {
	d := Fixture()
	half, _, err := PinMethod(library.HalfSizeID)
	if err != nil {
		t.Fatal(err)
	}
	d.Methods = append(d.Methods, half)
	other := d.Origins[0]
	other.ClaimKey = "news-" + news.SHA256("another-claim")
	other.Reference = "artifact:second"
	d.Origins = append(d.Origins, other)
	first, err := Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Methods[0], d.Methods[1] = d.Methods[1], d.Methods[0]
	d.Origins[0], d.Origins[1] = d.Origins[1], d.Origins[0]
	before, _ := json.Marshal(d)
	again, err := Seal(d)
	after, _ := json.Marshal(d)
	if err != nil || !reflect.DeepEqual(first, again) || !bytes.Equal(before, after) {
		t.Fatal("set order changed identity or caller data", err)
	}
	d.Origins[1].ClaimKey = d.Origins[0].ClaimKey
	if _, err = Seal(d); err == nil {
		t.Fatal("duplicate source claim accepted")
	}
}
func TestDeclarationRejectsIncompleteOrPromotedContracts(t *testing.T) {
	tests := map[string]func(*Declaration){
		"schema": func(d *Declaration) { d.Schema = "future" }, "label": func(d *Declaration) { d.Label = "" }, "qualified": func(d *Declaration) { d.EvidenceClass = "qualified" }, "real_class": func(d *Declaration) { d.EvidenceClass = "prospective_market_unqualified" }, "instrument": func(d *Declaration) { d.Instrument = "SPY" }, "mechanism": func(d *Declaration) { d.Mechanism.Statement = "" }, "long_mechanism": func(d *Declaration) { d.Mechanism.Statement = strings.Repeat("x", 1025) }, "zero_horizon": func(d *Declaration) { d.HorizonSeconds = 0 }, "unbounded_horizon": func(d *Declaration) { d.HorizonSeconds = 86401 }, "missing_method": func(d *Declaration) { d.Methods = nil }, "duplicate_method": func(d *Declaration) { d.Methods = append(d.Methods, d.Methods[0]) }, "changed_pin": func(d *Declaration) { d.Methods[0].SHA256 = news.SHA256("different") }, "wrong_pin_scope": func(d *Declaration) { d.Methods[0].Schema = "mantik-experiment-recipe-v1" }, "unknown_method": func(d *Declaration) { d.Methods[0].ID = "unknown" }, "missing_origins": func(d *Declaration) { d.Origins = nil }, "false_digest_shape": func(d *Declaration) { d.Origins[0].RecordingSHA256 = "invalid" }, "false_claim_shape": func(d *Declaration) { d.Origins[0].ClaimKey = "arbitrary" }, "reference_control": func(d *Declaration) { d.Origins[0].Reference = "line\nbreak" }, "wrong_metric": func(d *Declaration) { d.Prediction.Metric = "unregistered_metric" }, "wrong_unit": func(d *Declaration) { d.Prediction.Unit = "count" }, "wrong_comparison": func(d *Declaration) { d.Prediction.Comparison = "approximately" }, "different_null_metric": func(d *Declaration) { d.Null.Metric = "realized_pnl_cents" }, "different_refutation_metric": func(d *Declaration) { d.Refutation.Metric = "realized_pnl_cents" }, "same_condition_code": func(d *Declaration) { d.Null.Code = d.Prediction.Code }, "overlapping_null": func(d *Declaration) { d.Null.Comparison = "ge" }, "overlapping_refutation": func(d *Declaration) { d.Refutation.Comparison = "ne" }, "huge_threshold": func(d *Declaration) { d.Prediction.Threshold = 1000000000001 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			d := Fixture()
			mutate(&d)
			if _, err := Seal(d); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}
func TestConditionBoundariesAndIntegerCountDomain(t *testing.T) {
	// Strict/inclusive boundaries must not declare both prediction and null true.
	for _, pair := range [][2]string{{"gt", "le"}, {"ge", "lt"}, {"eq", "ne"}, {"ne", "eq"}, {"lt", "ge"}, {"le", "gt"}} {
		d := Fixture()
		d.Prediction.Comparison = pair[0]
		d.Null.Comparison = pair[1]
		d.Refutation.Comparison = pair[1]
		if _, err := Seal(d); err != nil {
			t.Fatal(pair, err)
		}
	}
	d := Fixture()
	d.Prediction.Comparison = "ge"
	d.Null.Comparison = "le"
	if _, err := Seal(d); err == nil {
		t.Fatal("inclusive boundary overlap accepted")
	}
	d = Fixture()
	d.Prediction = Condition{Code: "some_actions", Metric: "actions", Comparison: "gt", Threshold: 0, Unit: "count"}
	d.Null = Condition{Code: "no_actions", Metric: "actions", Comparison: "eq", Threshold: 0, Unit: "count"}
	d.Refutation = Condition{Code: "zero_actions", Metric: "actions", Comparison: "eq", Threshold: 0, Unit: "count"}
	if _, err := Seal(d); err != nil {
		t.Fatal(err)
	}
	d.Refutation.Comparison = "lt"
	if _, err := Seal(d); err == nil {
		t.Fatal("impossible negative-count refutation accepted")
	}
}
func TestStrictDeclarationReader(t *testing.T) {
	raw, _ := json.Marshal(Fixture())
	if _, err := Read(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	inputs := [][]byte{append(bytes.Clone(raw), []byte(" {}")...), bytes.Replace(raw, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1), bytes.Replace(raw, []byte(`"schema":`), []byte(`"Schema":`), 1), bytes.Replace(raw, []byte(`"label":`), []byte(`"risk_policy":"widen","label":`), 1), []byte(strings.Repeat("x", MaxBytes+1)), []byte(strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18)), append(bytes.Clone(raw), 0xff)}
	for i, input := range inputs {
		if _, err := Read(bytes.NewReader(input)); err == nil {
			t.Fatal("malformed input accepted", i)
		}
	}
}
