package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type AllocationPolicyStore interface {
	RegisterAllocationPolicy(context.Context, allocation.PolicyCommand, func() time.Time) (allocation.PolicyReceipt, error)
	AllocationPolicies(context.Context) (allocation.PolicyCatalog, error)
}

func selectAllocationPolicy(c allocation.PolicyCatalog, cmd allocation.PolicyCommand) (allocation.PolicyRecord, bool, error) {
	for _, r := range c.Records {
		if r.Command.Policy.ID == cmd.Policy.ID || r.Command.AccountID == cmd.AccountID {
			if !reflect.DeepEqual(r.Command, cmd) {
				return allocation.PolicyRecord{}, false, allocation.ErrPolicyConflict
			}
			return r, true, nil
		}
	}
	return allocation.PolicyRecord{}, false, nil
}
func allocationFunding(c paperaccount.Catalog, id string) (paperaccount.Account, error) {
	for _, a := range c.Accounts {
		if a.Config.ID == id {
			return a, nil
		}
	}
	return paperaccount.Account{}, allocation.ErrPolicyNotFound
}
func allocationOwnership(c allocation.PolicyCatalog, fixtureAccount string) error {
	for _, r := range c.Records {
		if r.Command.AccountID == fixtureAccount {
			return allocation.ErrPolicyConflict
		}
	}
	return nil
}

// Caller holds the same aggregate lock as fixture binding and account writes.
func (m *Memory) allocationPoliciesLocked() (allocation.PolicyCatalog, error) {
	a, err := m.paperAccountsLocked()
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	all := []allocation.PolicyRecord{}
	for id, r := range m.allocationPolicies {
		if id != r.Command.Policy.ID {
			return allocation.PolicyCatalog{}, allocation.ErrInvalid
		}
		all = append(all, r)
	}
	c, err := allocation.PoliciesFor(a, all)
	if err != nil {
		return c, err
	}
	book, err := m.fixtureCapitalLocked()
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	if book != nil {
		v, err := book.Snapshot()
		if err != nil {
			return allocation.PolicyCatalog{}, err
		}
		if err = allocationOwnership(c, v.AccountID); err != nil {
			return allocation.PolicyCatalog{}, err
		}
	}
	return c, nil
}
func (m *Memory) AllocationPolicies(ctx context.Context) (allocation.PolicyCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return allocation.PolicyCatalog{}, err
	}
	return m.allocationPoliciesLocked()
}
func (m *Memory) RegisterAllocationPolicy(ctx context.Context, cmd allocation.PolicyCommand, now func() time.Time) (allocation.PolicyReceipt, error) {
	if cmd.Validate() != nil {
		return allocation.PolicyReceipt{}, allocation.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if cmd.ReceiverID != m.receiverID || !ValidReceiverID(cmd.ReceiverID) {
		return allocation.PolicyReceipt{}, ErrReceiverMismatch
	}
	if cmd.Validate() != nil {
		return allocation.PolicyReceipt{}, allocation.ErrInvalid
	}
	c, err := m.allocationPoliciesLocked()
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if b := m.state.FixtureCapitalBinding; b != nil && b.AccountID == cmd.AccountID {
		return allocation.PolicyReceipt{}, allocation.ErrPolicyConflict
	}
	r, duplicate, err := selectAllocationPolicy(c, cmd)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if duplicate {
		return allocation.PolicyReceipt{Record: r, Duplicate: true}, nil
	}
	a, err := m.paperAccountsLocked()
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	funding, err := allocationFunding(a, cmd.AccountID)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if funding.SHA256 != cmd.Policy.AccountSHA256 {
		return allocation.PolicyReceipt{}, allocation.ErrPolicyConflict
	}
	if err = cmd.Policy.ValidateFor(funding); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	r, err = allocation.NewPolicyRecord(cmd, funding, now())
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	m.allocationPolicies[cmd.Policy.ID] = clone(r)
	return allocation.PolicyReceipt{Record: r}, nil
}

// The caller's state/receiver lock order matches BindFixtureCapital. Reads use
// one repeatable-read snapshot; the account catalogue is always owner supplied.
func readAllocationPolicies(ctx context.Context, tx pgx.Tx, receiver string, s domain.State) (allocation.PolicyCatalog, error) {
	accounts, err := readPaperAccounts(ctx, tx, receiver)
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,account_id,payload FROM mantik_allocation_policies ORDER BY id LIMIT $1", paperaccount.MaxAccounts+1)
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	all := []allocation.PolicyRecord{}
	for rows.Next() {
		var id, account string
		var raw []byte
		var r allocation.PolicyRecord
		if err = rows.Scan(&id, &account, &raw); err == nil {
			err = workerplan.ReadBoundedShape(raw, &r, allocation.MaxPolicyRecordBytes)
		}
		if err != nil {
			rows.Close()
			return allocation.PolicyCatalog{}, err
		}
		if r.Command.Policy.ID != id || r.Command.AccountID != account {
			rows.Close()
			return allocation.PolicyCatalog{}, allocation.ErrInvalid
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	c, err := allocation.PoliciesFor(accounts, all)
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	book, err := readFixtureCapital(ctx, tx, s)
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	if book != nil {
		v, err := book.Snapshot()
		if err != nil {
			return allocation.PolicyCatalog{}, err
		}
		if err = allocationOwnership(c, v.AccountID); err != nil {
			return allocation.PolicyCatalog{}, err
		}
	}
	return c, nil
}
func (p *Postgres) AllocationPolicies(ctx context.Context) (allocation.PolicyCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	var raw []byte
	var s domain.State
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return allocation.PolicyCatalog{}, err
	}
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return allocation.PolicyCatalog{}, err
	}
	if json.Unmarshal(raw, &s) != nil {
		return allocation.PolicyCatalog{}, allocation.ErrInvalid
	}
	c, err := readAllocationPolicies(ctx, tx, receiver, s)
	if err != nil {
		return allocation.PolicyCatalog{}, err
	}
	return c, tx.Commit(ctx)
}
func (p *Postgres) RegisterAllocationPolicy(ctx context.Context, cmd allocation.PolicyCommand, now func() time.Time) (allocation.PolicyReceipt, error) {
	if cmd.Validate() != nil {
		return allocation.PolicyReceipt{}, allocation.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	var s domain.State
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if json.Unmarshal(raw, &s) != nil {
		return allocation.PolicyReceipt{}, allocation.ErrInvalid
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if receiver != cmd.ReceiverID {
		return allocation.PolicyReceipt{}, ErrReceiverMismatch
	}
	c, err := readAllocationPolicies(ctx, tx, receiver, s)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if b := s.FixtureCapitalBinding; b != nil && b.AccountID == cmd.AccountID {
		return allocation.PolicyReceipt{}, allocation.ErrPolicyConflict
	}
	r, duplicate, err := selectAllocationPolicy(c, cmd)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if duplicate {
		return allocation.PolicyReceipt{Record: r, Duplicate: true}, tx.Commit(ctx)
	}
	accounts, err := readPaperAccounts(ctx, tx, receiver)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	a, err := allocationFunding(accounts, cmd.AccountID)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if a.SHA256 != cmd.Policy.AccountSHA256 {
		return allocation.PolicyReceipt{}, allocation.ErrPolicyConflict
	}
	if err = cmd.Policy.ValidateFor(a); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	r, err = allocation.NewPolicyRecord(cmd, a, now())
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return allocation.PolicyReceipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_allocation_policies(id,account_id,payload) VALUES($1,$2,$3)", cmd.Policy.ID, cmd.AccountID, payload); err != nil {
		return allocation.PolicyReceipt{}, err
	}
	return allocation.PolicyReceipt{Record: r}, tx.Commit(ctx)
}

var _ AllocationPolicyStore = (*Memory)(nil)
var _ AllocationPolicyStore = (*Postgres)(nil)
