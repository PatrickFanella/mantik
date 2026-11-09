package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"github.com/jackc/pgx/v5"
)

type RoutingStore interface {
	RegisterTrigger(context.Context, domain.TriggerDefinition) (domain.TriggerDefinition, error)
	RouteInput(context.Context, domain.RoutingInput) (domain.RoutingReceipt, error)
	CloseRoutingCase(context.Context, string, string, time.Time) error
}

func (m *Memory) RegisterTrigger(ctx context.Context, d domain.TriggerDefinition) (domain.TriggerDefinition, error) {
	if err := ctx.Err(); err != nil {
		return d, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := routing.Register(m.state.Routing, d)
	if err != nil {
		return d, err
	}
	m.state.Routing = next
	for _, stored := range next.Definitions {
		if stored.ID == d.ID {
			return clone(stored), nil
		}
	}
	return d, errors.New("registered definition missing")
}

func (m *Memory) RouteInput(ctx context.Context, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	return m.routeInput(ctx, "", i)
}
func (m *Memory) routeInput(ctx context.Context, receiver string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	if i.Source == routing.OwnedSocialResearchSource {
		return domain.RoutingReceipt{}, routing.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.RoutingReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != "" && receiver != m.receiverID {
		return domain.RoutingReceipt{}, ErrReceiverMismatch
	}
	if _, active := m.state.Routing.Inputs[i.ID]; !active {
		old, _, err := m.feedArchivedSourceLocked(ctx, "input", i.ID)
		if err == nil {
			if err := retiredArchiveSource(m.feedRetention, old); err != nil {
				return domain.RoutingReceipt{}, err
			}
			var record domain.RetainedRoutingInput
			if json.Unmarshal(old.Payload, &record) != nil || !reflect.DeepEqual(record.Input, i) {
				return domain.RoutingReceipt{}, routing.ErrConflict
			}
			receipt := clone(record.Receipt)
			receipt.Duplicate = true
			return receipt, nil
		}
		if !errors.Is(err, ErrFeedArchiveNotFound) {
			return domain.RoutingReceipt{}, err
		}
	}
	next, receipt, err := routing.Apply(m.state.Routing, i)
	if err != nil {
		return receipt, err
	}
	m.state.Routing = next
	return clone(receipt), nil
}

func (m *Memory) CloseRoutingCase(ctx context.Context, id, owner string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := routing.Close(m.state.Routing, id, owner, at)
	if err == nil {
		m.state.Routing = next
	}
	return err
}

func (p *Postgres) routingMutation(ctx context.Context, update func(*domain.RoutingState) (*domain.RoutingState, any, error)) (any, error) {
	return p.routingMutationForReceiver(ctx, "", update)
}
func (p *Postgres) routingMutationForReceiver(ctx context.Context, receiver string, update func(*domain.RoutingState) (*domain.RoutingState, any, error)) (any, error) {
	return p.routingMutationWithInput(ctx, receiver, nil, update)
}
func (p *Postgres) routingMutationWithInput(ctx context.Context, receiver string, input *domain.RoutingInput, update func(*domain.RoutingState) (*domain.RoutingState, any, error)) (any, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if receiver != "" {
		var id string
		if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&id); err != nil {
			return nil, err
		}
		if id != receiver {
			return nil, ErrReceiverMismatch
		}
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return nil, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if input != nil {
		if _, active := state.Routing.Inputs[input.ID]; !active {
			old, _, err := readFeedArchivedSource(ctx, tx, "input", input.ID)
			if err == nil {
				r, err := readFeedRetention(ctx, tx)
				if err != nil {
					return nil, err
				}
				if err := retiredArchiveSource(r, old); err != nil {
					return nil, err
				}
				var record domain.RetainedRoutingInput
				if json.Unmarshal(old.Payload, &record) != nil || !reflect.DeepEqual(record.Input, *input) {
					return nil, routing.ErrConflict
				}
				receipt := record.Receipt
				receipt.Duplicate = true
				if err := tx.Commit(ctx); err != nil {
					return nil, err
				}
				return receipt, nil
			}
			if !errors.Is(err, ErrFeedArchiveNotFound) {
				return nil, err
			}
		}
	}
	next, value, err := update(state.Routing)
	if err != nil {
		return nil, err
	}
	state.Routing = next
	raw, err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return value, nil
}

func (p *Postgres) RegisterTrigger(ctx context.Context, d domain.TriggerDefinition) (domain.TriggerDefinition, error) {
	value, err := p.routingMutation(ctx, func(s *domain.RoutingState) (*domain.RoutingState, any, error) {
		next, err := routing.Register(s, d)
		if err != nil {
			return s, nil, err
		}
		for _, stored := range next.Definitions {
			if stored.ID == d.ID {
				return next, stored, nil
			}
		}
		return s, nil, errors.New("registered definition missing")
	})
	if err != nil {
		return d, err
	}
	return value.(domain.TriggerDefinition), nil
}

func (p *Postgres) RouteInput(ctx context.Context, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	return p.routeInput(ctx, "", i)
}
func (p *Postgres) routeInput(ctx context.Context, receiver string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	if i.Source == routing.OwnedSocialResearchSource {
		return domain.RoutingReceipt{}, routing.ErrInvalid
	}
	value, err := p.routingMutationWithInput(ctx, receiver, &i, func(s *domain.RoutingState) (*domain.RoutingState, any, error) {
		next, receipt, err := routing.Apply(s, i)
		return next, receipt, err
	})
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	return value.(domain.RoutingReceipt), nil
}

func (p *Postgres) CloseRoutingCase(ctx context.Context, id, owner string, at time.Time) error {
	_, err := p.routingMutation(ctx, func(s *domain.RoutingState) (*domain.RoutingState, any, error) {
		next, err := routing.Close(s, id, owner, at)
		return next, nil, err
	})
	return err
}

// Derive protection for an existing v1 open position from its retained finalized
// entry. Preserve the original event bytes; missing authority aborts the upgrade.
func backfillRouting(ctx context.Context, tx pgx.Tx) error {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Case.Entry == nil || state.Case.Exit != nil {
		return nil
	}
	if err := tx.QueryRow(ctx, "SELECT payload->'decision' FROM mantik_events WHERE id=$1", state.Case.Entry.DecisionID).Scan(&raw); err != nil {
		return err
	}
	var d domain.Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	if d.ID != state.Case.Entry.DecisionID || d.CaseID != state.Case.ID || d.StrategyVersionID != state.Strategy.ID || !d.FinalizedAt.Equal(state.Case.Entry.ExecutedAt) || !state.Case.ExitBy.Equal(d.FinalizedAt.Add(time.Duration(d.Safety.MaxHoldingSeconds)*time.Second)) {
		return errors.New("legacy open position lacks matching finalized authority")
	}
	d.FutureConditions = routing.FutureDefinitions(d, state.Strategy.Symbol)
	next, err := routing.BindFuture(state.Routing, d, state.Strategy.Symbol)
	if err != nil {
		return err
	}
	state.Routing = next
	raw, err = json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw)
	return err
}

var _ RoutingStore = (*Memory)(nil)
var _ RoutingStore = (*Postgres)(nil)
