package storage

import (
	"context"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"github.com/jackc/pgx/v5"
)

func submissionCommand(s account.CappedState, o account.BookOrder, marks map[string]string) (account.BookCommand, error) {
	for _, event := range s.Events {
		if event.Kind == "portfolio_reserve" && event.Order != nil && event.Order.ID == o.ID {
			retained := *event.Order
			if !retained.At.Equal(o.At) {
				return account.BookCommand{}, account.ErrBookConflict
			}
			retained.At = o.At
			if retained != o {
				return account.BookCommand{}, account.ErrBookConflict
			}
			if err := account.CheckPortfolioSubmission(s, o, marks); err != nil {
				return account.BookCommand{}, err
			}
			return account.BookCommand{ID: "reserve-" + o.ID, Kind: "portfolio_reserve", At: o.At, Order: &o, Marks: marks}, nil
		}
	}
	return account.BookCommand{}, errors.New("submission requires retained portfolio reservation")
}

// CheckCappedSubmission reads current owner controls and family admission from
// one fenced snapshot after adapter preparation. It writes no new authority or
// reservation and cannot turn recovery into another broker POST.
func (m *Memory) CheckCappedSubmission(ctx context.Context, id string, o account.BookOrder, marks map[string]string) (account.CappedState, error) {
	if err := ctx.Err(); err != nil {
		return account.CappedState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.cappedBooks[id]
	if !ok {
		return account.CappedState{}, account.ErrBookNotFound
	}
	cmd, err := submissionCommand(s, o, marks)
	if err != nil {
		return account.CappedState{}, err
	}
	if !o.ReduceOnly {
		if err = m.checkPortfolioAdmissionsLocked(s, cmd); err != nil {
			return account.CappedState{}, err
		}
	}
	return copyCapped(s)
}

func (p *Postgres) CheckCappedSubmission(ctx context.Context, id string, o account.BookOrder, marks map[string]string) (account.CappedState, error) {
	if _, err := p.CappedBook(ctx, id); err != nil {
		return account.CappedState{}, err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return account.CappedState{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM mantik_capped_books WHERE id=$1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return account.CappedState{}, account.ErrBookNotFound
	}
	if err != nil {
		return account.CappedState{}, err
	}
	s, err := p.loadCapped(ctx, tx, id)
	if err != nil {
		return account.CappedState{}, err
	}
	cmd, err := submissionCommand(s, o, marks)
	if err != nil {
		return account.CappedState{}, err
	}
	if !o.ReduceOnly {
		if err = p.checkPortfolioAdmissionsTx(ctx, tx, s, cmd); err != nil {
			return account.CappedState{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return account.CappedState{}, err
	}
	return s, nil
}
