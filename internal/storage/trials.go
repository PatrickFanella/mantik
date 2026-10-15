package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"github.com/jackc/pgx/v5"
)

type TrialStore interface {
	RegisterTrial(context.Context, trial.Command, func() time.Time) (trial.Registration, error)
	Trial(context.Context, string) (trial.Registration, error)
	HypothesisTrials(context.Context, string) (trial.History, error)
}

func prepareTrial(all []trial.Registration, c trial.Command, p hypothesis.Publication, now func() time.Time) (trial.Registration, bool, error) {
	if err := c.Validate(); err != nil {
		return trial.Registration{}, false, err
	}
	for _, r := range all {
		if r.ID == c.RequestID {
			if !trial.SameRequest(r, c) {
				return r, false, trial.ErrConflict
			}
			return r, true, nil
		}
	}
	if len(all) >= trial.MaxRegistrations {
		return trial.Registration{}, false, trial.ErrCapacity
	}
	if p.ID == "" {
		return trial.Registration{}, false, trial.ErrNotFound
	}
	at := now()
	var parent *trial.Registration
	for i := range all {
		if at.Before(all[i].RegisteredAt) {
			return trial.Registration{}, false, trial.ErrConflict
		}
		if all[i].ID == c.ParentID {
			parent = &all[i]
		}
	}
	if c.ParentID != "" && parent == nil {
		return trial.Registration{}, false, trial.ErrNotFound
	}
	r, err := trial.Register(c, p, parent, at)
	return r, false, err
}
func trialHistory(id string, all []trial.Summary) trial.History {
	h := trial.History{Schema: trial.HistorySchema, HypothesisID: id, Authority: "research_trial_declaration_only", Registrations: []trial.Summary{}}
	h.Registrations = append(h.Registrations, all...)
	slices.SortFunc(h.Registrations, func(a, b trial.Summary) int {
		if c := a.RegisteredAt.Compare(b.RegisteredAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return h
}
func (m *Memory) RegisterTrial(ctx context.Context, c trial.Command, now func() time.Time) (trial.Registration, error) {
	if err := ctx.Err(); err != nil {
		return trial.Registration{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(c.ReceiverID) || c.ReceiverID != m.receiverID {
		return trial.Registration{}, ErrReceiverMismatch
	}
	all := make([]trial.Registration, 0, len(m.trials))
	for _, r := range m.trials {
		all = append(all, r)
	}
	r, duplicate, err := prepareTrial(all, c, m.publications[c.PublicationID], now)
	if err != nil {
		return trial.Registration{}, err
	}
	if !duplicate {
		m.trials[r.ID] = clone(r)
	}
	return clone(r), nil
}
func (m *Memory) Trial(ctx context.Context, id string) (trial.Registration, error) {
	if err := ctx.Err(); err != nil {
		return trial.Registration{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.trials[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	return clone(r), nil
}
func (m *Memory) HypothesisTrials(ctx context.Context, id string) (trial.History, error) {
	if err := ctx.Err(); err != nil {
		return trial.History{}, err
	}
	if !hypothesis.QuestionID.MatchString(id) {
		return trial.History{}, trial.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exists := false
	for _, p := range m.publications {
		if p.Sealed.ID == id {
			exists = true
		}
	}
	if !exists {
		return trial.History{}, trial.ErrNotFound
	}
	all := make([]trial.Summary, 0, len(m.trials))
	for _, r := range m.trials {
		if r.HypothesisID == id {
			all = append(all, trial.Summarize(r))
		}
	}
	return clone(trialHistory(id, all)), nil
}
func (p *Postgres) RegisterTrial(ctx context.Context, c trial.Command, now func() time.Time) (trial.Registration, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return trial.Registration{}, ErrReceiverMismatch
	}
	if err := c.Validate(); err != nil {
		return trial.Registration{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return trial.Registration{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Registrations and publications are immutable. This receiver-row lock only
	// serializes trial capacity, parent ordering and exact retry decisions.
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return trial.Registration{}, err
	}
	if receiver != c.ReceiverID {
		return trial.Registration{}, ErrReceiverMismatch
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_hypothesis_trials ORDER BY id LIMIT $1", trial.MaxRegistrations+1)
	if err != nil {
		return trial.Registration{}, err
	}
	all, err := readTrials(rows)
	if err != nil {
		return trial.Registration{}, err
	}
	var raw []byte
	var publication hypothesis.Publication
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE id=$1", c.PublicationID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return trial.Registration{}, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &publication); err != nil {
			return trial.Registration{}, err
		}
	}
	r, duplicate, err := prepareTrial(all, c, publication, now)
	if err != nil {
		return trial.Registration{}, err
	}
	if !duplicate {
		raw, err = json.Marshal(r)
		if err != nil {
			return trial.Registration{}, err
		}
		var parent any
		if r.ParentID != "" {
			parent = r.ParentID
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_hypothesis_trials(id,hypothesis_id,publication_id,parent_id,payload) VALUES($1,$2,$3,$4,$5)", r.ID, r.HypothesisID, r.PublicationID, parent, raw); err != nil {
			return trial.Registration{}, err
		}
	}
	return r, tx.Commit(ctx)
}
func readTrials(rows pgx.Rows) ([]trial.Registration, error) {
	defer rows.Close()
	all := []trial.Registration{}
	for rows.Next() {
		var raw []byte
		var r trial.Registration
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, rows.Err()
}
func (p *Postgres) Trial(ctx context.Context, id string) (trial.Registration, error) {
	var raw []byte
	var r trial.Registration
	err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_trials WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, trial.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (p *Postgres) HypothesisTrials(ctx context.Context, id string) (trial.History, error) {
	if !hypothesis.QuestionID.MatchString(id) {
		return trial.History{}, trial.ErrInvalid
	}
	// One statement snapshot distinguishes an existing question with no trials
	// from an unknown question, without loading any publication source material.
	rows, err := p.pool.Query(ctx, "SELECT (t.payload - 'recipe') || jsonb_build_object('recipe_sha256',t.payload->'recipe'->>'sha256') FROM (SELECT 1 FROM mantik_hypothesis_publications WHERE question_id=$1 LIMIT 1) q LEFT JOIN mantik_hypothesis_trials t ON t.hypothesis_id=$1 ORDER BY t.id LIMIT $2", id, trial.MaxRegistrations+1)
	if err != nil {
		return trial.History{}, err
	}
	defer rows.Close()
	all := []trial.Summary{}
	exists := false
	for rows.Next() {
		exists = true
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return trial.History{}, err
		}
		if raw != nil {
			var r trial.Summary
			if err = json.Unmarshal(raw, &r); err != nil {
				return trial.History{}, err
			}
			all = append(all, r)
		}
	}
	if err = rows.Err(); err != nil {
		return trial.History{}, err
	}
	if !exists {
		return trial.History{}, trial.ErrNotFound
	}
	if len(all) > trial.MaxRegistrations {
		return trial.History{}, trial.ErrCapacity
	}
	return trialHistory(id, all), nil
}

var _ TrialStore = (*Memory)(nil)
var _ TrialStore = (*Postgres)(nil)
