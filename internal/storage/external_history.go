package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"github.com/jackc/pgx/v5"
)

type ExternalHistoryStore interface {
	ExternalTrialHistory(context.Context, string) (workertrial.History, error)
}

func (m *Memory) ExternalTrialHistory(ctx context.Context, id string) (workertrial.History, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.History{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.trials[id]
	if !ok {
		return workertrial.History{}, trial.ErrNotFound
	}
	if len(m.externalJobs) > workertrial.MaxJobs || checkExternalAttempts(m.externalAttempts) != nil {
		return workertrial.History{}, trial.ErrInvalid
	}
	jobs := []workertrial.Record{}
	runs := []trial.Run{}
	attempts := []workertrial.Attempt{}
	for jobID, j := range m.externalJobs {
		if j.TrialID != id {
			continue
		}
		jobs = append(jobs, j)
		runs = append(runs, m.trialRuns[j.Command.AttemptID])
		for _, a := range m.externalAttempts {
			if a.Intent.Command.JobID == jobID {
				attempts = append(attempts, a)
			}
		}
	}
	h, err := workertrial.MakeHistory(r, jobs, runs, attempts)
	return clone(h), err
}
func (p *Postgres) ExternalTrialHistory(ctx context.Context, id string) (workertrial.History, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return workertrial.History{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, err := loadRunTrial(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return workertrial.History{}, trial.ErrNotFound
	}
	if err != nil {
		return workertrial.History{}, err
	}
	rows, err := tx.Query(ctx, `SELECT j.payload,r.payload FROM mantik_external_trial_jobs j JOIN mantik_trial_runs r ON r.attempt_id=j.attempt_id WHERE j.payload->>'trial_id'=$1 ORDER BY j.id LIMIT $2`, id, workertrial.MaxJobs+1)
	if err != nil {
		return workertrial.History{}, err
	}
	jobs := []workertrial.Record{}
	runs := []trial.Run{}
	for rows.Next() {
		var jb, rb []byte
		var j workertrial.Record
		var run trial.Run
		if err = rows.Scan(&jb, &rb); err == nil {
			err = json.Unmarshal(jb, &j)
		}
		if err == nil {
			err = json.Unmarshal(rb, &run)
		}
		if err != nil {
			rows.Close()
			return workertrial.History{}, err
		}
		jobs = append(jobs, j)
		runs = append(runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return workertrial.History{}, err
	}
	all, err := loadExternalAttempts(ctx, tx)
	if err != nil {
		return workertrial.History{}, err
	}
	attempts := []workertrial.Attempt{}
	for _, j := range jobs {
		for _, a := range all {
			if a.Intent.Command.JobID == j.Command.RequestID {
				attempts = append(attempts, a)
			}
		}
	}
	h, err := workertrial.MakeHistory(r, jobs, runs, attempts)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

var _ ExternalHistoryStore = (*Memory)(nil)
var _ ExternalHistoryStore = (*Postgres)(nil)
