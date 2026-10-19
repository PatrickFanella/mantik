package storage

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type PaperAccountStore interface {
	CreatePaperAccount(context.Context, string, paperaccount.Config, func() time.Time) (paperaccount.Receipt, error)
	PaperAccounts(context.Context) (paperaccount.Catalog, error)
}

func (m *Memory) paperAccountsLocked() (paperaccount.Catalog, error) {
	all := []paperaccount.Account{}
	for id, a := range m.paperAccounts {
		if id != a.Config.ID {
			return paperaccount.Catalog{}, paperaccount.ErrInvalid
		}
		all = append(all, a)
	}
	return paperaccount.MakeCatalog(m.receiverID, all)
}
func (m *Memory) PaperAccounts(ctx context.Context) (paperaccount.Catalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperaccount.Catalog{}, err
	}
	return m.paperAccountsLocked()
}
func (m *Memory) CreatePaperAccount(ctx context.Context, receiver string, c paperaccount.Config, now func() time.Time) (paperaccount.Receipt, error) {
	if c.Validate() != nil {
		return paperaccount.Receipt{}, paperaccount.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperaccount.Receipt{}, err
	}
	if receiver != m.receiverID || !ValidReceiverID(receiver) {
		return paperaccount.Receipt{}, ErrReceiverMismatch
	}
	catalog, err := m.paperAccountsLocked()
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	for _, a := range catalog.Accounts {
		if a.Config.ID == c.ID {
			if a.Config != c {
				return paperaccount.Receipt{}, paperaccount.ErrConflict
			}
			return paperaccount.Receipt{Account: a, Duplicate: true}, nil
		}
	}
	if len(catalog.Accounts) >= paperaccount.MaxAccounts {
		return paperaccount.Receipt{}, paperaccount.ErrCapacity
	}
	a, err := paperaccount.Create(receiver, c, now())
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	m.paperAccounts[c.ID] = a
	return paperaccount.Receipt{Account: a}, nil
}
func decodePaperAccount(raw []byte, id, receiver string) (paperaccount.Account, error) {
	var a paperaccount.Account
	if workerplan.ReadBoundedShape(raw, &a, 16<<10) != nil || a.Config.ID != id || a.ReceiverID != receiver || a.Validate() != nil {
		return paperaccount.Account{}, paperaccount.ErrInvalid
	}
	return a, nil
}

// The receiver row serializes creation and the capacity check across processes.
// Reads use one repeatable-read snapshot for the receiver and complete catalogue.
func readPaperAccounts(ctx context.Context, tx pgx.Tx, receiver string) (paperaccount.Catalog, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_accounts ORDER BY id LIMIT $1", paperaccount.MaxAccounts+1)
	if err != nil {
		return paperaccount.Catalog{}, err
	}
	defer rows.Close()
	all := []paperaccount.Account{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return paperaccount.Catalog{}, err
		}
		a, err := decodePaperAccount(raw, id, receiver)
		if err != nil {
			return paperaccount.Catalog{}, err
		}
		all = append(all, a)
	}
	if err = rows.Err(); err != nil {
		return paperaccount.Catalog{}, err
	}
	return paperaccount.MakeCatalog(receiver, all)
}
func (p *Postgres) PaperAccounts(ctx context.Context) (paperaccount.Catalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return paperaccount.Catalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return paperaccount.Catalog{}, err
	}
	c, err := readPaperAccounts(ctx, tx, receiver)
	if err != nil {
		return c, err
	}
	if err = tx.Commit(ctx); err != nil {
		return paperaccount.Catalog{}, err
	}
	return c, nil
}
func (p *Postgres) CreatePaperAccount(ctx context.Context, receiver string, c paperaccount.Config, now func() time.Time) (paperaccount.Receipt, error) {
	if c.Validate() != nil {
		return paperaccount.Receipt{}, paperaccount.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var actual string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&actual); err != nil {
		return paperaccount.Receipt{}, err
	}
	if receiver != actual || !ValidReceiverID(receiver) {
		return paperaccount.Receipt{}, ErrReceiverMismatch
	}
	catalog, err := readPaperAccounts(ctx, tx, actual)
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	for _, a := range catalog.Accounts {
		if a.Config.ID == c.ID {
			if a.Config != c {
				return paperaccount.Receipt{}, paperaccount.ErrConflict
			}
			return paperaccount.Receipt{Account: a, Duplicate: true}, tx.Commit(ctx)
		}
	}
	if len(catalog.Accounts) >= paperaccount.MaxAccounts {
		return paperaccount.Receipt{}, paperaccount.ErrCapacity
	}
	a, err := paperaccount.Create(actual, c, now())
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return paperaccount.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_accounts(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
		return paperaccount.Receipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return paperaccount.Receipt{}, err
	}
	return paperaccount.Receipt{Account: a}, nil
}

var _ PaperAccountStore = (*Memory)(nil)
var _ PaperAccountStore = (*Postgres)(nil)
