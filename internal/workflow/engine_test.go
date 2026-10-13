package workflow

import (
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func TestLifecycle(t *testing.T) {
	state := InitialState()
	var decisions []domain.Decision
	for _, o := range Fixtures() {
		var events []domain.Event
		var err error
		state, events, err = Apply(state, o)
		if err != nil {
			t.Fatal(err)
		}
		for i, e := range events {
			if e.Decision != nil {
				decisions = append(decisions, *e.Decision)
			}
			if e.Outcome != nil && (i == 0 || events[i-1].Decision == nil || events[i-1].Decision.Status != "finalized") {
				t.Fatal("execution preceded finalization")
			}
		}
	}
	if state.Case.Status != "closed" || state.Case.Entry.PriceCents != 10000 || state.Case.Exit.PriceCents != 9400 || state.Case.Exit.RealizedPnLCents != -6000 {
		t.Fatalf("unexpected lifecycle: %+v", state.Case)
	}
	if len(decisions) != 2 || decisions[0].Action != "enter" || decisions[1].ReasonCode != "stop_crossed" {
		t.Fatalf("unexpected decisions: %+v", decisions)
	}
}

func TestEntryRejections(t *testing.T) {
	for _, test := range []struct {
		name  string
		price int64
		delay time.Duration
	}{
		{"stale", 10000, 61 * time.Second}, {"below_stop", 9400, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := Fixtures()[1]
			o.PriceCents = test.price
			o.ReceivedAt = o.OccurredAt.Add(test.delay)
			state, events, err := Apply(InitialState(), o)
			if err != nil {
				t.Fatal(err)
			}
			if state.Case.Entry != nil {
				t.Fatal("rejected input opened a position")
			}
			found := false
			for _, e := range events {
				if e.Decision != nil && e.Decision.Action == "abstain" {
					found = true
				}
				if e.Outcome != nil {
					t.Fatal("rejected input executed")
				}
			}
			if !found {
				t.Fatal("missing abstention")
			}
		})
	}
}

func TestOutOfOrderDoesNotReplaceQuote(t *testing.T) {
	state, _, err := Apply(InitialState(), Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	o := Fixtures()[0]
	o.PriceCents = 9400
	next, events, err := Apply(state, o)
	if err != nil {
		t.Fatal(err)
	}
	if next.Case.Status != "open" || !next.LastOccurredAt.Equal(state.LastOccurredAt) {
		t.Fatal("old quote altered position")
	}
	if events[len(events)-1].Decision.Action != "abstain" {
		t.Fatal("old quote was not rejected")
	}
}

func TestTargetAndHoldingDeadline(t *testing.T) {
	for _, reason := range []string{"target_crossed", "holding_deadline"} {
		t.Run(reason, func(t *testing.T) {
			state, _, err := Apply(InitialState(), Fixtures()[1])
			if err != nil {
				t.Fatal(err)
			}
			o := Fixtures()[2]
			if reason == "target_crossed" {
				o.PriceCents = 11100
			} else {
				o.OccurredAt = state.Case.ExitBy
				o.ReceivedAt = o.OccurredAt
			}
			next, events, err := Apply(state, o)
			if err != nil {
				t.Fatal(err)
			}
			if next.Case.Status != "closed" {
				t.Fatal("exit did not close position")
			}
			if events[len(events)-2].Decision.ReasonCode != reason {
				t.Fatal("wrong exit authority")
			}
		})
	}
}

func TestInvalidSafetyFailsWithoutEvents(t *testing.T) {
	state := InitialState()
	state.Strategy.Safety.MaxHoldingSeconds = 0
	_, events, err := Apply(state, Fixtures()[1])
	if err == nil || len(events) != 0 {
		t.Fatal("incomplete safety admitted")
	}
}
