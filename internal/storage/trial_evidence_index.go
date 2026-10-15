package storage

import (
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type TrialEvidenceIndexStore interface {
	TrialEvidenceIndex(context.Context, string) (trial.EvidenceIndex, error)
}

func (m *Memory) TrialEvidenceIndex(ctx context.Context, id string) (trial.EvidenceIndex, error) {
	if err := ctx.Err(); err != nil {
		return trial.EvidenceIndex{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.trials[id]
	if !ok {
		return trial.EvidenceIndex{}, trial.ErrNotFound
	}
	attempts := []trial.Attempt{}
	runs := []trial.Run{}
	for _, a := range m.trialAttempts {
		if a.TrialID == id {
			attempts = append(attempts, a)
			if run, ok := m.trialRuns[a.ID]; ok {
				runs = append(runs, run)
			}
		}
	}
	return trial.MakeEvidenceIndex(r, attempts, runs)
}
func (p *Postgres) TrialEvidenceIndex(ctx context.Context, id string) (trial.EvidenceIndex, error) {
	// One statement captures the complete bounded catalogue without artifact bodies.
	rows, err := p.pool.Query(ctx, `SELECT t.payload,a.payload,r.payload FROM mantik_hypothesis_trials t
		LEFT JOIN mantik_trial_attempts a ON a.trial_id=t.id
		LEFT JOIN mantik_trial_runs r ON r.attempt_id=a.id
		WHERE t.id=$1 ORDER BY a.id LIMIT $2`, id, trial.MaxAttempts+1)
	if err != nil {
		return trial.EvidenceIndex{}, err
	}
	defer rows.Close()
	var registration trial.Registration
	attempts := []trial.Attempt{}
	runs := []trial.Run{}
	for rows.Next() {
		var r, a, run []byte
		if err = rows.Scan(&r, &a, &run); err != nil {
			return trial.EvidenceIndex{}, err
		}
		if err = json.Unmarshal(r, &registration); err != nil {
			return trial.EvidenceIndex{}, err
		}
		if a != nil {
			var intent trial.Attempt
			if err = json.Unmarshal(a, &intent); err != nil {
				return trial.EvidenceIndex{}, err
			}
			attempts = append(attempts, intent)
		}
		if run != nil {
			var metadata trial.Run
			if err = json.Unmarshal(run, &metadata); err != nil {
				return trial.EvidenceIndex{}, err
			}
			runs = append(runs, metadata)
		}
	}
	if err = rows.Err(); err != nil {
		return trial.EvidenceIndex{}, err
	}
	if registration.ID == "" {
		return trial.EvidenceIndex{}, trial.ErrNotFound
	}
	return trial.MakeEvidenceIndex(registration, attempts, runs)
}

var _ TrialEvidenceIndexStore = (*Memory)(nil)
var _ TrialEvidenceIndexStore = (*Postgres)(nil)
