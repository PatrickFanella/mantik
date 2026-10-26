package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestOutcomeCatalogueReusesExactEvaluationOnly(t *testing.T) {
	m := NewMemory()
	ownedOutcomeContracts(t, m)
	decisionEval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "storage_contract_only"}, nil
	}
	calls := 0
	eval := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		calls++
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "decision_abstained"}, nil
	}
	got, err := m.PaperOutcomes(context.Background(), decisionEval, eval)
	if err != nil || len(got.Records) != MaxPaperOutcomes || calls != 2 {
		t.Fatal("expected two actual basis/clock populations", len(got.Records), calls, err)
	}
	for _, r := range got.Records {
		if !reflect.DeepEqual(r, m.paperOutcomes[r.Command.ID].Record) {
			t.Fatal("memo changed retained record", r.Command.ID)
		}
	}
	calls = 0
	if _, err = m.PaperOutcomes(context.Background(), decisionEval, eval); err != nil || calls != 2 {
		t.Fatal("cache escaped catalogue read", calls, err)
	}
	// A self-rehashed result cannot borrow the valid evaluation cached for an
	// equal basis. Command metadata and result envelopes are checked per row.
	old := clone(m.paperOutcomes["decision-01"])
	bad := clone(old)
	bad.Record.Evaluation.ReasonCode = "forged"
	bad.Record.SHA256 = ""
	bad.Record.SHA256 = workerplan.Hash(bad.Record)
	m.paperOutcomes["decision-01"] = bad
	if _, err = m.PaperOutcomes(context.Background(), decisionEval, eval); err == nil {
		t.Fatal("cached input hid altered output")
	}
	m.paperOutcomes["decision-01"] = old
	// A previously cached basis must not skip actual owner-custody checks.
	bad = clone(old)
	bad.Basis.Observation.Feed.ReceiverID = "other"
	bad.Record.BasisSHA256 = workerplan.Hash(bad.Basis)
	bad.Record.SHA256 = ""
	bad.Record.SHA256 = workerplan.Hash(bad.Record)
	m.paperOutcomes["decision-01"] = bad
	if _, err = m.PaperOutcomes(context.Background(), decisionEval, eval); err == nil {
		t.Fatal("altered retained feed receiver accepted")
	}
}

func TestOutcomeMemoEqualityDetachmentAndErrors(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	basis := PaperOutcomeBasis{}
	basis.Observation.Feed.Routing = &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{"input": {Input: domain.RoutingInput{ID: "input"}}}}
	calls := 0
	eval := memoPaperOutcomeEvaluator(func(b PaperOutcomeBasis, a time.Time) (PaperOutcomeEvaluation, error) {
		calls++
		b.Observation.Feed.Routing.Inputs["input"] = domain.RetainedRoutingInput{}
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: a.Location().String()}, nil
	})
	before := clone(basis)
	first, err := eval(basis, at)
	if err != nil {
		t.Fatal(err)
	}
	again, err := eval(basis, at)
	if err != nil || calls != 1 || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, basis) {
		t.Fatal("memo mutated basis or missed exact reuse", calls, err)
	}
	// JSON encodes both clocks identically; full equality must still distinguish
	// their locations. Hash equality alone is insufficient for arbitrary callbacks.
	named := at.In(time.FixedZone("named-zero", 0))
	different, err := eval(basis, named)
	if err != nil || calls != 2 || different.ReasonCode == first.ReasonCode {
		t.Fatal("serialized equality aliased distinct inputs", calls, err)
	}
	changed := clone(basis)
	changed.Decision.Command.ID = "another"
	if _, err = eval(changed, at); err != nil || calls != 3 {
		t.Fatal("different basis reused", calls, err)
	}
	failures := 0
	want := errors.New("callback failed")
	fails := memoPaperOutcomeEvaluator(func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		failures++
		return PaperOutcomeEvaluation{}, want
	})
	for range 2 {
		if _, err = fails(basis, at); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if failures != 2 {
		t.Fatal("errors cached", failures)
	}
}

func TestOutcomeMemoDoesNotRetainOversizedBasis(t *testing.T) {
	calls := 0
	eval := memoPaperOutcomeEvaluator(func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		calls++
		return PaperOutcomeEvaluation{Status: "not_applicable"}, nil
	})
	b := PaperOutcomeBasis{}
	b.Decision.Evaluation.ReasonCode = strings.Repeat("x", MaxPaperOutcomeBytes)
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	for range 2 {
		if _, err := eval(b, at); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("oversized basis retained", calls)
	}
}

func TestOutcomeCustodyMemoExactInputsAndErrors(t *testing.T) {
	at := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	c := PaperDecisionCommand{ID: "decision"}
	b := PaperDecisionBasis{}
	b.Feed.Routing = &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{"input": {Input: domain.RoutingInput{ID: "input"}}}}
	calls := 0
	verify := memoPaperOutcomeCustody(func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error { calls++; return nil })
	for i := 0; i < 64; i++ {
		if err := verify(c, b, at, true); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("equal historical custody repeatedly verified", calls)
	}
	for _, change := range []func(*outcomeCustodyInput){
		func(i *outcomeCustodyInput) { i.Command.ID = "different" },
		func(i *outcomeCustodyInput) { i.At = i.At.Add(time.Second) },
		func(i *outcomeCustodyInput) { i.Membership = false },
		func(i *outcomeCustodyInput) { i.Basis.Feed.ReceiverID = "other" },
	} {
		input := outcomeCustodyInput{c, clone(b), at, true}
		change(&input)
		before := calls
		if err := verify(input.Command, input.Basis, input.At, input.Membership); err != nil || calls != before+1 {
			t.Fatal("distinct custody input reused", calls, err)
		}
	}
	// Retained keys cannot alias the caller's map, and serialization-equivalent
	// clocks with different Go locations cannot acquire another clock's success.
	b.Feed.Routing.Inputs["input"] = domain.RetainedRoutingInput{Input: domain.RoutingInput{ID: "changed"}}
	before := calls
	if err := verify(c, b, at, true); err != nil || calls != before+1 {
		t.Fatal("caller mutation reused detached key", calls, err)
	}
	named := at.In(time.FixedZone("named-zero", 0))
	before = calls
	for i := 0; i < 2; i++ {
		if err := verify(c, b, named, true); err != nil {
			t.Fatal(err)
		}
	}
	if calls != before+2 {
		t.Fatal("clock altered by serialization was cached", calls)
	}
	failures := 0
	want := errors.New("custody unavailable")
	fail := memoPaperOutcomeCustody(func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error { failures++; return want })
	for i := 0; i < 2; i++ {
		if err := fail(c, b, at, true); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if failures != 2 {
		t.Fatal("custody error reused", failures)
	}
	// Non-serializable and oversized inputs still reach the verifier unchanged.
	b.Feed.ReceiverID = strings.Repeat("x", MaxPaperOutcomeBytes)
	oversized := 0
	bounded := memoPaperOutcomeCustody(func(_ PaperDecisionCommand, got PaperDecisionBasis, _ time.Time, _ bool) error {
		oversized++
		if !reflect.DeepEqual(got, b) {
			t.Fatal("verification input changed")
		}
		return nil
	})
	for i := 0; i < 2; i++ {
		if err := bounded(c, b, at, true); err != nil {
			t.Fatal(err)
		}
	}
	if oversized != 2 {
		t.Fatal("oversized custody retained", oversized)
	}
	invalid := at.AddDate(10000, 0, 0)
	for i := 0; i < 2; i++ {
		if err := bounded(c, b, invalid, true); err != nil {
			t.Fatal(err)
		}
	}
	if oversized != 4 {
		t.Fatal("nonserializable input cached", oversized)
	}
}
