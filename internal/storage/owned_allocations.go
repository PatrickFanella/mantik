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

// Execution steps share this journal so effects commit with the ledger.
// Every append replays the whole journal, about 1 ms per retained record
// per replay on Kvant, so the bound stays well inside the 5 s request
// deadline. Unbounded operation needs archival checkpoints.
const MaxOwnedAllocations = 256

// maxOwnedAllocations is MaxOwnedAllocations; capacity tests lower it.
var maxOwnedAllocations = MaxOwnedAllocations

const MaxOwnedAllocationBytes = 64 << 20

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
	ExecutionSafety []OwnedExecutionSafety `json:"execution_safety,omitempty"`
	// Comparison v15 only: owner-issued releases of expired reservations,
	// applied to the policy ledger immediately before Entry.
	Expirations []allocation.LedgerEntry `json:"expirations,omitempty"`
	// Comparison v16 only: owner execution effects from open entry books up
	// to At, per policy, applied before Expirations and Entry.
	Executions          []OwnedExecutionAdvance           `json:"executions,omitempty"`
	EvaluationContext   *OwnedAllocationEvaluationContext `json:"evaluation_context,omitempty"`
	Schema              string                            `json:"schema"`
	Command             OwnedAllocationCommand            `json:"command"`
	At                  time.Time                         `json:"at"`
	PolicySHA256        string                            `json:"policy_sha256"`
	LedgerSHA256        string                            `json:"ledger_sha256"`
	Entry               allocation.LedgerEntry            `json:"entry"`
	Authority           string                            `json:"authority"`
	ExecutionAvailable  bool                              `json:"execution_available"`
	QualificationCredit bool                              `json:"qualification_credit"`
	AdmissionChecks     []OwnedAdmissionReadiness         `json:"admission_checks,omitempty"`
	ExecutionCosts      []OwnedExecutionCost              `json:"execution_costs,omitempty"`
	SHA256              string                            `json:"sha256"`
}
type OwnedAllocationReceipt struct {
	Record    OwnedAllocationRecord `json:"record"`
	Duplicate bool                  `json:"duplicate"`
}
type OwnedAllocationCatalog struct {
	Capital    []OwnedCapitalReadback  `json:"capital"`
	ReceiverID string                  `json:"receiver_id"`
	Records    []OwnedAllocationRecord `json:"records"`
	Executions []OwnedExecutionRecord  `json:"executions,omitempty"`
	Ledgers    []allocation.Ledger     `json:"ledgers"`
}
type retainedOwnedAllocation struct {
	Record OwnedAllocationRecord `json:"record"`
	// Capture the policy catalogue used for assignment. Later policies do not
	// rewrite past choices. Every captured policy must still match its owner.
	Policies               allocation.PolicyCatalog           `json:"policies"`
	EvaluationDeclarations *PaperEvaluationDeclarationCatalog `json:"evaluation_declarations,omitempty"`
	// An execution step is retained here with a zero Record.
	Execution *OwnedExecutionRecord `json:"execution,omitempty"`
}
type ownedAllocationJournal struct {
	// Checkpoint seals every record before Records; see checkpoints.go.
	Checkpoint *OwnedAllocationCheckpoint `json:"checkpoint,omitempty"`
	Records    []retainedOwnedAllocation  `json:"records"`
	// archived is the sealed history, loaded only by readers that list it.
	archived []retainedOwnedAllocation
	// lookup finds a sealed record by command identity for exact retries.
	lookup func(string) (*retainedOwnedAllocation, error)
	// sealing holds records an append sealed; the store archives them.
	sealing []retainedOwnedAllocation
	// unbounded lifts the active record bound for a full-history audit.
	unbounded bool
}
type OwnedAllocationStore interface {
	CompareOwnedAllocation(context.Context, OwnedAllocationCommand, func() time.Time, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationReceipt, error)
	OwnedAllocations(context.Context, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationCatalog, error)
}

// Resolve all selected decisions to one unambiguous owner policy. Mixed-account
// and overlapping policy assignments fail closed instead of double reserving.
func ownedComparison(c OwnedAllocationCommand, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, at time.Time) (allocation.PolicyRecord, allocation.Comparison, error) {
	return ownedComparisonFunding(c, policies, decisions, strategies, retained, at, true, true)
}

func ownedComparisonFunding(c OwnedAllocationCommand, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, at time.Time, protective, modelFreshness bool) (allocation.PolicyRecord, allocation.Comparison, error) {
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
		if modelFreshness && selected.Command.ExecutionModel != nil {
			if selected.Command.ExecutionModel.Validate() != nil {
				return selected, out, ErrOwnedAllocationInvalid
			}
			modelUntil := input.Input.OccurredAt.Add(time.Duration(selected.Command.ExecutionModel.MaxQuoteAgeSeconds) * time.Second)
			if modelUntil.Before(until) {
				until = modelUntil
			}
		}
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

func replayOwnedAllocations(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, declarationSource PaperEvaluationDeclarationCatalog, assessmentSources ...PaperEvaluationAssessmentCatalog) (OwnedAllocationCatalog, error) {
	out, _, err := replayOwnedJournal(ctx, owner, journal, policies, decisions, strategies, retained, x, declarationSource, assessmentSources...)
	return out, err
}

// ownedJournalState is what an append continues from after replay.
type ownedJournalState struct {
	books    []ownedEntryBook
	last     time.Time
	aliases  *proposalAliases
	granted  []string
	executed map[string]bool
	// sealedBooks names proposals that books in sealed records owned.
	sealedBooks map[string]bool
}

// bookProposals lists every proposal an entry book owns, sealed or active.
func (s ownedJournalState) bookProposals() map[string]bool {
	out := executedProposals(s.books)
	for id := range s.sealedBooks {
		out[id] = true
	}
	return out
}

// ownedReplayFailure keeps a context error retryable. Any other failure to
// replay a retained ledger means the retained records are invalid.
func ownedReplayFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrOwnedAllocationInvalid
}

func replayOwnedJournal(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, declarationSource PaperEvaluationDeclarationCatalog, assessmentSources ...PaperEvaluationAssessmentCatalog) (outCatalog OwnedAllocationCatalog, outState ownedJournalState, outErr error) {
	out := OwnedAllocationCatalog{ReceiverID: owner, Records: []OwnedAllocationRecord{}, Ledgers: []allocation.Ledger{}}
	state := ownedJournalState{}
	assessments, err := allocationAssessmentSource(owner, assessmentSources)
	if err != nil {
		return OwnedAllocationCatalog{}, state, err
	}
	if !ValidReceiverID(owner) || journal.Records == nil || !journal.unbounded && len(journal.Records) > maxOwnedAllocations || policies.Validate() != nil {
		return out, state, ErrOwnedAllocationInvalid
	}
	resolved := make([]allocation.PolicyRecord, len(journal.Records))
	seen := map[string]bool{}
	aliases := newProposalAliases(retained)
	var checked *allocation.PolicyCatalog
	var last time.Time
	sealed, err := openOwnedCheckpoint(ctx, owner, journal, policies, &out, seen, aliases)
	if err != nil {
		return OwnedAllocationCatalog{}, state, err
	}
	if journal.Checkpoint != nil {
		last, state.last = journal.Checkpoint.At, journal.Checkpoint.At
		state.sealedBooks = map[string]bool{}
		for _, id := range journal.Checkpoint.BookProposals {
			state.sealedBooks[id] = true
		}
	}
	for index, item := range journal.Records {
		if err := ctx.Err(); err != nil {
			return out, state, err
		}
		if item.Execution != nil {
			e := item.Execution
			if x == nil {
				return out, state, ErrOwnedExecutionUnavailable
			}
			if !reflect.DeepEqual(item.Record, OwnedAllocationRecord{}) || !reflect.DeepEqual(item.Policies, allocation.PolicyCatalog{}) || item.EvaluationDeclarations != nil || e.Command.ReceiverID != owner || seen[e.Command.ID] || e.At.Before(last) {
				return out, state, ErrOwnedAllocationInvalid
			}
			seen[e.Command.ID] = true
			last = e.At
			continue
		}
		r := item.Record
		if r.Command.ReceiverID != owner || seen[r.Command.ID] || r.At.Before(last) {
			return out, state, ErrOwnedAllocationInvalid
		}
		// Consecutive records almost always capture the same catalogue, so
		// validate and match each distinct capture once.
		checkedPolicies := checked != nil && reflect.DeepEqual(item.Policies, *checked)
		if !checkedPolicies && item.Policies.Validate() != nil {
			return out, state, ErrOwnedAllocationInvalid
		}
		// v5 was introduced after source aliases were rejected at append. A
		// portable rehash must not bypass that same invariant during replay.
		// Earlier schemas retain their historical representation and retries.
		if r.Schema == "mantik-owned-allocation-deferral-v5" || r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema))))))))) {
			if err := aliases.check(r.Command); err != nil {
				return OwnedAllocationCatalog{}, state, err
			}
		}
		aliases.add(r.Command)
		for _, old := range item.Policies.Records {
			if checkedPolicies {
				break
			}
			found := false
			for _, current := range policies.Records {
				if reflect.DeepEqual(old, current) {
					found = true
					break
				}
			}
			if !found {
				return out, state, ErrOwnedAllocationConflict
			}
		}
		checked = &item.Policies
		policy, comparison, err := ownedComparisonFunding(r.Command, item.Policies, decisions, strategies, retained, r.At, r.Schema == "mantik-owned-allocation-deferral-v4" || (r.Schema == "mantik-owned-allocation-deferral-v5" || r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))))))))), (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema))))
		if err != nil {
			return out, state, err
		}
		// The grant schema rederives its admissions from the captured
		// declarations and the owner-replayed assessments at its own clock.
		if isOwnedGrantSchema(r.Schema) {
			if item.EvaluationDeclarations == nil {
				return out, state, ErrOwnedAllocationInvalid
			}
			comparison.Admissions, err = ownedEvaluationAdmissions(comparison.Proposals, policy, strategies, *item.EvaluationDeclarations, assessments, r.At)
			if err != nil {
				return out, state, err
			}
		}

		if !reflect.DeepEqual(r.Entry.At, r.At.UTC()) || !reflect.DeepEqual(r.Entry.Command, allocation.LedgerCommand{ID: r.Command.ID, Comparison: &comparison}) {
			return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
		}
		resolved[index] = policy
		seen[r.Command.ID] = true
		last = r.At
	}
	ledgers := map[string]allocation.Ledger{}
	for _, l := range sealed {
		ledgers[l.Policy.Command.Policy.ID] = l
	}
	for index, item := range journal.Records {
		advances := item.Record.Executions
		if item.Execution != nil {
			advances = item.Execution.Executions
		}
		for _, a := range advances {
			ledger, ok := ledgers[a.PolicyID]
			if !ok {
				return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
			}
			ledger.Entries = append(ledger.Entries, a.Entries...)
			ledgers[a.PolicyID] = ledger
		}
		if item.Execution != nil {
			continue
		}
		policy := resolved[index]
		key := policy.Command.Policy.ID
		ledger, ok := ledgers[key]
		if !ok {
			var err error
			ledger, err = newOwnedLedger(policy, item.Record.Schema)
			if err != nil {
				return OwnedAllocationCatalog{}, state, err
			}
		}
		ledger.Entries = append(ledger.Entries, item.Record.Expirations...)
		ledger.Entries = append(ledger.Entries, item.Record.Entry)
		ledgers[key] = ledger
	}
	prefixes := map[string][]allocation.LedgerPrefix{}
	for key, ledger := range ledgers {
		ledger.SHA256 = ""
		ledger.SHA256 = workerplan.Hash(ledger)
		var err error
		prefixes[key], err = allocation.ReplayLedgerPrefixes(ctx, ledger)
		if err != nil {
			return OwnedAllocationCatalog{}, state, ownedReplayFailure(err)
		}
		ledgers[key] = ledger
	}
	// Source ownership above and accounting here are both checked in full
	// before any receipt escapes. Re-evaluating each already-validated ledger
	// prefix through AppendLedger would repeat all prior transitions.
	indices := map[string]int{}
	var currentDeclarations *PaperEvaluationDeclarationCatalog
	for index, item := range journal.Records {
		if err := ctx.Err(); err != nil {
			return OwnedAllocationCatalog{}, state, err
		}
		advances, at := item.Record.Executions, item.Record.At
		if item.Execution != nil {
			advances, at = item.Execution.Executions, item.Execution.At
		}
		// Every record advances each open entry book to its own clock first.
		// Rederive those effects from the ledgers as they stood before it.
		if len(state.books) > 0 || len(advances) > 0 {
			if x == nil {
				return OwnedAllocationCatalog{}, state, ErrOwnedExecutionUnavailable
			}
			before := map[string]allocation.Ledger{}
			for _, b := range state.books {
				if l, ok := ledgers[b.policyID]; ok {
					before[b.policyID] = ledgerPrefix(l, indices[b.policyID])
				}
			}
			expected, _, err := advanceAll(ctx, before, state.books, *x, at)
			if err != nil {
				return OwnedAllocationCatalog{}, state, err
			}
			if len(expected) != len(advances) || len(advances) > 0 && !reflect.DeepEqual(expected, advances) {
				return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
			}
		}
		for _, a := range advances {
			indices[a.PolicyID] += len(a.Entries)
		}
		if item.Execution != nil {
			e := *item.Execution
			want := OwnedExecutionRecord{Schema: OwnedExecutionStepSchema, Command: e.Command, At: e.At.UTC(), Executions: advances, Authority: ownedExecutionAuthority}
			want.SHA256 = workerplan.Hash(want)
			if len(advances) == 0 || !paperaccount.ID.MatchString(e.Command.ID) || !reflect.DeepEqual(want, e) {
				return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
			}
			out.Executions = append(out.Executions, clone(e))
			state.last = e.At
			continue
		}
		r := item.Record
		// An older writer cannot leave open books unadvanced.
		if len(state.books) > 0 && r.Schema != OwnedExecutionSchema {
			return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
		}
		policy := resolved[index]
		comparison := *r.Entry.Command.Comparison
		key := policy.Command.Policy.ID
		start := indices[key]
		indices[key] += len(r.Expirations)
		if indices[key] >= len(prefixes[key]) {
			return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
		}
		prefix := prefixes[key][indices[key]]
		indices[key]++
		if isOwnedGrantSchema(r.Schema) {
			// Releases must be exactly those due from the state before this
			// record, at its clock; the ledger walk already applied them.
			before := allocation.LedgerProjection{}
			if start > 0 {
				before = prefixes[key][start-1].Projection
			} else if l := ledgers[key]; l.Base != nil {
				// The first active entry follows the sealed base.
				if before, err = allocation.ReplayLedger(ctx, ledgerPrefix(l, 0)); err != nil {
					return OwnedAllocationCatalog{}, state, ownedReplayFailure(err)
				}
			}
			due := ownedReservationExpirations(before, r.At, state.bookProposals())
			if len(due) != len(r.Expirations) {
				return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
			}
			for i, e := range r.Expirations {
				if e.Command.ID != due[i].ID || e.Command.Comparison != nil || e.Command.Effect == nil || !reflect.DeepEqual(*e.Command.Effect, due[i]) || !e.At.Equal(r.At) {
					return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
				}
			}
		} else if len(r.Expirations) != 0 || len(prefix.Projection.Reservations) != 0 || prefix.Projection.Snapshot.AvailableCashCents != policy.Account.Config.OpeningCashCents {
			return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
		}
		var err error

		expected := OwnedAllocationRecord{Schema: "mantik-owned-allocation-deferral-v1", Command: clone(r.Command), At: r.At.UTC(), PolicySHA256: policy.SHA256, LedgerSHA256: prefix.LedgerSHA256, Entry: r.Entry, Authority: "service_owned_allocation_deferral_only"}
		switch r.Schema {
		case "mantik-owned-allocation-deferral-v1":
			// Legacy records retain their exact wire shape and hash.
			if policy.Command.ExecutionModel != nil {
				return out, state, ErrOwnedAllocationInvalid
			}
		case "mantik-owned-allocation-deferral-v2", "mantik-owned-allocation-deferral-v3", "mantik-owned-allocation-deferral-v4", "mantik-owned-allocation-deferral-v5", "mantik-owned-allocation-deferral-v6", "mantik-owned-allocation-deferral-v7", "mantik-owned-allocation-deferral-v8", "mantik-owned-allocation-deferral-v9", "mantik-owned-allocation-deferral-v10", "mantik-owned-allocation-deferral-v11", "mantik-owned-allocation-deferral-v12", "mantik-owned-allocation-deferral-v13", "mantik-owned-allocation-deferral-v14", "mantik-owned-allocation-deferral-v15", OwnedExecutionSchema:
			expected.Schema = r.Schema
			if isOwnedGrantSchema(r.Schema) {
				expected.Authority, expected.Expirations = ownedAllocationGrantAuthority, r.Expirations
			}
			if r.Schema == OwnedExecutionSchema {
				expected.Executions = r.Executions
			}
			expected.AdmissionChecks, err = ownedAdmissionReadiness(comparison, decisions, strategies, retained, r.At)
			if err != nil {
				return out, state, err
			}
			if r.Schema == "mantik-owned-allocation-deferral-v3" || r.Schema == "mantik-owned-allocation-deferral-v4" || (r.Schema == "mantik-owned-allocation-deferral-v5" || r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))))))))) && policy.Command.ExecutionModel != nil {
				expected.ExecutionCosts, err = ownedExecutionCosts(comparison, policy, strategies, retained, r.Schema == "mantik-owned-allocation-deferral-v4" || (r.Schema == "mantik-owned-allocation-deferral-v5" || r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))))))))))
				if err != nil {
					return out, state, err
				}
			} else if policy.Command.ExecutionModel != nil {
				return out, state, ErrOwnedAllocationInvalid
			}
		default:
			return out, state, ErrOwnedAllocationInvalid
		}
		if r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))) {
			expected.ExecutionSafety, err = ownedExecutionSafety(expected, policy, strategies, retained)
			if err != nil {
				return out, state, err
			}
		}
		if r.Schema == "mantik-owned-allocation-deferral-v5" || r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema))))))))) {
			expected.AdmissionChecks, err = bindOwnedAdmissionReadiness(expected, policy)
			if err != nil {
				return out, state, err
			}
		}
		if r.Schema == "mantik-owned-allocation-deferral-v6" || (r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema))))))))) {
			if currentDeclarations == nil {
				d, e := ownedEvaluationDeclarationSource(owner, policies, strategies, declarationSource)
				if e != nil {
					return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
				}
				currentDeclarations = &d
			}
			current := *currentDeclarations
			if item.EvaluationDeclarations == nil {
				return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
			}
			captured, e := validateOwnedEvaluationCapture(owner, *item.EvaluationDeclarations, current, policies, strategies, r.At)
			if e != nil {
				return OwnedAllocationCatalog{}, state, e
			}
			if r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))))))) {
				available := PaperEvaluationDeclarationCatalog{ReceiverID: owner, Records: []PaperEvaluationDeclaration{}}
				for _, d := range current.Records {
					if !d.DeclaredAt.After(r.At) {
						available.Records = append(available.Records, d)
					}
				}
				if !reflect.DeepEqual(captured, available) {
					return out, state, ErrOwnedAllocationConflict
				}
			}
			expected.EvaluationContext = ownedAllocationEvaluationContext(expected, policy, captured)
			if r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema))))))) {
				expected.EvaluationContext = ownedAllocationAssessmentContext(expected, policy, captured, assessments)
			}
		} else if item.EvaluationDeclarations != nil || r.EvaluationContext != nil {
			return OwnedAllocationCatalog{}, state, ErrOwnedAllocationInvalid
		}
		if r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || (r.Schema == "mantik-owned-allocation-deferral-v14" || (r.Schema == "mantik-owned-allocation-deferral-v15" || r.Schema == OwnedExecutionSchema)))))) {
			expected.AdmissionChecks, err = bindOwnedAssessmentReadiness(expected, policy, assessments)
			if err != nil {
				return OwnedAllocationCatalog{}, state, err
			}
		}
		expected.SHA256 = workerplan.Hash(expected)
		if !reflect.DeepEqual(expected, r) {
			return out, state, ErrOwnedAllocationInvalid
		}
		out.Records = append(out.Records, clone(r))
		if r.Schema == OwnedExecutionSchema {
			books, err := ownedEntryBooks(r, policy, strategies, retained)
			if err != nil {
				return OwnedAllocationCatalog{}, state, err
			}
			state.books = append(state.books, books...)
		}
		state.last = r.At
	}
	for _, l := range ledgers {
		out.Ledgers = append(out.Ledgers, l)
	}
	slices.SortFunc(out.Ledgers, func(a, b allocation.Ledger) int {
		return strings.Compare(a.Policy.Command.Policy.ID, b.Policy.Command.Policy.ID)
	})
	granted := []string{}
	executed := map[string]bool{}
	if journal.Checkpoint != nil {
		granted = append(granted, journal.Checkpoint.Granted...)
		for _, id := range journal.Checkpoint.Executed {
			executed[id] = true
		}
	}
	for index, item := range journal.Records {
		if isOwnedGrantSchema(item.Record.Schema) && !slices.Contains(granted, resolved[index].Command.Policy.ID) {
			granted = append(granted, resolved[index].Command.Policy.ID)
		}
	}
	for index, item := range journal.Records {
		if item.Execution == nil && item.Record.Schema == OwnedExecutionSchema {
			executed[resolved[index].Command.Policy.ID] = true
		}
	}
	slices.Sort(granted)
	state.aliases, state.granted, state.executed = aliases, granted, executed
	out.Capital, err = ownedExecutedCapitalReadbacks(ctx, owner, policies, out.Ledgers, executed, granted...)
	if err != nil {
		return OwnedAllocationCatalog{}, state, err
	}
	return clone(out), state, nil
}

// newOwnedLedger starts a policy ledger at its first record. A ledger that
// starts with a v16 comparison is a cost ledger, so a paper exit whose fees
// exceed its sale proceeds can close against available cash. A ledger begun
// by an earlier comparison keeps its schema and hashes, even after v16
// records join it.
func newOwnedLedger(policy allocation.PolicyRecord, schema string) (allocation.Ledger, error) {
	if schema == OwnedExecutionSchema {
		return allocation.NewCostLedger(policy)
	}
	return allocation.NewLedger(policy)
}

// ledgerPrefix is l with only its first n entries, rehashed.
func ledgerPrefix(l allocation.Ledger, n int) allocation.Ledger {
	l.Entries = slices.Clone(l.Entries[:n])
	l.SHA256 = ""
	l.SHA256 = workerplan.Hash(l)
	return l
}

func appendOwnedAllocation(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, c OwnedAllocationCommand, now func() time.Time, sourceClock time.Time, declarationSource PaperEvaluationDeclarationCatalog, assessmentSources ...PaperEvaluationAssessmentCatalog) (outJournal ownedAllocationJournal, outReceipt OwnedAllocationReceipt, outErr error) {
	// A context that ends while an append runs makes it fail with the context
	// error: a timeout stays retryable, and nothing computed after
	// cancellation commits.
	defer func() {
		if err := ctx.Err(); err != nil {
			outReceipt, outErr = OwnedAllocationReceipt{}, err
		}
	}()
	catalog, state, err := replayOwnedJournal(ctx, owner, journal, policies, decisions, strategies, retained, x, declarationSource, assessmentSources...)
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
	if item, err := archivedRetry(journal, c.ID); err != nil {
		return journal, OwnedAllocationReceipt{}, err
	} else if item != nil {
		if item.Execution != nil || !reflect.DeepEqual(item.Record.Command, c) {
			return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationConflict
		}
		return journal, OwnedAllocationReceipt{clone(item.Record), true}, nil
	}
	if next, ok, err := sealOwnedJournal(ctx, journal, catalog, state); err != nil {
		return journal, OwnedAllocationReceipt{}, err
	} else if ok {
		journal = next
		if catalog, state, err = replayOwnedJournal(ctx, owner, journal, policies, decisions, strategies, retained, x, declarationSource, assessmentSources...); err != nil {
			return journal, OwnedAllocationReceipt{}, err
		}
	}
	if len(journal.Records) >= maxOwnedAllocations {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationCapacity
	}
	// The replayed index holds every earlier proposal, sealed ones included.
	if err := state.aliases.check(c); err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	if now == nil {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	at := now().UTC()
	assessments, err := allocationAssessmentSource(owner, assessmentSources)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	for _, assessment := range assessments.Records {
		if assessment.AssessedAt.After(at) {
			return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
		}
	}
	// New readiness must not predate source state already observed by this
	// owner transaction. Exact retries return above without sampling a clock;
	// historical replay keeps each retained record's original comparison time.
	if at.Before(sourceClock) {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	policy, comparison, err := ownedComparison(c, policies, decisions, strategies, retained, at)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	declarations, err := ownedEvaluationDeclarationSource(owner, policies, strategies, declarationSource)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	for _, d := range declarations.Records {
		if d.DeclaredAt.After(at) {
			return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
		}
	}
	comparison.Admissions, err = ownedEvaluationAdmissions(comparison.Proposals, policy, strategies, declarations, assessments, at)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}

	// Advance every open entry book to this clock before anything else.
	ledgers := map[string]allocation.Ledger{}
	for _, l := range catalog.Ledgers {
		ledgers[l.Policy.Command.Policy.ID] = l
	}
	var executions []OwnedExecutionAdvance
	if x != nil {
		executions, _, err = advanceAll(ctx, ledgers, state.books, *x, at)
		if err != nil {
			return journal, OwnedAllocationReceipt{}, err
		}
	} else if len(state.books) > 0 {
		return journal, OwnedAllocationReceipt{}, ErrOwnedExecutionUnavailable
	}
	schema := OwnedAllocationGrantSchema
	if x != nil {
		schema = OwnedExecutionSchema
	}
	ledger, err := newOwnedLedger(policy, schema)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	for _, l := range ledgers {
		if l.Policy.SHA256 == policy.SHA256 {
			ledger = l
			break
		}
	}
	clock := func() time.Time { return at }
	conflict := func(err error) error {
		if errors.Is(err, allocation.ErrLedgerConflict) {
			return ErrOwnedAllocationConflict
		}
		return err
	}
	projection, err := allocation.ReplayLedger(ctx, ledger)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	var expirations []allocation.LedgerEntry
	for _, e := range ownedReservationExpirations(projection, at, state.bookProposals()) {
		var receipt allocation.LedgerReceipt
		ledger, _, receipt, err = allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: e.ID, Effect: &e}, clock)
		if err != nil {
			return journal, OwnedAllocationReceipt{}, conflict(err)
		}
		expirations = append(expirations, ledger.Entries[receipt.Sequence-1])
	}
	next, _, receipt, err := allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: c.ID, Comparison: &comparison}, clock)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, conflict(err)
	}
	r := OwnedAllocationRecord{Schema: OwnedAllocationGrantSchema, Command: clone(c), At: at, PolicySHA256: policy.SHA256, LedgerSHA256: next.SHA256, Entry: next.Entries[receipt.Sequence-1], Expirations: expirations, Authority: ownedAllocationGrantAuthority}
	if x != nil {
		r.Schema, r.Executions = OwnedExecutionSchema, executions
	}
	r.AdmissionChecks, err = ownedAdmissionReadiness(comparison, decisions, strategies, retained, at)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	if policy.Command.ExecutionModel != nil {
		r.ExecutionCosts, err = ownedExecutionCosts(comparison, policy, strategies, retained, true)
		if err != nil {
			return journal, OwnedAllocationReceipt{}, err
		}
	}
	r.ExecutionSafety, err = ownedExecutionSafety(r, policy, strategies, retained)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	r.AdmissionChecks, err = bindOwnedAdmissionReadiness(r, policy)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	r.EvaluationContext = ownedAllocationAssessmentContext(r, policy, declarations, assessments)
	r.AdmissionChecks, err = bindOwnedAssessmentReadiness(r, policy, assessments)
	if err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	r.SHA256 = workerplan.Hash(r)
	sealing := journal.sealing
	journal = clone(journal)
	journal.sealing = sealing
	journal.Records = append(journal.Records, retainedOwnedAllocation{Record: r, Policies: clone(policies), EvaluationDeclarations: &declarations})
	if _, err = replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained, x, declarationSource, assessmentSources...); err != nil {
		return journal, OwnedAllocationReceipt{}, err
	}
	raw, err := json.Marshal(journal)
	if err != nil || len(raw) > MaxOwnedAllocationBytes {
		return journal, OwnedAllocationReceipt{}, ErrOwnedAllocationCapacity
	}
	return journal, OwnedAllocationReceipt{clone(r), false}, nil
}

// proposalAliases indexes earlier proposals by economic source, so replay
// checks each record against all earlier ones without rescanning them.
// Earlier pins resolve lazily, at the first later check, as they always did.
type proposalAliases struct {
	retained map[string]retainedPaperDecision
	pending  []OwnedDecisionPin
	previous map[string]map[string]bool
}

func newProposalAliases(retained map[string]retainedPaperDecision) *proposalAliases {
	return &proposalAliases{retained: retained, previous: map[string]map[string]bool{}}
}

func (a *proposalAliases) add(c OwnedAllocationCommand) {
	a.pending = append(a.pending, c.Decisions...)
}

// source binds a proposal to its actual retained strategy and provider market
// update. A request, decision or capture ID is not an independent economic
// signal, so capture IDs, receiver times, registration aliases and raw-record
// pins are ignored. The same finalized decision may be reconsidered; another
// decision cannot select the same source.
func (a *proposalAliases) source(pin OwnedDecisionPin) (string, error) {
	v, ok := a.retained[pin.ID]
	if !ok || v.Record.SHA256 != pin.SHA256 || v.Basis.Feed.Routing == nil {
		return "", ErrOwnedAllocationConflict
	}
	quote, ok := v.Basis.Feed.Routing.Inputs[v.Record.Evaluation.Coverage.Quote.InputID]
	collector, registered := v.Basis.Feed.Collectors.Entries[v.Record.Command.RegistrationID]
	if !ok || !registered || quote.Input.Kind != "quote" || quote.Input.PriceCents != v.Record.Evaluation.PriceCents {
		return "", ErrOwnedAllocationConflict
	}
	return workerplan.Hash(struct {
		StrategySHA256 string
		Provider       string
		Feed           string
		Symbol         string
		OccurredAt     time.Time
	}{v.Basis.Strategy.SHA256, collector.Registration.Provider, collector.Registration.Feed, quote.Input.Symbol, quote.Input.OccurredAt.UTC()}), nil
}

// check refuses a command that selects an alias of any earlier proposal or
// two aliases of one source. Historical bytes and retries stay intact.
func (a *proposalAliases) check(c OwnedAllocationCommand) error {
	for len(a.pending) > 0 {
		pin := a.pending[0]
		key, err := a.source(pin)
		if err != nil {
			return err
		}
		if a.previous[key] == nil {
			a.previous[key] = map[string]bool{}
		}
		a.previous[key][pin.ID] = true
		a.pending = a.pending[1:]
	}
	selected := map[string]string{}
	for _, pin := range c.Decisions {
		key, err := a.source(pin)
		if err != nil {
			return err
		}
		if old := selected[key]; old != "" && old != pin.ID {
			return ErrOwnedAllocationConflict
		}
		for old := range a.previous[key] {
			if old != pin.ID {
				return ErrOwnedAllocationConflict
			}
		}
		selected[key] = pin.ID
	}
	return nil
}

func (m *Memory) ownedAllocationSourcesLocked(ctx context.Context, eval PaperDecisionEvaluator) (allocation.PolicyCatalog, PaperDecisionCatalog, paperstrategy.Catalog, error) {
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return policies, PaperDecisionCatalog{}, paperstrategy.Catalog{}, err
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return policies, PaperDecisionCatalog{}, strategies, err
	}
	decisions, err := m.activePaperDecisionsLocked(ctx, strategies, m.decisionFeedLocked(), eval)
	return policies, decisions, strategies, err
}
func (m *Memory) CompareOwnedAllocation(ctx context.Context, c OwnedAllocationCommand, now func() time.Time, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationReceipt, error) {
	if c.Validate() != nil {
		return OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationReceipt{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(ctx, eval)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	assessments, err := m.allocationAssessmentOwnersLocked(ctx, eval, oe)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}

	journal := m.ownedAllocationsForAppendLocked()
	decisions, retained, err := m.allocationOwnerDecisionsLocked(ctx, decisions, journal, c.Decisions, eval)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	next, r, err := appendOwnedAllocation(ctx, m.receiverID, journal, policies, decisions, strategies, retained, m.ownedExecutionLocked(), c, now, m.collectors.LastClock, declarations, assessments)
	if err != nil {
		return OwnedAllocationReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return OwnedAllocationReceipt{}, err
	}
	if !r.Duplicate {
		if err := m.commitOwnedAllocationsLocked(next); err != nil {
			return OwnedAllocationReceipt{}, err
		}
	}
	return r, nil
}
func (m *Memory) OwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationCatalog{}, err
	}
	p, d, s, err := m.ownedAllocationSourcesLocked(ctx, eval)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, s, p)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	assessments, err := m.allocationAssessmentOwnersLocked(ctx, eval, oe)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}

	journal := m.ownedAllocationHistoryLocked()
	d, retained, err := m.allocationOwnerDecisionsLocked(ctx, d, journal, nil, eval)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	catalog, err := replayOwnedAllocations(ctx, m.receiverID, journal, p, d, s, retained, m.ownedExecutionLocked(), declarations, assessments)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	return catalog, nil
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

// ownedJournalAppend computes a journal append inside the owner transaction.
// It reports whether the journal changed and must be written.
type ownedJournalAppend func(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, sourceClock time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error)

func (p *Postgres) ownedAllocationAccess(ctx context.Context, write bool, pins []OwnedDecisionPin, apply ownedJournalAppend, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationCatalog, OwnedAllocationReceipt, error) {
	return p.ownedAllocationAccessFromJournal(ctx, write, pins, apply, nil, eval, outcomeEvals...)
}

type ownedAllocationJournalRead func(context.Context, pgx.Tx, ownedAllocationJournal) (ownedAllocationJournal, error)

func (p *Postgres) ownedAllocationAccessFromJournal(ctx context.Context, write bool, pins []OwnedDecisionPin, apply ownedJournalAppend, selectJournal ownedAllocationJournalRead, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationCatalog, OwnedAllocationReceipt, error) {
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
	decisions, err := readActivePaperDecisions(ctx, tx, owner, retained, strategies, feed, eval)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	if selectJournal != nil {
		if write {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
		}
		journal, err = selectJournal(ctx, tx, journal)
		if err != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
		}
	} else if write {
		journal.lookup = postgresArchiveLookup(ctx, tx)
	} else if journal.Checkpoint != nil {
		if journal.archived, err = readOwnedAllocationArchive(ctx, tx); err != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
		}
	}
	plans, err := readPaperEvaluationDeclarations(ctx, tx)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(owner, plans, strategies, policies)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	assessmentCatalog := PaperEvaluationAssessmentCatalog{ReceiverID: owner, Records: []PaperEvaluationAssessment{}}
	if len(assessments) > 0 {
		if oe == nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, ErrPaperEvaluationAssessmentInvalid
		}
		outcomes, e := readPaperOutcomes(ctx, tx)
		if e != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
		}
		if _, e = readActivePaperOutcomes(ctx, tx, owner, outcomes, retained, strategies, feed, eval, oe); e != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
		}
		assessmentCatalog, e = readPaperAssessmentOwners(ctx, tx, owner, assessments, eval, oe, p.portfolioEvaluator)
		if e != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
		}
	}
	decisions, retained, err = readAllocationOwnerDecisions(ctx, tx, decisions, retained, journal, pins, eval)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	var receipt OwnedAllocationReceipt
	if apply != nil {
		previousCheckpoint := journal.Checkpoint
		next, changed, e := apply(ctx, owner, journal, policies, decisions, strategies, retained, p.ownedExecution(feed), feed.Collectors.LastClock, declarations, assessmentCatalog)
		if e != nil {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
		}
		if changed && !write {
			return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
		}
		journal = next
		if changed {
			if e = writeOwnedAllocationArchive(ctx, tx, journal); e != nil {
				return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
			}
			if e = writeOwnedAllocationIndex(ctx, tx, journal, previousCheckpoint); e != nil {
				return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
			}
			raw, e = json.Marshal(journal)
			if e != nil {
				return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
			}
			_, e = tx.Exec(ctx, "INSERT INTO mantik_owned_allocations(singleton,payload) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET payload=EXCLUDED.payload", raw)
			if e != nil {
				return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, e
			}
		}
	}
	catalog, err := replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained, p.ownedExecution(feed), declarations, assessmentCatalog)
	if err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	// A constructed projection or receipt is not a committed acknowledgment.
	// Even a successful SQL write can fail at COMMIT (for example a deferred
	// constraint). Transport errors can leave the outcome uncertain: read the
	// owner journal before retrying the same command identity.
	if err = tx.Commit(ctx); err != nil {
		return OwnedAllocationCatalog{}, OwnedAllocationReceipt{}, err
	}
	return catalog, receipt, nil
}
func (p *Postgres) CompareOwnedAllocation(ctx context.Context, c OwnedAllocationCommand, now func() time.Time, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationReceipt, error) {
	if c.Validate() != nil {
		return OwnedAllocationReceipt{}, ErrOwnedAllocationInvalid
	}
	var r OwnedAllocationReceipt
	_, _, e := p.ownedAllocationAccess(ctx, true, c.Decisions, func(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, sourceClock time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		next, receipt, err := appendOwnedAllocation(ctx, owner, journal, policies, decisions, strategies, retained, x, c, now, sourceClock, declarations, assessments)
		r = receipt
		return next, err == nil && !receipt.Duplicate, err
	}, eval, outcomeEvals...)
	if e != nil {
		return OwnedAllocationReceipt{}, e
	}
	return r, nil
}
func (p *Postgres) OwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationCatalog, error) {
	c, _, e := p.ownedAllocationAccess(ctx, false, nil, nil, eval, outcomeEvals...)
	return c, e
}
