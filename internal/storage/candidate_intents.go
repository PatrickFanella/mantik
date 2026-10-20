package storage

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type CandidateWorkerIntentStore interface {
	CandidateWorkerIntentHistory(context.Context) (candidateattempt.History, error)
	StartCandidateWorker(context.Context, candidateattempt.Command, func() time.Time) (candidateattempt.Intent, error)
	CandidateWorkerIntent(context.Context, string) (candidateattempt.Intent, error)
	CandidateWorkerIntents(context.Context) ([]candidateattempt.Intent, error)
}

func candidateRegistrationMap(all []candidatelineage.Registration) map[string]candidatelineage.Registration {
	out := make(map[string]candidatelineage.Registration, len(all))
	for _, r := range all {
		out[r.ID] = r
	}
	return out
}

func validateCandidateIntents(receiverID string, all []candidateattempt.Intent, regs map[string]candidatelineage.Registration, input func(string) ([]byte, error)) error {
	if candidateattempt.ValidateHistory(receiverID, all) != nil {
		return candidateattempt.ErrInvalid
	}
	checked := map[string]bool{}
	for _, i := range all {
		r, ok := regs[i.Command.RegistrationID]
		if !ok || i.VerifyBinding(r) != nil {
			return candidateattempt.ErrInvalid
		}
		if !checked[i.Command.Job.SHA256] {
			raw, err := input(r.ID)
			if err != nil {
				return err
			}
			if i.Command.Job.Plan.VerifyDataset(raw) != nil {
				return candidateattempt.ErrInvalid
			}
			checked[i.Command.Job.SHA256] = true
		}
	}
	return nil
}

func appendCandidateIntent(all []candidateattempt.Intent, c candidateattempt.Command, regs map[string]candidatelineage.Registration, raw []byte, now func() time.Time) (candidateattempt.Intent, bool, error) {
	duplicate := false
	for _, i := range all {
		if i.Command.RequestID == c.RequestID {
			duplicate = true
		}
	}
	r, exists := regs[c.RegistrationID]
	if !exists && !duplicate {
		return candidateattempt.Intent{}, false, candidateattempt.ErrNotFound
	}
	i, err := candidateattempt.Append(all, c, r, raw, now)
	return i, duplicate, err
}

// The caller holds m.mu. The registry and all intent evidence are checked before
// a selected row or exact retry can hide damage in an unrelated declaration.
func (m *Memory) candidateIntentSnapshot() ([]candidateattempt.Intent, map[string]candidatelineage.Registration, error) {
	registered, err := m.candidateSnapshot()
	if err != nil {
		return nil, nil, err
	}
	regs := candidateRegistrationMap(registered)
	all := make([]candidateattempt.Intent, 0, len(m.candidateWorkerIntents))
	for id, i := range m.candidateWorkerIntents {
		if id != i.Command.RequestID {
			return nil, nil, candidateattempt.ErrInvalid
		}
		all = append(all, i)
	}
	slices.SortFunc(all, func(a, b candidateattempt.Intent) int { return a.Sequence - b.Sequence })
	err = validateCandidateIntents(m.receiverID, all, regs, func(id string) ([]byte, error) { return m.candidateDatasets[id], nil })
	return all, regs, err
}

func (m *Memory) StartCandidateWorker(ctx context.Context, c candidateattempt.Command, now func() time.Time) (candidateattempt.Intent, error) {
	if err := ctx.Err(); err != nil {
		return candidateattempt.Intent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return candidateattempt.Intent{}, ErrReceiverMismatch
	}
	if c.Validate() != nil {
		return candidateattempt.Intent{}, candidateattempt.ErrInvalid
	}
	all, regs, err := m.candidateIntentSnapshot()
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	i, duplicate, err := appendCandidateIntent(all, c, regs, m.candidateDatasets[c.RegistrationID], now)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	if !duplicate {
		m.candidateWorkerIntents[c.RequestID] = clone(i)
	}
	return clone(i), nil
}

func (m *Memory) CandidateWorkerIntentHistory(ctx context.Context) (candidateattempt.History, error) {
	if err := ctx.Err(); err != nil {
		return candidateattempt.History{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, regs, err := m.candidateIntentSnapshot()
	if err != nil {
		return candidateattempt.History{}, err
	}
	registered := make([]candidatelineage.Registration, 0, len(regs))
	for _, r := range regs {
		registered = append(registered, r)
	}
	slices.SortFunc(registered, func(a, b candidatelineage.Registration) int { return a.Sequence - b.Sequence })
	return candidateattempt.HistoryFor(m.receiverID, registered, all)
}
func (m *Memory) CandidateWorkerIntents(ctx context.Context) ([]candidateattempt.Intent, error) {
	h, err := m.CandidateWorkerIntentHistory(ctx)
	return h.Intents, err
}

func selectCandidateIntent(all []candidateattempt.Intent, id string) (candidateattempt.Intent, error) {
	if !review.ID.MatchString(id) {
		return candidateattempt.Intent{}, candidateattempt.ErrInvalid
	}
	for _, i := range all {
		if i.Command.RequestID == id {
			return i, nil
		}
	}
	return candidateattempt.Intent{}, candidateattempt.ErrNotFound
}

func (m *Memory) CandidateWorkerIntent(ctx context.Context, id string) (candidateattempt.Intent, error) {
	all, err := m.CandidateWorkerIntents(ctx)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	return selectCandidateIntent(all, id)
}

func loadCandidateIntents(ctx context.Context, tx pgx.Tx, receiverID string, regs map[string]candidatelineage.Registration) ([]candidateattempt.Intent, error) {
	rows, err := tx.Query(ctx, "SELECT id,sequence,registration_id,payload FROM mantik_candidate_worker_intents ORDER BY sequence LIMIT $1", candidateattempt.MaxAttempts+1)
	if err != nil {
		return nil, err
	}
	all := []candidateattempt.Intent{}
	for rows.Next() {
		var id, registrationID string
		var sequence int
		var raw []byte
		var i candidateattempt.Intent
		if err = rows.Scan(&id, &sequence, &registrationID, &raw); err == nil {
			err = workerplan.ReadBoundedShape(raw, &i, candidateattempt.MaxBytes)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		if i.Command.RequestID != id || i.Sequence != sequence || i.Command.RegistrationID != registrationID {
			rows.Close()
			return nil, candidateattempt.ErrInvalid
		}
		all = append(all, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	err = validateCandidateIntents(receiverID, all, regs, func(id string) ([]byte, error) {
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT content FROM mantik_candidate_datasets WHERE id=$1", id).Scan(&raw)
		return raw, err
	})
	return all, err
}

func (p *Postgres) StartCandidateWorker(ctx context.Context, c candidateattempt.Command, now func() time.Time) (candidateattempt.Intent, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return candidateattempt.Intent{}, ErrReceiverMismatch
	}
	if c.Validate() != nil {
		return candidateattempt.Intent{}, candidateattempt.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiverID string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiverID); err != nil {
		return candidateattempt.Intent{}, err
	}
	if receiverID != c.ReceiverID {
		return candidateattempt.Intent{}, ErrReceiverMismatch
	}
	_, registered, raw, err := loadCandidates(ctx, tx, c.RegistrationID)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	regs := candidateRegistrationMap(registered)
	all, err := loadCandidateIntents(ctx, tx, receiverID, regs)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	i, duplicate, err := appendCandidateIntent(all, c, regs, raw, now)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	if !duplicate {
		payload, err := json.Marshal(i)
		if err != nil {
			return candidateattempt.Intent{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_candidate_worker_intents(id,sequence,registration_id,payload) VALUES($1,$2,$3,$4)", c.RequestID, i.Sequence, c.RegistrationID, payload); err != nil {
			return candidateattempt.Intent{}, err
		}
	}
	return i, tx.Commit(ctx)
}

func (p *Postgres) CandidateWorkerIntentHistory(ctx context.Context) (candidateattempt.History, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return candidateattempt.History{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receiverID, registered, _, err := loadCandidates(ctx, tx, "")
	if err != nil {
		return candidateattempt.History{}, err
	}
	all, err := loadCandidateIntents(ctx, tx, receiverID, candidateRegistrationMap(registered))
	if err != nil {
		return candidateattempt.History{}, err
	}
	h, err := candidateattempt.HistoryFor(receiverID, registered, all)
	if err != nil {
		return candidateattempt.History{}, err
	}
	return h, tx.Commit(ctx)
}
func (p *Postgres) CandidateWorkerIntents(ctx context.Context) ([]candidateattempt.Intent, error) {
	h, err := p.CandidateWorkerIntentHistory(ctx)
	return h.Intents, err
}

func (p *Postgres) CandidateWorkerIntent(ctx context.Context, id string) (candidateattempt.Intent, error) {
	all, err := p.CandidateWorkerIntents(ctx)
	if err != nil {
		return candidateattempt.Intent{}, err
	}
	return selectCandidateIntent(all, id)
}

var _ CandidateWorkerIntentStore = (*Memory)(nil)
var _ CandidateWorkerIntentStore = (*Postgres)(nil)
