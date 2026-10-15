package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

type CollectorSubmissionStore interface {
	SubmitCollectorResearch(context.Context, string, feedcollector.Fence, domain.RoutingInput, func() time.Time) (domain.RoutingReceipt, error)
}

func collectorReplay(s feedcollector.State, i domain.RoutingInput, r *domain.RoutingState) (domain.RoutingReceipt, error) {
	old, ok := r.Inputs[i.ID]
	a := s.Admissions[i.ID]
	if !ok || !reflect.DeepEqual(old.Input, i) || !reflect.DeepEqual(old.Receipt, a.Receipt) {
		return domain.RoutingReceipt{}, feedcollector.ErrConflict
	}
	receipt := old.Receipt
	receipt.Duplicate = true
	return receipt, nil
}

func (m *Memory) SubmitCollectorResearch(ctx context.Context, receiver string, f feedcollector.Fence, i domain.RoutingInput, now func() time.Time) (domain.RoutingReceipt, error) {
	if err := validateResearch(receiver, i); err != nil {
		return domain.RoutingReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.RoutingReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID {
		return domain.RoutingReceipt{}, ErrReceiverMismatch
	}
	at := now().UTC()
	replay, err := feedcollector.CheckSubmission(m.collectors, f, i, at)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	if replay {
		receipt, err := collectorReplay(m.collectors, i, m.state.Routing)
		return clone(receipt), err
	}
	if _, exists := m.collectors.Admissions[i.ID]; exists {
		if _, err := collectorReplay(m.collectors, i, m.state.Routing); err != nil {
			return domain.RoutingReceipt{}, err
		}
	}
	nextRouting, receipt, err := routing.Apply(m.state.Routing, i)
	if err != nil {
		return receipt, err
	}
	nextCollectors, err := feedcollector.RetainAdmission(m.collectors, f, i, receipt, at)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	m.state.Routing = nextRouting
	m.collectors = clone(nextCollectors)
	return clone(receipt), nil
}

func (p *Postgres) SubmitCollectorResearch(ctx context.Context, receiver string, f feedcollector.Fence, i domain.RoutingInput, now func() time.Time) (domain.RoutingReceipt, error) {
	if err := validateResearch(receiver, i); err != nil {
		return domain.RoutingReceipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&expected); err != nil {
		return domain.RoutingReceipt{}, err
	}
	if receiver != expected {
		return domain.RoutingReceipt{}, ErrReceiverMismatch
	}
	// Lock ownership before routing. Claim/renew/stop serialize on the same row;
	// no lease preflight can race a takeover before input/admission commit.
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_feed_collectors WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return domain.RoutingReceipt{}, err
	}
	var collectors feedcollector.State
	if err := json.Unmarshal(raw, &collectors); err != nil {
		return domain.RoutingReceipt{}, err
	}
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return domain.RoutingReceipt{}, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return domain.RoutingReceipt{}, err
	}
	at := now().UTC()
	replay, err := feedcollector.CheckSubmission(collectors, f, i, at)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	if replay {
		receipt, err := collectorReplay(collectors, i, state.Routing)
		if err != nil {
			return receipt, err
		}
		return receipt, tx.Commit(ctx)
	}
	if _, exists := collectors.Admissions[i.ID]; exists {
		if _, err := collectorReplay(collectors, i, state.Routing); err != nil {
			return domain.RoutingReceipt{}, err
		}
	}
	nextRouting, receipt, err := routing.Apply(state.Routing, i)
	if err != nil {
		return receipt, err
	}
	nextCollectors, err := feedcollector.RetainAdmission(collectors, f, i, receipt, at)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	state.Routing = nextRouting
	raw, err = json.Marshal(state)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return domain.RoutingReceipt{}, err
	}
	raw, err = json.Marshal(nextCollectors)
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", raw); err != nil {
		return domain.RoutingReceipt{}, err
	}
	return receipt, tx.Commit(ctx)
}

var _ CollectorSubmissionStore = (*Memory)(nil)
var _ CollectorSubmissionStore = (*Postgres)(nil)
