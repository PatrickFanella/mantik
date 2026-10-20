package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// GeneratedCandidateStore retains explicit generation links without invoking a
// model, compiling source, starting workers or changing execution authority.
type GeneratedCandidateStore interface {
	RegisterGeneratedCandidate(context.Context, generatedcandidate.Command, func() time.Time) (generatedcandidate.Artifact, error)
	GeneratedCandidateRegistry(context.Context) (generatedcandidate.Registry, error)
	GeneratedCandidateEvidence(context.Context, string) (generatedcandidate.Artifact, error)
}

// Validate the entire marker/link complement and every complete source artifact
// before returning any selected value. A portable compact hash is insufficient.
func generatedSnapshot(receiver string, regs []candidatelineage.Registration, markers map[string]string, links map[string]generatedcandidate.Link, wanted string, read func(string) (candidategeneration.OutcomeArtifact, error)) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
	fail := func(err error) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	if wanted != "" && !review.ID.MatchString(wanted) {
		return fail(generatedcandidate.ErrInvalid)
	}
	if len(markers) != len(links) {
		return fail(generatedcandidate.ErrInvalid)
	}
	ordered := []generatedcandidate.Link{}
	for id, l := range links {
		if id != l.Command.RequestID || markers[id] != l.SHA256 {
			return fail(generatedcandidate.ErrInvalid)
		}
		ordered = append(ordered, l)
	}
	slices.SortFunc(ordered, func(a, b generatedcandidate.Link) int { return a.RegistrationSequence - b.RegistrationSequence })
	legacy, err := candidatelineage.RegistryFor(receiver, regs)
	if err != nil {
		return fail(err)
	}
	registry, err := generatedcandidate.RegistryFor(legacy, ordered)
	if err != nil {
		return fail(err)
	}
	var selected generatedcandidate.Artifact
	for _, l := range ordered {
		a, err := read(l.Command.GenerationRequestID)
		if err != nil {
			return fail(err)
		}
		full, err := generatedcandidate.NewArtifact(l, regs[l.RegistrationSequence-1], a)
		if err != nil {
			return fail(err)
		}
		if l.Command.RequestID == wanted {
			selected = full
		}
	}
	if wanted != "" && selected.Schema == "" {
		return fail(candidatelineage.ErrNotFound)
	}
	return registry, selected, nil
}
func outcomeFromRows(intents []candidategeneration.Intent, outcomes []candidategeneration.Outcome, id string, encoded []byte) (candidategeneration.OutcomeArtifact, error) {
	o, err := selectGenerationOutcome(outcomes, id)
	if err != nil {
		return candidategeneration.OutcomeArtifact{}, err
	}
	r, err := candidategeneration.ReadRecording(bytes.NewReader(encoded))
	if err != nil {
		return candidategeneration.OutcomeArtifact{}, err
	}
	return candidategeneration.NewOutcomeArtifact(intents[o.IntentSequence-1], o, r)
}

// Caller holds m.mu. The lower-level helpers avoid recursion into candidateSnapshot.
func (m *Memory) generatedCandidateSnapshot(wanted string) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
	regs, err := m.candidateRowsSnapshot()
	if err != nil {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	intents, outcomes, _, err := m.generationOutcomeSnapshot("")
	if err != nil {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	return generatedSnapshot(m.receiverID, regs, m.generatedMarkers, m.generatedLinks, wanted, func(id string) (candidategeneration.OutcomeArtifact, error) {
		return outcomeFromRows(intents, outcomes, id, m.generationRecordings[id])
	})
}
func (m *Memory) candidateSnapshot() ([]candidatelineage.Registration, error) {
	r, _, err := m.generatedCandidateSnapshot("")
	if err != nil {
		return nil, err
	}
	return r.Candidates.Registrations, nil
}
func (m *Memory) RegisterGeneratedCandidate(ctx context.Context, c generatedcandidate.Command, now func() time.Time) (generatedcandidate.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return generatedcandidate.Artifact{}, ErrReceiverMismatch
	}
	if c.Validate() != nil {
		return generatedcandidate.Artifact{}, generatedcandidate.ErrInvalid
	}
	registry, _, err := m.generatedCandidateSnapshot("")
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	intents, outcomes, _, err := m.generationOutcomeSnapshot("")
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	a, err := outcomeFromRows(intents, outcomes, c.GenerationRequestID, m.generationRecordings[c.GenerationRequestID])
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	raw := m.generationDatasets[c.GenerationRequestID]
	l, r, duplicate, err := generatedcandidate.Append(registry.Candidates.Registrations, registry.Links, c, m.publications[a.Intent.Command.PublicationID], a, raw, now)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	result, err := generatedcandidate.NewArtifact(l, r, a)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if !duplicate {
		m.candidates[r.ID] = clone(r)
		m.candidateDatasets[r.ID] = slices.Clone(raw)
		m.generatedLinks[r.ID] = l
		m.generatedMarkers[r.ID] = l.SHA256
	}
	return result, nil
}
func (m *Memory) GeneratedCandidateRegistry(ctx context.Context) (generatedcandidate.Registry, error) {
	if err := ctx.Err(); err != nil {
		return generatedcandidate.Registry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, _, err := m.generatedCandidateSnapshot("")
	return r, err
}
func (m *Memory) GeneratedCandidateEvidence(ctx context.Context, id string) (generatedcandidate.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if !review.ID.MatchString(id) {
		return generatedcandidate.Artifact{}, generatedcandidate.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, a, err := m.generatedCandidateSnapshot(id)
	return a, err
}

// All reads share the caller's repeatable-read transaction; writes hold the
// existing receiver lock. Full generation recordings are validated one at a time.
func loadGeneratedCandidates(ctx context.Context, tx pgx.Tx, wanted string) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
	fail := func(err error) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	receiver, regs, _, err := loadCandidateRows(ctx, tx, "")
	if err != nil {
		return fail(err)
	}
	_, intents, _, err := loadGenerationIntents(ctx, tx, "")
	if err != nil {
		return fail(err)
	}
	outcomes, _, err := loadGenerationOutcomes(ctx, tx, receiver, intents, "")
	if err != nil {
		return fail(err)
	}
	markers := map[string]string{}
	links := map[string]generatedcandidate.Link{}
	rows, err := tx.Query(ctx, "SELECT id,generation_link_sha256 FROM mantik_candidate_registrations WHERE generation_link_sha256 IS NOT NULL LIMIT $1", candidatelineage.MaxRegistrations+1)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var id, sha string
		if err := rows.Scan(&id, &sha); err != nil {
			rows.Close()
			return fail(err)
		}
		markers[id] = sha
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fail(err)
	}
	rows, err = tx.Query(ctx, "SELECT id,sha256,payload FROM mantik_generated_candidate_links LIMIT $1", candidatelineage.MaxRegistrations+1)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var id, sha string
		var raw []byte
		var l generatedcandidate.Link
		if err := rows.Scan(&id, &sha, &raw); err != nil {
			rows.Close()
			return fail(err)
		}
		if workerplan.ReadBoundedShape(raw, &l, generatedcandidate.MaxLinkBytes) != nil || l.Validate() != nil || id != l.Command.RequestID || sha != l.SHA256 {
			rows.Close()
			return fail(generatedcandidate.ErrInvalid)
		}
		links[id] = l
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fail(err)
	}
	return generatedSnapshot(receiver, regs, markers, links, wanted, func(id string) (candidategeneration.OutcomeArtifact, error) {
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT recording FROM mantik_candidate_generation_outcomes WHERE id=$1", id).Scan(&raw); err != nil {
			return candidategeneration.OutcomeArtifact{}, err
		}
		return outcomeFromRows(intents, outcomes, id, raw)
	})
}

// Preserve the legacy read signature while enforcing generated-link retention on
// ordinary registration reads, retries, dataset exports and worker declarations.
func loadCandidates(ctx context.Context, tx pgx.Tx, wanted string) (string, []candidatelineage.Registration, []byte, error) {
	r, _, err := loadGeneratedCandidates(ctx, tx, "")
	if err != nil {
		return "", nil, nil, err
	}
	var raw []byte
	// Preserve the legacy loader's absent-selection behavior: consumers choose
	// their own typed not-found error after validating the complete prefix.
	for _, registration := range r.Candidates.Registrations {
		if registration.ID == wanted {
			if err := tx.QueryRow(ctx, "SELECT content FROM mantik_candidate_datasets WHERE id=$1", wanted).Scan(&raw); err != nil {
				return "", nil, nil, err
			}
			break
		}
	}
	return r.ReceiverID, r.Candidates.Registrations, raw, nil
}
func (p *Postgres) RegisterGeneratedCandidate(ctx context.Context, c generatedcandidate.Command, now func() time.Time) (generatedcandidate.Artifact, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return generatedcandidate.Artifact{}, ErrReceiverMismatch
	}
	if c.Validate() != nil {
		return generatedcandidate.Artifact{}, generatedcandidate.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if receiver != c.ReceiverID {
		return generatedcandidate.Artifact{}, ErrReceiverMismatch
	}
	registry, _, err := loadGeneratedCandidates(ctx, tx, "")
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	_, intents, raw, err := loadGenerationIntents(ctx, tx, c.GenerationRequestID)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	outcomes, record, err := loadGenerationOutcomes(ctx, tx, receiver, intents, c.GenerationRequestID)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	ih, err := candidategeneration.IntentsFor(receiver, intents)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	oh, err := candidategeneration.OutcomesFor(ih, outcomes)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	a, err := generationOutcomeArtifact(oh, record, c.GenerationRequestID)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	var pub hypothesis.Publication
	var payload []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE id=$1", a.Intent.Command.PublicationID).Scan(&payload); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if json.Unmarshal(payload, &pub) != nil {
		return generatedcandidate.Artifact{}, generatedcandidate.ErrInvalid
	}
	l, r, duplicate, err := generatedcandidate.Append(registry.Candidates.Registrations, registry.Links, c, pub, a, raw, now)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	result, err := generatedcandidate.NewArtifact(l, r, a)
	if err != nil {
		return generatedcandidate.Artifact{}, err
	}
	if !duplicate {
		registration, err := json.Marshal(r)
		if err != nil {
			return generatedcandidate.Artifact{}, err
		}
		link, err := json.Marshal(l)
		if err != nil {
			return generatedcandidate.Artifact{}, err
		}
		var parent any
		if r.ParentID != "" {
			parent = r.ParentID
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_candidate_registrations(id,sequence,publication_id,parent_id,payload,generation_link_sha256) VALUES($1,$2,$3,$4,$5,$6)", r.ID, r.Sequence, r.PublicationID, parent, registration, l.SHA256); err != nil {
			return generatedcandidate.Artifact{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_candidate_datasets(id,content) VALUES($1,$2)", r.ID, raw); err != nil {
			return generatedcandidate.Artifact{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_generated_candidate_links(id,sha256,payload) VALUES($1,$2,$3)", r.ID, l.SHA256, link); err != nil {
			return generatedcandidate.Artifact{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return generatedcandidate.Artifact{}, err
	}
	return result, nil
}
func (p *Postgres) generatedCandidateRead(ctx context.Context, id string) (generatedcandidate.Registry, generatedcandidate.Artifact, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, a, err := loadGeneratedCandidates(ctx, tx, id)
	if err != nil {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return generatedcandidate.Registry{}, generatedcandidate.Artifact{}, err
	}
	return r, a, nil
}
func (p *Postgres) GeneratedCandidateRegistry(ctx context.Context) (generatedcandidate.Registry, error) {
	r, _, err := p.generatedCandidateRead(ctx, "")
	return r, err
}
func (p *Postgres) GeneratedCandidateEvidence(ctx context.Context, id string) (generatedcandidate.Artifact, error) {
	if !review.ID.MatchString(id) {
		return generatedcandidate.Artifact{}, generatedcandidate.ErrInvalid
	}
	_, a, err := p.generatedCandidateRead(ctx, id)
	return a, err
}
