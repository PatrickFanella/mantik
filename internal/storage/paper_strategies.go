package storage

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type PaperStrategyStore interface {
	RegisterPaperStrategy(context.Context, paperstrategy.Command, func() time.Time) (paperstrategy.Receipt, error)
	PaperStrategies(context.Context) (paperstrategy.Catalog, error)
}

func (m *Memory) paperStrategiesLocked() (paperstrategy.Catalog, error) {
	all := []paperstrategy.Record{}
	for id, r := range m.paperStrategies {
		if id != r.Command.Definition.ID {
			return paperstrategy.Catalog{}, paperstrategy.ErrInvalid
		}
		all = append(all, r)
	}
	return paperstrategy.CatalogFor(m.receiverID, all)
}
func (m *Memory) PaperStrategies(ctx context.Context) (paperstrategy.Catalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperstrategy.Catalog{}, err
	}
	return m.paperStrategiesLocked()
}
func (m *Memory) RegisterPaperStrategy(ctx context.Context, c paperstrategy.Command, now func() time.Time) (paperstrategy.Receipt, error) {
	if c.Validate() != nil {
		return paperstrategy.Receipt{}, paperstrategy.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperstrategy.Receipt{}, err
	}
	if c.ReceiverID != m.receiverID {
		return paperstrategy.Receipt{}, ErrReceiverMismatch
	}
	all, err := m.paperStrategiesLocked()
	if err != nil {
		return paperstrategy.Receipt{}, err
	}
	r, err := paperstrategy.Append(all, c, now)
	if err != nil {
		return paperstrategy.Receipt{}, err
	}
	if !r.Duplicate {
		if err := paperStrategySearchClock(m.receiverID, m.evaluationAssessments, r.Record.RegisteredAt); err != nil {
			return paperstrategy.Receipt{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return paperstrategy.Receipt{}, err
	}
	if !r.Duplicate {
		m.paperStrategies[c.Definition.ID] = r.Record
	}
	return r, nil
}
func readPaperStrategies(ctx context.Context, tx pgx.Tx, owner string) (paperstrategy.Catalog, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_strategies ORDER BY id LIMIT $1", paperstrategy.MaxDefinitions+1)
	if err != nil {
		return paperstrategy.Catalog{}, err
	}
	defer rows.Close()
	all := []paperstrategy.Record{}
	for rows.Next() {
		var id string
		var raw []byte
		var r paperstrategy.Record
		if err := rows.Scan(&id, &raw); err != nil {
			return paperstrategy.Catalog{}, err
		}
		if workerplan.ReadBoundedShape(raw, &r, paperstrategy.MaxRecordBytes) != nil || id != r.Command.Definition.ID {
			return paperstrategy.Catalog{}, paperstrategy.ErrInvalid
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return paperstrategy.Catalog{}, err
	}
	return paperstrategy.CatalogFor(owner, all)
}
func (p *Postgres) PaperStrategies(ctx context.Context) (paperstrategy.Catalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return paperstrategy.Catalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return paperstrategy.Catalog{}, err
	}
	c, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return paperstrategy.Catalog{}, err
	}
	return c, tx.Commit(ctx)
}
func (p *Postgres) RegisterPaperStrategy(ctx context.Context, c paperstrategy.Command, now func() time.Time) (paperstrategy.Receipt, error) {
	if c.Validate() != nil {
		return paperstrategy.Receipt{}, paperstrategy.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return paperstrategy.Receipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
		return paperstrategy.Receipt{}, err
	}
	if c.ReceiverID != owner {
		return paperstrategy.Receipt{}, ErrReceiverMismatch
	}
	all, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return paperstrategy.Receipt{}, err
	}
	r, err := paperstrategy.Append(all, c, now)
	if err != nil {
		return paperstrategy.Receipt{}, err
	}
	if !r.Duplicate {
		// Assessment issuance holds this receiver lock while retaining its cut.
		assessments, err := readPaperEvaluationAssessments(ctx, tx)
		if err != nil {
			return paperstrategy.Receipt{}, err
		}
		if err = paperStrategySearchClock(owner, assessments, r.Record.RegisteredAt); err != nil {
			return paperstrategy.Receipt{}, err
		}
		raw, err := json.Marshal(r.Record)
		if err != nil {
			return paperstrategy.Receipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_strategies(id,payload) VALUES($1,$2)", c.Definition.ID, raw); err != nil {
			return paperstrategy.Receipt{}, err
		}
	}
	return r, tx.Commit(ctx)
}

var _ PaperStrategyStore = (*Memory)(nil)
var _ PaperStrategyStore = (*Postgres)(nil)
