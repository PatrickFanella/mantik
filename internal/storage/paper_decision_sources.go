package storage

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"github.com/jackc/pgx/v5"
)

// PaperDecisionSources is internal service input, never an HTTP request or an
// execution grant. Both values come from the same validated owner snapshot.
type PaperDecisionSources struct {
	Decision PaperDecisionRecord
	Basis    PaperDecisionBasis
}
type PaperDecisionSourceStore interface {
	PaperDecisionSources(context.Context, string, PaperDecisionEvaluator) (PaperDecisionSources, error)
}

func selectPaperDecisionSources(id string, all map[string]retainedPaperDecision) (PaperDecisionSources, error) {
	if !paperaccount.ID.MatchString(id) {
		return PaperDecisionSources{}, ErrPaperDecisionInvalid
	}
	v, ok := all[id]
	if !ok {
		return PaperDecisionSources{}, ErrPaperDecisionNotFound
	}
	return clone(PaperDecisionSources{v.Record, v.Basis}), nil
}
func (m *Memory) PaperDecisionSources(ctx context.Context, id string, eval PaperDecisionEvaluator) (PaperDecisionSources, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperDecisionSources{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperDecisionSources{}, err
	}
	if _, err = paperDecisionCatalog(m.receiverID, m.paperDecisions, s, m.decisionFeedLocked(), eval); err != nil {
		return PaperDecisionSources{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionSources{}, err
	}
	return selectPaperDecisionSources(id, m.paperDecisions)
}
func (p *Postgres) PaperDecisionSources(ctx context.Context, id string, eval PaperDecisionEvaluator) (PaperDecisionSources, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperDecisionSources{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperDecisionSources{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperDecisionSources{}, err
	}
	all, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperDecisionSources{}, err
	}
	if _, err = paperDecisionCatalog(feed.ReceiverID, all, strategies, feed, eval); err != nil {
		return PaperDecisionSources{}, err
	}
	out, err := selectPaperDecisionSources(id, all)
	if err != nil {
		return PaperDecisionSources{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperDecisionSources{}, err
	}
	return out, nil
}

var _ PaperDecisionSourceStore = (*Memory)(nil)
var _ PaperDecisionSourceStore = (*Postgres)(nil)
