package storage

import (
	"context"
	"encoding/json"
	"errors"
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
// This view describes the implemented deferral owner, not future funded effects.
type PaperEconomicAttribution struct {
	Status           string `json:"status"`
	ExecutedQuantity *int64 `json:"executed_quantity"`
	FeesCents        *int64 `json:"fees_cents"`
	RealizedPnLCents *int64 `json:"realized_pnl_cents"`
	BaselineStatus   string `json:"baseline_status"`
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
func paperAttribution(ctx context.Context, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, outcomes PaperOutcomeCatalog, allocations OwnedAllocationCatalog, allReviews map[string]OwnedAdmissionReview) (PaperAttributionCatalog, error) {
	if decisions.ReceiverID != strategies.ReceiverID || outcomes.ReceiverID != decisions.ReceiverID || allocations.ReceiverID != decisions.ReceiverID {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	out := PaperAttributionCatalog{Schema: "mantik-owned-paper-attribution-v2", ReceiverID: decisions.ReceiverID, Rows: []PaperAttributionRow{}, Authority: "service_owned_phase_attribution_only"}
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
		s, err := paperstrategy.Select(strategies, d.Command.StrategyID)
		if err != nil || s.SHA256 != d.Command.StrategySHA256 || d.Command.ReceiverID != out.ReceiverID || seen[d.Command.ID] {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
		seen[d.Command.ID] = true
		row := PaperAttributionRow{Decision: d, Method: s.Command.Definition.Method, Symbol: s.Command.Definition.Symbol, EvidenceClass: s.Command.Definition.InputEvidenceClass, DefinitionSHA256: s.DefinitionSHA256, ForecastStatus: "native_predicate_not_calibrated_forecast", ObservationStatus: "not_assessed", Observations: []PaperOutcomeRecord{}, Allocations: []PaperAllocationAttribution{}, Economics: PaperEconomicAttribution{Status: "owned_execution_unavailable", BaselineStatus: "economic_baselines_unavailable"}}
		for _, o := range outcomes.Records {
			if o.Command.DecisionID != d.Command.ID {
				continue
			}
			if o.Command.DecisionSHA256 != d.SHA256 {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
			row.Observations = append(row.Observations, o)
			row.ObservationStatus = "assessments_retained"
		}
		for _, a := range allocations.Records {
			if a.Entry.Plan == nil {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
			for _, choice := range a.Entry.Plan.Decisions {
				if choice.Proposal.ID != d.Command.ID {
					continue
				}
				if choice.Proposal.DecisionSHA256 != d.SHA256 || choice.Proposal.StrategyID != d.Command.StrategyID {
					return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
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
						return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
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
		scoped := OwnedAdmissionReviewCatalog{ReceiverID: out.ReceiverID, Records: []OwnedAdmissionReview{}, CurrentIDs: []string{}}
		for _, review := range reviews.Records {
			if review.Command.DecisionID != d.Command.ID {
				continue
			}
			if review.Command.DecisionSHA256 != d.SHA256 {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
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
			if !seen[pin.ID] {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
		}
	}
	for _, review := range reviews.Records {
		if !seen[review.Command.DecisionID] {
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

func (m *Memory) PaperAttribution(ctx context.Context, decisionEval PaperDecisionEvaluator, outcomeEval PaperOutcomeEvaluator) (PaperAttributionCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperAttributionCatalog{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	outcomes, err := paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, m.decisionFeedLocked(), decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	assessmentCatalog, err := paperEvaluationAssessmentCatalog(ctx, m.receiverID, m.evaluationAssessments, paperEvaluationSources{corporateSources: m.corporateActionSources, referenceSources: m.corporateReferenceSources, declarations: declarations, decisions: decisions, outcomes: outcomes, retained: m.paperDecisions, sourceClock: m.collectors.LastClock, strategies: strategies, policies: policies, retainedOutcomes: m.paperOutcomes, portfolioEvaluator: m.portfolioEvaluator})
	if err != nil {
		return PaperAttributionCatalog{}, err
	}

	allocations, err := replayOwnedAllocations(ctx, m.receiverID, m.ownedAllocationHistoryLocked(), policies, decisions, strategies, m.paperDecisions, m.ownedExecutionLocked(), declarations, assessmentCatalog)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err := paperAttribution(ctx, decisions, strategies, outcomes, allocations, m.admissionReviews)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	sources, err := m.decisionReviewSocialSourcesLocked(DecisionReviewCommand{}, m.decisionReviews)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	return withClaimReviewAttribution(ctx, out, decisions, m.decisionReviews, sources)
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
	decisions, err := paperDecisionCatalog(feed.ReceiverID, retained, strategies, feed, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	outcomes, err := paperOutcomeCatalog(feed.ReceiverID, all, retained, strategies, feed, decisionEval, outcomeEval)
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
	corporate, err := readCorporateActionSources(ctx, tx, false)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	references, err := readCorporateReferenceSources(ctx, tx, false)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	assessmentCatalog, err := paperEvaluationAssessmentCatalog(ctx, feed.ReceiverID, assessments, paperEvaluationSources{corporateSources: corporate.Records, referenceSources: references.Records, declarations: declarations, decisions: decisions, outcomes: outcomes, retained: retained, sourceClock: feed.Collectors.LastClock, strategies: strategies, policies: policies, retainedOutcomes: all, portfolioEvaluator: p.portfolioEvaluator})
	if err != nil {
		return PaperAttributionCatalog{}, err
	}

	allocations, err := replayOwnedAllocations(ctx, feed.ReceiverID, journal, policies, decisions, strategies, retained, p.ownedExecution(feed), declarations, assessmentCatalog)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allReviews, err := readOwnedAdmissionReviews(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err := paperAttribution(ctx, decisions, strategies, outcomes, allocations, allReviews)
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
	out, err = withClaimReviewAttribution(ctx, out, decisions, claims, sources)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperAttributionCatalog{}, err
	}
	return out, nil
}
