package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"github.com/jackc/pgx/v5"
)

type TrialAttemptStore interface {
	StartTrialAttempt(context.Context, trial.AttemptCommand, func() time.Time) (trial.Attempt, error)
	CancelTrialAttempt(context.Context, string, string, func() time.Time) (trial.Attempt, error)
	TrialAttempt(context.Context, string) (trial.Attempt, error)
	TrialAttempts(context.Context, string) (trial.AttemptHistory, error)
}

func prepareAttempt(all []trial.Attempt, c trial.AttemptCommand, r trial.Registration, now func() time.Time) (trial.Attempt, bool, error) {
	if err := c.Validate(); err != nil {
		return trial.Attempt{}, false, err
	}
	for _, a := range all {
		if a.ID == c.RequestID {
			if !trial.SameAttempt(a, c) {
				return a, false, trial.ErrConflict
			}
			return a, true, nil
		}
	}
	if len(all) >= trial.MaxAttempts {
		return trial.Attempt{}, false, trial.ErrCapacity
	}
	if r.ID == "" {
		return trial.Attempt{}, false, trial.ErrNotFound
	}
	at := now()
	for _, a := range all {
		if at.Before(a.StartedAt) || !a.ClosedAt.IsZero() && at.Before(a.ClosedAt) {
			return trial.Attempt{}, false, trial.ErrConflict
		}
	}
	a, err := trial.StartAttempt(c, r, at)
	return a, false, err
}
func cancelAttempt(all []trial.Attempt, id string, now func() time.Time) (trial.Attempt, error) {
	for _, a := range all {
		if a.ID == id {
			if a.Status == "cancelled" {
				return a, nil
			}
			at := now()
			for _, other := range all {
				if at.Before(other.StartedAt) || !other.ClosedAt.IsZero() && at.Before(other.ClosedAt) {
					return a, trial.ErrConflict
				}
			}
			return trial.CancelAttempt(a, at)
		}
	}
	return trial.Attempt{}, trial.ErrNotFound
}
func attemptsHistory(id string, all []trial.Attempt) trial.AttemptHistory {
	h := trial.AttemptHistory{Schema: trial.AttemptsSchema, TrialID: id, Attempts: []trial.Attempt{}}
	for _, a := range all {
		if a.TrialID == id {
			h.Attempts = append(h.Attempts, a)
		}
	}
	slices.SortFunc(h.Attempts, func(a, b trial.Attempt) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return h
}
func (m *Memory) StartTrialAttempt(ctx context.Context, c trial.AttemptCommand, now func() time.Time) (trial.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return trial.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return trial.Attempt{}, ErrReceiverMismatch
	}
	all := make([]trial.Attempt, 0, len(m.trialAttempts))
	for _, a := range m.trialAttempts {
		all = append(all, a)
	}
	a, duplicate, err := prepareAttempt(all, c, m.trials[c.TrialID], now)
	if err != nil {
		return trial.Attempt{}, err
	}
	if !duplicate {
		m.trialAttempts[a.ID] = a
	}
	return a, nil
}
func (m *Memory) CancelTrialAttempt(ctx context.Context, receiver, id string, now func() time.Time) (trial.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return trial.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return trial.Attempt{}, ErrReceiverMismatch
	}
	if _, exists := m.trialRuns[id]; exists {
		return trial.Attempt{}, trial.ErrConflict
	}
	all := make([]trial.Attempt, 0, len(m.trialAttempts))
	for _, a := range m.trialAttempts {
		all = append(all, a)
	}
	a, err := cancelAttempt(all, id, now)
	if err == nil {
		m.trialAttempts[a.ID] = a
	}
	return a, err
}
func (m *Memory) TrialAttempt(ctx context.Context, id string) (trial.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return trial.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.trialAttempts[id]
	if !ok {
		return a, trial.ErrNotFound
	}
	return a, nil
}
func (m *Memory) TrialAttempts(ctx context.Context, id string) (trial.AttemptHistory, error) {
	if err := ctx.Err(); err != nil {
		return trial.AttemptHistory{}, err
	}
	if !review.ID.MatchString(id) {
		return trial.AttemptHistory{}, trial.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.trials[id]; !ok {
		return trial.AttemptHistory{}, trial.ErrNotFound
	}
	all := []trial.Attempt{}
	for _, a := range m.trialAttempts {
		all = append(all, a)
	}
	return attemptsHistory(id, all), nil
}
func (p *Postgres) attemptMutation(ctx context.Context, receiver string, apply func(pgx.Tx, []trial.Attempt) (trial.Attempt, error)) (trial.Attempt, error) {
	if !ValidReceiverID(receiver) {
		return trial.Attempt{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return trial.Attempt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&expected); err != nil {
		return trial.Attempt{}, err
	}
	if expected != receiver {
		return trial.Attempt{}, ErrReceiverMismatch
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_trial_attempts ORDER BY id LIMIT $1", trial.MaxAttempts+1)
	if err != nil {
		return trial.Attempt{}, err
	}
	all, err := readAttempts(rows)
	if err != nil {
		return trial.Attempt{}, err
	}
	a, err := apply(tx, all)
	if err != nil {
		return trial.Attempt{}, err
	}
	return a, tx.Commit(ctx)
}
func readAttempts(rows pgx.Rows) ([]trial.Attempt, error) {
	defer rows.Close()
	all := []trial.Attempt{}
	for rows.Next() {
		var raw []byte
		var a trial.Attempt
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		all = append(all, a)
	}
	return all, rows.Err()
}
func (p *Postgres) StartTrialAttempt(ctx context.Context, c trial.AttemptCommand, now func() time.Time) (trial.Attempt, error) {
	if err := c.Validate(); err != nil {
		return trial.Attempt{}, err
	}
	return p.attemptMutation(ctx, c.ReceiverID, func(tx pgx.Tx, all []trial.Attempt) (trial.Attempt, error) {
		var raw []byte
		var r trial.Registration
		err := tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_trials WHERE id=$1", c.TrialID).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return trial.Attempt{}, err
		}
		if err == nil {
			if err = json.Unmarshal(raw, &r); err != nil {
				return trial.Attempt{}, err
			}
		}
		a, duplicate, err := prepareAttempt(all, c, r, now)
		if err != nil {
			return a, err
		}
		if !duplicate {
			raw, err = json.Marshal(a)
			if err != nil {
				return a, err
			}
			_, err = tx.Exec(ctx, "INSERT INTO mantik_trial_attempts(id,trial_id,payload) VALUES($1,$2,$3)", a.ID, a.TrialID, raw)
		}
		return a, err
	})
}
func (p *Postgres) CancelTrialAttempt(ctx context.Context, receiver, id string, now func() time.Time) (trial.Attempt, error) {
	if !review.ID.MatchString(id) {
		return trial.Attempt{}, trial.ErrInvalid
	}
	return p.attemptMutation(ctx, receiver, func(tx pgx.Tx, all []trial.Attempt) (trial.Attempt, error) {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_trial_runs WHERE attempt_id=$1)", id).Scan(&exists); err != nil {
			return trial.Attempt{}, err
		}
		if exists {
			return trial.Attempt{}, trial.ErrConflict
		}
		a, err := cancelAttempt(all, id, now)
		if err != nil {
			return a, err
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return a, err
		}
		_, err = tx.Exec(ctx, "UPDATE mantik_trial_attempts SET payload=$2 WHERE id=$1", id, raw)
		return a, err
	})
}
func (p *Postgres) TrialAttempt(ctx context.Context, id string) (trial.Attempt, error) {
	var raw []byte
	var a trial.Attempt
	err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_trial_attempts WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, trial.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(raw, &a)
	return a, err
}
func (p *Postgres) TrialAttempts(ctx context.Context, id string) (trial.AttemptHistory, error) {
	if !review.ID.MatchString(id) {
		return trial.AttemptHistory{}, trial.ErrInvalid
	}
	rows, err := p.pool.Query(ctx, "SELECT a.payload FROM mantik_hypothesis_trials t LEFT JOIN mantik_trial_attempts a ON a.trial_id=t.id WHERE t.id=$1 ORDER BY a.id LIMIT $2", id, trial.MaxAttempts+1)
	if err != nil {
		return trial.AttemptHistory{}, err
	}
	defer rows.Close()
	exists := false
	all := []trial.Attempt{}
	for rows.Next() {
		exists = true
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return trial.AttemptHistory{}, err
		}
		if raw != nil {
			var a trial.Attempt
			if err = json.Unmarshal(raw, &a); err != nil {
				return trial.AttemptHistory{}, err
			}
			all = append(all, a)
		}
	}
	if err = rows.Err(); err != nil {
		return trial.AttemptHistory{}, err
	}
	if !exists {
		return trial.AttemptHistory{}, trial.ErrNotFound
	}
	if len(all) > trial.MaxAttempts {
		return trial.AttemptHistory{}, trial.ErrCapacity
	}
	return attemptsHistory(id, all), nil
}

var _ TrialAttemptStore = (*Memory)(nil)
var _ TrialAttemptStore = (*Postgres)(nil)
