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
	Schema             string                  `json:"schema"`
	ReceiverID         string                  `json:"receiver_id"`
	Account            paperaccount.Account    `json:"account"`
	PolicyID           string                  `json:"policy_id"`
	PolicySHA256       string                  `json:"policy_sha256"`
	LedgerSHA256       string                  `json:"ledger_sha256"`
	AsOf               time.Time               `json:"as_of"`
	Source             string                  `json:"source"`
	AvailableCashCents int64                   `json:"available_cash_cents"`
	RealizedPnLCents   int64                   `json:"realized_pnl_cents"`
	Positions          []allocation.Exposure   `json:"positions"`
	Orders             []allocation.Exposure   `json:"orders"`
	Commitments        []allocation.Commitment `json:"commitments"`
	// Capital v2 only: reservations held by owner-issued paper admissions.
	Reservations        []allocation.Reservation `json:"reservations,omitempty"`
	Authority           string                   `json:"authority"`
	ExecutionAvailable  bool                     `json:"execution_available"`
	QualificationCredit bool                     `json:"qualification_credit"`
	SHA256              string                   `json:"sha256"`
}

// Sources have already been replayed in one owner lock/transaction. Never accept
// a projection from a caller, silently skip an orphan ledger, or invent effects.
// granted names the policies whose ledgers hold a comparison v15 record. Only
// those may carry reservations, and only reserved, unfilled ones: no execution
// owner records fills, closes or realized results yet. A ledger without
// reservations keeps the v1 readback.
func ownedCapitalReadbacks(ctx context.Context, owner string, policies allocation.PolicyCatalog, ledgers []allocation.Ledger, granted ...string) ([]OwnedCapitalReadback, error) {
	return ownedExecutedCapitalReadbacks(ctx, owner, policies, ledgers, nil, granted...)
}

// ownedExecutedCapitalReadbacks also accepts executed policies, whose ledgers
// hold a comparison v16 record. Their readback is v3: reservations may be
// filled by entry execution, so held, filled and released amounts together
// account for each proposal and for the opening cash. No owner closes
// positions or records unknown outcomes yet, so both must still be absent.
func ownedExecutedCapitalReadbacks(ctx context.Context, owner string, policies allocation.PolicyCatalog, ledgers []allocation.Ledger, executed map[string]bool, granted ...string) ([]OwnedCapitalReadback, error) {
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
			// A sealed ledger may have no entries after its base.
			if l.Policy.SHA256 != p.SHA256 || len(l.Entries) == 0 && l.Base == nil {
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
		row := OwnedCapitalReadback{Schema: "mantik-owned-deferral-capital-v1", ReceiverID: owner, Account: state.Account, PolicyID: id, PolicySHA256: p.SHA256, LedgerSHA256: l.SHA256, AsOf: state.AsOf, Source: source, AvailableCashCents: state.AvailableCashCents, RealizedPnLCents: state.RealizedPnLCents, Positions: state.Positions, Orders: state.Orders, Commitments: state.Commitments, Authority: "service_owned_deferral_capital_readback_only"}
		if exists && executed[id] && len(projection.Reservations) > 0 {
			held, filled := int64(0), 0
			for _, r := range projection.Reservations {
				if r.ClosedCostCents != 0 || r.ProceedsCents != 0 || r.Unknown || r.ReservedCents+r.FilledCents+r.ReleasedCents != r.Proposal.NotionalCents {
					return fail(ErrOwnedAllocationConflict)
				}
				held += r.ReservedCents + r.FilledCents
				if r.FilledCents > 0 {
					filled++
				}
			}
			if state.AvailableCashCents+held != p.Account.Config.OpeningCashCents || state.RealizedPnLCents != 0 || len(state.Positions) != filled || len(state.Commitments) != len(projection.Reservations) {
				return fail(ErrOwnedAllocationConflict)
			}
			row.Schema, row.Source, row.Authority, row.Reservations = "mantik-owned-allocation-capital-v3", "replayed_allocation_journal", "service_owned_paper_execution_capital_readback_only", projection.Reservations
		} else if exists && slices.Contains(granted, id) && len(projection.Reservations) > 0 {
			reserved := int64(0)
			for _, r := range projection.Reservations {
				if r.FilledCents != 0 || r.ClosedCostCents != 0 || r.ProceedsCents != 0 || r.Unknown || r.ReservedCents+r.ReleasedCents != r.Proposal.NotionalCents {
					return fail(ErrOwnedAllocationConflict)
				}
				reserved += r.ReservedCents
			}
			if state.AvailableCashCents+reserved != p.Account.Config.OpeningCashCents || state.RealizedPnLCents != 0 || len(state.Positions) != 0 || len(state.Commitments) != len(projection.Reservations) {
				return fail(ErrOwnedAllocationConflict)
			}
			row.Schema, row.Source, row.Authority, row.Reservations = "mantik-owned-allocation-capital-v2", "replayed_allocation_journal", "service_owned_reserved_capital_readback_only", projection.Reservations
		} else if len(projection.Reservations) != 0 || state.AvailableCashCents != p.Account.Config.OpeningCashCents || state.RealizedPnLCents != 0 || len(state.Positions) != 0 || len(state.Orders) != 0 || len(state.Commitments) != 0 {
			// A deferral-only ledger never funds anything.
			return fail(ErrOwnedAllocationConflict)
		}
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
