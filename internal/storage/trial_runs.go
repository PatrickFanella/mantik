package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"github.com/jackc/pgx/v5"
)

type TrialRunStore interface {
	BeginTrialRun(context.Context, string, string, string, string, func() time.Time) (trial.RunWork, error)
	FinishTrialRun(context.Context, string, string, string, string, func() time.Time) (trial.Run, error)
	ResolveTrialRun(context.Context, string, string, func() time.Time) (trial.Run, error)
	TrialRun(context.Context, string) (trial.Run, error)
	TrialRunArtifact(context.Context, string, string) (trial.RunArtifact, error)
}

func runClock(all []trial.Run, at time.Time) error {
	for _, r := range all {
		if at.Before(r.StartedAt) || !r.FinishedAt.IsZero() && at.Before(r.FinishedAt) {
			return trial.ErrConflict
		}
	}
	return nil
}
func existingRun(r trial.Run, exists bool) (trial.RunWork, bool, error) {
	if !exists {
		return trial.RunWork{}, false, nil
	}
	if r.Status == "running" {
		return trial.RunWork{}, true, trial.ErrConflict
	}
	return trial.RunWork{Run: r}, true, nil
}
func (m *Memory) BeginTrialRun(ctx context.Context, receiver, id, input, invocation string, now func() time.Time) (trial.RunWork, error) {
	if err := ctx.Err(); err != nil {
		return trial.RunWork{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return trial.RunWork{}, ErrReceiverMismatch
	}
	if w, exists, err := existingRun(m.trialRuns[id], m.trialRuns[id].AttemptID != ""); exists {
		return w, err
	}
	a, ok := m.trialAttempts[id]
	if !ok {
		return trial.RunWork{}, trial.ErrNotFound
	}
	registration := m.trials[a.TrialID]
	at := now()
	all := []trial.Run{}
	for _, r := range m.trialRuns {
		all = append(all, r)
	}
	if err := runClock(all, at); err != nil {
		return trial.RunWork{}, err
	}
	r, err := trial.BeginRun(a, registration, input, invocation, at)
	if err != nil {
		return trial.RunWork{}, err
	}
	m.trialRuns[id] = r
	m.trialInputs[id] = input
	return trial.RunWork{Run: r, Registration: clone(registration), Input: input, Acquired: true}, nil
}
func (m *Memory) FinishTrialRun(ctx context.Context, receiver, id, invocation, raw string, now func() time.Time) (trial.Run, error) {
	if err := ctx.Err(); err != nil {
		return trial.Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return trial.Run{}, ErrReceiverMismatch
	}
	r, ok := m.trialRuns[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	if r.Status != "running" {
		if r.InvocationID == invocation && r.ResultSHA256 == news.SHA256(raw) {
			return r, nil
		}
		return r, trial.ErrConflict
	}
	all := []trial.Run{}
	for _, v := range m.trialRuns {
		all = append(all, v)
	}
	at := now()
	if err := runClock(all, at); err != nil {
		return r, err
	}
	updated, err := trial.FinishRun(r, m.trials[r.TrialID], invocation, raw, at)
	if err != nil {
		return r, err
	}
	m.trialRuns[id] = updated
	m.trialResults[id] = raw
	return updated, nil
}
func (m *Memory) ResolveTrialRun(ctx context.Context, receiver, id string, now func() time.Time) (trial.Run, error) {
	if err := ctx.Err(); err != nil {
		return trial.Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return trial.Run{}, ErrReceiverMismatch
	}
	r, ok := m.trialRuns[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	if r.Status == "incomplete" {
		return r, nil
	}
	all := []trial.Run{}
	for _, v := range m.trialRuns {
		all = append(all, v)
	}
	at := now()
	if err := runClock(all, at); err != nil {
		return r, err
	}
	updated, err := trial.ResolveRun(r, at)
	if err == nil {
		m.trialRuns[id] = updated
	}
	return updated, err
}
func (m *Memory) TrialRun(ctx context.Context, id string) (trial.Run, error) {
	if err := ctx.Err(); err != nil {
		return trial.Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.trialRuns[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	return r, nil
}
func runArtifact(r trial.Run, kind, raw string) (trial.RunArtifact, error) {
	if r.Validate() != nil {
		return trial.RunArtifact{}, trial.ErrInvalid
	}
	expected := r.InputSHA256
	if kind == "result" {
		expected = r.ResultSHA256
	} else if kind != "input" {
		return trial.RunArtifact{}, trial.ErrInvalid
	}
	if raw == "" {
		return trial.RunArtifact{}, trial.ErrNotFound
	}
	if expected != news.SHA256(raw) {
		return trial.RunArtifact{}, trial.ErrInvalid
	}
	return trial.RunArtifact{AttemptID: r.AttemptID, Kind: kind, SHA256: expected, Content: raw}, nil
}
func (m *Memory) TrialRunArtifact(ctx context.Context, id, kind string) (trial.RunArtifact, error) {
	if err := ctx.Err(); err != nil {
		return trial.RunArtifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.trialRuns[id]
	if !ok {
		return trial.RunArtifact{}, trial.ErrNotFound
	}
	raw := m.trialInputs[id]
	if kind == "result" {
		raw = m.trialResults[id]
	}
	return runArtifact(r, kind, raw)
}
func (p *Postgres) runMutation(ctx context.Context, receiver string, apply func(pgx.Tx, []trial.Run) (trial.RunWork, error)) (trial.RunWork, error) {
	if !ValidReceiverID(receiver) {
		return trial.RunWork{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return trial.RunWork{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&expected); err != nil {
		return trial.RunWork{}, err
	}
	if expected != receiver {
		return trial.RunWork{}, ErrReceiverMismatch
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_trial_runs ORDER BY attempt_id LIMIT $1", trial.MaxAttempts+1)
	if err != nil {
		return trial.RunWork{}, err
	}
	all := []trial.Run{}
	for rows.Next() {
		var raw []byte
		var r trial.Run
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err != nil {
			rows.Close()
			return trial.RunWork{}, err
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return trial.RunWork{}, err
	}
	if len(all) > trial.MaxAttempts {
		return trial.RunWork{}, trial.ErrCapacity
	}
	work, err := apply(tx, all)
	if err != nil {
		return work, err
	}
	return work, tx.Commit(ctx)
}
func loadRunTrial(ctx context.Context, tx pgx.Tx, id string) (trial.Registration, error) {
	var raw []byte
	var r trial.Registration
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_trials WHERE id=$1", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	return r, err
}
func (p *Postgres) BeginTrialRun(ctx context.Context, receiver, id, input, invocation string, now func() time.Time) (trial.RunWork, error) {
	return p.runMutation(ctx, receiver, func(tx pgx.Tx, all []trial.Run) (trial.RunWork, error) {
		for _, r := range all {
			if r.AttemptID == id {
				w, _, err := existingRun(r, true)
				return w, err
			}
		}
		var raw []byte
		var a trial.Attempt
		err := tx.QueryRow(ctx, "SELECT payload FROM mantik_trial_attempts WHERE id=$1", id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return trial.RunWork{}, trial.ErrNotFound
		}
		if err != nil {
			return trial.RunWork{}, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return trial.RunWork{}, err
		}
		registration, err := loadRunTrial(ctx, tx, a.TrialID)
		if err != nil {
			return trial.RunWork{}, err
		}
		at := now()
		if err = runClock(all, at); err != nil {
			return trial.RunWork{}, err
		}
		r, err := trial.BeginRun(a, registration, input, invocation, at)
		if err != nil {
			return trial.RunWork{}, err
		}
		raw, err = json.Marshal(r)
		if err != nil {
			return trial.RunWork{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_trial_runs(attempt_id,payload) VALUES($1,$2)", id, raw); err != nil {
			return trial.RunWork{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_trial_run_artifacts(attempt_id,input_text) VALUES($1,$2)", id, input); err != nil {
			return trial.RunWork{}, err
		}
		return trial.RunWork{Run: r, Registration: registration, Input: input, Acquired: true}, nil
	})
}
func (p *Postgres) FinishTrialRun(ctx context.Context, receiver, id, invocation, raw string, now func() time.Time) (trial.Run, error) {
	w, err := p.runMutation(ctx, receiver, func(tx pgx.Tx, all []trial.Run) (trial.RunWork, error) {
		for _, r := range all {
			if r.AttemptID == id {
				if r.Status != "running" {
					if r.InvocationID == invocation && r.ResultSHA256 == news.SHA256(raw) {
						return trial.RunWork{Run: r}, nil
					}
					return trial.RunWork{}, trial.ErrConflict
				}
				registration, err := loadRunTrial(ctx, tx, r.TrialID)
				if err != nil {
					return trial.RunWork{}, err
				}
				at := now()
				if err = runClock(all, at); err != nil {
					return trial.RunWork{}, err
				}
				updated, err := trial.FinishRun(r, registration, invocation, raw, at)
				if err != nil {
					return trial.RunWork{}, err
				}
				metadata, err := json.Marshal(updated)
				if err != nil {
					return trial.RunWork{}, err
				}
				if _, err = tx.Exec(ctx, "UPDATE mantik_trial_runs SET payload=$2 WHERE attempt_id=$1", id, metadata); err != nil {
					return trial.RunWork{}, err
				}
				tag, err := tx.Exec(ctx, "UPDATE mantik_trial_run_artifacts SET result_text=$2 WHERE attempt_id=$1", id, raw)
				if err != nil {
					return trial.RunWork{}, err
				}
				if tag.RowsAffected() != 1 {
					return trial.RunWork{}, trial.ErrConflict
				}
				return trial.RunWork{Run: updated}, nil
			}
		}
		return trial.RunWork{}, trial.ErrNotFound
	})
	return w.Run, err
}
func (p *Postgres) ResolveTrialRun(ctx context.Context, receiver, id string, now func() time.Time) (trial.Run, error) {
	w, err := p.runMutation(ctx, receiver, func(tx pgx.Tx, all []trial.Run) (trial.RunWork, error) {
		for _, r := range all {
			if r.AttemptID == id {
				if r.Status == "incomplete" {
					return trial.RunWork{Run: r}, nil
				}
				at := now()
				if err := runClock(all, at); err != nil {
					return trial.RunWork{}, err
				}
				updated, err := trial.ResolveRun(r, at)
				if err != nil {
					return trial.RunWork{}, err
				}
				raw, err := json.Marshal(updated)
				if err == nil {
					_, err = tx.Exec(ctx, "UPDATE mantik_trial_runs SET payload=$2 WHERE attempt_id=$1", id, raw)
				}
				return trial.RunWork{Run: updated}, err
			}
		}
		return trial.RunWork{}, trial.ErrNotFound
	})
	return w.Run, err
}
func (p *Postgres) TrialRun(ctx context.Context, id string) (trial.Run, error) {
	var raw []byte
	var r trial.Run
	err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_trial_runs WHERE attempt_id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, trial.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (p *Postgres) TrialRunArtifact(ctx context.Context, id, kind string) (trial.RunArtifact, error) {
	if kind != "input" && kind != "result" {
		return trial.RunArtifact{}, trial.ErrInvalid
	}
	var metadata []byte
	var raw *string
	err := p.pool.QueryRow(ctx, "SELECT r.payload, CASE WHEN $2='input' THEN a.input_text ELSE a.result_text END FROM mantik_trial_runs r JOIN mantik_trial_run_artifacts a USING(attempt_id) WHERE r.attempt_id=$1", id, kind).Scan(&metadata, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return trial.RunArtifact{}, trial.ErrNotFound
	}
	if err != nil {
		return trial.RunArtifact{}, err
	}
	if raw == nil {
		return trial.RunArtifact{}, trial.ErrNotFound
	}
	var r trial.Run
	if err = json.Unmarshal(metadata, &r); err != nil {
		return trial.RunArtifact{}, err
	}
	return runArtifact(r, kind, *raw)
}

var _ TrialRunStore = (*Memory)(nil)
var _ TrialRunStore = (*Postgres)(nil)
