package storage

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"github.com/jackc/pgx/v5"
)

type FeedCollectorSnapshot struct {
	ReceiverID string              `json:"receiver_id"`
	State      feedcollector.State `json:"state"`
}

type FeedCollectorStore interface {
	FeedCollectors(context.Context) (FeedCollectorSnapshot, error)
	FeedCollectorCommand(context.Context, string, feedcollector.Command, func() time.Time) (feedcollector.Receipt, error)
}

func (m *Memory) FeedCollectors(ctx context.Context) (FeedCollectorSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return FeedCollectorSnapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return FeedCollectorSnapshot{ReceiverID: m.receiverID, State: clone(m.collectors)}, nil
}

func (m *Memory) FeedCollectorCommand(ctx context.Context, receiver string, c feedcollector.Command, now func() time.Time) (feedcollector.Receipt, error) {
	if err := validateCollectorCommand(receiver, c); err != nil {
		return feedcollector.Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return feedcollector.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID {
		return feedcollector.Receipt{}, ErrReceiverMismatch
	}
	next, receipt, err := feedcollector.Apply(m.collectors, c, now())
	if err != nil {
		return receipt, err
	}
	if _, retained := m.collectors.Commands[c.RequestID]; !retained {
		if err := m.sourceObservationClockLocked(receipt.At); err != nil {
			return feedcollector.Receipt{}, err
		}
	}
	m.collectors = clone(next)
	return clone(receipt), nil
}

func validateCollectorCommand(receiver string, c feedcollector.Command) error {
	if !ValidReceiverID(receiver) {
		return feedcollector.ErrInvalid
	}
	return feedcollector.Validate(c)
}

func (p *Postgres) FeedCollectors(ctx context.Context) (FeedCollectorSnapshot, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedCollectorSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var out FeedCollectorSnapshot
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ReceiverID); err != nil {
		return FeedCollectorSnapshot{}, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_feed_collectors WHERE singleton").Scan(&raw); err != nil {
		return FeedCollectorSnapshot{}, err
	}
	if err := json.Unmarshal(raw, &out.State); err != nil {
		return FeedCollectorSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedCollectorSnapshot{}, err
	}
	return out, nil
}

func (p *Postgres) FeedCollectorCommand(ctx context.Context, receiver string, c feedcollector.Command, now func() time.Time) (feedcollector.Receipt, error) {
	if err := validateCollectorCommand(receiver, c); err != nil {
		return feedcollector.Receipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return feedcollector.Receipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&expected); err != nil {
		return feedcollector.Receipt{}, err
	}
	if expected != receiver {
		return feedcollector.Receipt{}, ErrReceiverMismatch
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_feed_collectors WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return feedcollector.Receipt{}, err
	}
	var state feedcollector.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return feedcollector.Receipt{}, err
	}
	// Sample the service clock after acquiring the ownership lock. Concurrent
	// requests cannot carry pre-lock timestamps that appear to rewind the clock.
	next, receipt, err := feedcollector.Apply(state, c, now())
	if err != nil {
		return receipt, err
	}
	if _, retained := state.Commands[c.RequestID]; retained {
		if err := tx.Commit(ctx); err != nil {
			return feedcollector.Receipt{}, err
		}
		return receipt, nil
	}
	if err := readSourceObservationClock(ctx, tx, expected, receipt.At); err != nil {
		return feedcollector.Receipt{}, err
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return feedcollector.Receipt{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", raw); err != nil {
		return feedcollector.Receipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return feedcollector.Receipt{}, err
	}
	return receipt, nil
}

var _ FeedCollectorStore = (*Memory)(nil)
var _ FeedCollectorStore = (*Postgres)(nil)
