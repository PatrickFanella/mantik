package storage

import (
	"slices"

	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperAttributionBytes = 8 << 20

var ErrPaperAttributionInvalid = errors.New("invalid owned paper attribution")

type PaperAllocationAttribution struct {
	ExecutionSafety       *OwnedExecutionSafety           `json:"execution_safety,omitempty"`
	EvaluationContext     *PaperEvaluationPlanAttribution `json:"evaluation_context,omitempty"`
	ComparisonID          string                          `json:"comparison_id"`
	RecordSHA256          string                          `json:"record_sha256"`
	PolicySHA256          string                          `json:"policy_sha256"`
	AccountID             string                          `json:"account_id"`
	At                    time.Time                       `json:"at"`
	Status                string                          `json:"status"`
	Reasons               []string                        `json:"reasons"`
	ReconsiderOn          []string                        `json:"reconsider_on"`
	RequestedFundingCents int64                           `json:"requested_funding_cents"`
	CostCeiling           *OwnedExecutionCost             `json:"cost_ceiling,omitempty"`
	AdmissionReadiness    *OwnedAdmissionReadiness        `json:"admission_readiness,omitempty"`
}

// This is a projection of retained comparison context, not a fresh plan lookup.
type PaperEvaluationPlanAttribution struct {
	AssessmentSourceSHA256 string                     `json:"assessment_source_sha256,omitempty"`
	Schema                 string                     `json:"schema"`
	SourceSHA256           string                     `json:"source_sha256"`
	Binding                OwnedEvaluationPlanBinding `json:"binding"`
	Authority              string                     `json:"authority"`
	AdmissionAvailable     bool                       `json:"admission_available"`
	ExecutionAvailable     bool                       `json:"execution_available"`
	QualificationCredit    bool                       `json:"qualification_credit"`
}

// Unknown economic values are null, not manufactured zero costs or PnL.
//
// A decision the allocation owner reserved carries its paper execution: every
// retained fill, close and release effect for its proposal, and their sums.
// Status is owned_execution_unavailable without a reservation, then
// reserved_unfilled, released_unfilled, position_open or position_closed.
// Fees are inside each fill's cost and each close's net proceeds, and the
// ledger does not retain share counts, so ExecutedQuantity and FeesCents stay
// null. RealizedPnLCents is net proceeds less the closed cost basis, set once
// something has closed.
type PaperEconomicAttribution struct {
	Status           string `json:"status"`
	ExecutedQuantity *int64 `json:"executed_quantity"`
	FeesCents        *int64 `json:"fees_cents"`
	RealizedPnLCents *int64 `json:"realized_pnl_cents"`
	BaselineStatus   string `json:"baseline_status"`
	// Baseline is null when no passive or no-action figure exists yet.
	Baseline         *PaperBaselineComparison `json:"baseline,omitempty"`
	ReservedCents    *int64                   `json:"reserved_cents,omitempty"`
	FilledCostCents  *int64                   `json:"filled_cost_cents,omitempty"`
	ClosedCostCents  *int64                   `json:"closed_cost_cents,omitempty"`
	NetProceedsCents *int64                   `json:"net_proceeds_cents,omitempty"`
	ReleasedCents    *int64                   `json:"released_cents,omitempty"`
	// Effects is null without a reservation and a list, possibly empty,
	// with one, so a reserved decision with nothing filled keeps it.
	Effects []PaperExecutionEffect `json:"effects"`
}

// PaperBaselineMethod is the comparison paperAttributionBaseline computes.
const PaperBaselineMethod = "owned-forward-quote-hold-vs-cash-v1"

// PaperBaselineComparison sets a decision against two alternatives, in whole
// basis points rounded toward zero. Passive is holding from the assessment's
// entry price to its forward quote. No-action is cash, which earns nothing, so
// the excess over it equals the strategy's own return. The passive window is
// the strategy's forward horizon and can differ from how long the position was
// held.
type PaperBaselineComparison struct {
	Method                string `json:"method"`
	OutcomeID             string `json:"outcome_id,omitempty"`
	EntryPriceCents       *int64 `json:"entry_price_cents,omitempty"`
	ObservedPriceCents    *int64 `json:"observed_price_cents,omitempty"`
	PassiveReturnBPS      *int64 `json:"passive_return_bps,omitempty"`
	StrategyReturnBPS     *int64 `json:"strategy_return_bps,omitempty"`
	ExcessOverPassiveBPS  *int64 `json:"excess_over_passive_bps,omitempty"`
	ExcessOverNoActionBPS *int64 `json:"excess_over_no_action_bps,omitempty"`
}

// PaperAttributionBaseline derives a row's baseline status and comparison from
// its execution result and retained forward observations. The CLI calls it
// too, so a display cannot show figures the row does not support.
func PaperAttributionBaseline(e PaperEconomicAttribution, observations []PaperOutcomeRecord) (string, *PaperBaselineComparison) {
	out := &PaperBaselineComparison{Method: PaperBaselineMethod}
	var observed *PaperOutcomeRecord
	for i := range observations {
		o := &observations[i]
		if o.Evaluation.Status == "observed" && o.Evaluation.EntryPriceCents > 0 && (observed == nil || o.EvaluatedAt.After(observed.EvaluatedAt)) {
			observed = o
		}
	}
	var passive *int64
	if observed != nil {
		entry, price, bps := observed.Evaluation.EntryPriceCents, observed.Evaluation.ObservedPriceCents, observed.Evaluation.PriceDeltaCents*10000/observed.Evaluation.EntryPriceCents
		out.OutcomeID, out.EntryPriceCents, out.ObservedPriceCents, out.PassiveReturnBPS, passive = observed.Command.ID, &entry, &price, &bps, &bps
	}
	switch e.Status {
	case "position_closed":
		if passive == nil {
			return "passive_observation_unavailable", nil
		}
		strategy := *e.RealizedPnLCents * 10000 / *e.ClosedCostCents
		excess, noAction := strategy-*passive, strategy
		out.StrategyReturnBPS, out.ExcessOverPassiveBPS, out.ExcessOverNoActionBPS = &strategy, &excess, &noAction
		return "passive_and_no_action_compared", out
	case "position_open", "reserved_unfilled":
		if passive == nil {
			return "position_not_closed", nil
		}
		return "position_not_closed", out
	default:
		if passive == nil {
			return "no_capital_committed", nil
		}
		return "no_capital_committed", out
	}
}

// PaperExecutionEffect is one retained ledger effect for a decision's
// proposal, with the comparison or execution step that committed it.
type PaperExecutionEffect struct {
	RecordID      string    `json:"record_id"`
	EffectID      string    `json:"effect_id"`
	Kind          string    `json:"kind"`
	At            time.Time `json:"at"`
	AmountCents   int64     `json:"amount_cents"`
	ProceedsCents int64     `json:"proceeds_cents"`
}

// The complete per-decision review chain is a projection of retained non-grant
// owner records. Global sequence numbers may have gaps for other decisions.
type PaperAdmissionReviewHistory struct {
	SourceSHA256        string                 `json:"source_sha256"`
	Records             []OwnedAdmissionReview `json:"records"`
	CurrentID           string                 `json:"current_id"`
	Authority           string                 `json:"authority"`
	AdmissionAvailable  bool                   `json:"admission_available"`
	ExecutionAvailable  bool                   `json:"execution_available"`
	QualificationCredit bool                   `json:"qualification_credit"`
}
type PaperAttributionRow struct {
	ClaimReviewHistory     *PaperClaimReviewHistory     `json:"claim_review_history,omitempty"`
	AdmissionReviewHistory *PaperAdmissionReviewHistory `json:"admission_review_history,omitempty"`
	Decision               PaperDecisionRecord          `json:"decision"`
	Method                 string                       `json:"method"`
	Symbol                 string                       `json:"symbol"`
	EvidenceClass          string                       `json:"evidence_class"`
	DefinitionSHA256       string                       `json:"definition_sha256"`
	ForecastStatus         string                       `json:"forecast_status"`
	ObservationStatus      string                       `json:"observation_status"`
	Observations           []PaperOutcomeRecord         `json:"observations"`
	Allocations            []PaperAllocationAttribution `json:"allocations"`
	Economics              PaperEconomicAttribution     `json:"economics"`
	QualificationCredit    bool                         `json:"qualification_credit"`
	SHA256                 string                       `json:"sha256"`
}
type PaperAttributionCatalog struct {
	Schema              string                `json:"schema"`
	ReceiverID          string                `json:"receiver_id"`
	SourceSHA256        string                `json:"source_sha256"`
	Rows                []PaperAttributionRow `json:"rows"`
	Authority           string                `json:"authority"`
	ExecutionAvailable  bool                  `json:"execution_available"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}
type PaperAttributionStore interface {
	PaperAttribution(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperAttributionCatalog, error)
}

// All source catalogues have been replayed against actual retained owner sources
// in one Memory lock or repeatable-read transaction before this join.
//
// Rows cover the active decisions. dependencies is that catalogue plus the
// approved retired decisions allocation replay resolved from their original
// archives: a comparison or admission review may still name one of those, but
// a reference to any other absent decision remains an orphan.
func paperAttribution(ctx context.Context, decisions, dependencies PaperDecisionCatalog, strategies paperstrategy.Catalog, outcomes PaperOutcomeCatalog, allocations OwnedAllocationCatalog, allReviews map[string]OwnedAdmissionReview) (PaperAttributionCatalog, error) {
	if decisions.ReceiverID != strategies.ReceiverID || outcomes.ReceiverID != decisions.ReceiverID || allocations.ReceiverID != decisions.ReceiverID || dependencies.ReceiverID != decisions.ReceiverID {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	resolved := map[string]string{}
	for _, d := range dependencies.Records {
		resolved[d.Command.ID] = d.SHA256
	}
	out := PaperAttributionCatalog{Schema: "mantik-owned-paper-attribution-v5", ReceiverID: decisions.ReceiverID, Rows: []PaperAttributionRow{}, Authority: "service_owned_phase_attribution_only"}
	reviews, err := ownedAdmissionReviewCatalog(ctx, decisions.ReceiverID, allReviews, allocations)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out.SourceSHA256 = workerplan.Hash(struct {
		Decisions   PaperDecisionCatalog
		Outcomes    PaperOutcomeCatalog
		Allocations OwnedAllocationCatalog
		Reviews     OwnedAdmissionReviewCatalog
	}{decisions, outcomes, allocations, reviews})
	seen := map[string]bool{}
	for _, d := range decisions.Records {
		if err := ctx.Err(); err != nil {
			return PaperAttributionCatalog{}, err
		}
		if seen[d.Command.ID] {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
		seen[d.Command.ID] = true
		row, err := paperAttributionRow(d, out.ReceiverID, strategies, outcomes, allocations, reviews.Records)
		if err != nil {
			return PaperAttributionCatalog{}, err
		}
		out.Rows = append(out.Rows, row)
	}
	// Reject orphaned source rows instead of silently hiding them in a join.
	for _, o := range outcomes.Records {
		if !seen[o.Command.DecisionID] {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
	}
	for _, a := range allocations.Records {
		for _, pin := range a.Command.Decisions {
			if !seen[pin.ID] && resolved[pin.ID] != pin.SHA256 {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
		}
	}
	for _, review := range reviews.Records {
		if !seen[review.Command.DecisionID] && resolved[review.Command.DecisionID] != review.Command.DecisionSHA256 {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > MaxPaperAttributionBytes {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	return clone(out), nil
}

// paperAttributionRow joins one active decision with its outcomes, the
// comparisons and execution steps in allocations and its admission reviews.
// Records naming other decisions are ignored, so the complete catalogue and a
// decision-scoped selection of the same owners produce the same row.
func paperAttributionRow(d PaperDecisionRecord, receiver string, strategies paperstrategy.Catalog, outcomes PaperOutcomeCatalog, allocations OwnedAllocationCatalog, reviews []OwnedAdmissionReview) (PaperAttributionRow, error) {
	s, err := paperstrategy.Select(strategies, d.Command.StrategyID)
	if err != nil || s.SHA256 != d.Command.StrategySHA256 || d.Command.ReceiverID != receiver {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	row := PaperAttributionRow{Decision: d, Method: s.Command.Definition.Method, Symbol: s.Command.Definition.Symbol, EvidenceClass: s.Command.Definition.InputEvidenceClass, DefinitionSHA256: s.DefinitionSHA256, ForecastStatus: "native_predicate_not_calibrated_forecast", ObservationStatus: "not_assessed", Observations: []PaperOutcomeRecord{}, Allocations: []PaperAllocationAttribution{}, Economics: PaperEconomicAttribution{Status: "owned_execution_unavailable", BaselineStatus: "economic_baselines_unavailable"}}
	for _, o := range outcomes.Records {
		if o.Command.DecisionID != d.Command.ID {
			continue
		}
		if o.Command.DecisionSHA256 != d.SHA256 {
			return PaperAttributionRow{}, ErrPaperAttributionInvalid
		}
		row.Observations = append(row.Observations, o)
		row.ObservationStatus = "assessments_retained"
	}
	for _, a := range allocations.Records {
		if a.Entry.Plan == nil {
			return PaperAttributionRow{}, ErrPaperAttributionInvalid
		}
		for _, choice := range a.Entry.Plan.Decisions {
			if choice.Proposal.ID != d.Command.ID {
				continue
			}
			if choice.Proposal.DecisionSHA256 != d.SHA256 || choice.Proposal.StrategyID != d.Command.StrategyID {
				return PaperAttributionRow{}, ErrPaperAttributionInvalid
			}
			item := PaperAllocationAttribution{ComparisonID: a.Command.ID, RecordSHA256: a.SHA256, PolicySHA256: a.PolicySHA256, AccountID: a.Entry.Plan.AccountID, At: a.At, Status: choice.Status, Reasons: choice.Reasons, ReconsiderOn: choice.ReconsiderOn, RequestedFundingCents: choice.Proposal.NotionalCents}
			if a.EvaluationContext != nil {
				for _, binding := range a.EvaluationContext.Decisions {
					if binding.DecisionID == d.Command.ID {
						item.EvaluationContext = &PaperEvaluationPlanAttribution{Schema: "mantik-paper-attribution-evaluation-context-v1", SourceSHA256: a.EvaluationContext.SourceSHA256, Binding: clone(binding), Authority: a.EvaluationContext.Authority, AdmissionAvailable: a.EvaluationContext.AdmissionAvailable, ExecutionAvailable: a.EvaluationContext.ExecutionAvailable, QualificationCredit: a.EvaluationContext.QualificationCredit}
						if a.EvaluationContext.Schema == "mantik-owned-allocation-evaluation-context-v2" {
							item.EvaluationContext.Schema = "mantik-paper-attribution-evaluation-context-v2"
							item.EvaluationContext.AssessmentSourceSHA256 = a.EvaluationContext.AssessmentSourceSHA256
						}
						break
					}
				}
				if item.EvaluationContext == nil {
					return PaperAttributionRow{}, ErrPaperAttributionInvalid
				}
			}
			for _, cost := range a.ExecutionCosts {
				if cost.DecisionID == d.Command.ID {
					v := cost
					item.CostCeiling = &v
					break
				}
			}
			for _, safety := range a.ExecutionSafety {
				if safety.DecisionID == d.Command.ID {
					v := clone(safety)
					item.ExecutionSafety = &v
					break
				}
			}
			for _, check := range a.AdmissionChecks {
				if check.DecisionID == d.Command.ID {
					v := clone(check)
					item.AdmissionReadiness = &v
					break
				}
			}
			row.Allocations = append(row.Allocations, item)
		}
	}
	row.Economics = paperExecutionAttribution(d.Command.ID, row.Allocations, allocations)
	row.Economics.BaselineStatus, row.Economics.Baseline = PaperAttributionBaseline(row.Economics, row.Observations)
	scoped := OwnedAdmissionReviewCatalog{ReceiverID: receiver, Records: []OwnedAdmissionReview{}, CurrentIDs: []string{}}
	for _, review := range reviews {
		if review.Command.DecisionID != d.Command.ID {
			continue
		}
		if review.Command.DecisionSHA256 != d.SHA256 {
			return PaperAttributionRow{}, ErrPaperAttributionInvalid
		}
		scoped.Records = append(scoped.Records, clone(review))
	}
	current := ""
	if len(scoped.Records) > 0 {
		current = scoped.Records[len(scoped.Records)-1].Command.ID
		scoped.CurrentIDs = []string{current}
	}
	row.AdmissionReviewHistory = &PaperAdmissionReviewHistory{SourceSHA256: workerplan.Hash(scoped), Records: scoped.Records, CurrentID: current, Authority: "service_owned_non_grant_admission_review_history_only"}
	row.SHA256 = workerplan.Hash(row)
	return row, nil
}

func (m *Memory) PaperAttribution(ctx context.Context, decisionEval PaperDecisionEvaluator, outcomeEval PaperOutcomeEvaluator) (PaperAttributionCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperAttributionCatalog{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(ctx, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	// Active owner intervals, not the feed checkpoint's closed count, decide
	// completeness: approved retirement removes rows that checkpoint counted.
	outcomes, err := m.activePaperOutcomesLocked(ctx, strategies, m.decisionFeedLocked(), decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	assessmentCatalog, err := m.paperAssessmentOwnersLocked(ctx, decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}

	history := m.ownedAllocationHistoryLocked()
	dependencies, owners, err := m.allocationOwnerDecisionsLocked(ctx, decisions, history, nil, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allocations, err := replayOwnedAllocations(ctx, m.receiverID, history, policies, dependencies, strategies, owners, m.ownedExecutionLocked(), declarations, assessmentCatalog)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err := paperAttribution(ctx, decisions, dependencies, strategies, outcomes, allocations, m.admissionReviews)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	sources, err := m.decisionReviewSocialSourcesLocked(DecisionReviewCommand{}, m.decisionReviews)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	// Claim reviews can name an approved retired decision; resolve it from
	// its first original archive, as the review catalogue does.
	reviewed, err := m.reviewOwnerDecisionsLocked(ctx, decisions, DecisionReviewCommand{}, m.decisionReviews, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	return withClaimReviewAttribution(ctx, out, reviewed, m.decisionReviews, sources)
}
func (p *Postgres) PaperAttribution(ctx context.Context, decisionEval PaperDecisionEvaluator, outcomeEval PaperOutcomeEvaluator) (PaperAttributionCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	// Active owner intervals, not the feed checkpoint's closed count, decide
	// completeness: approved retirement removes rows that checkpoint counted.
	decisions, err := readActivePaperDecisions(ctx, tx, feed.ReceiverID, retained, strategies, feed, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	outcomes, err := readActivePaperOutcomes(ctx, tx, feed.ReceiverID, all, retained, strategies, feed, decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return PaperAttributionCatalog{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, feed.ReceiverID, state)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	journal, err := readOwnedAllocationHistory(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	plans, err := readPaperEvaluationDeclarations(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(feed.ReceiverID, plans, strategies, policies)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	assessmentCatalog, err := readPaperAssessmentOwners(ctx, tx, feed.ReceiverID, assessments, decisionEval, outcomeEval, p.portfolioEvaluator)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}

	dependencies, owners, err := readAllocationOwnerDecisions(ctx, tx, decisions, retained, journal, nil, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allocations, err := replayOwnedAllocations(ctx, feed.ReceiverID, journal, policies, dependencies, strategies, owners, p.ownedExecution(feed), declarations, assessmentCatalog)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allReviews, err := readOwnedAdmissionReviews(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err := paperAttribution(ctx, decisions, dependencies, strategies, outcomes, allocations, allReviews)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	claims, err := readDecisionReviews(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	var sources []socialwatch.Journal
	if decisionReviewNeedsSources(DecisionReviewCommand{}, claims) {
		j, err := readSocialJournal(ctx, tx, feed.ReceiverID)
		if err != nil {
			return PaperAttributionCatalog{}, err
		}
		sources = []socialwatch.Journal{j}
	}
	reviewed, err := readReviewOwnerDecisions(ctx, tx, decisions, DecisionReviewCommand{}, claims, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err = withClaimReviewAttribution(ctx, out, reviewed, claims, sources)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperAttributionCatalog{}, err
	}
	return out, nil
}

// paperExecutionAttribution collects the decision's effects from comparison
// records and execution steps, archived ones included, so it survives
// checkpoints that seal settled reservations out of the ledgers.
func paperExecutionAttribution(decisionID string, choices []PaperAllocationAttribution, allocations OwnedAllocationCatalog) PaperEconomicAttribution {
	var reserved int64
	for _, c := range choices {
		if c.Status == "reserve_plan" {
			reserved += c.RequestedFundingCents
		}
	}
	if reserved == 0 {
		return PaperEconomicAttribution{Status: "owned_execution_unavailable"}
	}
	effects := []PaperExecutionEffect{}
	collect := func(record string, entries []allocation.LedgerEntry) {
		for _, e := range entries {
			if f := e.Command.Effect; f != nil && f.DecisionID == decisionID {
				effects = append(effects, PaperExecutionEffect{RecordID: record, EffectID: f.ID, Kind: f.Kind, At: e.At, AmountCents: f.AmountCents, ProceedsCents: f.ProceedsCents})
			}
		}
	}
	for _, r := range allocations.Records {
		for _, x := range r.Executions {
			collect(r.Command.ID, x.Entries)
		}
		collect(r.Command.ID, r.Expirations)
	}
	for _, step := range allocations.Executions {
		for _, x := range step.Executions {
			collect(step.Command.ID, x.Entries)
		}
	}
	return paperEconomicsFromEffects(reserved, effects)
}

// paperEconomicsFromEffects is the one accounting rule for a decision's
// reserved funding and retained effects. Paper attribution and the bounded
// allocation contribution total both use it.
func paperEconomicsFromEffects(reserved int64, effects []PaperExecutionEffect) PaperEconomicAttribution {
	out := PaperEconomicAttribution{Status: "owned_execution_unavailable"}
	if reserved == 0 {
		return out
	}
	slices.SortStableFunc(effects, func(a, b PaperExecutionEffect) int { return a.At.Compare(b.At) })
	var filled, closed, proceeds, released int64
	for _, e := range effects {
		switch e.Kind {
		case "fill":
			filled += e.AmountCents
		case "close":
			closed += e.AmountCents
			proceeds += e.ProceedsCents
		case "release":
			released += e.AmountCents
		}
	}
	out.ReservedCents, out.FilledCostCents, out.ClosedCostCents, out.NetProceedsCents, out.ReleasedCents, out.Effects = &reserved, &filled, &closed, &proceeds, &released, effects
	switch {
	case filled == 0 && released == 0:
		out.Status = "reserved_unfilled"
	case filled == 0:
		out.Status = "released_unfilled"
	case closed < filled:
		out.Status = "position_open"
	default:
		out.Status = "position_closed"
	}
	if closed > 0 {
		realized := proceeds - closed
		out.RealizedPnLCents = &realized
	}
	return out
}
