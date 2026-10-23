package storage

import (
	"context"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// OwnedCapitalReadback describes only this owner's deferral journal. It is not
// a writable snapshot or an assertion about external executions or holdings.
// Policies without comparisons derive their opening balance from the retained
// account and policy; no genesis event is inserted by a read.
type OwnedCapitalReadback struct {
	Schema              string                  `json:"schema"`
	ReceiverID          string                  `json:"receiver_id"`
	Account             paperaccount.Account    `json:"account"`
	PolicyID            string                  `json:"policy_id"`
	PolicySHA256        string                  `json:"policy_sha256"`
	LedgerSHA256        string                  `json:"ledger_sha256"`
	AsOf                time.Time               `json:"as_of"`
	Source              string                  `json:"source"`
	AvailableCashCents  int64                   `json:"available_cash_cents"`
	RealizedPnLCents    int64                   `json:"realized_pnl_cents"`
	Positions           []allocation.Exposure   `json:"positions"`
	Orders              []allocation.Exposure   `json:"orders"`
	Commitments         []allocation.Commitment `json:"commitments"`
	Authority           string                  `json:"authority"`
	ExecutionAvailable  bool                    `json:"execution_available"`
	QualificationCredit bool                    `json:"qualification_credit"`
	SHA256              string                  `json:"sha256"`
}

// Sources have already been replayed in one owner lock/transaction. Never accept
// a projection from a caller, silently skip an orphan ledger, or invent effects.
func ownedCapitalReadbacks(ctx context.Context, owner string, policies allocation.PolicyCatalog, ledgers []allocation.Ledger) ([]OwnedCapitalReadback, error) {
	fail := func(err error) ([]OwnedCapitalReadback, error) { return nil, err }
	if !ValidReceiverID(owner) || policies.ReceiverID != owner || policies.Validate() != nil {
		return fail(ErrOwnedAllocationInvalid)
	}
	byPolicy := map[string]allocation.Ledger{}
	for _, l := range ledgers {
		id := l.Policy.Command.Policy.ID
		if _, exists := byPolicy[id]; exists {
			return fail(ErrOwnedAllocationConflict)
		}
		byPolicy[id] = l
	}
	out := []OwnedCapitalReadback{}
	for _, p := range policies.Records {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		id := p.Command.Policy.ID
		l, exists := byPolicy[id]
		source := "replayed_deferral_journal"
		if exists {
			if l.Policy.SHA256 != p.SHA256 || len(l.Entries) == 0 {
				return fail(ErrOwnedAllocationConflict)
			}
			delete(byPolicy, id)
		} else {
			var err error
			l, err = allocation.NewLedger(p)
			if err != nil {
				return fail(err)
			}
			source = "registered_opening_capital"
		}
		projection, err := allocation.ReplayLedger(ctx, l)
		if err != nil {
			return fail(err)
		}
		state := projection.Snapshot
		// This version reflects a deferral-only owner, not funded execution. A future
		// financial owner must change this contract deliberately with its own proof.
		if len(projection.Reservations) != 0 || state.AvailableCashCents != p.Account.Config.OpeningCashCents || state.RealizedPnLCents != 0 || len(state.Positions) != 0 || len(state.Orders) != 0 || len(state.Commitments) != 0 {
			return fail(ErrOwnedAllocationConflict)
		}
		row := OwnedCapitalReadback{Schema: "mantik-owned-deferral-capital-v1", ReceiverID: owner, Account: state.Account, PolicyID: id, PolicySHA256: p.SHA256, LedgerSHA256: l.SHA256, AsOf: state.AsOf, Source: source, AvailableCashCents: state.AvailableCashCents, RealizedPnLCents: state.RealizedPnLCents, Positions: state.Positions, Orders: state.Orders, Commitments: state.Commitments, Authority: "service_owned_deferral_capital_readback_only"}
		row.SHA256 = workerplan.Hash(row)
		out = append(out, clone(row))
	}
	if len(byPolicy) != 0 {
		return fail(ErrOwnedAllocationConflict)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	slices.SortFunc(out, func(a, b OwnedCapitalReadback) int { return strings.Compare(a.Account.Config.ID, b.Account.Config.ID) })
	return out, nil
}
