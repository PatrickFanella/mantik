package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"github.com/jackc/pgx/v5"
)

type ExternalJobStore interface {
	DeclareExternalJob(context.Context, workertrial.Command, func() time.Time) (workertrial.Record, error)
	ExternalJob(context.Context, string) (workertrial.Record, error)
	ExternalJobInput(context.Context, string, string) (workertrial.Artifact, error)
}

func externalRetry(all []workertrial.Record, c workertrial.Command) (workertrial.Record, bool, error) {
	if c.Validate() != nil {
		return workertrial.Record{}, false, trial.ErrInvalid
	}
	for _, r := range all {
		if r.Validate() != nil {
			return r, false, trial.ErrInvalid
		}
		if r.Command.RequestID == c.RequestID {
			if r.Command != c {
				return r, false, trial.ErrConflict
			}
			return r, true, nil
		}
	}
	if len(all) >= workertrial.MaxJobs {
		return workertrial.Record{}, false, trial.ErrCapacity
	}
	return workertrial.Record{}, false, nil
}
func externalClock(all []workertrial.Record, at time.Time) error {
	for _, r := range all {
		if at.Before(r.DeclaredAt) {
			return trial.ErrConflict
		}
	}
	return nil
}
func (m *Memory) DeclareExternalJob(ctx context.Context, c workertrial.Command, now func() time.Time) (workertrial.Record, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return workertrial.Record{}, ErrReceiverMismatch
	}
	all := []workertrial.Record{}
	for _, r := range m.externalJobs {
		all = append(all, r)
	}
	if r, exists, err := externalRetry(all, c); exists || err != nil {
		return clone(r), err
	}
	run, ok := m.trialRuns[c.AttemptID]
	if !ok {
		return workertrial.Record{}, trial.ErrNotFound
	}
	at := now()
	if err := externalClock(all, at); err != nil {
		return workertrial.Record{}, err
	}
	r, inputs, err := workertrial.Build(c, m.trials[run.TrialID], run, m.trialInputs[c.AttemptID], m.trialResults[c.AttemptID], at)
	if err != nil {
		return r, err
	}
	m.externalJobs[c.RequestID] = clone(r)
	m.externalInputs[c.RequestID] = clone(inputs)
	return clone(r), nil
}
func (m *Memory) ExternalJob(ctx context.Context, id string) (workertrial.Record, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.externalJobs[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	return clone(r), r.Validate()
}
func (m *Memory) ExternalJobInput(ctx context.Context, id, name string) (workertrial.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Artifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.externalJobs[id]
	if !ok {
		return workertrial.Artifact{}, trial.ErrNotFound
	}
	return workertrial.Input(r, name, string(m.externalInputs[id][name]))
}
func (p *Postgres) DeclareExternalJob(ctx context.Context, c workertrial.Command, now func() time.Time) (workertrial.Record, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return workertrial.Record{}, ErrReceiverMismatch
	}
	if c.Validate() != nil {
		return workertrial.Record{}, trial.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return workertrial.Record{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return workertrial.Record{}, err
	}
	if receiver != c.ReceiverID {
		return workertrial.Record{}, ErrReceiverMismatch
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_external_trial_jobs ORDER BY id LIMIT $1", workertrial.MaxJobs+1)
	if err != nil {
		return workertrial.Record{}, err
	}
	all := []workertrial.Record{}
	for rows.Next() {
		var raw []byte
		var r workertrial.Record
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err != nil {
			rows.Close()
			return r, err
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return workertrial.Record{}, err
	}
	if len(all) > workertrial.MaxJobs {
		return workertrial.Record{}, trial.ErrCapacity
	}
	if r, exists, err := externalRetry(all, c); exists || err != nil {
		return r, err
	}
	var run trial.Run
	var metadata []byte
	var input string
	var result *string
	err = tx.QueryRow(ctx, "SELECT r.payload,a.input_text,a.result_text FROM mantik_trial_runs r JOIN mantik_trial_run_artifacts a USING(attempt_id) WHERE r.attempt_id=$1", c.AttemptID).Scan(&metadata, &input, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return workertrial.Record{}, trial.ErrNotFound
	}
	if err != nil {
		return workertrial.Record{}, err
	}
	if err = json.Unmarshal(metadata, &run); err != nil {
		return workertrial.Record{}, err
	}
	if result == nil {
		return workertrial.Record{}, trial.ErrConflict
	}
	registration, err := loadRunTrial(ctx, tx, run.TrialID)
	if err != nil {
		return workertrial.Record{}, err
	}
	at := now()
	if err = externalClock(all, at); err != nil {
		return workertrial.Record{}, err
	}
	r, inputs, err := workertrial.Build(c, registration, run, input, *result, at)
	if err != nil {
		return r, err
	}
	metadata, err = json.Marshal(r)
	if err != nil {
		return r, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_external_trial_jobs(id,attempt_id,payload) VALUES($1,$2,$3)", c.RequestID, c.AttemptID, metadata); err != nil {
		return r, err
	}
	for name, raw := range inputs {
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_external_trial_job_inputs(id,reference,content) VALUES($1,$2,$3)", c.RequestID, name, string(raw)); err != nil {
			return r, err
		}
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) ExternalJob(ctx context.Context, id string) (workertrial.Record, error) {
	var raw []byte
	var r workertrial.Record
	err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_external_trial_jobs WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, trial.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	return r, r.Validate()
}
func (p *Postgres) ExternalJobInput(ctx context.Context, id, name string) (workertrial.Artifact, error) {
	var raw []byte
	var content *string
	err := p.pool.QueryRow(ctx, "SELECT j.payload,i.content FROM mantik_external_trial_jobs j LEFT JOIN mantik_external_trial_job_inputs i ON i.id=j.id AND i.reference=$2 WHERE j.id=$1", id, name).Scan(&raw, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return workertrial.Artifact{}, trial.ErrNotFound
	}
	if err != nil {
		return workertrial.Artifact{}, err
	}
	var r workertrial.Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return workertrial.Artifact{}, err
	}
	if content == nil {
		return workertrial.Artifact{}, trial.ErrInvalid
	}
	return workertrial.Input(r, name, *content)
}

var _ ExternalJobStore = (*Memory)(nil)
var _ ExternalJobStore = (*Postgres)(nil)
