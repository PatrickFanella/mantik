package storage

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// TimerStore exposes fenced durable claims without giving workers order access.
type TimerStore interface {
	ClaimTimers(context.Context, string, time.Time, time.Duration) ([]domain.TimerClaim, error)
	CompleteTimer(context.Context, domain.TimerClaim, time.Time) (domain.TimerReceipt, error)
}

// ClaimTimers serializes leases with observation and routing mutations.
func (m *Memory) ClaimTimers(ctx context.Context, worker string, now time.Time, lease time.Duration) ([]domain.TimerClaim, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next, claims, err := routing.ClaimTimers(m.state.Routing, worker, now, lease)
	if err == nil {
		m.state.Routing = next
	}
	return clone(claims), err
}

// CompleteTimer retains one receipt and audit effect per fenced job generation.
func (m *Memory) CompleteTimer(ctx context.Context, claim domain.TimerClaim, now time.Time) (domain.TimerReceipt, error) {
	if err := ctx.Err(); err != nil {
		return domain.TimerReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	retained, err := routing.CheckClaim(m.state.Routing, claim, now)
	if err != nil {
		return domain.TimerReceipt{}, err
	}
	if retained != nil {
		return clone(*retained), nil
	}
	next, receipt, err := workflow.CompleteTimer(m.state, claim, now)
	if err != nil {
		return receipt, err
	}
	m.state = next
	m.events = append(m.events, receipt.Events...)
	return clone(receipt), nil
}

// ClaimTimers commits leases under the existing aggregate row lock.
func (p *Postgres) ClaimTimers(ctx context.Context, worker string, now time.Time, lease time.Duration) ([]domain.TimerClaim, error) {
	value, err := p.routingMutation(ctx, func(s *domain.RoutingState) (*domain.RoutingState, any, error) {
		next, claims, err := routing.ClaimTimers(s, worker, now, lease)
		return next, claims, err
	})
	if err != nil {
		return nil, err
	}
	return value.([]domain.TimerClaim), nil
}

// CompleteTimer commits state, receipt, protection, and timer audit atomically.
func (p *Postgres) CompleteTimer(ctx context.Context, claim domain.TimerClaim, now time.Time) (domain.TimerReceipt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.TimerReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return domain.TimerReceipt{}, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return domain.TimerReceipt{}, err
	}
	retained, err := routing.CheckClaim(state.Routing, claim, now)
	if err != nil {
		return domain.TimerReceipt{}, err
	}
	if retained != nil {
		return *retained, tx.Commit(ctx)
	}
	next, receipt, err := workflow.CompleteTimer(state, claim, now)
	if err != nil {
		return receipt, err
	}
	for _, event := range receipt.Events {
		eventRaw, err := json.Marshal(event)
		if err != nil {
			return receipt, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,NULL,$2)", event.ID, eventRaw); err != nil {
			return receipt, err
		}
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return receipt, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

var _ TimerStore = (*Memory)(nil)
var _ TimerStore = (*Postgres)(nil)

func backfillTimers(ctx context.Context, tx pgx.Tx) error {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	// A retained quote must have the exact last usable occurrence watermark.
	// If its evidence is missing, deadline handling records an exception.
	if state.LastQuote == nil && !state.LastOccurredAt.IsZero() {
		rows, err := tx.Query(ctx, "SELECT payload FROM mantik_observations")
		if err != nil {
			return err
		}
		for rows.Next() {
			var observation domain.Observation
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(raw, &observation); err != nil {
				rows.Close()
				return err
			}
			if observation.OccurredAt.Equal(state.LastOccurredAt) && workflow.Validate(observation) == nil && observation.ReceivedAt.Sub(observation.OccurredAt) <= time.Duration(state.Strategy.Safety.MaxQuoteAgeSeconds)*time.Second {
				if state.LastQuote == nil || observation.ReceivedAt.Before(state.LastQuote.ReceivedAt) {
					quote := observation
					state.LastQuote = &quote
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	routing.SyncTimers(state.Routing)
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw)
	return err
}
