package actions_test

import (
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func queued(t *testing.T) (domain.State, domain.Action, time.Time) {
	t.Helper()
	q := workflow.Fixtures()[1]
	s, _, err := workflow.Queue(workflow.InitialState(), q)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Actions.Items {
		return s, a, q.ReceivedAt
	}
	t.Fatal("missing queued authority")
	return s, domain.Action{}, time.Time{}
}

func TestQueueIdentityAndAuthority(t *testing.T) {
	s, a, now := queued(t)
	if !actions.Active(s) || s.Case.Entry != nil {
		t.Fatal("queue enacted intent")
	}
	id, err := actions.Enqueue(&s, a.Intent.Decision, a.Intent.Quote, "quote", "")
	if err != nil || id != a.Intent.ID || len(s.Actions.Items) != 1 {
		t.Fatal("retry multiplied intent")
	}
	d := a.Intent.Decision
	d.Quantity++
	if _, err := actions.Enqueue(&s, d, a.Intent.Quote, "quote", ""); !errors.Is(err, actions.ErrConflict) {
		t.Fatal("changed identity accepted")
	}
	d = a.Intent.Decision
	d.Status = "proposed"
	if _, err := actions.Enqueue(&s, d, a.Intent.Quote, "quote", ""); err == nil {
		t.Fatal("unfinalized action accepted")
	}
	if _, _, err := actions.Evaluate(s, a, now.Add(30*time.Second)); err == nil {
		t.Fatal("expiry boundary accepted")
	}
	if _, _, err := actions.Evaluate(s, a, now.Add(-time.Second)); err == nil {
		t.Fatal("future finalized authority accepted")
	}
	s.Strategy.Safety.StopCents++
	if _, _, err := actions.Evaluate(s, a, now); err == nil {
		t.Fatal("changed safety accepted")
	}
}

func TestClaimsClockAndAdapterStatuses(t *testing.T) {
	s, a, now := queued(t)
	if _, err := actions.Claim(&s, "bad/worker", now, time.Second); err == nil {
		t.Fatal("invalid worker")
	}
	if _, err := actions.Claim(&s, "worker", now, 6*time.Minute); err == nil {
		t.Fatal("unbounded lease")
	}
	claims, err := actions.Claim(&s, "worker", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	if _, err := actions.Claim(&s, "other", now.Add(-time.Second), time.Second); !errors.Is(err, routing.ErrClockBackwards) {
		t.Fatal("rewind accepted")
	}
	if _, err := actions.CheckClaim(s, c, now.Add(time.Second)); !errors.Is(err, actions.ErrLeaseLost) {
		t.Fatal("expired claim accepted")
	}
	for _, status := range []string{"accepted", "partial"} {
		events, err := actions.Settle(&s, c, domain.ActionEffect{Intent: a.Intent, Status: status}, now)
		if err != nil || len(events) != 1 || s.Case.Entry != nil || !actions.Active(s) {
			t.Fatal("unsettled status fabricated complete fill", err)
		}
		events, err = actions.Settle(&s, c, domain.ActionEffect{Intent: a.Intent, Status: status}, now)
		if err != nil || len(events) != 0 {
			t.Fatal("status retry duplicated audit")
		}
	}
	out, _, err := actions.Evaluate(s, a, now)
	if err != nil {
		t.Fatal(err)
	}
	bad := out
	bad.PriceCents++
	if _, err := actions.Settle(&s, c, domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &bad}, now); !errors.Is(err, actions.ErrConflict) {
		t.Fatal("tampered adapter evidence applied")
	}
	if _, err := actions.Settle(&s, c, domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out}, now); err != nil {
		t.Fatal(err)
	}
	if actions.Active(s) || s.Case.Entry == nil {
		t.Fatal("completed effect not applied")
	}
	if _, err := actions.Settle(&s, c, domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out}, now); err != nil {
		t.Fatal("completion retry failed")
	}
}

func TestQueuedExitPreservesEconomics(t *testing.T) {
	s, a, now := queued(t)
	claims, err := actions.Claim(&s, "entry", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := actions.Evaluate(s, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Settle(&s, claims[0], domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out}, now); err != nil {
		t.Fatal(err)
	}
	q := workflow.Fixtures()[3]
	s, _, err = workflow.Queue(s, q)
	if err != nil {
		t.Fatal(err)
	}
	claims, err = actions.Claim(&s, "exit", q.ReceivedAt, time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	a = s.Actions.Items[claims[0].ID]
	out, _, err = actions.Evaluate(s, a, q.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Settle(&s, claims[0], domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out}, q.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	if s.Case.Status != "closed" || s.Case.Exit.RealizedPnLCents != -6000 || actions.Active(s) {
		t.Fatal("durable fixture economics changed")
	}
}
