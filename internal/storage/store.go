// Package storage commits observations, decisions, actions, and state together.
package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

var ErrConflict = errors.New("observation id already exists with different content")

type Store interface {
	Ingest(context.Context, domain.Observation) (domain.Receipt, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	Ping(context.Context) error
	Close()
}

func canonical(o domain.Observation) (domain.Observation, []byte, error) {
	o.OccurredAt, o.ReceivedAt = o.OccurredAt.UTC(), o.ReceivedAt.UTC()
	if err := workflow.Validate(o); err != nil {
		return o, nil, err
	}
	raw, err := json.Marshal(o)
	return o, raw, err
}

type Memory struct {
	evidence     map[string]FeedEvidenceRecord
	collectors   feedcollector.State
	receiverID   string
	mu           sync.Mutex
	queued       bool
	effects      map[string]domain.ActionEffect
	state        domain.State
	observations map[string][]byte
	receipts     map[string]domain.Receipt
	events       []domain.Event
}

func NewMemory() *Memory {
	return &Memory{collectors: feedcollector.InitialState(), receiverID: "receiver-" + rand.Text(), state: workflow.InitialState(), effects: map[string]domain.ActionEffect{}, observations: map[string][]byte{}, receipts: map[string]domain.Receipt{}, events: []domain.Event{}}
}

// NewQueuedMemory exercises the persistent delivery contract without a database.
func NewQueuedMemory() *Memory { m := NewMemory(); m.queued = true; return m }

// NewMemoryFor selects a registered immutable method for an isolated replay.
// Service/PostgreSQL startup continues to select the original demo explicitly.
func NewMemoryFor(id string) (*Memory, error) {
	state, err := workflow.InitialStateFor(id)
	if err != nil {
		return nil, err
	}
	m := NewMemory()
	m.state = state
	return m, nil
}

func (m *Memory) Ingest(ctx context.Context, o domain.Observation) (domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	o, raw, err := canonical(o)
	if err != nil {
		return domain.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.observations[o.ID]; ok {
		if !bytes.Equal(previous, raw) {
			return domain.Receipt{}, ErrConflict
		}
		r := clone(m.receipts[o.ID])
		r.Duplicate = true
		return r, nil
	}
	apply := workflow.Apply
	if m.queued {
		apply = workflow.Queue
	}
	state, events, err := apply(m.state, o)
	if err != nil {
		return domain.Receipt{}, err
	}
	r := domain.Receipt{ObservationID: o.ID, Events: events}
	m.state, m.observations[o.ID], m.receipts[o.ID] = state, raw, clone(r)
	m.events = append(m.events, events...)
	return clone(r), nil
}

func (m *Memory) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(domain.Snapshot{Mode: "paper_fixture", ObservationCount: int64(len(m.observations)), State: m.state, Events: m.events}), nil
}

func clone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(raw, &result)
	return result
}

func (m *Memory) Ping(ctx context.Context) error { return ctx.Err() }
func (m *Memory) Close()                         {}
