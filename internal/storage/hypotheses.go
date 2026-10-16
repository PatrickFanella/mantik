package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"github.com/jackc/pgx/v5"
)

type HypothesisStore interface {
	PublishHypothesis(context.Context, string, string, string, hypothesis.Declaration, func() time.Time) (hypothesis.Publication, error)
	Hypothesis(context.Context, string) (hypothesis.Question, error)
}

func question(id string, publications []hypothesis.Publication) (hypothesis.Question, error) {
	if !hypothesis.QuestionID.MatchString(id) {
		return hypothesis.Question{}, hypothesis.ErrInvalid
	}
	q := hypothesis.Question{Schema: hypothesis.QuestionSchema, ID: id, Authority: "research_declaration_only", Publications: []hypothesis.Publication{}}
	for _, p := range publications {
		if p.Sealed.ID == id {
			q.Publications = append(q.Publications, p)
		}
	}
	if len(q.Publications) == 0 {
		return q, hypothesis.ErrNotFound
	}
	slices.SortFunc(q.Publications, func(a, b hypothesis.Publication) int {
		if c := a.PublishedAt.Compare(b.PublishedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return q, nil
}

// preparePublication handles exact retries before checking present-day evidence.
// A retry recovers the immutable receipt; it does not reapply an old review.
func preparePublication(publications []hypothesis.Publication, receiver, id, reviewID string, d hypothesis.Declaration, s newscase.State, a review.Attempt, now func() time.Time) (hypothesis.Publication, bool, error) {
	sealed, err := hypothesis.Seal(d)
	if err != nil || !review.ID.MatchString(id) || !review.ID.MatchString(reviewID) {
		return hypothesis.Publication{}, false, hypothesis.ErrInvalid
	}
	for _, p := range publications {
		if p.ID == id {
			if !hypothesis.SamePublication(p, receiver, reviewID, sealed) {
				return p, false, hypothesis.ErrConflict
			}
			return p, true, nil
		}
	}
	if len(publications) >= hypothesis.MaxPublications {
		return hypothesis.Publication{}, false, hypothesis.ErrCapacity
	}
	if a.ID == "" {
		return hypothesis.Publication{}, false, review.ErrNotFound
	}
	if !hypothesis.ReuseSourceExists(a, publications) {
		return hypothesis.Publication{}, false, hypothesis.ErrConflict
	}
	at := now()
	reused := false
	for _, p := range publications {
		if at.Before(p.PublishedAt) {
			return hypothesis.Publication{}, false, hypothesis.ErrConflict
		}
		if p.Sealed.ID == sealed.ID {
			reused = true
		}
	}
	p, err := hypothesis.Publish(s, a, receiver, id, d, at)
	p.QuestionReused = reused
	return p, false, err
}
func (m *Memory) PublishHypothesis(ctx context.Context, receiver, id, reviewID string, d hypothesis.Declaration, now func() time.Time) (hypothesis.Publication, error) {
	if err := ctx.Err(); err != nil {
		return hypothesis.Publication{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return hypothesis.Publication{}, ErrReceiverMismatch
	}
	all := make([]hypothesis.Publication, 0, len(m.publications))
	for _, p := range m.publications {
		all = append(all, p)
	}
	p, duplicate, err := preparePublication(all, receiver, id, reviewID, d, m.news, m.reviews[reviewID], now)
	if err != nil {
		return hypothesis.Publication{}, err
	}
	if !duplicate {
		m.publications[id] = clone(p)
	}
	return clone(p), nil
}
func (m *Memory) Hypothesis(ctx context.Context, id string) (hypothesis.Question, error) {
	if err := ctx.Err(); err != nil {
		return hypothesis.Question{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]hypothesis.Publication, 0, len(m.publications))
	for _, p := range m.publications {
		if p.Sealed.ID == id {
			all = append(all, p)
		}
	}
	q, err := question(id, all)
	return clone(q), err
}
func (p *Postgres) PublishHypothesis(ctx context.Context, receiver, id, reviewID string, d hypothesis.Declaration, now func() time.Time) (hypothesis.Publication, error) {
	if !ValidReceiverID(receiver) {
		return hypothesis.Publication{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return hypothesis.Publication{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&expected); err != nil {
		return hypothesis.Publication{}, err
	}
	if receiver != expected {
		return hypothesis.Publication{}, ErrReceiverMismatch
	}
	// Same first lock as news/review writes, including capacity and reuse decisions.
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return hypothesis.Publication{}, err
	}
	var state newscase.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return hypothesis.Publication{}, err
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_hypothesis_publications ORDER BY id LIMIT $1", hypothesis.MaxPublications+1)
	if err != nil {
		return hypothesis.Publication{}, err
	}
	all := []hypothesis.Publication{}
	for rows.Next() {
		var stored hypothesis.Publication
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &stored)
		}
		if err != nil {
			rows.Close()
			return hypothesis.Publication{}, err
		}
		all = append(all, stored)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return hypothesis.Publication{}, err
	}
	a, err := loadReview(ctx, tx, reviewID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return hypothesis.Publication{}, err
	}
	published, duplicate, err := preparePublication(all, receiver, id, reviewID, d, state, a, now)
	if err != nil {
		return hypothesis.Publication{}, err
	}
	if !duplicate {
		raw, err = json.Marshal(published)
		if err != nil {
			return hypothesis.Publication{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_hypothesis_publications(id,question_id,payload) VALUES($1,$2,$3)", id, published.Sealed.ID, raw); err != nil {
			return hypothesis.Publication{}, err
		}
	}
	return published, tx.Commit(ctx)
}
func (p *Postgres) Hypothesis(ctx context.Context, id string) (hypothesis.Question, error) {
	if !hypothesis.QuestionID.MatchString(id) {
		return hypothesis.Question{}, hypothesis.ErrInvalid
	}
	rows, err := p.pool.Query(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE question_id=$1 ORDER BY id LIMIT $2", id, hypothesis.MaxPublications+1)
	if err != nil {
		return hypothesis.Question{}, err
	}
	defer rows.Close()
	all := []hypothesis.Publication{}
	for rows.Next() {
		var raw []byte
		var stored hypothesis.Publication
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &stored)
		}
		if err != nil {
			return hypothesis.Question{}, err
		}
		all = append(all, stored)
	}
	if err = rows.Err(); err != nil {
		return hypothesis.Question{}, err
	}
	if len(all) > hypothesis.MaxPublications {
		return hypothesis.Question{}, hypothesis.ErrCapacity
	}
	return question(id, all)
}

var _ HypothesisStore = (*Memory)(nil)
var _ HypothesisStore = (*Postgres)(nil)
