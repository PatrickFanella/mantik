package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
	"time"
)

type SocialWatchStore interface {
	ApplySocialCommand(context.Context, socialwatch.Command, func() time.Time) (socialwatch.Receipt, error)
	SocialHistory(context.Context) (socialwatch.Catalog, error)
}

func (m *Memory) socialJournalLocked() (socialwatch.Journal, error) {
	if m.socialJournal == nil {
		return socialwatch.New(m.receiverID)
	}
	if m.socialJournal.ReceiverID != m.receiverID {
		return socialwatch.Journal{}, ErrReceiverMismatch
	}
	return clone(*m.socialJournal), nil
}
func (m *Memory) ApplySocialCommand(ctx context.Context, c socialwatch.Command, now func() time.Time) (socialwatch.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return socialwatch.Receipt{}, err
	}
	old, err := m.socialJournalLocked()
	if err != nil {
		return socialwatch.Receipt{}, err
	}
	next, r, err := socialwatch.Append(old, c, now)
	if err != nil {
		return socialwatch.Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return socialwatch.Receipt{}, err
	}
	if !r.Duplicate {
		if err := socialDecisionReviewClock(m.receiverID, m.decisionReviews, r.Entry.At); err != nil {
			return socialwatch.Receipt{}, err
		}
		m.socialJournal = &next
	}
	return r, nil
}
func (m *Memory) SocialHistory(ctx context.Context) (socialwatch.Catalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return socialwatch.Catalog{}, err
	}
	j, err := m.socialJournalLocked()
	if err != nil {
		return socialwatch.Catalog{}, err
	}
	return socialwatch.Read(j)
}
func readSocialJournal(ctx context.Context, tx pgx.Tx, owner string) (socialwatch.Journal, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_social_watch_journal WHERE singleton").Scan(&raw)
	if err == pgx.ErrNoRows {
		return socialwatch.New(owner)
	}
	if err != nil {
		return socialwatch.Journal{}, err
	}
	var j socialwatch.Journal
	if workerplan.ReadBoundedShape(raw, &j, socialwatch.MaxJournalBytes) != nil {
		return j, socialwatch.ErrInvalid
	}
	if j.ReceiverID != owner {
		return j, ErrReceiverMismatch
	}
	return j, nil
}
func (p *Postgres) ApplySocialCommand(ctx context.Context, c socialwatch.Command, now func() time.Time) (socialwatch.Receipt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return socialwatch.Receipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// This owner takes only receiver: it never acquires state/collector after it
	// and cannot invert the collector -> state -> receiver order of other owners.
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
		return socialwatch.Receipt{}, err
	}
	j, err := readSocialJournal(ctx, tx, owner)
	if err != nil {
		return socialwatch.Receipt{}, err
	}
	next, r, err := socialwatch.Append(j, c, now)
	if err != nil {
		return socialwatch.Receipt{}, err
	}
	if !r.Duplicate {
		reviews, e := readDecisionReviews(ctx, tx)
		if e != nil {
			return socialwatch.Receipt{}, e
		}
		if e = socialDecisionReviewClock(owner, reviews, r.Entry.At); e != nil {
			return socialwatch.Receipt{}, e
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return socialwatch.Receipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_social_watch_journal(singleton,payload) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET payload=excluded.payload", raw); err != nil {
			return socialwatch.Receipt{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return socialwatch.Receipt{}, err
	}
	return r, nil
}
func (p *Postgres) SocialHistory(ctx context.Context) (socialwatch.Catalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return socialwatch.Catalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return socialwatch.Catalog{}, err
	}
	j, err := readSocialJournal(ctx, tx, owner)
	if err != nil {
		return socialwatch.Catalog{}, err
	}
	out, err := socialwatch.Read(j)
	if err != nil {
		return socialwatch.Catalog{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return socialwatch.Catalog{}, err
	}
	return out, nil
}

var _ SocialWatchStore = (*Memory)(nil)
var _ SocialWatchStore = (*Postgres)(nil)
