package storage

import (
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"github.com/jackc/pgx/v5"
)

type HypothesisCatalogueStore interface {
	HypothesisCatalogue(context.Context) (hypothesis.Catalogue, error)
}

func (m *Memory) HypothesisCatalogue(ctx context.Context) (hypothesis.Catalogue, error) {
	if err := ctx.Err(); err != nil {
		return hypothesis.Catalogue{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]hypothesis.Publication, 0, len(m.publications))
	for _, p := range m.publications {
		all = append(all, p)
	}
	return hypothesis.MakeCatalogue(m.receiverID, all)
}

func (p *Postgres) HypothesisCatalogue(ctx context.Context) (hypothesis.Catalogue, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hypothesis.Catalogue{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return hypothesis.Catalogue{}, err
	}
	all, err := loadHypothesisPublications(ctx, tx)
	if err != nil {
		return hypothesis.Catalogue{}, err
	}
	c, err := hypothesis.MakeCatalogue(receiver, all)
	if err != nil {
		return hypothesis.Catalogue{}, err
	}
	return c, tx.Commit(ctx)
}

var _ HypothesisCatalogueStore = (*Memory)(nil)
var _ HypothesisCatalogueStore = (*Postgres)(nil)

func loadHypothesisPublications(ctx context.Context, tx pgx.Tx) ([]hypothesis.Publication, error) {
	rows, err := tx.Query(ctx, "SELECT id,question_id,payload FROM mantik_hypothesis_publications ORDER BY id LIMIT $1", hypothesis.MaxPublications+1)
	if err != nil {
		return nil, err
	}
	all := []hypothesis.Publication{}
	for rows.Next() {
		var id, questionID string
		var raw []byte
		var publication hypothesis.Publication
		if err = rows.Scan(&id, &questionID, &raw); err == nil {
			err = json.Unmarshal(raw, &publication)
		}
		if err == nil && (id != publication.ID || questionID != publication.Sealed.ID) {
			err = hypothesis.ErrInvalid
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, publication)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(all) > hypothesis.MaxPublications {
		return nil, hypothesis.ErrCapacity
	}
	return all, nil
}
