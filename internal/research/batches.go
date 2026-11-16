package research

import (
	"context"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func replyBatch(d Declaration, body []byte, at time.Time, step int) strategytrial.Batch {
	// Use the same scanner as inspection/dispatch, including rejected transport,
	// malformed, duplicate and excess proposals. Exact reply bytes stay private.
	events := []Event{}
	for range step {
		events = append(events, Event{Kind: "call"})
	}
	events = append(events, Event{Kind: "reply", Body: body})
	return strategytrial.Batch{ID: attemptID(d.ID, step, 0), Family: d.Family, Reply: canonical(body), At: at, TrialIDs: AttemptIDs(d.ID, events)}
}
func reserveReply(ctx context.Context, s Trials, d Declaration, body []byte, at time.Time, step int) error {
	b := replyBatch(d, body, at, step)
	return s.ReserveStrategyTrialBatch(ctx, b)
}

func recoverReply(ctx context.Context, s Trials, d Declaration, step int) (Event, error) {
	b, e := s.StrategyTrialBatch(ctx, attemptID(d.ID, step, 0))
	if errors.Is(e, strategytrial.ErrNotFound) {
		return Event{}, ErrInterrupted
	}
	if e != nil {
		return Event{}, e
	}
	want := replyBatch(d, b.Reply, b.At, step)
	if digest(marshal(want)) != digest(marshal(b)) {
		return Event{}, ErrConflict
	}
	return Event{Kind: "reply", At: b.At, Body: b.Reply}, nil
}
