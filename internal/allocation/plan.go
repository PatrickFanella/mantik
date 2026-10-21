// Package allocation compares proposals under owner-supplied paper policy.
// It has no persistence, admission issuer, model, network or execution access.
package allocation

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxProposals = 128
const MaxExposures = 256
const MaxRules = 64
const MaxCommitments = 256

var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)

var ErrInvalid = errors.New("invalid paper allocation policy, snapshot or proposals")

type Strategy struct {
	ID             string `json:"id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	LimitCents     int64  `json:"limit_cents"`
	Priority       int    `json:"priority"`
}
type Symbol struct {
	ID         string   `json:"id"`
	LimitCents int64    `json:"limit_cents"`
	Groups     []string `json:"groups"`
}
type Group struct {
	ID         string `json:"id"`
	LimitCents int64  `json:"limit_cents"`
}

// Policy is supplied by the allocation owner, never by a research proposal.
// Correlation groups are declared gross-exposure buckets, not estimated returns.
type Policy struct {
	ID                    string     `json:"id"`
	AccountSHA256         string     `json:"account_sha256"`
	MaxSnapshotAgeSeconds int        `json:"max_snapshot_age_seconds"`
	Strategies            []Strategy `json:"strategies"`
	Symbols               []Symbol   `json:"symbols"`
	Groups                []Group    `json:"groups"`
}

// Admission must come from the owner's validated admission registry. A value
// passed to this pure planner is an assertion, not qualification attestation.
type Admission struct {
	StrategyID     string    `json:"strategy_id"`
	ArtifactSHA256 string    `json:"artifact_sha256"`
	EvidenceSHA256 string    `json:"evidence_sha256"`
	ValidFrom      time.Time `json:"valid_from"`
	ValidUntil     time.Time `json:"valid_until"`
}

// Orders include all outstanding cash reservations, including unknown effects.
// Partial fills appear as disjoint filled cost and remaining reserved amounts.
type Exposure struct {
	ID             string `json:"id"`
	DecisionID     string `json:"decision_id"`
	DecisionSHA256 string `json:"decision_sha256"`
	StrategyID     string `json:"strategy_id"`
	Symbol         string `json:"symbol"`
	AmountCents    int64  `json:"amount_cents"`
}

// Commitment identities survive completed orders and closed positions. Removing
// an exposure must never make the same decision eligible for another allocation.
type Commitment struct {
	DecisionID     string `json:"decision_id"`
	DecisionSHA256 string `json:"decision_sha256"`
	StrategyID     string `json:"strategy_id"`
	Symbol         string `json:"symbol"`
}
type Snapshot struct {
	Account            paperaccount.Account `json:"account"`
	AsOf               time.Time            `json:"as_of"`
	AvailableCashCents int64                `json:"available_cash_cents"`
	RealizedPnLCents   int64                `json:"realized_pnl_cents"`
	Positions          []Exposure           `json:"positions"`
	Orders             []Exposure           `json:"orders"`
	Admissions         []Admission          `json:"admissions"`
	Commitments        []Commitment         `json:"commitments"`
}

// Proposal carries no account, priority, policy, group or admission fields.
type Proposal struct {
	ID             string    `json:"id"`
	DecisionSHA256 string    `json:"decision_sha256"`
	StrategyID     string    `json:"strategy_id"`
	Symbol         string    `json:"symbol"`
	Origin         string    `json:"origin"`
	NotionalCents  int64     `json:"notional_cents"`
	ValidUntil     time.Time `json:"valid_until"`
}
type Decision struct {
	Proposal     Proposal `json:"proposal"`
	Status       string   `json:"status"`
	Reasons      []string `json:"reasons"`
	ReconsiderOn []string `json:"reconsider_on"`
}
type Plan struct {
	Schema                  string     `json:"schema"`
	PolicySHA256            string     `json:"policy_sha256"`
	SnapshotSHA256          string     `json:"snapshot_sha256"`
	ProposalsSHA256         string     `json:"proposals_sha256"`
	AccountID               string     `json:"account_id"`
	ReceiverID              string     `json:"receiver_id"`
	At                      time.Time  `json:"at"`
	Decisions               []Decision `json:"decisions"`
	RemainingCashCents      int64      `json:"remaining_cash_cents"`
	ExistingCommittedCents  int64      `json:"existing_committed_cents"`
	PlannedReservationCents int64      `json:"planned_reservation_cents"`
	Authority               string     `json:"authority"`
	ExecutionAvailable      bool       `json:"execution_available"`
	QualificationCredit     bool       `json:"qualification_credit"`
	SHA256                  string     `json:"sha256"`
}

func validTime(t time.Time) bool { return !t.IsZero() && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999 }
func validHash(s string) bool {
	return len(s) == 64 && strings.IndexFunc(s, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') }) == -1
}
func amount(n int64) bool { return n > 0 && n <= paperaccount.MaxOpeningCashCents }
func id(s string) bool    { return paperaccount.ID.MatchString(s) }

// Evaluate makes one deterministic all-or-nothing comparison. It does not
// mutate inputs or reserve money. A store must serialize validation and commit
// against its authoritative snapshot; portable hashes cannot supply authority.
func Evaluate(ctx context.Context, policy Policy, state Snapshot, proposals []Proposal, at time.Time) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !id(policy.ID) || !validHash(policy.AccountSHA256) || state.Account.Validate() != nil || policy.AccountSHA256 != state.Account.SHA256 || policy.MaxSnapshotAgeSeconds < 1 || policy.MaxSnapshotAgeSeconds > 3600 || !validTime(at) || !validTime(state.AsOf) || state.AsOf.Before(state.Account.CreatedAt) || at.Before(state.AsOf) || at.Sub(state.AsOf) > time.Duration(policy.MaxSnapshotAgeSeconds)*time.Second || state.Positions == nil || state.Orders == nil || state.Admissions == nil || proposals == nil || len(proposals) > MaxProposals || len(state.Positions)+len(state.Orders) > MaxExposures {
		return Plan{}, ErrInvalid
	}
	if len(policy.Strategies) == 0 || len(policy.Strategies) > MaxRules || len(policy.Symbols) == 0 || len(policy.Symbols) > MaxRules || policy.Groups == nil || len(policy.Groups) > MaxRules {
		return Plan{}, ErrInvalid
	}
	strategies := map[string]Strategy{}
	symbols := map[string]Symbol{}
	groups := map[string]int64{}
	for _, r := range policy.Strategies {
		if !id(r.ID) || !validHash(r.ArtifactSHA256) || !amount(r.LimitCents) || r.Priority < 0 || r.Priority > MaxRules {
			return Plan{}, ErrInvalid
		}
		if _, ok := strategies[r.ID]; ok {
			return Plan{}, ErrInvalid
		}
		strategies[r.ID] = r
	}
	for _, r := range policy.Groups {
		if !id(r.ID) || !amount(r.LimitCents) {
			return Plan{}, ErrInvalid
		}
		if _, ok := groups[r.ID]; ok {
			return Plan{}, ErrInvalid
		}
		groups[r.ID] = r.LimitCents
	}
	for _, r := range policy.Symbols {
		if !symbolPattern.MatchString(r.ID) || !amount(r.LimitCents) || r.Groups == nil || len(r.Groups) > MaxRules {
			return Plan{}, ErrInvalid
		}
		if _, ok := symbols[r.ID]; ok {
			return Plan{}, ErrInvalid
		}
		seen := map[string]bool{}
		for _, g := range r.Groups {
			if _, ok := groups[g]; !ok || seen[g] {
				return Plan{}, ErrInvalid
			}
			seen[g] = true
		}
		symbols[r.ID] = r
	}
	if len(state.Admissions) > MaxRules || state.Commitments == nil || len(state.Commitments) > MaxCommitments {
		return Plan{}, ErrInvalid
	}
	admissions := map[string]Admission{}
	for _, a := range state.Admissions {
		r, ok := strategies[a.StrategyID]
		if !ok || r.ArtifactSHA256 != a.ArtifactSHA256 || !validHash(a.EvidenceSHA256) || !validTime(a.ValidFrom) || !validTime(a.ValidUntil) || !a.ValidUntil.After(a.ValidFrom) {
			return Plan{}, ErrInvalid
		}
		if _, exists := admissions[a.StrategyID]; exists {
			return Plan{}, ErrInvalid
		}
		admissions[a.StrategyID] = a
	}
	if state.AvailableCashCents < 0 || state.AvailableCashCents > 2*paperaccount.MaxOpeningCashCents || state.RealizedPnLCents < -paperaccount.MaxOpeningCashCents || state.RealizedPnLCents > paperaccount.MaxOpeningCashCents {
		return Plan{}, ErrInvalid
	}
	byStrategy := map[string]int64{}
	bySymbol := map[string]int64{}
	byGroup := map[string]int64{}
	add := func(strategy, symbol string, n int64) {
		byStrategy[strategy] += n
		bySymbol[symbol] += n
		for _, g := range symbols[symbol].Groups {
			byGroup[g] += n
		}
	}
	exposureIDs := map[string]bool{}
	committedDecisions := map[string]Commitment{}
	decisionHashes := map[string]string{}
	for _, c := range state.Commitments {
		_, strategyOK := strategies[c.StrategyID]
		_, symbolOK := symbols[c.Symbol]
		if !id(c.DecisionID) || !validHash(c.DecisionSHA256) || !strategyOK || !symbolOK {
			return Plan{}, ErrInvalid
		}
		if _, exists := committedDecisions[c.DecisionID]; exists {
			return Plan{}, ErrInvalid
		}
		if _, exists := decisionHashes[c.DecisionSHA256]; exists {
			return Plan{}, ErrInvalid
		}
		committedDecisions[c.DecisionID] = c
		decisionHashes[c.DecisionSHA256] = c.DecisionID
	}
	var committed int64
	for _, list := range [][]Exposure{state.Positions, state.Orders} {
		for _, e := range list {
			_, strategyOK := strategies[e.StrategyID]
			_, symbolOK := symbols[e.Symbol]
			if !id(e.ID) || exposureIDs[e.ID] || !id(e.DecisionID) || !validHash(e.DecisionSHA256) || !strategyOK || !symbolOK || !amount(e.AmountCents) {
				return Plan{}, ErrInvalid
			}
			if old, ok := committedDecisions[e.DecisionID]; !ok || old.DecisionSHA256 != e.DecisionSHA256 || old.StrategyID != e.StrategyID || old.Symbol != e.Symbol {
				return Plan{}, ErrInvalid
			}
			if old, ok := decisionHashes[e.DecisionSHA256]; ok && old != e.DecisionID {
				return Plan{}, ErrInvalid
			}
			exposureIDs[e.ID] = true
			committed += e.AmountCents
			add(e.StrategyID, e.Symbol, e.AmountCents)
		}
	}
	if committed > state.Account.Config.CapitalLimitCents || state.AvailableCashCents+committed != state.Account.Config.OpeningCashCents+state.RealizedPnLCents {
		return Plan{}, ErrInvalid
	}
	seen := map[string]bool{}
	proposalHashes := map[string]bool{}
	for _, p := range proposals {
		if !id(p.ID) || seen[p.ID] || proposalHashes[p.DecisionSHA256] || !id(p.StrategyID) || !symbolPattern.MatchString(p.Symbol) || !validHash(p.DecisionSHA256) || !amount(p.NotionalCents) || !validTime(p.ValidUntil) || (p.Origin != "research" && p.Origin != "copy_follow") {
			return Plan{}, ErrInvalid
		}
		seen[p.ID] = true
		proposalHashes[p.DecisionSHA256] = true
	}
	ordered := slices.Clone(proposals)
	slices.SortFunc(ordered, func(a, b Proposal) int {
		ar, aok := strategies[a.StrategyID]
		br, bok := strategies[b.StrategyID]
		if aok != bok {
			if aok {
				return -1
			}
			return 1
		}
		if ar.Priority != br.Priority {
			return ar.Priority - br.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := Plan{Schema: "mantik-paper-allocation-plan-v1", PolicySHA256: workerplan.Hash(policy), SnapshotSHA256: workerplan.Hash(state), ProposalsSHA256: workerplan.Hash(proposals), AccountID: state.Account.Config.ID, ReceiverID: state.Account.ReceiverID, At: at.UTC(), Decisions: []Decision{}, RemainingCashCents: state.AvailableCashCents, ExistingCommittedCents: committed, Authority: "owner_asserted_allocation_plan_only"}
	plannedCount := 0
	for _, p := range ordered {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		d := Decision{Proposal: p, Status: "reserve_plan", Reasons: []string{}, ReconsiderOn: []string{}}
		reject := func(reason string) {
			d.Status = "reject"
			d.Reasons = append(d.Reasons, reason)
			d.ReconsiderOn = []string{"new_finalized_decision"}
		}
		deferFor := func(reason, trigger string) {
			d.Status = "defer"
			d.Reasons = append(d.Reasons, reason)
			if !slices.Contains(d.ReconsiderOn, trigger) {
				d.ReconsiderOn = append(d.ReconsiderOn, trigger)
			}
		}
		strategy, known := strategies[p.StrategyID]
		symbol, supported := symbols[p.Symbol]
		admission, admitted := admissions[p.StrategyID]
		switch {
		case !known:
			reject("strategy_not_registered")
		case !supported:
			reject("symbol_not_supported")
		case !p.ValidUntil.After(at):
			reject("decision_expired")
		case decisionHashes[p.DecisionSHA256] != "" && decisionHashes[p.DecisionSHA256] != p.ID:
			reject("decision_identity_conflict")
		case committedDecisions[p.ID].DecisionID != "":
			e := committedDecisions[p.ID]
			if e.DecisionSHA256 != p.DecisionSHA256 || e.StrategyID != p.StrategyID || e.Symbol != p.Symbol {
				reject("decision_identity_conflict")
			} else {
				d.Status = "already_committed"
				d.Reasons = []string{"retained_allocation_commitment"}
			}
		case !admitted || at.Before(admission.ValidFrom) || !at.Before(admission.ValidUntil):
			deferFor("admission_unavailable", "admission_reassessment")
		default:
			// Reject an amount that cannot fit even an empty account; only
			// temporary competition can be reconsidered without a new decision.
			intrinsic := p.NotionalCents > state.Account.Config.CapitalLimitCents || p.NotionalCents > strategy.LimitCents || p.NotionalCents > symbol.LimitCents
			for _, g := range symbol.Groups {
				intrinsic = intrinsic || p.NotionalCents > groups[g]
			}
			if intrinsic {
				reject("proposal_exceeds_policy")
				break
			}
			if len(state.Commitments)+plannedCount >= MaxCommitments {
				deferFor("commitment_history_capacity", "new_funded_account_policy")
			}
			if p.NotionalCents > out.RemainingCashCents {
				deferFor("cash_unavailable", "cash_or_commitments_changed")
			}
			if p.NotionalCents > state.Account.Config.CapitalLimitCents-committed-out.PlannedReservationCents {
				deferFor("portfolio_limit", "cash_or_commitments_changed")
			}
			if p.NotionalCents > strategy.LimitCents-byStrategy[p.StrategyID] {
				deferFor("strategy_limit", "strategy_exposure_changed")
			}
			if p.NotionalCents > symbol.LimitCents-bySymbol[p.Symbol] {
				deferFor("symbol_concentration", "symbol_exposure_changed")
			}
			for _, g := range symbol.Groups {
				if p.NotionalCents > groups[g]-byGroup[g] {
					deferFor("correlation_group:"+g, "group_exposure_changed")
				}
			}
			if d.Status == "reserve_plan" {
				plannedCount++
				out.RemainingCashCents -= p.NotionalCents
				out.PlannedReservationCents += p.NotionalCents
				add(p.StrategyID, p.Symbol, p.NotionalCents)
				d.Reasons = []string{"within_owner_policy"}
			}
		}
		out.Decisions = append(out.Decisions, d)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
