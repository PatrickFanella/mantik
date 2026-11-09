package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

var ErrPaperEvaluationEvidenceInvalid = errors.New("invalid owned paper evaluation evidence")

const MaxPaperEvaluationEvidenceBytes = 8 << 20

type PaperEvaluationEvidenceRow struct {
	Decision            PaperDecisionRecord  `json:"decision"`
	EvidenceClass       string               `json:"evidence_class"`
	QuoteInputID        string               `json:"quote_input_id"`
	OccurrenceSHA256    string               `json:"occurrence_sha256"`
	Status              string               `json:"status"`
	CanonicalDecisionID string               `json:"canonical_decision_id"`
	Outcomes            []PaperOutcomeRecord `json:"outcomes"`
	LateOutcomes        []PaperOutcomeRecord `json:"late_outcomes"`
}

// This is a necessary count bound, not an independent-sample attestation or
// a passing evaluation. Crossing the threshold never issues an admission.
type PaperEvaluationSampleBound struct {
	Method                       string `json:"method"`
	RequiredIndependentDecisions int    `json:"required_independent_decisions"`
	ObservedOccurrenceUpperBound int    `json:"observed_occurrence_upper_bound"`
	Status                       string `json:"status"`
	ReasonCode                   string `json:"reason_code"`
	IndependenceEstablished      bool   `json:"independence_established"`
	AdmissionAvailable           bool   `json:"admission_available"`
	QualificationCredit          bool   `json:"qualification_credit"`
}

func paperEvaluationSampleBound(required, observed int, deadlineElapsed bool) PaperEvaluationSampleBound {
	r := PaperEvaluationSampleBound{Method: "predeclared-observed-occurrence-upper-bound-v1", RequiredIndependentDecisions: required, ObservedOccurrenceUpperBound: observed, Status: "pending_outcome_deadline", ReasonCode: "predeclared_outcome_window_not_closed"}
	if deadlineElapsed {
		r.Status = "independence_unproven"
		r.ReasonCode = "occurrence_count_does_not_establish_independent_samples"
		if observed < required {
			r.Status = "insufficient_occurrences"
			r.ReasonCode = "observed_occurrence_upper_bound_below_predeclared_minimum"
		}
	}
	return r
}

type PaperEvaluationVariantCount struct {
	SampleBound              PaperEvaluationSampleBound `json:"sample_bound"`
	StrategyID               string                     `json:"strategy_id"`
	SelectedDecisions        int                        `json:"selected_decisions"`
	ObservedOutcomeDecisions int                        `json:"observed_outcome_decisions"`
}
type PaperEvaluationEvidence struct {
	Selection                   *PaperEvaluationWindowSelection `json:"selection,omitempty"`
	Schema                      string                          `json:"schema"`
	ReceiverID                  string                          `json:"receiver_id"`
	Declaration                 PaperEvaluationDeclaration      `json:"declaration"`
	AsOf                        time.Time                       `json:"as_of"`
	Phase                       string                          `json:"phase"`
	SourceSHA256                string                          `json:"source_sha256"`
	Rows                        []PaperEvaluationEvidenceRow    `json:"rows"`
	Variants                    []PaperEvaluationVariantCount   `json:"variants"`
	DistinctProviderOccurrences int                             `json:"distinct_provider_occurrences"`
	Authority                   string                          `json:"authority"`
	IndependentSamplesAvailable bool                            `json:"independent_samples_available"`
	EvaluationAvailable         bool                            `json:"evaluation_available"`
	AdmissionAvailable          bool                            `json:"admission_available"`
	ExecutionAvailable          bool                            `json:"execution_available"`
	QualificationCredit         bool                            `json:"qualification_credit"`
	SHA256                      string                          `json:"sha256"`
}
type PaperEvaluationEvidenceStore interface {
	PaperEvaluationEvidence(context.Context, string, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationEvidence, error)
}

// All catalogues and retained bases have already been replayed against their
// actual owners in one locked or repeatable-read snapshot. Counts describe
// retained research evidence, never statistical independence or economic fills.
func paperEvaluationEvidence(ctx context.Context, id string, declarations PaperEvaluationDeclarationCatalog, decisions PaperDecisionCatalog, outcomes PaperOutcomeCatalog, retained map[string]retainedPaperDecision, at time.Time) (PaperEvaluationEvidence, error) {
	out := PaperEvaluationEvidence{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !declarationTime(at) || declarations.ReceiverID != decisions.ReceiverID || outcomes.ReceiverID != decisions.ReceiverID {
		return out, ErrPaperEvaluationEvidenceInvalid
	}
	var declaration PaperEvaluationDeclaration
	for _, r := range declarations.Records {
		if r.DeclaredAt.After(at) {
			return out, ErrPaperEvaluationEvidenceInvalid
		}
		if r.Command.ID == id {
			declaration = r
		}
	}
	if declaration.SHA256 == "" {
		return out, ErrPaperEvaluationDeclarationNotFound
	}
	if at.Before(declaration.DeclaredAt) {
		return out, ErrPaperEvaluationEvidenceInvalid
	}
	out = PaperEvaluationEvidence{Schema: "mantik-owned-paper-evaluation-evidence-v3", ReceiverID: declarations.ReceiverID, Declaration: clone(declaration), AsOf: at.UTC(), Rows: []PaperEvaluationEvidenceRow{}, Variants: []PaperEvaluationVariantCount{}, Authority: "service_owned_evaluation_evidence_selection_only"}
	out.SourceSHA256 = workerplan.Hash(struct {
		Declarations PaperEvaluationDeclarationCatalog
		Decisions    PaperDecisionCatalog
		Outcomes     PaperOutcomeCatalog
	}{declarations, decisions, outcomes})
	plan := declaration.Command.Plan
	switch {
	case at.Before(plan.WindowStart):
		out.Phase = "awaiting_window"
	case at.Before(plan.WindowEnd):
		out.Phase = "observation_window"
	case at.Before(plan.OutcomeDeadline):
		out.Phase = "awaiting_outcome_deadline"
	default:
		out.Phase = "outcome_deadline_elapsed"
	}
	variants := map[string]PaperEvaluationVariant{}
	for _, v := range plan.Variants {
		variants[v.ID] = v
		out.Variants = append(out.Variants, PaperEvaluationVariantCount{StrategyID: v.ID})
	}
	ordered := clone(decisions.Records)
	for _, r := range outcomes.Records {
		if r.EvaluatedAt.After(at) {
			return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
		}
	}
	slices.SortFunc(ordered, func(a, b PaperDecisionRecord) int {
		if c := a.FinalizedAt.Compare(b.FinalizedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Command.ID, b.Command.ID)
	})
	seen := map[string]string{}
	occurrences := map[string]bool{}
	for _, decision := range ordered {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationEvidence{}, err
		}
		if decision.FinalizedAt.After(at) {
			return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
		}
		v, member := variants[decision.Command.StrategyID]
		if !member || v.SHA256 != decision.Command.StrategySHA256 {
			continue
		}
		basis, ok := retained[decision.Command.ID]
		if !ok || basis.Record.SHA256 != decision.SHA256 || basis.Basis.Feed.Routing == nil {
			return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
		}
		row := PaperEvaluationEvidenceRow{Decision: clone(decision), EvidenceClass: basis.Basis.Strategy.Command.Definition.InputEvidenceClass, QuoteInputID: decision.Evaluation.Coverage.Quote.InputID, Outcomes: []PaperOutcomeRecord{}, LateOutcomes: []PaperOutcomeRecord{}}
		for _, outcome := range outcomes.Records {
			if outcome.EvaluatedAt.After(at) {
				return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
			}
			if outcome.Command.DecisionID == decision.Command.ID {
				// Keep post-deadline attempts inspectable without letting a later
				// report clock expand the predeclared outcome sample. The exact
				// deadline is inclusive; future-as-of attempts still fail above.
				if outcome.EvaluatedAt.After(plan.OutcomeDeadline) {
					row.LateOutcomes = append(row.LateOutcomes, clone(outcome))
				} else {
					row.Outcomes = append(row.Outcomes, clone(outcome))
				}
			}
		}
		quote, present := basis.Basis.Feed.Routing.Inputs[row.QuoteInputID]
		collector, registered := basis.Basis.Feed.Collectors.Entries[decision.Command.RegistrationID]
		switch {
		case decision.FinalizedAt.Before(plan.WindowStart):
			row.Status = "decision_before_window"
		case !decision.FinalizedAt.Before(plan.WindowEnd):
			row.Status = "decision_after_window"
		case decision.Evaluation.Action != "entry_candidate":
			row.Status = "abstention"
		case !present || !registered || quote.Input.Kind != "quote":
			return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
		case quote.Input.OccurredAt.Before(plan.WindowStart):
			row.Status = "quote_before_window"
		case !quote.Input.OccurredAt.Before(plan.WindowEnd):
			row.Status = "quote_after_window"
		default:
			input := quote.Input
			if input.EvidenceClass != row.EvidenceClass || input.PriceCents != decision.Evaluation.PriceCents {
				return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
			}
			available := input.Provenance != nil && !input.ReceivedAt.IsZero() && !input.OccurredAt.IsZero() && !input.Provenance.AvailableAt.IsZero() && !input.ReceivedAt.After(decision.FinalizedAt) && !input.OccurredAt.After(decision.FinalizedAt) && !input.Provenance.AvailableAt.After(decision.FinalizedAt)
			for _, record := range basis.Basis.Feed.Records {
				if record.PublishedAt.IsZero() || record.PublishedAt.After(decision.FinalizedAt) {
					available = false
				}
			}
			switch {
			case !available:
				row.Status = "source_unavailable_at_decision"
			case row.EvidenceClass == "synthetic_unqualified":
				row.Status = "synthetic_not_selected"
			case row.EvidenceClass != "recorded_market_unqualified" && row.EvidenceClass != "prospective_market_unqualified":
				return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
			default:
				// Ignore capture/receipt identity and price corrections at the same
				// occurrence. Different feeds are not attested as independent.
				row.OccurrenceSHA256 = workerplan.Hash(struct {
					Provider, Feed, Symbol string
					OccurredAt             time.Time
				}{collector.Registration.Provider, collector.Registration.Feed, input.Symbol, input.OccurredAt.UTC()})
				key := v.SHA256 + ":" + row.OccurrenceSHA256
				if old := seen[key]; old != "" {
					row.Status = "duplicate_strategy_occurrence"
					row.CanonicalDecisionID = old
				} else {
					row.Status = "selected_research_evidence"
					row.CanonicalDecisionID = decision.Command.ID
					seen[key] = decision.Command.ID
					occurrences[row.OccurrenceSHA256] = true
					for i := range out.Variants {
						if out.Variants[i].StrategyID == v.ID {
							out.Variants[i].SelectedDecisions++
							for _, o := range row.Outcomes {
								if o.Evaluation.Status == "observed" {
									out.Variants[i].ObservedOutcomeDecisions++
									break
								}
							}
						}
					}
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}
	for i := range out.Variants {
		v := &out.Variants[i]
		v.SampleBound = paperEvaluationSampleBound(plan.MinIndependentDecisions, v.ObservedOutcomeDecisions, out.Phase == "outcome_deadline_elapsed")
	}
	out.DistinctProviderOccurrences = len(occurrences)
	if err := ctx.Err(); err != nil {
		return PaperEvaluationEvidence{}, err
	}
	raw, err := json.Marshal(out)
	// The unsigned wire already includes the empty SHA256 field. Reserve the
	// 64 hexadecimal bytes added by the final hash before enforcing the cap.
	if err != nil || len(raw)+64 > MaxPaperEvaluationEvidenceBytes {
		return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
	}
	out.SHA256 = workerplan.Hash(out)
	return clone(out), nil
}

func (m *Memory) PaperEvaluationEvidence(ctx context.Context, id string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationEvidence{}, err
	}
	if now == nil {
		return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
	}
	at := now()
	s, selection, err := m.paperWindowSourcesLocked(ctx, id, at, de, oe)
	if err != nil {
		return PaperEvaluationEvidence{}, err
	}
	return paperWindowEvidence(ctx, id, s, at, selection)
}
func (p *Postgres) PaperEvaluationEvidence(ctx context.Context, id string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationEvidence, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperEvaluationEvidence{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if now == nil {
		return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
	}
	at := now()
	s, selection, err := readPaperWindowSources(ctx, tx, id, at, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperEvaluationEvidence{}, err
	}
	out, err := paperWindowEvidence(ctx, id, s, at, selection)
	if err != nil {
		return PaperEvaluationEvidence{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperEvaluationEvidence{}, err
	}
	return out, nil
}

var _ PaperEvaluationEvidenceStore = (*Memory)(nil)
var _ PaperEvaluationEvidenceStore = (*Postgres)(nil)
