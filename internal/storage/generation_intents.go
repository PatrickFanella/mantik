package storage

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type CandidateGenerationIntentStore interface {
	StartCandidateGeneration(context.Context, candidategeneration.Command, []byte, func() time.Time) (candidategeneration.Intent, error)
	CandidateGenerationIntentHistory(context.Context) (candidategeneration.IntentHistory, error)
	CandidateGenerationIntent(context.Context, string) (candidategeneration.Intent, error)
	CandidateGenerationIntentEvidence(context.Context, string) (candidategeneration.IntentArtifact, error)
}

func generationIntentRow(i candidategeneration.Intent, p hypothesis.Publication, raw []byte) error {
	if i.VerifyBinding(p) != nil || i.Command.Plan.VerifyDataset(raw) != nil {
		return candidategeneration.ErrInvalid
	}
	return nil
}
func selectGenerationIntent(all []candidategeneration.Intent, id string) (candidategeneration.Intent, error) {
	if !review.ID.MatchString(id) {
		return candidategeneration.Intent{}, candidategeneration.ErrInvalid
	}
	for _, i := range all {
		if i.Command.RequestID == id {
			return i, nil
		}
	}
	return candidategeneration.Intent{}, candidategeneration.ErrNotFound
}

// The caller holds m.mu. Never validate only the selected row or the retry.
func (m *Memory) generationIntentSnapshot(wanted string) ([]candidategeneration.Intent, []byte, error) {
	if len(m.generationIntents) != len(m.generationDatasets) {
		return nil, nil, candidategeneration.ErrInvalid
	}
	all := make([]candidategeneration.Intent, 0, len(m.generationIntents))
	var selected []byte
	for id, i := range m.generationIntents {
		if id != i.Command.RequestID || generationIntentRow(i, m.publications[i.Command.PublicationID], m.generationDatasets[id]) != nil {
			return nil, nil, candidategeneration.ErrInvalid
		}
		all = append(all, i)
		if id == wanted {
			selected = slices.Clone(m.generationDatasets[id])
		}
	}
	slices.SortFunc(all, func(a, b candidategeneration.Intent) int { return a.Sequence - b.Sequence })
	return all, selected, candidategeneration.ValidateIntents(m.receiverID, all)
}
func (m *Memory) StartCandidateGeneration(ctx context.Context, c candidategeneration.Command, raw []byte, now func() time.Time) (candidategeneration.Intent, error) {
	if err := ctx.Err(); err != nil {
		return candidategeneration.Intent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return candidategeneration.Intent{}, err
	}
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return candidategeneration.Intent{}, ErrReceiverMismatch
	}
	all, _, err := m.generationIntentSnapshot("")
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	i, duplicate, err := candidategeneration.AppendIntent(all, c, m.publications[c.PublicationID], raw, now)
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	if err := ctx.Err(); err != nil {
		return candidategeneration.Intent{}, err
	}
	if !duplicate {
		m.generationIntents[c.RequestID] = clone(i)
		m.generationDatasets[c.RequestID] = slices.Clone(raw)
	}
	return clone(i), nil
}
func (m *Memory) CandidateGenerationIntentHistory(ctx context.Context) (candidategeneration.IntentHistory, error) {
	if err := ctx.Err(); err != nil {
		return candidategeneration.IntentHistory{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, _, err := m.generationIntentSnapshot("")
	if err != nil {
		return candidategeneration.IntentHistory{}, err
	}
	return candidategeneration.IntentsFor(m.receiverID, all)
}
func (m *Memory) CandidateGenerationIntent(ctx context.Context, id string) (candidategeneration.Intent, error) {
	h, err := m.CandidateGenerationIntentHistory(ctx)
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	return selectGenerationIntent(h.Intents, id)
}
func (m *Memory) CandidateGenerationIntentEvidence(ctx context.Context, id string) (candidategeneration.IntentArtifact, error) {
	if err := ctx.Err(); err != nil {
		return candidategeneration.IntentArtifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, raw, err := m.generationIntentSnapshot(id)
	if err != nil {
		return candidategeneration.IntentArtifact{}, err
	}
	i, err := selectGenerationIntent(all, id)
	if err != nil {
		return candidategeneration.IntentArtifact{}, err
	}
	return candidategeneration.IntentArtifact{Schema: candidategeneration.IntentArtifactSchema, Intent: clone(i), Dataset: raw}, nil
}

// Streams bounded rows and exact datasets. Only the selected raw artifact is
// retained in memory beyond validation, all in the caller's transaction.
func loadGenerationIntents(ctx context.Context, tx pgx.Tx, wanted string) (string, []candidategeneration.Intent, []byte, error) {
	var receiver string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return "", nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id,i.sequence,i.publication_id,i.payload,p.payload,d.content
 FROM mantik_candidate_generation_intents i
 LEFT JOIN mantik_hypothesis_publications p ON p.id=i.publication_id
 LEFT JOIN mantik_candidate_generation_datasets d ON d.id=i.id
 ORDER BY i.sequence LIMIT $1`, candidategeneration.MaxIntents+1)
	if err != nil {
		return "", nil, nil, err
	}
	defer rows.Close()
	all := []candidategeneration.Intent{}
	var selected []byte
	for rows.Next() {
		var id, pubID string
		var sequence int
		var payload, pubRaw, raw []byte
		if err := rows.Scan(&id, &sequence, &pubID, &payload, &pubRaw, &raw); err != nil {
			return "", nil, nil, err
		}
		var i candidategeneration.Intent
		var pub hypothesis.Publication
		if workerplan.ReadBoundedShape(payload, &i, candidategeneration.MaxIntentBytes) != nil || json.Unmarshal(pubRaw, &pub) != nil || id != i.Command.RequestID || sequence != i.Sequence || pubID != i.Command.PublicationID || generationIntentRow(i, pub, raw) != nil {
			return "", nil, nil, candidategeneration.ErrInvalid
		}
		all = append(all, i)
		if id == wanted {
			selected = slices.Clone(raw)
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, nil, err
	}
	return receiver, all, selected, candidategeneration.ValidateIntents(receiver, all)
}
func (p *Postgres) StartCandidateGeneration(ctx context.Context, c candidategeneration.Command, raw []byte, now func() time.Time) (candidategeneration.Intent, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return candidategeneration.Intent{}, ErrReceiverMismatch
	}
	if c.Validate() != nil || c.Plan.VerifyDataset(raw) != nil {
		return candidategeneration.Intent{}, candidategeneration.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return candidategeneration.Intent{}, err
	}
	if receiver != c.ReceiverID {
		return candidategeneration.Intent{}, ErrReceiverMismatch
	}
	_, all, _, err := loadGenerationIntents(ctx, tx, "")
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	var pubRaw []byte
	var pub hypothesis.Publication
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE id=$1", c.PublicationID).Scan(&pubRaw)
	if err != nil && err != pgx.ErrNoRows {
		return candidategeneration.Intent{}, err
	}
	if err == nil && json.Unmarshal(pubRaw, &pub) != nil {
		return candidategeneration.Intent{}, candidategeneration.ErrInvalid
	}
	i, duplicate, err := candidategeneration.AppendIntent(all, c, pub, raw, now)
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	if !duplicate {
		payload, err := json.Marshal(i)
		if err != nil {
			return candidategeneration.Intent{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_candidate_generation_intents(id,sequence,publication_id,payload) VALUES($1,$2,$3,$4)", c.RequestID, i.Sequence, c.PublicationID, payload); err != nil {
			return candidategeneration.Intent{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_candidate_generation_datasets(id,content) VALUES($1,$2)", c.RequestID, raw); err != nil {
			return candidategeneration.Intent{}, err
		}
	}
	return i, tx.Commit(ctx)
}
func (p *Postgres) generationIntentRead(ctx context.Context, id string) (string, []candidategeneration.Intent, []byte, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receiver, all, raw, err := loadGenerationIntents(ctx, tx, id)
	if err != nil {
		return "", nil, nil, err
	}
	return receiver, all, raw, tx.Commit(ctx)
}
func (p *Postgres) CandidateGenerationIntentHistory(ctx context.Context) (candidategeneration.IntentHistory, error) {
	receiver, all, _, err := p.generationIntentRead(ctx, "")
	if err != nil {
		return candidategeneration.IntentHistory{}, err
	}
	return candidategeneration.IntentsFor(receiver, all)
}
func (p *Postgres) CandidateGenerationIntent(ctx context.Context, id string) (candidategeneration.Intent, error) {
	h, err := p.CandidateGenerationIntentHistory(ctx)
	if err != nil {
		return candidategeneration.Intent{}, err
	}
	return selectGenerationIntent(h.Intents, id)
}
func (p *Postgres) CandidateGenerationIntentEvidence(ctx context.Context, id string) (candidategeneration.IntentArtifact, error) {
	_, all, raw, err := p.generationIntentRead(ctx, id)
	if err != nil {
		return candidategeneration.IntentArtifact{}, err
	}
	i, err := selectGenerationIntent(all, id)
	if err != nil {
		return candidategeneration.IntentArtifact{}, err
	}
	return candidategeneration.IntentArtifact{Schema: candidategeneration.IntentArtifactSchema, Intent: i, Dataset: raw}, nil
}

var _ CandidateGenerationIntentStore = (*Memory)(nil)
var _ CandidateGenerationIntentStore = (*Postgres)(nil)
