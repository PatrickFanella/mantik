package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// CandidateStore retains declarations and their exact datasets without starting
// workers. Read methods validate the entire receiver-owned prefix and artifacts.
type CandidateStore interface {
	CandidateRegistry(context.Context) (candidatelineage.Registry, error)
	CandidateEvidence(context.Context, string) (candidatelineage.Artifact, error)
	RegisterCandidate(context.Context, candidatelineage.Command, []byte, func() time.Time) (candidatelineage.Registration, error)
	CandidateRegistrations(context.Context) ([]candidatelineage.Registration, error)
	CandidateRegistration(context.Context, string) (candidatelineage.Registration, error)
	CandidateDataset(context.Context, string) ([]byte, error)
}

func candidateRow(r candidatelineage.Registration, publication hypothesis.Publication, parents map[string]candidatelineage.Registration, raw []byte) error {
	var parent *candidatelineage.Registration
	if r.ParentID != "" {
		p, ok := parents[r.ParentID]
		if !ok {
			return candidatelineage.ErrInvalid
		}
		parent = &p
	}
	if r.VerifyBinding(publication, parent) != nil || r.Candidate.VerifyDataset(raw) != nil {
		return candidatelineage.ErrInvalid
	}
	return nil
}

func prepareCandidate(all []candidatelineage.Registration, c candidatelineage.Command, publication hypothesis.Publication, now func() time.Time) (candidatelineage.Registration, bool, error) {
	for _, r := range all {
		if r.ID == c.RequestID {
			v, err := candidatelineage.Append(all, c, publication, time.Time{})
			return v, true, err
		}
	}
	if publication.ID == "" {
		return candidatelineage.Registration{}, false, candidatelineage.ErrNotFound
	}
	if len(all) == candidatelineage.MaxRegistrations {
		return candidatelineage.Registration{}, false, candidatelineage.ErrCapacity
	}
	r, err := candidatelineage.Append(all, c, publication, now())
	return r, false, err
}

// The caller holds m.mu. Validation occurs before filtering a selected row, so
// unrelated damaged evidence cannot silently disappear from the registry.
func (m *Memory) candidateRowsSnapshot() ([]candidatelineage.Registration, error) {
	all := make([]candidatelineage.Registration, 0, len(m.candidates))
	if len(m.candidates) != len(m.candidateDatasets) {
		return nil, candidatelineage.ErrInvalid
	}
	for id, r := range m.candidates {
		if id != r.ID {
			return nil, candidatelineage.ErrInvalid
		}
		all = append(all, r)
	}
	slices.SortFunc(all, func(a, b candidatelineage.Registration) int { return a.Sequence - b.Sequence })
	if candidatelineage.ValidateHistory(m.receiverID, all) != nil {
		return nil, candidatelineage.ErrInvalid
	}
	parents := map[string]candidatelineage.Registration{}
	for _, r := range all {
		if candidateRow(r, m.publications[r.PublicationID], parents, m.candidateDatasets[r.ID]) != nil {
			return nil, candidatelineage.ErrInvalid
		}
		parents[r.ID] = r
	}
	return all, nil
}

func (m *Memory) RegisterCandidate(ctx context.Context, c candidatelineage.Command, raw []byte, now func() time.Time) (candidatelineage.Registration, error) {
	if err := ctx.Err(); err != nil {
		return candidatelineage.Registration{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return candidatelineage.Registration{}, ErrReceiverMismatch
	}
	if c.Validate() != nil || c.Candidate.VerifyDataset(raw) != nil {
		return candidatelineage.Registration{}, candidatelineage.ErrInvalid
	}
	all, err := m.candidateSnapshot()
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	r, duplicate, err := prepareCandidate(all, c, m.publications[c.PublicationID], now)
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	if !duplicate {
		m.candidates[r.ID], m.candidateDatasets[r.ID] = clone(r), slices.Clone(raw)
	}
	return clone(r), nil
}

func (m *Memory) CandidateRegistrations(ctx context.Context) ([]candidatelineage.Registration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.candidateSnapshot()
	return clone(all), err
}

func selectCandidate(all []candidatelineage.Registration, id string) (candidatelineage.Registration, error) {
	if !review.ID.MatchString(id) {
		return candidatelineage.Registration{}, candidatelineage.ErrInvalid
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return candidatelineage.Registration{}, candidatelineage.ErrNotFound
}

func (m *Memory) CandidateRegistration(ctx context.Context, id string) (candidatelineage.Registration, error) {
	all, err := m.CandidateRegistrations(ctx)
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	return selectCandidate(all, id)
}

func (m *Memory) CandidateRegistry(ctx context.Context) (candidatelineage.Registry, error) {
	if err := ctx.Err(); err != nil {
		return candidatelineage.Registry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.candidateSnapshot()
	if err != nil {
		return candidatelineage.Registry{}, err
	}
	return candidatelineage.RegistryFor(m.receiverID, all)
}
func (m *Memory) CandidateEvidence(ctx context.Context, id string) (candidatelineage.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return candidatelineage.Artifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.candidateSnapshot()
	if err != nil {
		return candidatelineage.Artifact{}, err
	}
	r, err := selectCandidate(all, id)
	if err != nil {
		return candidatelineage.Artifact{}, err
	}
	return candidatelineage.Artifact{Schema: candidatelineage.ArtifactSchema, Registration: clone(r), Dataset: slices.Clone(m.candidateDatasets[id])}, nil
}
func (m *Memory) CandidateDataset(ctx context.Context, id string) ([]byte, error) {
	a, err := m.CandidateEvidence(ctx, id)
	return a.Dataset, err
}

// loadCandidates streams at most 33 rows and checks each dataset before moving
// on, retaining only metadata and an explicitly selected artifact in memory.
func loadCandidateRows(ctx context.Context, tx pgx.Tx, wanted string) (string, []candidatelineage.Registration, []byte, error) {
	var receiverID string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiverID); err != nil {
		return "", nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.sequence,r.publication_id,r.parent_id,r.payload,p.payload,a.content
 FROM mantik_candidate_registrations r
 LEFT JOIN mantik_hypothesis_publications p ON p.id=r.publication_id
 LEFT JOIN mantik_candidate_datasets a ON a.id=r.id
 ORDER BY r.sequence LIMIT $1`, candidatelineage.MaxRegistrations+1)
	if err != nil {
		return "", nil, nil, err
	}
	defer rows.Close()
	all := []candidatelineage.Registration{}
	parents := map[string]candidatelineage.Registration{}
	var selected []byte
	for rows.Next() {
		var id, publicationID string
		var parentID *string
		var sequence int
		var raw, publicationRaw, data []byte
		if err = rows.Scan(&id, &sequence, &publicationID, &parentID, &raw, &publicationRaw, &data); err != nil {
			return "", nil, nil, err
		}
		var r candidatelineage.Registration
		var publication hypothesis.Publication
		if workerplan.ReadBoundedShape(raw, &r, candidatelineage.MaxBytes) != nil || json.Unmarshal(publicationRaw, &publication) != nil {
			return "", nil, nil, candidatelineage.ErrInvalid
		}
		parent := ""
		if parentID != nil {
			parent = *parentID
		}
		if r.ID != id || r.Sequence != sequence || r.PublicationID != publicationID || r.ParentID != parent || candidateRow(r, publication, parents, data) != nil {
			return "", nil, nil, candidatelineage.ErrInvalid
		}
		all = append(all, r)
		parents[r.ID] = r
		if id == wanted {
			selected = slices.Clone(data)
		}
	}
	if err = rows.Err(); err != nil {
		return "", nil, nil, err
	}
	if candidatelineage.ValidateHistory(receiverID, all) != nil {
		return "", nil, nil, candidatelineage.ErrInvalid
	}
	return receiverID, all, selected, nil
}

func (p *Postgres) RegisterCandidate(ctx context.Context, c candidatelineage.Command, raw []byte, now func() time.Time) (candidatelineage.Registration, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return candidatelineage.Registration{}, ErrReceiverMismatch
	}
	if c.Validate() != nil || c.Candidate.VerifyDataset(raw) != nil {
		return candidatelineage.Registration{}, candidatelineage.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiverID string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiverID); err != nil {
		return candidatelineage.Registration{}, err
	}
	if receiverID != c.ReceiverID {
		return candidatelineage.Registration{}, ErrReceiverMismatch
	}
	_, all, _, err := loadCandidates(ctx, tx, "")
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	var publicationRaw []byte
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE id=$1", c.PublicationID).Scan(&publicationRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return candidatelineage.Registration{}, err
	}
	var publication hypothesis.Publication
	if err == nil {
		err = json.Unmarshal(publicationRaw, &publication)
	} else {
		err = nil
	}
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	r, duplicate, err := prepareCandidate(all, c, publication, now)
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	if !duplicate {
		metadata, err := json.Marshal(r)
		if err != nil {
			return candidatelineage.Registration{}, err
		}
		var parent any
		if r.ParentID != "" {
			parent = r.ParentID
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_candidate_registrations(id,sequence,publication_id,parent_id,payload) VALUES($1,$2,$3,$4,$5)", r.ID, r.Sequence, r.PublicationID, parent, metadata); err != nil {
			return candidatelineage.Registration{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_candidate_datasets(id,content) VALUES($1,$2)", r.ID, raw); err != nil {
			return candidatelineage.Registration{}, err
		}
	}
	return r, tx.Commit(ctx)
}

func (p *Postgres) candidateSnapshot(ctx context.Context, wanted string) (string, []candidatelineage.Registration, []byte, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receiverID, all, raw, err := loadCandidates(ctx, tx, wanted)
	if err != nil {
		return "", nil, nil, err
	}
	return receiverID, all, raw, tx.Commit(ctx)
}
func (p *Postgres) CandidateRegistry(ctx context.Context) (candidatelineage.Registry, error) {
	receiverID, all, _, err := p.candidateSnapshot(ctx, "")
	if err != nil {
		return candidatelineage.Registry{}, err
	}
	return candidatelineage.RegistryFor(receiverID, all)
}
func (p *Postgres) CandidateRegistrations(ctx context.Context) ([]candidatelineage.Registration, error) {
	_, all, _, err := p.candidateSnapshot(ctx, "")
	return all, err
}
func (p *Postgres) CandidateRegistration(ctx context.Context, id string) (candidatelineage.Registration, error) {
	_, all, _, err := p.candidateSnapshot(ctx, "")
	if err != nil {
		return candidatelineage.Registration{}, err
	}
	return selectCandidate(all, id)
}
func (p *Postgres) CandidateEvidence(ctx context.Context, id string) (candidatelineage.Artifact, error) {
	_, all, raw, err := p.candidateSnapshot(ctx, id)
	if err != nil {
		return candidatelineage.Artifact{}, err
	}
	r, err := selectCandidate(all, id)
	if err != nil {
		return candidatelineage.Artifact{}, err
	}
	return candidatelineage.Artifact{Schema: candidatelineage.ArtifactSchema, Registration: r, Dataset: raw}, nil
}
func (p *Postgres) CandidateDataset(ctx context.Context, id string) ([]byte, error) {
	a, err := p.CandidateEvidence(ctx, id)
	return a.Dataset, err
}

var _ CandidateStore = (*Memory)(nil)
var _ CandidateStore = (*Postgres)(nil)
