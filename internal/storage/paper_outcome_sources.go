package storage

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"github.com/jackc/pgx/v5"
)

// PaperOutcomeSources is internal service input, never an HTTP request or an
// execution grant. Both values come from the same validated owner snapshot.
type PaperOutcomeSources struct {
	Outcome PaperOutcomeRecord
	Basis   PaperOutcomeBasis
}
type PaperOutcomeSourceStore interface {
	PaperOutcomeSources(context.Context, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperOutcomeSources, error)
}

func selectPaperOutcomeSources(id string, all map[string]retainedPaperOutcome) (PaperOutcomeSources, error) {
	if !paperaccount.ID.MatchString(id) {
		return PaperOutcomeSources{}, ErrPaperOutcomeInvalid
	}
	v, ok := all[id]
	if !ok {
		return PaperOutcomeSources{}, ErrPaperOutcomeNotFound
	}
	return clone(PaperOutcomeSources{v.Record, v.Basis}), nil
}
func (m *Memory) PaperOutcomeSources(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperOutcomeSources, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperOutcomeSources{}, err
	}
	r, retired, err := m.paperTargetMetadataLocked(ctx, "outcome", id)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	if retired {
		owners, err := m.archivedPaperTargetLocked(ctx, r, de, oe)
		if err != nil {
			return PaperOutcomeSources{}, err
		}
		return selectPaperOutcomeSources(id, owners.outcomes)
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	if _, err = paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, s, m.decisionFeedLocked(), de, oe); err != nil {
		return PaperOutcomeSources{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperOutcomeSources{}, err
	}
	out, err := selectPaperOutcomeSources(id, m.paperOutcomes)
	if errors.Is(err, ErrPaperOutcomeNotFound) && r.ID != "" {
		return PaperOutcomeSources{}, ErrPaperOutcomeConflict
	}
	return out, err
}
func (p *Postgres) PaperOutcomeSources(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperOutcomeSources, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, retired, err := readPaperTargetMetadata(ctx, tx, "outcome", id)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	if retired {
		owners, err := readArchivedPaperTarget(ctx, tx, r, de, oe)
		if err != nil {
			return PaperOutcomeSources{}, err
		}
		out, err := selectPaperOutcomeSources(id, owners.outcomes)
		if err != nil {
			return PaperOutcomeSources{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PaperOutcomeSources{}, err
		}
		return out, nil
	}
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	if _, err = paperOutcomeCatalog(feed.ReceiverID, all, decisions, strategies, feed, de, oe); err != nil {
		return PaperOutcomeSources{}, err
	}
	out, err := selectPaperOutcomeSources(id, all)
	if errors.Is(err, ErrPaperOutcomeNotFound) && r.ID != "" {
		return PaperOutcomeSources{}, ErrPaperOutcomeConflict
	}
	if err != nil {
		return PaperOutcomeSources{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperOutcomeSources{}, err
	}
	return out, nil
}

var _ PaperOutcomeSourceStore = (*Memory)(nil)
var _ PaperOutcomeSourceStore = (*Postgres)(nil)
