package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxOwnedAllocations = 32
const MaxOwnedAllocationBytes = 8 << 20

var ErrOwnedAllocationInvalid = errors.New("invalid owned allocation comparison")
var ErrOwnedAllocationConflict = errors.New("owned allocation identity, policy or decision conflict")
var ErrOwnedAllocationCapacity = errors.New("owned allocation comparison capacity exhausted")

// A caller can select retained decisions, never an account, balance, proposal,
// policy, admission or effect. Policy assignment is made from owner records.
type OwnedDecisionPin struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type OwnedAllocationCommand struct {
	ID         string             `json:"id"`
	ReceiverID string             `json:"receiver_id"`
	Decisions  []OwnedDecisionPin `json:"decisions"`
}

func (c OwnedAllocationCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || len(c.Decisions) == 0 || len(c.Decisions) > MaxPaperDecisions {
		return ErrOwnedAllocationInvalid
	}
	for i, p := range c.Decisions {
		if !paperaccount.ID.MatchString(p.ID) || !decisionPin.MatchString(p.SHA256) || i > 0 && c.Decisions[i-1].ID >= p.ID {
			return ErrOwnedAllocationInvalid
		}
	}
	return nil
}

type OwnedAllocationRecord struct {
	Schema              string                    `json:"schema"`
	Command             OwnedAllocationCommand    `json:"command"`
	At                  time.Time                 `json:"at"`
	PolicySHA256        string                    `json:"policy_sha256"`
	LedgerSHA256        string                    `json:"ledger_sha256"`
	Entry               allocation.LedgerEntry    `json:"entry"`
	Authority           string                    `json:"authority"`
	ExecutionAvailable  bool                      `json:"execution_available"`
	QualificationCredit bool                      `json:"qualification_credit"`
	AdmissionChecks     []OwnedAdmissionReadiness `json:"admission_checks,omitempty"`
	ExecutionCosts      []OwnedExecutionCost      `json:"execution_costs,omitempty"`
	SHA256              string                    `json:"sha256"`
}
type OwnedAllocationReceipt struct {
	Record    OwnedAllocationRecord `json:"record"`
	Duplicate bool                  `json:"duplicate"`
}
type OwnedAllocationCatalog struct {
	ReceiverID string                  `json:"receiver_id"`
	Records    []OwnedAllocationRecord `json:"records"`
	Ledgers    []allocation.Ledger     `json:"ledgers"`
}
type retainedOwnedAllocation struct {
	Record OwnedAllocationRecord `json:"record"`
	// Capture the policy catalogue used for assignment. Later policies do not
	// rewrite past choices. Every captured policy must still match its owner.
	Policies allocation.PolicyCatalog `json:"policies"`
}
type ownedAllocationJournal struct {
	Records []retainedOwnedAllocation `json:"records"`
}
type OwnedAllocationStore interface {
	CompareOwnedAllocation(context.Context, OwnedAllocationCommand, func() time.Time, PaperDecisionEvaluator) (OwnedAllocationReceipt, error)
	OwnedAllocations(context.Context, PaperDecisionEvaluator) (OwnedAllocationCatalog, error)
}

// Resolve all selected decisions to one unambiguous owner policy. Mixed-account
// and overlapping policy assignments fail closed instead of double reserving.
func ownedComparison(c OwnedAllocationCommand, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, at time.Time) (allocation.PolicyRecord, allocation.Comparison, error) {
	return ownedComparisonFunding(c, policies, decisions, strategies, retained, at, true)
}

func ownedComparisonFunding(c OwnedAllocationCommand, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, at time.Time, protective bool) (allocation.PolicyRecord, allocation.Comparison, error) {
	out := allocation.Comparison{Proposals: []allocation.Proposal{}, Admissions: []allocation.Admission{}}
	if c.Validate() != nil || policies.Validate() != nil || policies.ReceiverID != c.ReceiverID || decisions.ReceiverID != c.ReceiverID || strategies.ReceiverID != c.ReceiverID {
		return allocation.PolicyRecord{}, out, ErrOwnedAllocationInvalid
	}
	var selected allocation.PolicyRecord
	for _, pin := range c.Decisions {
		var decision PaperDecisionRecord
		for _, r := range decisions.Records {
			if r.Command.ID == pin.ID {
				decision = r
				break
			}
		}
		if decision.Validate() != nil || decision.SHA256 != pin.SHA256 || decision.Evaluation.Action != "entry_candidate" || at.Before(decision.FinalizedAt) {
			return selected, out, ErrOwnedAllocationConflict
		}
		strategy, err := paperstrategy.Select(strategies, decision.Command.StrategyID)
		if err != nil || strategy.SHA256 != decision.Command.StrategySHA256 {
			return selected, out, ErrOwnedAllocationConflict
		}
		d := strategy.Command.Definition
		price := decision.Evaluation.PriceCents
		if price <= d.Safety.StopCents || price > d.EntryAtOrBelowCents || d.Quantity > d.Safety.MaxNotionalCents/price {
			return selected, out, ErrOwnedAllocationInvalid
		}
		matches := []allocation.PolicyRecord{}
		for _, policy := range policies.Records {
			for _, rule := range policy.Command.Policy.Strategies {
				if rule.ID == d.ID && rule.ArtifactSHA256 == strategy.DefinitionSHA256 {
					if at.Before(policy.RegisteredAt) {
						return selected, out, ErrOwnedAllocationConflict
					}
					matches = append(matches, policy)
					break
				}
			}
		}
		if len(matches) != 1 || selected.SHA256 != "" && selected.SHA256 != matches[0].SHA256 {
			return selected, out, ErrOwnedAllocationConflict
		}
		selected = matches[0]
		basis, ok := retained[pin.ID]
		if !ok || basis.Record.SHA256 != decision.SHA256 || basis.Basis.Feed.Routing == nil {
			return selected, out, ErrOwnedAllocationConflict
		}
		input, ok := basis.Basis.Feed.Routing.Inputs[decision.Evaluation.Coverage.Quote.InputID]
		if !ok || input.Input.Kind != "quote" || input.Input.PriceCents != price {
			return selected, out, ErrOwnedAllocationConflict
		}
		// Expiry follows the actual price occurrence, not the comparison clock.
		until := input.Input.OccurredAt.Add(time.Duration(min(d.Safety.MaxQuoteAgeSeconds, 60)) * time.Second)
		notional := price * d.Quantity
		if selected.Command.ExecutionModel != nil {
			derive := executionCost
			if protective {
				derive = executionCostWithProtection
			}
			cost, err := derive(selected, decision, strategy, until)
			if err != nil {
				return selected, out, err
			}
			notional = cost.MaxDebitCents
			if cost.ProtectiveFunding != nil {
				notional = cost.ProtectiveFunding.TotalFundingCents
			}
		}
		out.Proposals = append(out.Proposals, allocation.Proposal{ID: pin.ID, DecisionSHA256: pin.SHA256, StrategyID: d.ID, Symbol: d.Symbol, Origin: "research", NotionalCents: notional, ValidUntil: until})
	}
	return selected, out, nil
}

func replayOwnedAllocations(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision) (OwnedAllocationCatalog, error) {
	out := OwnedAllocationCatalog{ReceiverID: owner, Records: []OwnedAllocationRecord{}, Ledgers: []allocation.Ledger{}}
	if !ValidReceiverID(owner) || journal.Records == nil || len(journal.Records) > MaxOwnedAllocations || policies.Validate() != nil {
		return out, ErrOwnedAllocationInvalid
	}
	ledgers := map[string]allocation.Ledger{}
	seen := map[string]bool{}
	var last time.Time
	for _, item := range journal.Records {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		r := item.Record
		if r.Command.ReceiverID != owner || seen[r.Command.ID] || r.At.Before(last) || item.Policies.Validate() != nil {
			return out, ErrOwnedAllocationInvalid
		}
		for _, old := range item.Policies.Records {
			found := false
			for _, current := range policies.Records {
				if reflect.DeepEqual(old, current) {
					found = true
					break
				}
			}
			if !found {
				return out, ErrOwnedAllocationConflict
			}
		}
		policy, comparison, err := ownedComparisonFunding(r.Command, item.Policies, decisions, strategies, retained, r.At, r.Schema == "mantik-owned-allocation-deferral-v4")
		if err != nil {
			return out, err
		}

		ledger, ok := ledgers[policy.Command.Policy.ID]
		if !ok {
			ledger, err = allocation.NewLedger(policy)
			if err != nil {
				return out, err
			}
		}
		next, projection, receipt, err := allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: r.Command.ID, Comparison: &comparison}, func() time.Time { return r.At })
		if err != nil || len(projection.Reservations) != 0 || projection.Snapshot.AvailableCashCents != policy.Account.Config.OpeningCashCents {
			return out, ErrOwnedAllocationInvalid
		}
		expected := OwnedAllocationRecord{Schema: "mantik-owned-allocation-deferral-v1", Command: clone(r.Command), At: r.At.UTC(), PolicySHA256: policy.SHA256, LedgerSHA256: next.SHA256, Entry: next.Entries[receipt.Sequence-1], Authority: "service_owned_allocation_deferral_only"}
		switch r.Schema {
		case "mantik-owned-allocation-deferral-v1":
			// Legacy records retain their exact wire shape and hash.
			if policy.Command.ExecutionModel != nil {
				return out, ErrOwnedAllocationInvalid
			}
		case "mantik-owned-allocation-deferral-v2", "mantik-owned-allocation-deferral-v3", "mantik-owned-allocation-deferral-v4":
			expected.Schema = r.Schema
			expected.AdmissionChecks, err = ownedAdmissionReadiness(comparison, decisions, strategies, retained, r.At)
			if err != nil {
				return out, err
			}
			if r.Schema == "mantik-owned-allocation-deferral-v3" || r.Schema == "mantik-owned-allocation-deferral-v4" {
				expected.ExecutionCosts, err = ownedExecutionCosts(comparison, policy, strategies, retained, r.Schema == "mantik-owned-allocation-deferral-v4")
				if err != nil {
					return out, err
				}
			} else if policy.Command.ExecutionModel != nil {
				return out, ErrOwnedAllocationInvalid
			}
		default:
			return out, ErrOwnedAllocationInvalid
		}
		expected.SHA256 = workerplan.Hash(expected)
		if !reflect.DeepEqual(expected, r) {
			return out, ErrOwnedAllocationInvalid
		}
		seen[r.Command.ID] = true
		last = r.At
		ledgers[policy.Command.Policy.ID] = next
		out.Records = append(out.Records, clone(r))
	}
	for _, l := range ledgers {
		out.Ledgers = append(out.Ledgers, l)
	}
	slices.SortFunc(out.Ledgers, func(a, b allocation.Ledger) int {
		return strings.Compare(a.Policy.Command.Policy.ID, b.Policy.Command.Policy.ID)
	})
	return clone(out), nil
}

func appendOwnedAllocation(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, c OwnedAllocationCommand, now func() time.Time) (ownedAllocationJournal, OwnedAllocationReceipt, error) {
	catalog, err := replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	if c.Validate() != nil {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	if c.ReceiverID != owner {
		return journal, OwnedAllocationReceipt{}, ErrReceiverMismatch
	}
	for _, r := range catalog.Records {
		if r.Command.ID == c.ID {
			if !reflect.DeepEqual(r.Command, c) {
				return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationConflict
			}
			return journal, OwnedAllocationReceipt{clone(r), true}, nil
		}
	}
	if len(journal.Records) >= MaxOwnedAllocations {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationCapacity
	}
	if now == nil {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	at := now().UTC()
	policy, comparison, err := ownedComparison(c, policies, decisions, strategies, retained, at)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}

	ledger, err := allocation.NewLedger(policy)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	for _, l := range catalog.Ledgers {
		if l.Policy.SHA256 == policy.SHA256 {
			ledger = l
			break
		}
	}
	next, _, receipt, err := allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: c.ID, Comparison: &comparison}, func() time.Time { return at })
	if err != nil {
		if errors.Is(err, allocation.ErrLedgerConflict) {
			return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationConflict
		}
		return journal, OwnedAllocationReceipt{}, err
	}
	r := OwnedAllocationRecord{Schema: "mantik-owned-allocation-deferral-v1", Command: clone(c), At: at, PolicySHA256: policy.SHA256, LedgerSHA256: next.SHA256, Entry: next.Entries[receipt.Sequence-1], Authority: "service_owned_allocation_deferral_only"}
	r.Schema = "mantik-owned-allocation-deferral-v2"
	r.AdmissionChecks, err = ownedAdmissionReadiness(comparison, decisions, strategies, retained, at)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	if policy.Command.ExecutionModel != nil {
		r.Schema = "mantik-owned-allocation-deferral-v4"
		r.ExecutionCosts, err = ownedExecutionCosts(comparison, policy, strategies, retained, r.Schema == "mantik-owned-allocation-deferral-v4")
		if err != nil {
			return journal, OwnedAllocationReceipt{}, err
		}
	}
	r.SHA256 = workerplan.Hash(r)
	journal = clone(journal)
	journal.Records = append(journal.Records, retainedOwnedAllocation{r, clone(policies)})
	if _, err = replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained); err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	raw, err := json.Marshal(journal)
	if err != nil || len(raw) > MaxOwnedAllocationBytes {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationCapacity
	}
	return journal, OwnedAllocationReceipt{clone(r), false}, nil
}

func (m *Memory) ownedAllocationSourcesLocked(eval PaperDecisionEvaluator) (allocation.PolicyCatalog, PaperDecisionCatalog, paperstrategy.Catalog, error) {
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return policies, PaperDecisionCatalog{}, paperstrategy.Catalog{}, err
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return policies, PaperDecisionCatalog{}, strategies, err
	}
	decisions, err := paperDecisionCatalog(m.receiverID, m.paperDecisions, strategies, m.decisionFeedLocked(), eval)
	return policies, decisions, strategies, err
}
func (m *Memory) CompareOwnedAllocation(ctx context.Context, c OwnedAllocationCommand, now func() time.Time, eval PaperDecisionEvaluator) (OwnedAllocationReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationReceipt{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(eval)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	next, r, err := appendOwnedAllocation(ctx, m.receiverID, m.ownedAllocations, policies, decisions, strategies, m.paperDecisions, c, now)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return OwnedAllocationReceipt{}, err
	}
	if !r.Duplicate {
		m.ownedAllocations = clone(next)
	}
	return r, nil
}
func (m *Memory) OwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator) (OwnedAllocationCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationCatalog{}, err
	}
	p, d, s, err := m.ownedAllocationSourcesLocked(eval)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	return replayOwnedAllocations(ctx, m.receiverID, m.ownedAllocations, p, d, s, m.paperDecisions)
}

func readOwnedAllocationJournal(ctx context.Context, tx pgx.Tx) (ownedAllocationJournal, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, nil
	}
	if err != nil {
		return ownedAllocationJournal{}, err
	}
	var j ownedAllocationJournal
	if workerplan.ReadBoundedShape(raw, &j, MaxOwnedAllocationBytes) != nil {
		return j, ErrOwnedAllocationInvalid
	}
	return j, nil
}
func (p *Postgres) ownedAllocationAccess(ctx context.Context, write bool, c OwnedAllocationCommand, now func() time.Time, eval PaperDecisionEvaluator) (OwnedAllocationCatalog, OwnedAllocationReceipt, error) {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		options = pgx.TxOptions{}
	}
	tx, err := p.pool.BeginTx(ctx, options)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Same collector -> state -> receiver order as decision finalization.
	feed, err := readFeedEvidence(ctx, tx, write)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	query := "SELECT id FROM mantik_research_receiver WHERE singleton"
	if write {
		query += " FOR UPDATE"
	}
	var owner string
	if err = tx.QueryRow(ctx, query).Scan(&owner); err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	if json.Unmarshal(raw, &state) != nil || owner != feed.ReceiverID {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, owner, state)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	decisions, err := paperDecisionCatalog(owner, retained, strategies, feed, eval)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	var receipt OwnedAllocationReceipt
	if write {
		next, r, e := appendOwnedAllocation(ctx, owner, journal, policies, decisions, strategies, retained, c, now)
		if e != nil {
			return OwnedAllocationCatalog{}, receipt, e
		}
		journal = next
		receipt = r
		if !r.Duplicate {
			raw, e = json.Marshal(journal)
			if e != nil {
				return OwnedAllocationCatalog{}, receipt, e
			}
			_, e = tx.Exec(ctx, "INSERT INTO mantik_owned_allocations(singleton,payload) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET payload=EXCLUDED.payload", raw)
			if e != nil {
				return OwnedAllocationCatalog{}, receipt, e
			}
		}
	}
	catalog, err := replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained)
	if err != nil {
		return catalog, receipt, err
	}
	return catalog, receipt, tx.Commit(ctx)
}
func (p *Postgres) CompareOwnedAllocation(ctx context.Context, c OwnedAllocationCommand, now func() time.Time, eval PaperDecisionEvaluator) (OwnedAllocationReceipt, error) {
	if c.Validate() != nil {
		return OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	_, r, e := p.ownedAllocationAccess(ctx, true, c, now, eval)
	return r, e
}
func (p *Postgres) OwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator) (OwnedAllocationCatalog, error) {
	c, _, e := p.ownedAllocationAccess(ctx, false, OwnedAllocationCommand{}, nil, eval)
	return c, e
}
