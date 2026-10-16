package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"github.com/jackc/pgx/v5"
)

type ExternalAttemptStore interface {
	StartExternalAttempt(context.Context, workertrial.AttemptCommand, func() time.Time) (workertrial.Intent, error)
	ReportExternalAttempt(context.Context, workertrial.ReportCommand, func() time.Time) (workertrial.Outcome, error)
	ExternalAttempt(context.Context, string) (workertrial.Attempt, error)
	ExternalAttempts(context.Context, string) ([]workertrial.Attempt, error)
	ExternalAttemptOutput(context.Context, string, string) (workertrial.Artifact, error)
}

func checkExternalAttempts(all map[string]workertrial.Attempt) error {
	if len(all) > workertrial.MaxAttempts {
		return trial.ErrCapacity
	}
	for id, a := range all {
		if a.Validate() != nil || id != a.Intent.Command.RequestID {
			return trial.ErrInvalid
		}
	}
	return nil
}
func startExternal(all map[string]workertrial.Attempt, c workertrial.AttemptCommand, job func() (workertrial.Record, error), now func() time.Time) (workertrial.Intent, bool, error) {
	if c.Validate() != nil {
		return workertrial.Intent{}, false, trial.ErrInvalid
	}
	if err := checkExternalAttempts(all); err != nil {
		return workertrial.Intent{}, false, err
	}
	if a, ok := all[c.RequestID]; ok {
		if a.Intent.Command != c {
			return workertrial.Intent{}, false, trial.ErrConflict
		}
		return a.Intent, false, nil
	}
	if len(all) >= workertrial.MaxAttempts {
		return workertrial.Intent{}, false, trial.ErrCapacity
	}
	r, err := job()
	if err != nil {
		return workertrial.Intent{}, false, err
	}
	at := now()
	for _, a := range all {
		if at.Before(a.Intent.CreatedAt) || a.Outcome != nil && at.Before(a.Outcome.RetainedAt) {
			return workertrial.Intent{}, false, trial.ErrConflict
		}
	}
	i, err := workertrial.Start(c, r, at)
	return i, err == nil, err
}
func reportExternal(all map[string]workertrial.Attempt, c workertrial.ReportCommand, job func() (workertrial.Record, error), now func() time.Time) (workertrial.Outcome, bool, error) {
	if c.Validate() != nil {
		return workertrial.Outcome{}, false, trial.ErrInvalid
	}
	if err := checkExternalAttempts(all); err != nil {
		return workertrial.Outcome{}, false, err
	}
	a, ok := all[c.AttemptID]
	if !ok {
		return workertrial.Outcome{}, false, trial.ErrNotFound
	}
	if a.Intent.Command.ReceiverID != c.ReceiverID || a.Intent.SHA256 != c.IntentSHA256 {
		return workertrial.Outcome{}, false, trial.ErrConflict
	}
	if a.Outcome != nil {
		if a.Outcome.RequestSHA256 != trial.Hash(c) {
			return workertrial.Outcome{}, false, trial.ErrConflict
		}
		return *a.Outcome, false, nil
	}
	r, err := job()
	if err != nil {
		return workertrial.Outcome{}, false, err
	}
	o, err := workertrial.Finish(a.Intent, r, c, now())
	return o, err == nil, err
}
func externalHistory(all map[string]workertrial.Attempt, id string) ([]workertrial.Attempt, error) {
	if err := checkExternalAttempts(all); err != nil {
		return nil, err
	}
	result := []workertrial.Attempt{}
	for _, a := range all {
		if a.Intent.Command.JobID == id {
			result = append(result, a)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Intent.Command.RequestID < result[j].Intent.Command.RequestID })
	return result, nil
}
func (m *Memory) StartExternalAttempt(ctx context.Context, c workertrial.AttemptCommand, now func() time.Time) (workertrial.Intent, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Intent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID {
		return workertrial.Intent{}, ErrReceiverMismatch
	}
	i, created, err := startExternal(m.externalAttempts, c, func() (workertrial.Record, error) {
		r, ok := m.externalJobs[c.JobID]
		if !ok {
			return r, trial.ErrNotFound
		}
		return r, nil
	}, now)
	if created {
		m.externalAttempts[c.RequestID] = workertrial.Attempt{Intent: clone(i)}
	}
	return clone(i), err
}
func (m *Memory) ReportExternalAttempt(ctx context.Context, c workertrial.ReportCommand, now func() time.Time) (workertrial.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Outcome{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID {
		return workertrial.Outcome{}, ErrReceiverMismatch
	}
	o, created, err := reportExternal(m.externalAttempts, c, func() (workertrial.Record, error) {
		a := m.externalAttempts[c.AttemptID]
		return m.externalJobs[a.Intent.Command.JobID], nil
	}, now)
	if created {
		a := m.externalAttempts[c.AttemptID]
		v := clone(o)
		a.Outcome = &v
		m.externalAttempts[c.AttemptID] = a
		m.externalOutputs[c.AttemptID] = map[string]string{"stdout": c.Stdout, "stderr": c.Stderr}
	}
	return clone(o), err
}
func (m *Memory) ExternalAttempt(ctx context.Context, id string) (workertrial.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.externalAttempts[id]
	if !ok {
		return a, trial.ErrNotFound
	}
	return clone(a), a.Validate()
}
func (m *Memory) ExternalAttempts(ctx context.Context, id string) ([]workertrial.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.externalJobs[id]; !ok {
		return nil, trial.ErrNotFound
	}
	all, err := externalHistory(m.externalAttempts, id)
	return clone(all), err
}
func (m *Memory) ExternalAttemptOutput(ctx context.Context, id, name string) (workertrial.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return workertrial.Artifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.externalAttempts[id]
	if !ok {
		return workertrial.Artifact{}, trial.ErrNotFound
	}
	return workertrial.Output(a, name, m.externalOutputs[id][name])
}
func loadExternalAttempts(ctx context.Context, tx pgx.Tx) (map[string]workertrial.Attempt, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_external_trial_attempts ORDER BY id LIMIT $1", workertrial.MaxAttempts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]workertrial.Attempt{}
	for rows.Next() {
		var id string
		var raw []byte
		var a workertrial.Attempt
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		all[id] = a
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return all, checkExternalAttempts(all)
}
func loadExternalJob(ctx context.Context, tx pgx.Tx, id string) (workertrial.Record, error) {
	var raw []byte
	var r workertrial.Record
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_external_trial_jobs WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, trial.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	if err != nil {
		return r, err
	}
	return r, r.Validate()
}
func (p *Postgres) externalAttemptTx(ctx context.Context, receiver string) (pgx.Tx, map[string]workertrial.Attempt, error) {
	if !ValidReceiverID(receiver) {
		return nil, nil, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	var actual string
	err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&actual)
	if err == nil && actual != receiver {
		err = ErrReceiverMismatch
	}
	var all map[string]workertrial.Attempt
	if err == nil {
		all, err = loadExternalAttempts(ctx, tx)
	}
	if err != nil {
		_ = tx.Rollback(context.Background())
		return nil, nil, err
	}
	return tx, all, nil
}
func (p *Postgres) StartExternalAttempt(ctx context.Context, c workertrial.AttemptCommand, now func() time.Time) (workertrial.Intent, error) {
	if c.Validate() != nil {
		return workertrial.Intent{}, trial.ErrInvalid
	}
	tx, all, err := p.externalAttemptTx(ctx, c.ReceiverID)
	if err != nil {
		return workertrial.Intent{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	i, created, err := startExternal(all, c, func() (workertrial.Record, error) { return loadExternalJob(ctx, tx, c.JobID) }, now)
	if err != nil || !created {
		return i, err
	}
	raw, err := json.Marshal(workertrial.Attempt{Intent: i})
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO mantik_external_trial_attempts(id,job_id,payload) VALUES($1,$2,$3)", c.RequestID, c.JobID, raw)
	}
	if err != nil {
		return i, err
	}
	return i, tx.Commit(ctx)
}
func (p *Postgres) ReportExternalAttempt(ctx context.Context, c workertrial.ReportCommand, now func() time.Time) (workertrial.Outcome, error) {
	if c.Validate() != nil {
		return workertrial.Outcome{}, trial.ErrInvalid
	}
	tx, all, err := p.externalAttemptTx(ctx, c.ReceiverID)
	if err != nil {
		return workertrial.Outcome{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	o, created, err := reportExternal(all, c, func() (workertrial.Record, error) {
		return loadExternalJob(ctx, tx, all[c.AttemptID].Intent.Command.JobID)
	}, now)
	if err != nil || !created {
		return o, err
	}
	a := all[c.AttemptID]
	a.Outcome = &o
	raw, err := json.Marshal(a)
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE mantik_external_trial_attempts SET payload=$2 WHERE id=$1", c.AttemptID, raw)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO mantik_external_trial_attempt_outputs(id,stdout,stderr) VALUES($1,$2,$3)", c.AttemptID, c.Stdout, c.Stderr)
	}
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
func (p *Postgres) ExternalAttempt(ctx context.Context, id string) (workertrial.Attempt, error) {
	var raw []byte
	var a workertrial.Attempt
	err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_external_trial_attempts WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, trial.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	return a, a.Validate()
}
func (p *Postgres) ExternalAttempts(ctx context.Context, id string) ([]workertrial.Attempt, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = loadExternalJob(ctx, tx, id); err != nil {
		return nil, err
	}
	all, err := loadExternalAttempts(ctx, tx)
	if err != nil {
		return nil, err
	}
	result, err := externalHistory(all, id)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
func (p *Postgres) ExternalAttemptOutput(ctx context.Context, id, name string) (workertrial.Artifact, error) {
	var raw []byte
	var stdout, stderr *string
	err := p.pool.QueryRow(ctx, "SELECT a.payload,o.stdout,o.stderr FROM mantik_external_trial_attempts a LEFT JOIN mantik_external_trial_attempt_outputs o USING(id) WHERE a.id=$1", id).Scan(&raw, &stdout, &stderr)
	if errors.Is(err, pgx.ErrNoRows) {
		return workertrial.Artifact{}, trial.ErrNotFound
	}
	if err != nil {
		return workertrial.Artifact{}, err
	}
	var a workertrial.Attempt
	if err = json.Unmarshal(raw, &a); err != nil {
		return workertrial.Artifact{}, err
	}
	if stdout == nil || stderr == nil {
		return workertrial.Artifact{}, trial.ErrConflict
	}
	body := ""
	if name == "stdout" {
		body = *stdout
	} else if name == "stderr" {
		body = *stderr
	}
	return workertrial.Output(a, name, body)
}

var _ ExternalAttemptStore = (*Memory)(nil)
var _ ExternalAttemptStore = (*Postgres)(nil)
