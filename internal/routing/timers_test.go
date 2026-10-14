package routing_test

import (
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

func timedDefinition(kind string) domain.TriggerDefinition {
	d := definition(kind, domain.Condition{})
	d.RepeatPolicy = "once"
	d.Condition = domain.Condition{Kind: kind, DueAt: at.Add(time.Minute)}
	if kind == "elapsed" {
		d.Condition = domain.Condition{Kind: kind, AfterSeconds: 120}
	}
	if kind == "expected_event_missing" {
		d.Condition.EventName = "done"
	}
	return d
}

func TestTimerContracts(t *testing.T) {
	for _, kind := range []string{"deadline", "elapsed", "expected_event_missing"} {
		t.Run(kind, func(t *testing.T) {
			d := timedDefinition(kind)
			s, err := routing.Register(routing.NewState(), d)
			if err != nil {
				t.Fatal(err)
			}
			due := routing.DueAt(d)
			s, claims, err := routing.ClaimTimers(s, "worker-a", due.Add(-time.Nanosecond), time.Second)
			if err != nil || len(claims) != 0 {
				t.Fatal("early timer", err)
			}
			s, claims, err = routing.ClaimTimers(s, "worker-a", due, time.Second)
			if err != nil || len(claims) != 1 {
				t.Fatal("deadline missed", err)
			}
			old := claims[0]
			_, other, err := routing.ClaimTimers(s, "worker-b", due.Add(time.Millisecond), time.Second)
			if err != nil || len(other) != 0 {
				t.Fatal("live claim stolen")
			}
			before := s
			if _, _, err := routing.ClaimTimers(s, "worker-b", due.Add(-time.Minute), time.Second); !errors.Is(err, routing.ErrClockBackwards) {
				t.Fatal("clock rewind accepted")
			}
			if before.Timers.LastClock != s.Timers.LastClock {
				t.Fatal("failed claim mutated clock")
			}
			s, claims, err = routing.ClaimTimers(s, "worker-b", due.Add(time.Second), time.Second)
			if err != nil || len(claims) != 1 || claims[0].Generation != old.Generation+1 {
				t.Fatal("expired lease not reclaimed")
			}
			if _, err := routing.CheckClaim(s, old, due.Add(time.Second)); !errors.Is(err, routing.ErrLeaseLost) {
				t.Fatal("old worker retained authority")
			}
			if _, err := routing.CheckClaim(s, claims[0], due.Add(2*time.Second)); !errors.Is(err, routing.ErrLeaseLost) {
				t.Fatal("lease boundary accepted")
			}
			next, receipt, status, err := routing.ApplyTimer(s, d.ID, due.Add(time.Second))
			if err != nil || status != "matched" || len(receipt.Matches) != 1 {
				t.Fatalf("timer routing: %+v %s %v", receipt, status, err)
			}
			result := domain.TimerReceipt{DefinitionID: d.ID, At: due.Add(time.Second), Status: status, Routing: receipt}
			routing.Complete(next, claims[0], result)
			retained, err := routing.CheckClaim(next, claims[0], due.Add(time.Hour))
			if err != nil || retained.Status != "matched" {
				t.Fatal("completion retry lost", err)
			}
			next, claims, err = routing.ClaimTimers(next, "worker-c", due.Add(time.Hour), time.Second)
			if err != nil || len(claims) != 0 || len(next.Cases) != 1 {
				t.Fatal("completed timer multiplied work")
			}
		})
	}
}

func TestExpectedEventEvidence(t *testing.T) {
	d := timedDefinition("expected_event_missing")
	for _, test := range []struct {
		name     string
		event    string
		received time.Time
		occurred time.Time
		status   string
	}{
		{"before", "done", at.Add(30 * time.Second), at.Add(30 * time.Second), "event_observed"},
		{"boundary", "done", routing.DueAt(d), routing.DueAt(d), "matched"},
		{"late", "done", at.Add(2 * time.Minute), at.Add(30 * time.Second), "matched"},
		{"wrong", "other", at.Add(30 * time.Second), at.Add(30 * time.Second), "matched"},
		{"stale", "done", at.Add(30 * time.Second), at.Add(-2 * time.Minute), "matched"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := routing.Register(routing.NewState(), d)
			if err != nil {
				t.Fatal(err)
			}
			input := domain.RoutingInput{ID: "expected-input", Kind: "event", Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", EventName: test.event, OccurredAt: test.occurred, ReceivedAt: test.received}
			s, receipt, err := routing.Apply(s, input)
			if err != nil || len(receipt.Matches) != 0 {
				t.Fatal("event should not itself match missing condition", err)
			}
			_, receipt, status, err := routing.ApplyTimer(s, d.ID, d.ValidUntil.Add(time.Hour))
			if err != nil || status != test.status {
				t.Fatalf("%s: %+v %v", status, receipt, err)
			}
		})
	}
}

func TestInvalidTimerDefinitionsAndIndependentExpiry(t *testing.T) {
	d := timedDefinition("deadline")
	for _, change := range []func(*domain.TriggerDefinition){
		func(d *domain.TriggerDefinition) { d.Condition.DueAt = time.Time{} },
		func(d *domain.TriggerDefinition) { d.Condition.DueAt = d.ValidUntil },
		func(d *domain.TriggerDefinition) { d.Condition.DueAt = d.ValidFrom.Add(-time.Second) },
		func(d *domain.TriggerDefinition) { d.RepeatPolicy = "reopen" },
		func(d *domain.TriggerDefinition) { d.Condition.PriceCents = 1 },
		func(d *domain.TriggerDefinition) {
			d.Condition.Kind = "elapsed"
			d.Condition.DueAt = time.Time{}
			d.Condition.AfterSeconds = -1
		},
	} {
		copy := d
		change(&copy)
		if _, err := routing.Register(routing.NewState(), copy); err == nil {
			t.Fatal("invalid timer accepted", copy)
		}
	}
	s, err := routing.Register(routing.NewState(), definition("expiry", domain.Condition{Kind: "event", EventName: "signal"}))
	if err != nil {
		t.Fatal(err)
	}
	s, _, err = routing.ClaimTimers(s, "worker", s.Definitions[0].ValidUntil, time.Second)
	if err != nil || s.Runtime["expiry"].Status != "expired" {
		t.Fatal("expiry needed new quote", err)
	}
	if _, _, err := routing.ClaimTimers(s, "bad/worker", at, time.Second); err == nil {
		t.Fatal("invalid worker accepted")
	}
	if _, _, err := routing.ClaimTimers(s, "worker", at, 6*time.Minute); err == nil {
		t.Fatal("unbounded lease accepted")
	}
	if _, _, _, err := routing.ApplyTimer(s, "unknown", at); !errors.Is(err, routing.ErrNotFound) {
		t.Fatal("unknown timer accepted")
	}
}
