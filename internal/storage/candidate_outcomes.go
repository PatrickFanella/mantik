package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type CandidateWorkerOutcomeStore interface {
	CandidateWorkerOutcomeEvidence(context.Context, string) (candidateevidence.Artifact, error)
	ReportCandidateWorkerOutcome(context.Context, candidateevidence.Report, func() time.Time) (candidateevidence.OutcomeRecord, error)
	CandidateWorkerOutcome(context.Context, string) (candidateevidence.OutcomeRecord, error)
	CandidateWorkerOutcomes(context.Context) ([]candidateevidence.OutcomeRecord, error)
	CandidateWorkerOutcomeCapture(context.Context, string) (candidateevidence.Capture, error)
}

func validateCandidateOutcomes(all []candidateevidence.OutcomeRecord, intents []candidateattempt.Intent, selected string, input func(string) ([]byte, error)) (candidateevidence.Capture, error) {
	if len(all) > candidateattempt.MaxAttempts {
		return candidateevidence.Capture{}, candidateattempt.ErrInvalid
	}
	index := map[string]candidateattempt.Intent{}
	for _, i := range intents {
		index[i.Command.RequestID] = i
	}
	last := 0
	var result candidateevidence.Capture
	for _, r := range all {
		i, ok := index[r.RequestID]
		if !ok || r.IntentSequence <= last || r.IntentSequence != i.Sequence {
			return candidateevidence.Capture{}, candidateattempt.ErrInvalid
		}
		last = r.IntentSequence
		raw, err := input(r.RequestID)
		if err != nil {
			return candidateevidence.Capture{}, err
		}
		c, err := candidateevidence.Read(bytes.NewReader(raw))
		if err != nil || r.VerifyCapture(i, c) != nil {
			return candidateevidence.Capture{}, candidateattempt.ErrInvalid
		}
		if selected == r.RequestID {
			result = c
		}
	}
	return result, nil
}

func retainCandidateOutcome(all []candidateevidence.OutcomeRecord, intents []candidateattempt.Intent, report candidateevidence.Report, now func() time.Time) (candidateevidence.OutcomeRecord, bool, error) {
	i, err := selectCandidateIntent(intents, report.RequestID)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, false, err
	}
	if report.Validate() != nil || report.Capture.VerifyIntent(i) != nil || report.ReceiverID != i.Command.ReceiverID || report.IntentSHA256 != i.SHA256 {
		return candidateevidence.OutcomeRecord{}, false, candidateevidence.ErrConflict
	}
	for _, r := range all {
		if r.RequestID == report.RequestID {
			if r.CaptureSHA256 != report.Capture.SHA256 {
				return candidateevidence.OutcomeRecord{}, false, candidateevidence.ErrConflict
			}
			return r, true, nil // Exact retry never consults a new clock.
		}
	}
	r, err := candidateevidence.RetainOutcome(i, report.Capture, now())
	return r, false, err
}

// Caller holds m.mu. All registry, intent, summary and capture evidence is checked
// before a selected read or exact retry can hide unrelated corruption.
func (m *Memory) candidateOutcomeSnapshot(selected string) ([]candidateevidence.OutcomeRecord, []candidateattempt.Intent, candidateevidence.Capture, error) {
	intents, _, err := m.candidateIntentSnapshot()
	if err != nil {
		return nil, nil, candidateevidence.Capture{}, err
	}
	if len(m.candidateWorkerOutcomes) != len(m.candidateOutcomeCaptures) {
		return nil, nil, candidateevidence.Capture{}, candidateattempt.ErrInvalid
	}
	all := make([]candidateevidence.OutcomeRecord, 0, len(m.candidateWorkerOutcomes))
	for id, r := range m.candidateWorkerOutcomes {
		if id != r.RequestID {
			return nil, nil, candidateevidence.Capture{}, candidateattempt.ErrInvalid
		}
		all = append(all, r)
	}
	for id := range m.candidateOutcomeCaptures {
		if _, ok := m.candidateWorkerOutcomes[id]; !ok {
			return nil, nil, candidateevidence.Capture{}, candidateattempt.ErrInvalid
		}
	}
	slices.SortFunc(all, func(a, b candidateevidence.OutcomeRecord) int { return a.IntentSequence - b.IntentSequence })
	c, err := validateCandidateOutcomes(all, intents, selected, func(id string) ([]byte, error) { return m.candidateOutcomeCaptures[id], nil })
	return all, intents, c, err
}

func (m *Memory) ReportCandidateWorkerOutcome(ctx context.Context, report candidateevidence.Report, now func() time.Time) (candidateevidence.OutcomeRecord, error) {
	if err := ctx.Err(); err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if report.ReceiverID != m.receiverID || !ValidReceiverID(report.ReceiverID) {
		return candidateevidence.OutcomeRecord{}, ErrReceiverMismatch
	}
	all, intents, _, err := m.candidateOutcomeSnapshot("")
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	r, duplicate, err := retainCandidateOutcome(all, intents, report, now)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	if !duplicate {
		raw, err := json.Marshal(report.Capture)
		if err != nil {
			return candidateevidence.OutcomeRecord{}, err
		}
		m.candidateWorkerOutcomes[r.RequestID], m.candidateOutcomeCaptures[r.RequestID] = r, raw
	}
	return r, nil
}

func (m *Memory) candidateOutcomeRead(ctx context.Context, id string) ([]candidateevidence.OutcomeRecord, candidateevidence.Capture, error) {
	if err := ctx.Err(); err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, _, c, err := m.candidateOutcomeSnapshot(id)
	return all, c, err
}

func selectCandidateOutcome(all []candidateevidence.OutcomeRecord, id string) (candidateevidence.OutcomeRecord, error) {
	if !review.ID.MatchString(id) {
		return candidateevidence.OutcomeRecord{}, candidateattempt.ErrInvalid
	}
	for _, r := range all {
		if r.RequestID == id {
			return r, nil
		}
	}
	return candidateevidence.OutcomeRecord{}, candidateattempt.ErrNotFound
}

func (m *Memory) CandidateWorkerOutcomes(ctx context.Context) ([]candidateevidence.OutcomeRecord, error) {
	all, _, err := m.candidateOutcomeRead(ctx, "")
	return all, err
}
func (m *Memory) CandidateWorkerOutcome(ctx context.Context, id string) (candidateevidence.OutcomeRecord, error) {
	all, _, err := m.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	return selectCandidateOutcome(all, id)
}
func (m *Memory) CandidateWorkerOutcomeCapture(ctx context.Context, id string) (candidateevidence.Capture, error) {
	all, c, err := m.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.Capture{}, err
	}
	_, err = selectCandidateOutcome(all, id)
	return c, err
}

func loadCandidateOutcomes(ctx context.Context, tx pgx.Tx, intents []candidateattempt.Intent, selected string) ([]candidateevidence.OutcomeRecord, candidateevidence.Capture, error) {
	rows, err := tx.Query(ctx, "SELECT id,sequence,payload FROM mantik_candidate_worker_outcomes ORDER BY sequence LIMIT $1", candidateattempt.MaxAttempts+1)
	if err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	all := []candidateevidence.OutcomeRecord{}
	for rows.Next() {
		var id string
		var sequence int
		var raw []byte
		var r candidateevidence.OutcomeRecord
		if err = rows.Scan(&id, &sequence, &raw); err == nil {
			err = workerplan.ReadBoundedShape(raw, &r, candidateevidence.MaxOutcomeBytes)
		}
		if err != nil || id != r.RequestID || sequence != r.IntentSequence {
			rows.Close()
			return nil, candidateevidence.Capture{}, candidateattempt.ErrInvalid
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	c, err := validateCandidateOutcomes(all, intents, selected, func(id string) ([]byte, error) {
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT capture FROM mantik_candidate_worker_outcomes WHERE id=$1", id).Scan(&raw)
		return raw, err
	})
	return all, c, err
}

func outcomeDependencies(ctx context.Context, tx pgx.Tx) (string, []candidateattempt.Intent, error) {
	receiver, registered, _, err := loadCandidates(ctx, tx, "")
	if err != nil {
		return "", nil, err
	}
	intents, err := loadCandidateIntents(ctx, tx, receiver, candidateRegistrationMap(registered))
	return receiver, intents, err
}

func (p *Postgres) ReportCandidateWorkerOutcome(ctx context.Context, report candidateevidence.Report, now func() time.Time) (candidateevidence.OutcomeRecord, error) {
	if !ValidReceiverID(report.ReceiverID) {
		return candidateevidence.OutcomeRecord{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	if receiver != report.ReceiverID {
		return candidateevidence.OutcomeRecord{}, ErrReceiverMismatch
	}
	_, intents, err := outcomeDependencies(ctx, tx)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	all, _, err := loadCandidateOutcomes(ctx, tx, intents, "")
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	r, duplicate, err := retainCandidateOutcome(all, intents, report, now)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	if !duplicate {
		payload, err := json.Marshal(r)
		if err != nil {
			return candidateevidence.OutcomeRecord{}, err
		}
		raw, err := json.Marshal(report.Capture)
		if err != nil {
			return candidateevidence.OutcomeRecord{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_candidate_worker_outcomes(id,sequence,payload,capture) VALUES($1,$2,$3,$4)", r.RequestID, r.IntentSequence, payload, raw); err != nil {
			return candidateevidence.OutcomeRecord{}, err
		}
	}
	return r, tx.Commit(ctx)
}

func (p *Postgres) candidateOutcomeRead(ctx context.Context, id string) ([]candidateevidence.OutcomeRecord, candidateevidence.Capture, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, intents, err := outcomeDependencies(ctx, tx)
	if err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	all, c, err := loadCandidateOutcomes(ctx, tx, intents, id)
	if err != nil {
		return nil, candidateevidence.Capture{}, err
	}
	return all, c, tx.Commit(ctx)
}
func (p *Postgres) CandidateWorkerOutcomes(ctx context.Context) ([]candidateevidence.OutcomeRecord, error) {
	all, _, err := p.candidateOutcomeRead(ctx, "")
	return all, err
}
func (p *Postgres) CandidateWorkerOutcome(ctx context.Context, id string) (candidateevidence.OutcomeRecord, error) {
	all, _, err := p.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.OutcomeRecord{}, err
	}
	return selectCandidateOutcome(all, id)
}
func (p *Postgres) CandidateWorkerOutcomeCapture(ctx context.Context, id string) (candidateevidence.Capture, error) {
	all, c, err := p.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.Capture{}, err
	}
	_, err = selectCandidateOutcome(all, id)
	return c, err
}

var _ CandidateWorkerOutcomeStore = (*Memory)(nil)
var _ CandidateWorkerOutcomeStore = (*Postgres)(nil)

func (p *Memory) CandidateWorkerOutcomeEvidence(ctx context.Context, id string) (candidateevidence.Artifact, error) {
	all, c, err := p.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.Artifact{}, err
	}
	r, err := selectCandidateOutcome(all, id)
	if err != nil {
		return candidateevidence.Artifact{}, err
	}
	return candidateevidence.NewArtifact(r, c)
}

func (p *Postgres) CandidateWorkerOutcomeEvidence(ctx context.Context, id string) (candidateevidence.Artifact, error) {
	all, c, err := p.candidateOutcomeRead(ctx, id)
	if err != nil {
		return candidateevidence.Artifact{}, err
	}
	r, err := selectCandidateOutcome(all, id)
	if err != nil {
		return candidateevidence.Artifact{}, err
	}
	return candidateevidence.NewArtifact(r, c)
}
