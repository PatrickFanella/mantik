package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
	"slices"
	"time"
)

type CandidateGenerationOutcomeStore interface {
	ReportCandidateGeneration(context.Context, candidategeneration.Report, func() time.Time) (candidategeneration.Outcome, error)
	CandidateGenerationOutcomeHistory(context.Context) (candidategeneration.OutcomeHistory, error)
	CandidateGenerationOutcome(context.Context, string) (candidategeneration.Outcome, error)
	CandidateGenerationOutcomeEvidence(context.Context, string) (candidategeneration.OutcomeArtifact, error)
}

func selectGenerationOutcome(all []candidategeneration.Outcome, id string) (candidategeneration.Outcome, error) {
	if !review.ID.MatchString(id) {
		return candidategeneration.Outcome{}, candidategeneration.ErrInvalid
	}
	for _, o := range all {
		if o.RequestID == id {
			return o, nil
		}
	}
	return candidategeneration.Outcome{}, candidategeneration.ErrNotFound
}

// The caller validates the complete authoritative intent/publication/dataset
// prefix first. Full recordings are then checked one at a time; only selected
// output is retained beyond validation.
func validateGenerationOutcomes(receiver string, intents []candidategeneration.Intent, all []candidategeneration.Outcome, selected string, read func(string) ([]byte, []byte, error)) (candidategeneration.Recording, error) {
	ih, err := candidategeneration.IntentsFor(receiver, intents)
	if err != nil {
		return candidategeneration.Recording{}, err
	}
	if _, err := candidategeneration.OutcomesFor(ih, all); err != nil {
		return candidategeneration.Recording{}, err
	}
	var result candidategeneration.Recording
	for _, o := range all {
		i := intents[o.IntentSequence-1]
		encoded, data, err := read(o.RequestID)
		if err != nil {
			return result, err
		}
		r, err := candidategeneration.ReadRecording(bytes.NewReader(encoded))
		if err != nil || r.VerifyDataset(data) != nil {
			return result, candidategeneration.ErrInvalid
		}
		if _, err := candidategeneration.NewOutcomeArtifact(i, o, r); err != nil {
			return result, err
		}
		if o.RequestID == selected {
			result = r
		}
	}
	return result, nil
}

// Caller holds m.mu. A selected read/retry cannot hide unrelated corruption.
func (m *Memory) generationOutcomeSnapshot(id string) ([]candidategeneration.Intent, []candidategeneration.Outcome, candidategeneration.Recording, error) {
	intents, _, err := m.generationIntentSnapshot("")
	if err != nil {
		return nil, nil, candidategeneration.Recording{}, err
	}
	if len(m.generationOutcomes) != len(m.generationRecordings) {
		return nil, nil, candidategeneration.Recording{}, candidategeneration.ErrInvalid
	}
	all := make([]candidategeneration.Outcome, 0, len(m.generationOutcomes))
	for key, o := range m.generationOutcomes {
		if key != o.RequestID {
			return nil, nil, candidategeneration.Recording{}, candidategeneration.ErrInvalid
		}
		all = append(all, o)
	}
	for key := range m.generationRecordings {
		if _, ok := m.generationOutcomes[key]; !ok {
			return nil, nil, candidategeneration.Recording{}, candidategeneration.ErrInvalid
		}
	}
	slices.SortFunc(all, func(a, b candidategeneration.Outcome) int { return a.IntentSequence - b.IntentSequence })
	r, err := validateGenerationOutcomes(m.receiverID, intents, all, id, func(key string) ([]byte, []byte, error) {
		return m.generationRecordings[key], m.generationDatasets[key], nil
	})
	return intents, all, r, err
}
func (m *Memory) ReportCandidateGeneration(ctx context.Context, r candidategeneration.Report, now func() time.Time) (candidategeneration.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return candidategeneration.Outcome{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return candidategeneration.Outcome{}, err
	}
	if !ValidReceiverID(r.ReceiverID) || r.ReceiverID != m.receiverID {
		return candidategeneration.Outcome{}, ErrReceiverMismatch
	}
	intents, all, _, err := m.generationOutcomeSnapshot("")
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	// Refuse a report without its own retained intent before checking raw pins.
	if _, err := selectGenerationIntent(intents, r.RequestID); err != nil {
		return candidategeneration.Outcome{}, err
	}
	if r.Recording.VerifyDataset(m.generationDatasets[r.RequestID]) != nil {
		return candidategeneration.Outcome{}, candidategeneration.ErrInvalid
	}
	ih, err := candidategeneration.IntentsFor(m.receiverID, intents)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	o, duplicate, err := candidategeneration.AppendOutcome(ih, all, r, now)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return candidategeneration.Outcome{}, err
	}
	if !duplicate {
		encoded, err := json.Marshal(r.Recording)
		if err != nil {
			return candidategeneration.Outcome{}, err
		}
		m.generationOutcomes[o.RequestID], m.generationRecordings[o.RequestID] = o, encoded
	}
	return o, nil
}
func (m *Memory) generationOutcomeRead(ctx context.Context, id string) (candidategeneration.OutcomeHistory, candidategeneration.Recording, error) {
	if err := ctx.Err(); err != nil {
		return candidategeneration.OutcomeHistory{}, candidategeneration.Recording{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	intents, all, r, err := m.generationOutcomeSnapshot(id)
	if err != nil {
		return candidategeneration.OutcomeHistory{}, r, err
	}
	ih, err := candidategeneration.IntentsFor(m.receiverID, intents)
	if err != nil {
		return candidategeneration.OutcomeHistory{}, r, err
	}
	h, err := candidategeneration.OutcomesFor(ih, all)
	return h, r, err
}
func (m *Memory) CandidateGenerationOutcomeHistory(ctx context.Context) (candidategeneration.OutcomeHistory, error) {
	h, _, err := m.generationOutcomeRead(ctx, "")
	return h, err
}
func (m *Memory) CandidateGenerationOutcome(ctx context.Context, id string) (candidategeneration.Outcome, error) {
	h, _, err := m.generationOutcomeRead(ctx, id)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	return selectGenerationOutcome(h.Outcomes, id)
}
func generationOutcomeArtifact(h candidategeneration.OutcomeHistory, r candidategeneration.Recording, id string) (candidategeneration.OutcomeArtifact, error) {
	o, err := selectGenerationOutcome(h.Outcomes, id)
	if err != nil {
		return candidategeneration.OutcomeArtifact{}, err
	}
	return candidategeneration.NewOutcomeArtifact(h.IntentHistory.Intents[o.IntentSequence-1], o, r)
}
func (m *Memory) CandidateGenerationOutcomeEvidence(ctx context.Context, id string) (candidategeneration.OutcomeArtifact, error) {
	h, r, err := m.generationOutcomeRead(ctx, id)
	if err != nil {
		return candidategeneration.OutcomeArtifact{}, err
	}
	return generationOutcomeArtifact(h, r, id)
}
func loadGenerationOutcomes(ctx context.Context, tx pgx.Tx, receiver string, intents []candidategeneration.Intent, id string) ([]candidategeneration.Outcome, candidategeneration.Recording, error) {
	rows, err := tx.Query(ctx, "SELECT id,sequence,payload FROM mantik_candidate_generation_outcomes ORDER BY sequence LIMIT $1", candidategeneration.MaxIntents+1)
	if err != nil {
		return nil, candidategeneration.Recording{}, err
	}
	all := []candidategeneration.Outcome{}
	for rows.Next() {
		var key string
		var seq int
		var raw []byte
		var o candidategeneration.Outcome
		if err := rows.Scan(&key, &seq, &raw); err != nil {
			rows.Close()
			return nil, candidategeneration.Recording{}, err
		}
		if workerplan.ReadBoundedShape(raw, &o, candidategeneration.MaxOutcomeBytes) != nil || key != o.RequestID || seq != o.IntentSequence {
			rows.Close()
			return nil, candidategeneration.Recording{}, candidategeneration.ErrInvalid
		}
		all = append(all, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, candidategeneration.Recording{}, err
	}
	r, err := validateGenerationOutcomes(receiver, intents, all, id, func(key string) ([]byte, []byte, error) {
		var rec, data []byte
		err := tx.QueryRow(ctx, `SELECT o.recording,d.content FROM mantik_candidate_generation_outcomes o LEFT JOIN mantik_candidate_generation_datasets d ON d.id=o.id WHERE o.id=$1`, key).Scan(&rec, &data)
		return rec, data, err
	})
	return all, r, err
}
func (p *Postgres) ReportCandidateGeneration(ctx context.Context, r candidategeneration.Report, now func() time.Time) (candidategeneration.Outcome, error) {
	if !ValidReceiverID(r.ReceiverID) {
		return candidategeneration.Outcome{}, ErrReceiverMismatch
	}
	if r.Validate() != nil {
		return candidategeneration.Outcome{}, candidategeneration.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return candidategeneration.Outcome{}, err
	}
	if r.ReceiverID != receiver {
		return candidategeneration.Outcome{}, ErrReceiverMismatch
	}
	_, intents, data, err := loadGenerationIntents(ctx, tx, r.RequestID)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	all, _, err := loadGenerationOutcomes(ctx, tx, receiver, intents, "")
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	if _, err := selectGenerationIntent(intents, r.RequestID); err != nil {
		return candidategeneration.Outcome{}, err
	}
	if r.Recording.VerifyDataset(data) != nil {
		return candidategeneration.Outcome{}, candidategeneration.ErrInvalid
	}
	ih, err := candidategeneration.IntentsFor(receiver, intents)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	o, duplicate, err := candidategeneration.AppendOutcome(ih, all, r, now)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	if !duplicate {
		payload, err := json.Marshal(o)
		if err != nil {
			return candidategeneration.Outcome{}, err
		}
		rec, err := json.Marshal(r.Recording)
		if err != nil {
			return candidategeneration.Outcome{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_candidate_generation_outcomes(id,sequence,payload,recording) VALUES($1,$2,$3,$4)", o.RequestID, o.IntentSequence, payload, rec); err != nil {
			return candidategeneration.Outcome{}, err
		}
	}
	return o, tx.Commit(ctx)
}
func (p *Postgres) generationOutcomeRead(ctx context.Context, id string) (candidategeneration.OutcomeHistory, candidategeneration.Recording, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return candidategeneration.OutcomeHistory{}, candidategeneration.Recording{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receiver, intents, _, err := loadGenerationIntents(ctx, tx, "")
	if err != nil {
		return candidategeneration.OutcomeHistory{}, candidategeneration.Recording{}, err
	}
	all, r, err := loadGenerationOutcomes(ctx, tx, receiver, intents, id)
	if err != nil {
		return candidategeneration.OutcomeHistory{}, r, err
	}
	ih, err := candidategeneration.IntentsFor(receiver, intents)
	if err != nil {
		return candidategeneration.OutcomeHistory{}, r, err
	}
	h, err := candidategeneration.OutcomesFor(ih, all)
	if err != nil {
		return candidategeneration.OutcomeHistory{}, r, err
	}
	return h, r, tx.Commit(ctx)
}
func (p *Postgres) CandidateGenerationOutcomeHistory(ctx context.Context) (candidategeneration.OutcomeHistory, error) {
	h, _, err := p.generationOutcomeRead(ctx, "")
	return h, err
}
func (p *Postgres) CandidateGenerationOutcome(ctx context.Context, id string) (candidategeneration.Outcome, error) {
	h, _, err := p.generationOutcomeRead(ctx, id)
	if err != nil {
		return candidategeneration.Outcome{}, err
	}
	return selectGenerationOutcome(h.Outcomes, id)
}
func (p *Postgres) CandidateGenerationOutcomeEvidence(ctx context.Context, id string) (candidategeneration.OutcomeArtifact, error) {
	h, r, err := p.generationOutcomeRead(ctx, id)
	if err != nil {
		return candidategeneration.OutcomeArtifact{}, err
	}
	return generationOutcomeArtifact(h, r, id)
}

var _ CandidateGenerationOutcomeStore = (*Memory)(nil)
var _ CandidateGenerationOutcomeStore = (*Postgres)(nil)
