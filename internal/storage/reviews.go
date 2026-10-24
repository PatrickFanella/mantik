package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	researchquestion "git.subcult.tv/subculture-collective/mantik/internal/question"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"github.com/jackc/pgx/v5"
)

type ReviewStore interface {
	StartReview(context.Context, string, string, string, int, func() time.Time) (review.View, error)
	CompleteReview(context.Context, string, string, []json.RawMessage, func() time.Time) (review.View, error)
	Review(context.Context, string) (review.View, error)
}

type ReviewProfileStore interface {
	StartReviewProfile(context.Context, string, string, string, int, string, func() time.Time) (review.View, error)
}

func (m *Memory) Review(ctx context.Context, id string) (review.View, error) {
	if err := ctx.Err(); err != nil {
		return review.View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.reviews[id]
	if !ok {
		return review.View{}, review.ErrNotFound
	}
	if _, err := review.Definition(a); err != nil {
		return review.View{}, err
	}
	return clone(review.Inspect(a, m.news)), nil
}
func (m *Memory) StartReview(ctx context.Context, receiver, id, caseID string, version int, now func() time.Time) (review.View, error) {
	return m.StartReviewProfile(ctx, receiver, id, caseID, version, investigation.TypeID, now)
}
func (m *Memory) StartReviewProfile(ctx context.Context, receiver, id, caseID string, version int, typeID string, now func() time.Time) (review.View, error) {
	if _, err := investigation.ProfileFor(typeID); err != nil {
		return review.View{}, review.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return review.View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID || !ValidReceiverID(receiver) {
		return review.View{}, ErrReceiverMismatch
	}
	if a, ok := m.reviews[id]; ok {
		if !review.SameStartProfile(a, receiver, caseID, version, typeID) {
			return review.View{}, review.ErrConflict
		}
		return clone(review.Inspect(a, m.news)), nil
	}
	if len(m.reviews) >= review.MaxAttempts {
		return review.View{}, review.ErrCapacity
	}
	at := now()
	var inventory *researchquestion.Inventory
	if typeID == investigation.ReuseTypeID {
		all := make([]hypothesis.Publication, 0, len(m.publications))
		for _, p := range m.publications {
			all = append(all, p)
		}
		pin, err := reviewInventory(receiver, all, at)
		if err != nil {
			return review.View{}, err
		}
		inventory = &pin
	}
	a, err := review.StartProfileInventory(m.news, receiver, id, caseID, version, typeID, inventory, at)
	if err != nil {
		return review.View{}, err
	}
	m.reviews[id] = clone(a)
	return clone(review.Inspect(a, m.news)), nil
}
func (m *Memory) CompleteReview(ctx context.Context, receiver, id string, steps []json.RawMessage, now func() time.Time) (review.View, error) {
	steps, digest, err := review.CanonicalSteps(steps)
	if err != nil {
		return review.View{}, err
	}
	if err = ctx.Err(); err != nil {
		return review.View{}, err
	}
	m.mu.Lock()
	if receiver != m.receiverID || !ValidReceiverID(receiver) {
		m.mu.Unlock()
		return review.View{}, ErrReceiverMismatch
	}
	a, ok := m.reviews[id]
	a = clone(a)
	m.mu.Unlock()
	if !ok {
		return review.View{}, review.ErrNotFound
	}
	if _, err := review.Definition(a); err != nil {
		return review.View{}, err
	}
	var result investigation.Result
	if a.Status == "pending" {
		result, err = review.Evaluate(ctx, a, steps)
		if err != nil {
			return review.View{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID {
		return review.View{}, ErrReceiverMismatch
	}
	current := m.reviews[id]
	if _, err := review.Definition(current); err != nil {
		return review.View{}, err
	}
	if current.Status != "pending" {
		if current.StepsSHA256 != digest {
			return review.View{}, review.ErrConflict
		}
		return clone(review.Inspect(current, m.news)), nil
	}
	a, err = review.Commit(current, m.news, steps, digest, result, now())
	if err != nil {
		return review.View{}, err
	}
	m.reviews[id] = clone(a)
	return clone(review.Inspect(a, m.news)), nil
}
func (p *Postgres) Review(ctx context.Context, id string) (review.View, error) {
	var attemptRaw, casesRaw []byte
	err := p.pool.QueryRow(ctx, "SELECT a.payload,n.payload->'cases' FROM mantik_news_reviews a CROSS JOIN mantik_news_cases n WHERE a.id=$1 AND n.singleton", id).Scan(&attemptRaw, &casesRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return review.View{}, review.ErrNotFound
	}
	if err != nil {
		return review.View{}, err
	}
	var a review.Attempt
	var cases map[string]newscase.Case
	if err = json.Unmarshal(attemptRaw, &a); err != nil {
		return review.View{}, err
	}
	if err = json.Unmarshal(casesRaw, &cases); err != nil {
		return review.View{}, err
	}
	if _, err := review.Definition(a); err != nil {
		return review.View{}, err
	}
	return review.Inspect(a, newscase.State{Cases: cases}), nil
}
func (p *Postgres) reviewMutation(ctx context.Context, receiver string, apply func(pgx.Tx, newscase.State) (review.Attempt, error)) (review.View, error) {
	if !ValidReceiverID(receiver) {
		return review.View{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return review.View{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&expected); err != nil {
		return review.View{}, err
	}
	if expected != receiver {
		return review.View{}, ErrReceiverMismatch
	}
	// Every news/review mutation takes this lock first. Models run before this
	// transaction; only trusted snapshot capture or completion checks run here.
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return review.View{}, err
	}
	var state newscase.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return review.View{}, err
	}
	a, err := apply(tx, state)
	if err != nil {
		return review.View{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return review.View{}, err
	}
	return review.Inspect(a, state), nil
}
func loadReview(ctx context.Context, tx pgx.Tx, id string) (review.Attempt, error) {
	var raw []byte
	var a review.Attempt
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_news_reviews WHERE id=$1 FOR UPDATE", id).Scan(&raw)
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(raw, &a)
	return a, err
}
func (p *Postgres) StartReview(ctx context.Context, receiver, id, caseID string, version int, now func() time.Time) (review.View, error) {
	return p.StartReviewProfile(ctx, receiver, id, caseID, version, investigation.TypeID, now)
}
func (p *Postgres) StartReviewProfile(ctx context.Context, receiver, id, caseID string, version int, typeID string, now func() time.Time) (review.View, error) {
	if _, err := investigation.ProfileFor(typeID); err != nil {
		return review.View{}, review.ErrInvalid
	}
	if !review.ID.MatchString(id) {
		return review.View{}, review.ErrInvalid
	}
	return p.reviewMutation(ctx, receiver, func(tx pgx.Tx, s newscase.State) (review.Attempt, error) {
		a, err := loadReview(ctx, tx, id)
		if err == nil {
			if !review.SameStartProfile(a, receiver, caseID, version, typeID) {
				return a, review.ErrConflict
			}
			return a, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return a, err
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_news_reviews").Scan(&count); err != nil {
			return a, err
		}
		if count >= review.MaxAttempts {
			return a, review.ErrCapacity
		}
		at := now()
		var inventory *researchquestion.Inventory
		if typeID == investigation.ReuseTypeID {
			all, err := loadHypothesisPublications(ctx, tx)
			if err != nil {
				return a, err
			}
			pin, err := reviewInventory(receiver, all, at)
			if err != nil {
				return a, err
			}
			inventory = &pin
		}
		a, err = review.StartProfileInventory(s, receiver, id, caseID, version, typeID, inventory, at)
		if err != nil {
			return a, err
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return a, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_news_reviews(id,payload) VALUES($1,$2)", id, raw)
		return a, err
	})
}
func (p *Postgres) CompleteReview(ctx context.Context, receiver, id string, steps []json.RawMessage, now func() time.Time) (review.View, error) {
	steps, digest, err := review.CanonicalSteps(steps)
	if err != nil {
		return review.View{}, err
	}
	current, err := p.Review(ctx, id)
	if err != nil {
		return review.View{}, err
	}
	if !ValidReceiverID(receiver) || current.Attempt.Pin.Subject.ReceiverID != receiver {
		return review.View{}, ErrReceiverMismatch
	}
	var result investigation.Result
	if current.Attempt.Status == "pending" {
		result, err = review.Evaluate(ctx, current.Attempt, steps)
		if err != nil {
			return review.View{}, err
		}
	}
	return p.reviewMutation(ctx, receiver, func(tx pgx.Tx, s newscase.State) (review.Attempt, error) {
		a, err := loadReview(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return a, review.ErrNotFound
		}
		if err != nil {
			return a, err
		}
		if _, err := review.Definition(a); err != nil {
			return a, err
		}
		if a.Status != "pending" {
			if a.StepsSHA256 != digest {
				return a, review.ErrConflict
			}
			return a, nil
		}
		a, err = review.Commit(a, s, steps, digest, result, now())
		if err != nil {
			return a, err
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return a, err
		}
		_, err = tx.Exec(ctx, "UPDATE mantik_news_reviews SET payload=$2 WHERE id=$1", id, raw)
		return a, err
	})
}

var _ ReviewStore = (*Memory)(nil)
var _ ReviewStore = (*Postgres)(nil)

var _ ReviewProfileStore = (*Memory)(nil)
var _ ReviewProfileStore = (*Postgres)(nil)

func reviewInventory(receiver string, all []hypothesis.Publication, at time.Time) (researchquestion.Inventory, error) {
	c, err := hypothesis.MakeCatalogue(receiver, all)
	if err != nil {
		return researchquestion.Inventory{}, err
	}
	for _, p := range all {
		if p.PublishedAt.After(at) {
			return researchquestion.Inventory{}, review.ErrConflict
		}
	}
	return c.ReviewInventory()
}
