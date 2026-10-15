package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"github.com/jackc/pgx/v5"
)

type TrialEvidenceStore interface {
	TrialEvidence(context.Context, string) (trial.Evidence, error)
}

func (m *Memory) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return trial.Evidence{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.trialAttempts[id]
	if !ok {
		return trial.Evidence{}, trial.ErrNotFound
	}
	r := m.trials[a.TrialID]
	e := trial.Evidence{Attempt: a, Registration: r, Publication: m.publications[r.PublicationID], Result: m.trialResults[id]}
	if run, ok := m.trialRuns[id]; ok {
		e.Run = &run
	}
	return clone(e), nil
}

func (p *Postgres) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	// One statement gives one MVCC snapshot, including run metadata and its body.
	var a, registration, publication, run []byte
	var result *string
	err := p.pool.QueryRow(ctx, `SELECT a.payload,t.payload,p.payload,r.payload,b.result_text
		FROM mantik_trial_attempts a
		JOIN mantik_hypothesis_trials t ON t.id=a.trial_id
		JOIN mantik_hypothesis_publications p ON p.id=t.payload->>'publication_id'
		LEFT JOIN mantik_trial_runs r ON r.attempt_id=a.id
		LEFT JOIN mantik_trial_run_artifacts b ON b.attempt_id=a.id
		WHERE a.id=$1`, id).Scan(&a, &registration, &publication, &run, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return trial.Evidence{}, trial.ErrNotFound
	}
	if err != nil {
		return trial.Evidence{}, err
	}
	var e trial.Evidence
	for _, pair := range []struct {
		raw    []byte
		target any
	}{{a, &e.Attempt}, {registration, &e.Registration}, {publication, &e.Publication}} {
		if err = json.Unmarshal(pair.raw, pair.target); err != nil {
			return trial.Evidence{}, err
		}
	}
	if run != nil {
		e.Run = new(trial.Run)
		if err = json.Unmarshal(run, e.Run); err != nil {
			return trial.Evidence{}, err
		}
	}
	if result != nil {
		e.Result = *result
	}
	return e, nil
}

var _ TrialEvidenceStore = (*Memory)(nil)
var _ TrialEvidenceStore = (*Postgres)(nil)
