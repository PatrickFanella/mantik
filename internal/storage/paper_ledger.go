package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
	"time"
)

type PaperLedgerStore interface {
	PaperLedgerRehearsal(context.Context, string) (paperledger.Rehearsal, error)
	AppendPaperLedgerRehearsal(context.Context, string, paperledger.AppendCommand, func() time.Time) (paperledger.AppendReceipt, error)
}

func (m *Memory) paperLedgerLocked(ctx context.Context, id string) (paperledger.Rehearsal, error) {
	a, ok := m.paperAccounts[id]
	if !ok {
		return paperledger.Rehearsal{}, paperaccount.ErrNotFound
	}
	if a.Config.ID != id || a.ReceiverID != m.receiverID || a.Validate() != nil {
		return paperledger.Rehearsal{}, paperledger.ErrInvalid
	}
	r, ok := m.paperLedgers[id]
	if !ok {
		return paperledger.NewRehearsal(ctx, a)
	}
	if r.Account != a {
		return paperledger.Rehearsal{}, paperledger.ErrInvalid
	}
	if err := r.Validate(ctx); err != nil {
		return paperledger.Rehearsal{}, err
	}
	return clone(r), nil
}
func (m *Memory) PaperLedgerRehearsal(ctx context.Context, id string) (paperledger.Rehearsal, error) {
	if !paperaccount.ID.MatchString(id) {
		return paperledger.Rehearsal{}, paperledger.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperledger.Rehearsal{}, err
	}
	return m.paperLedgerLocked(ctx, id)
}
func (m *Memory) AppendPaperLedgerRehearsal(ctx context.Context, id string, c paperledger.AppendCommand, now func() time.Time) (paperledger.AppendReceipt, error) {
	if !paperaccount.ID.MatchString(id) || c.Validate() != nil {
		return paperledger.AppendReceipt{}, paperledger.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return paperledger.AppendReceipt{}, err
	}
	if c.ReceiverID != m.receiverID {
		return paperledger.AppendReceipt{}, ErrReceiverMismatch
	}
	r, err := m.paperLedgerLocked(ctx, id)
	if err != nil {
		return paperledger.AppendReceipt{}, err
	}
	// A retry returns its original receipt without sampling a new clock. Append
	// validates the complete original command, including its original head pin.
	at := time.Time{}
	for _, old := range r.Accepted {
		if old.Command.Entry.ID == c.Entry.ID {
			at = old.AcceptedAt
			break
		}
	}
	if at.IsZero() {
		at = now()
	}
	next, receipt, err := paperledger.Append(ctx, r, c, at)
	if err != nil {
		return paperledger.AppendReceipt{}, err
	}
	if !receipt.Duplicate {
		m.paperLedgers[id] = clone(next)
	}
	return clone(receipt), nil
}
func readPaperLedger(ctx context.Context, tx pgx.Tx, id, receiver string) (paperledger.Rehearsal, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_paper_accounts WHERE id=$1", id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return paperledger.Rehearsal{}, paperaccount.ErrNotFound
		}
		return paperledger.Rehearsal{}, err
	}
	a, err := decodePaperAccount(raw, id, receiver)
	if err != nil {
		return paperledger.Rehearsal{}, err
	}
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_paper_ledger_rehearsals WHERE account_id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return paperledger.NewRehearsal(ctx, a)
	}
	if err != nil {
		return paperledger.Rehearsal{}, err
	}
	var r paperledger.Rehearsal
	if workerplan.ReadBoundedShape(raw, &r, paperledger.MaxRehearsalBytes) != nil || r.Account != a {
		return paperledger.Rehearsal{}, paperledger.ErrInvalid
	}
	if err = r.Validate(ctx); err != nil {
		return paperledger.Rehearsal{}, err
	}
	return r, nil
}
func (p *Postgres) PaperLedgerRehearsal(ctx context.Context, id string) (paperledger.Rehearsal, error) {
	if !paperaccount.ID.MatchString(id) {
		return paperledger.Rehearsal{}, paperledger.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return paperledger.Rehearsal{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return paperledger.Rehearsal{}, err
	}
	r, err := readPaperLedger(ctx, tx, id, receiver)
	if err != nil {
		return paperledger.Rehearsal{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return paperledger.Rehearsal{}, err
	}
	return r, nil
}
func (p *Postgres) AppendPaperLedgerRehearsal(ctx context.Context, id string, c paperledger.AppendCommand, now func() time.Time) (paperledger.AppendReceipt, error) {
	if !paperaccount.ID.MatchString(id) || c.Validate() != nil {
		return paperledger.AppendReceipt{}, paperledger.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return paperledger.AppendReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return paperledger.AppendReceipt{}, err
	}
	if receiver != c.ReceiverID {
		return paperledger.AppendReceipt{}, ErrReceiverMismatch
	}
	r, err := readPaperLedger(ctx, tx, id, receiver)
	if err != nil {
		return paperledger.AppendReceipt{}, err
	}
	at := time.Time{}
	for _, old := range r.Accepted {
		if old.Command.Entry.ID == c.Entry.ID {
			at = old.AcceptedAt
			break
		}
	}
	if at.IsZero() {
		at = now()
	}
	next, receipt, err := paperledger.Append(ctx, r, c, at)
	if err != nil {
		return paperledger.AppendReceipt{}, err
	}
	if receipt.Duplicate {
		return receipt, tx.Commit(ctx)
	}
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > paperledger.MaxRehearsalBytes {
		return paperledger.AppendReceipt{}, paperledger.ErrLimit
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_ledger_rehearsals(account_id,payload) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET payload=excluded.payload", id, raw); err != nil {
		return paperledger.AppendReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return paperledger.AppendReceipt{}, err
	}
	return receipt, nil
}
