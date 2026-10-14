package routing_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

var at = time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)

func definition(id string, c domain.Condition) domain.TriggerDefinition {
	return domain.TriggerDefinition{ID: id, Owner: "operator", CaseKey: "research-case", Behavior: "research", Condition: c, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "coalesce", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
}
func input(id string, n int) domain.RoutingInput {
	tick := at.Add(time.Duration(n) * time.Second)
	return domain.RoutingInput{ID: id, Kind: "event", Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", OccurredAt: tick, ReceivedAt: tick, EventName: "signal"}
}
func quote(id string, n int, price int64) domain.RoutingInput {
	i := input(id, n)
	i.Kind, i.EventName, i.PriceCents = "quote", "", price
	return i
}
func register(t *testing.T, s *domain.RoutingState, d domain.TriggerDefinition) *domain.RoutingState {
	t.Helper()
	next, err := routing.Register(s, d)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func apply(t *testing.T, s *domain.RoutingState, i domain.RoutingInput) (*domain.RoutingState, domain.RoutingReceipt) {
	t.Helper()
	next, r, err := routing.Apply(s, i)
	if err != nil {
		t.Fatal(err)
	}
	return next, r
}

func TestCrossingDeduplicationAndCoalescing(t *testing.T) {
	d := definition("cross-v1", domain.Condition{Kind: "threshold", Comparison: "cross_below", PriceCents: 10000})
	s := register(t, nil, d)
	s, r := apply(t, s, quote("baseline", 0, 10200))
	if len(r.Matches) != 0 {
		t.Fatal("initial quote was a crossing")
	}
	s, r = apply(t, s, quote("cross", 1, 10000))
	if len(r.Matches) != 1 || len(s.Cases) != 1 || s.Cases[0].Generation != 1 {
		t.Fatal("crossing did not open one case")
	}
	s, duplicate := apply(t, s, quote("cross", 1, 10000))
	if !duplicate.Duplicate || s.Cases[0].MatchCount != 1 {
		t.Fatal("retry multiplied case work")
	}
	s, r = apply(t, s, quote("still-below", 2, 9900))
	if len(r.Matches) != 0 {
		t.Fatal("remaining below counted as another crossing")
	}
	s, _ = apply(t, s, quote("above", 3, 10100))
	s, r = apply(t, s, quote("cross-again", 4, 10000))
	if r.Matches[0].Transition != "updated" || len(s.Cases) != 1 || s.Cases[0].MatchCount != 2 {
		t.Fatal("case did not coalesce")
	}
	changed := quote("cross-again", 4, 9800)
	if _, _, err := routing.Apply(s, changed); !errors.Is(err, routing.ErrConflict) {
		t.Fatal("changed duplicate input accepted")
	}
	closed, err := routing.Close(s, s.Cases[0].ID, "operator", at.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	closed, _ = apply(t, closed, quote("up", 6, 10100))
	closed, r = apply(t, closed, quote("down", 7, 9900))
	if len(closed.Cases) != 1 || len(r.Matches) != 0 || len(r.RejectedDefinitions) != 1 {
		t.Fatal("coalesce policy reopened closed case")
	}
	if s.Cases[0].Status != "open" {
		t.Fatal("closure mutated prior state")
	}
}

func TestRepeatPoliciesAndExpiry(t *testing.T) {
	for _, policy := range []string{"once", "coalesce", "reopen"} {
		t.Run(policy, func(t *testing.T) {
			d := definition("event-v1", domain.Condition{Kind: "event", EventName: "signal"})
			d.RepeatPolicy = policy
			d.ValidUntil = at.Add(4 * time.Second)
			s := register(t, nil, d)
			s, _ = apply(t, s, input("first", 0))
			var err error
			s, err = routing.Close(s, s.Cases[0].ID, "operator", at.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			s, r := apply(t, s, input("second", 2))
			want := 1
			if policy == "reopen" {
				want = 2
				if r.Matches[0].Transition != "reopened" || s.Cases[1].Generation != 2 {
					t.Fatal("missing explicit reopen generation")
				}
			}
			if len(s.Cases) != want {
				t.Fatal("repeat policy changed case multiplicity")
			}
			s, r = apply(t, s, input("expired", 4))
			if len(r.Matches) != 0 || s.Runtime[d.ID].Status != "expired" {
				t.Fatal("exclusive expiry failed")
			}
		})
	}
}

func TestCombinedEvidenceAndWindow(t *testing.T) {
	d := definition("combined-v1", domain.Condition{Kind: "all", All: []domain.Condition{{Kind: "threshold", Comparison: "cross_below", PriceCents: 10000}, {Kind: "event", EventName: "signal"}}})
	d.CombinationWindowSeconds = 5
	s := register(t, nil, d)
	s, _ = apply(t, s, quote("above", 0, 10100))
	s, r := apply(t, s, quote("cross", 1, 10000))
	if len(r.Matches) != 0 {
		t.Fatal("partial condition fired")
	}
	s, _ = apply(t, s, quote("still-below", 2, 9900))
	s, r = apply(t, s, input("signal", 3))
	if len(r.Matches) != 1 {
		t.Fatal("recent crossing fact was lost before event")
	}
	s, r = apply(t, s, input("late-signal", 10))
	if len(r.Matches) != 0 {
		t.Fatal("expired combination evidence fired")
	}
	// Same owner/case key receives one coalesced case across two definitions.
	other := definition("other-v1", domain.Condition{Kind: "event", EventName: "signal"})
	s = register(t, s, other)
	s, r = apply(t, s, input("new-signal", 11))
	if len(s.Cases) != 1 || r.Matches[0].CaseID != s.Cases[0].ID {
		t.Fatal("matching definitions multiplied cases")
	}
}

func TestStateChangesNeedAnObservedTransition(t *testing.T) {
	d := definition("state-v1", domain.Condition{Kind: "state_change", StateKey: "feed", StateValue: "ready"})
	s := register(t, nil, d)
	stateInput := func(id string, n int, value string) domain.RoutingInput {
		i := input(id, n)
		i.Kind, i.EventName, i.StateKey, i.StateValue = "state", "", "feed", value
		return i
	}
	s, r := apply(t, s, stateInput("initial", 0, "ready"))
	if len(r.Matches) != 0 {
		t.Fatal("unknown initial state treated as a transition")
	}
	s, _ = apply(t, s, stateInput("down", 1, "missing"))
	s, r = apply(t, s, stateInput("up", 2, "ready"))
	if len(r.Matches) != 1 {
		t.Fatal("state transition not detected")
	}
	s, r = apply(t, s, stateInput("same", 3, "ready"))
	if len(r.Matches) != 0 || s.Cases[0].MatchCount != 1 {
		t.Fatal("unchanged state repeated work")
	}
	s, r = apply(t, s, stateInput("old", 1, "missing"))
	if len(r.Matches) != 0 || s.Runtime[d.ID].Facts["0"].LastState != "ready" {
		t.Fatal("old input rewound state")
	}
}

func TestRegistrationCompatibilityAndAuthority(t *testing.T) {
	d := definition("event-v1", domain.Condition{Kind: "event", EventName: "signal"})
	s := register(t, nil, d)
	if retry, err := routing.Register(s, d); err != nil || len(retry.Definitions) != 1 {
		t.Fatal("definition retry diverged")
	}
	d.Condition.EventName = "different"
	if _, err := routing.Register(s, d); !errors.Is(err, routing.ErrConflict) {
		t.Fatal("definition changed under retained identity")
	}
	before, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "authority", "reserved", "source", "expiry", "shape", "combination"} {
		t.Run(name, func(t *testing.T) {
			d := definition("invalid-v1", domain.Condition{Kind: "event", EventName: "signal"})
			switch name {
			case "owner":
				d.Owner = ""
			case "authority":
				d.Owner, d.Behavior, d.SourceDecisionID, d.SubjectCaseID, d.RepeatPolicy = "operations", "act", "entry", "subject", "once"
			case "reserved":
				d.ID = "future-reserved"
			case "source":
				d.Requirements.Source = "invalid/source"
			case "expiry":
				d.ValidUntil = d.ValidFrom
			case "shape":
				d.Condition.PriceCents = 10
			case "combination":
				d.Condition = domain.Condition{Kind: "all", All: []domain.Condition{{Kind: "event", EventName: "signal"}, {Kind: "event", EventName: "signal"}}}
				d.CombinationWindowSeconds = 5
			}
			if _, err := routing.Register(s, d); err == nil {
				t.Fatal("unsupported definition admitted")
			}
		})
	}
	after, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("validation mutated state")
	}
}

func TestFinalizedFutureConditionsAndFixtureParity(t *testing.T) {
	state := workflow.InitialState()
	var entry domain.Decision
	var err error
	state, events, err := workflow.Apply(state, workflow.Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Decision != nil {
			entry = *event.Decision
		}
	}
	if len(entry.FutureConditions) != 3 || len(state.Routing.Definitions) != 3 {
		t.Fatal("entry lacked retained future conditions")
	}
	proposed := entry
	proposed.Status = "proposed"
	if _, err := routing.BindFuture(nil, proposed, "DEMO"); err == nil {
		t.Fatal("proposed decision granted future action")
	}
	changed := entry
	changed.FutureConditions = append([]domain.TriggerDefinition(nil), entry.FutureConditions...)
	changed.FutureConditions[0].Condition.PriceCents = 1
	if _, err := routing.BindFuture(nil, changed, "DEMO"); err == nil {
		t.Fatal("changed protection accepted")
	}
	state, _, err = workflow.Apply(state, workflow.Fixtures()[3])
	if err != nil {
		t.Fatal(err)
	}
	if state.Case.Exit.RealizedPnLCents != -6000 || len(state.Routing.Cases) != 1 || state.Routing.Cases[0].Status != "closed" {
		t.Fatal("condition routing changed fixture economics or left duplicate action work")
	}
	if _, err := routing.Close(state.Routing, state.Routing.Cases[0].ID, "operations", at.Add(4*time.Minute)); err == nil {
		t.Fatal("operator closed action authority case")
	}
}

func TestBoundsDoNotCreateExtraWork(t *testing.T) {
	d := definition("reopen-v1", domain.Condition{Kind: "event", EventName: "signal"})
	d.RepeatPolicy = "reopen"
	s := register(t, nil, d)
	for n := 0; n < 128; n++ {
		var r domain.RoutingReceipt
		s, r = apply(t, s, input(fmt.Sprintf("input-%d", n), n))
		var err error
		s, err = routing.Close(s, r.Matches[0].CaseID, "operator", at.Add(time.Duration(n)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	s, r := apply(t, s, input("capacity", 129))
	if len(s.Cases) != 128 || len(r.RejectedDefinitions) != 1 {
		t.Fatal("case bound did not reject excess work")
	}
	if _, err := routing.Close(s, "unknown", "operator", at); !errors.Is(err, routing.ErrNotFound) {
		t.Fatal("unknown case closure accepted")
	}
}
