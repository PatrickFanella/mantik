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

const MaxPaperEvaluationAssessments = 16
const MaxPaperEvaluationAssessmentBytes = MaxPaperEvaluationEvidenceBytes + 65536
const MaxPaperEvaluationAssessmentCatalogBytes = 16 << 20

var ErrPaperEvaluationAssessmentInvalid = errors.New("invalid owned paper evaluation assessment")
var ErrPaperEvaluationAssessmentConflict = errors.New("owned paper evaluation assessment identity or source conflict")
var ErrPaperEvaluationAssessmentCapacity = errors.New("owned paper evaluation assessment capacity exhausted")
var ErrPaperEvaluationAssessmentNotFound = errors.New("owned paper evaluation assessment not found")

// Callers select an actual predeclaration; they cannot supply evidence or results.
type PaperEvaluationAssessmentCommand struct {
	ID                string `json:"id"`
	ReceiverID        string `json:"receiver_id"`
	DeclarationID     string `json:"declaration_id"`
	DeclarationSHA256 string `json:"declaration_sha256"`
}

func (c PaperEvaluationAssessmentCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.DeclarationID) || !decisionPin.MatchString(c.DeclarationSHA256) {
		return ErrPaperEvaluationAssessmentInvalid
	}
	return nil
}

type PaperEvaluationCheck struct {
	StrategyID string `json:"strategy_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
}
type PaperEvaluationAssessment struct {
	CorporateCoverage   *PaperEvaluationCorporateCoverage   `json:"corporate_coverage,omitempty"`
	SearchHistory       *PaperEvaluationSearchHistory       `json:"search_history,omitempty"`
	SampleDesign        *PaperEvaluationSampleDesign        `json:"sample_design,omitempty"`
	Holdout             *PaperEvaluationHoldout             `json:"holdout,omitempty"`
	Portfolio           *PaperEvaluationPortfolio           `json:"portfolio,omitempty"`
	CorporateSourceCut  *PaperEvaluationCorporateSourceCut  `json:"corporate_source_cut,omitempty"`
	ForecastCalibration *PaperEvaluationForecastCalibration `json:"forecast_calibration,omitempty"`
	WarmUp              *PaperEvaluationWarmUp              `json:"warm_up,omitempty"`
	Schema              string                              `json:"schema"`
	Command             PaperEvaluationAssessmentCommand    `json:"command"`
	AssessedAt          time.Time                           `json:"assessed_at"`
	Recipe              string                              `json:"recipe"`
	Evidence            PaperEvaluationEvidence             `json:"evidence"`
	Checks              []PaperEvaluationCheck              `json:"checks"`
	Status              string                              `json:"status"`
	Authority           string                              `json:"authority"`
	EvaluationComplete  bool                                `json:"evaluation_complete"`
	AdmissionAvailable  bool                                `json:"admission_available"`
	ExecutionAvailable  bool                                `json:"execution_available"`
	QualificationCredit bool                                `json:"qualification_credit"`
	SHA256              string                              `json:"sha256"`
}
type PaperEvaluationAssessmentReceipt struct {
	Record    PaperEvaluationAssessment `json:"record"`
	Duplicate bool                      `json:"duplicate"`
}
type PaperEvaluationAssessmentCatalog struct {
	ReceiverID string                      `json:"receiver_id"`
	Records    []PaperEvaluationAssessment `json:"records"`
}
type PaperEvaluationAssessmentStore interface {
	AssessPaperEvaluation(context.Context, PaperEvaluationAssessmentCommand, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationAssessmentReceipt, error)
	PaperEvaluationAssessments(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error)
}

// This version can prove a necessary sample failure, never a sufficient pass.
// Other mandatory checks stay explicitly unavailable rather than being invented.
func newPaperEvaluationAssessment(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence) (PaperEvaluationAssessment, error) {
	if c.Validate() != nil || e.ReceiverID != c.ReceiverID || e.Declaration.Command.ID != c.DeclarationID || e.Declaration.SHA256 != c.DeclarationSHA256 || !declarationTime(e.AsOf) || e.Schema != "mantik-owned-paper-evaluation-evidence-v3" {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	unsigned := clone(e)
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != e.SHA256 || e.EvaluationAvailable || e.AdmissionAvailable || e.ExecutionAvailable || e.QualificationCredit || e.IndependentSamplesAvailable {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	r := PaperEvaluationAssessment{Schema: "mantik-owned-paper-evaluation-assessment-v1", Command: c, AssessedAt: e.AsOf, Recipe: "native-predeclared-necessary-checks-v1", Evidence: clone(e), Checks: []PaperEvaluationCheck{}, Status: "incomplete", Authority: "service_owned_incomplete_evaluation_assessment_only"}
	if e.Phase != "outcome_deadline_elapsed" {
		r.Status = "pending"
	}
	for _, v := range e.Variants {
		for _, name := range e.Declaration.PlannedChecks {
			check := PaperEvaluationCheck{StrategyID: v.StrategyID, Name: name, Status: "unavailable", ReasonCode: "applicable_recipe_evidence_and_method_unavailable"}
			if name == "sample_sufficiency" {
				check.ReasonCode = v.SampleBound.ReasonCode
				switch v.SampleBound.Status {
				case "pending_outcome_deadline":
					check.Status = "pending"
				case "insufficient_occurrences":
					check.Status = "failed"
					r.Status = "blocked"
				case "independence_unproven":
				default:
					return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
				}
			}
			r.Checks = append(r.Checks, check)
		}
	}
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
	}
	return r, nil
}

// A complete, already owner-replayed source snapshot. Filtering by issuance time
// permits later source records without changing old assessments. Source writers
// serialize their strictly later timestamps against every issued assessment.
type paperEvaluationSources struct {
	policies           allocation.PolicyCatalog
	retainedOutcomes   map[string]retainedPaperOutcome
	portfolioEvaluator PaperPortfolioEvaluator
	corporateSources   []CorporateActionSourceRecord
	referenceSources   []CorporateReferenceSourceRecord
	declarations       PaperEvaluationDeclarationCatalog
	decisions          PaperDecisionCatalog
	outcomes           PaperOutcomeCatalog
	retained           map[string]retainedPaperDecision
	sourceClock        time.Time
	strategies         paperstrategy.Catalog
}

func (s paperEvaluationSources) evidence(ctx context.Context, id string, at time.Time) (PaperEvaluationEvidence, error) {
	d := clone(s.declarations)
	d.Records = slices.DeleteFunc(d.Records, func(r PaperEvaluationDeclaration) bool { return r.DeclaredAt.After(at) })
	decisions := clone(s.decisions)
	decisions.Records = slices.DeleteFunc(decisions.Records, func(r PaperDecisionRecord) bool { return r.FinalizedAt.After(at) })
	outcomes := clone(s.outcomes)
	outcomes.Records = slices.DeleteFunc(outcomes.Records, func(r PaperOutcomeRecord) bool { return r.EvaluatedAt.After(at) })
	return paperEvaluationEvidence(ctx, id, d, decisions, outcomes, s.retained, at)
}
func (m *Memory) paperEvaluationSourcesLocked(de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperEvaluationSources, error) {
	corporate, err := corporateActionSourceCatalog(context.Background(), m.receiverID, m.corporateActionSources)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	references, err := corporateReferenceSourceCatalog(context.Background(), m.receiverID, m.corporateReferenceSources)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(de)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	outcomes, err := paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, m.decisionFeedLocked(), de, oe)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	return paperEvaluationSources{corporateSources: corporate.Records, referenceSources: references.Records, declarations: declarations, decisions: decisions, outcomes: outcomes, retained: m.paperDecisions, sourceClock: m.collectors.LastClock, strategies: strategies, policies: policies, retainedOutcomes: m.paperOutcomes, portfolioEvaluator: m.portfolioEvaluator}, nil
}
func readPaperEvaluationSources(ctx context.Context, tx pgx.Tx, write bool, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolioEval PaperPortfolioEvaluator) (paperEvaluationSources, error) {
	feed, err := readFeedEvidence(ctx, tx, write)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	if write {
		var owner string
		if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
			return paperEvaluationSources{}, err
		}
		if owner != feed.ReceiverID {
			return paperEvaluationSources{}, ErrReceiverMismatch
		}
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	decisions, err := paperDecisionCatalog(feed.ReceiverID, retained, strategies, feed, de)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	outcomes, err := paperOutcomeCatalog(feed.ReceiverID, all, retained, strategies, feed, de, oe)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return paperEvaluationSources{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return paperEvaluationSources{}, ErrPaperEvaluationAssessmentInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, feed.ReceiverID, state)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	plans, err := readPaperEvaluationDeclarations(ctx, tx)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(feed.ReceiverID, plans, strategies, policies)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	corporate, err := readCorporateActionSources(ctx, tx, false)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	references, err := readCorporateReferenceSources(ctx, tx, false)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	return paperEvaluationSources{corporateSources: corporate.Records, referenceSources: references.Records, declarations: declarations, decisions: decisions, outcomes: outcomes, retained: retained, sourceClock: feed.Collectors.LastClock, strategies: strategies, policies: policies, retainedOutcomes: all, portfolioEvaluator: portfolioEval}, nil
}
func paperEvaluationAssessmentCatalog(ctx context.Context, owner string, all map[string]PaperEvaluationAssessment, s paperEvaluationSources) (PaperEvaluationAssessmentCatalog, error) {
	out := PaperEvaluationAssessmentCatalog{ReceiverID: owner, Records: []PaperEvaluationAssessment{}}
	if !ValidReceiverID(owner) || owner != s.declarations.ReceiverID || len(all) > MaxPaperEvaluationAssessments {
		return out, ErrPaperEvaluationAssessmentInvalid
	}
	if _, err := corporateActionSourceCatalog(ctx, owner, s.corporateSources); err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	bytes := 0
	for id, r := range all {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationAssessmentCatalog{}, err
		}
		e, err := s.evidence(ctx, r.Command.DeclarationID, r.AssessedAt)
		if err != nil {
			return PaperEvaluationAssessmentCatalog{}, err
		}
		hasHoldout := slices.Contains([]string{"mantik-owned-paper-evaluation-assessment-v7", "mantik-owned-paper-evaluation-assessment-v8", "mantik-owned-paper-evaluation-assessment-v9", "mantik-owned-paper-evaluation-assessment-v10", "mantik-owned-paper-evaluation-assessment-v11", "mantik-owned-paper-evaluation-assessment-v12"}, r.Schema)
		hasPortfolio := r.Schema == "mantik-owned-paper-evaluation-assessment-v6" || hasHoldout
		hasCorporate := r.Schema == "mantik-owned-paper-evaluation-assessment-v5" || hasPortfolio
		var warmup *PaperEvaluationWarmUp
		if slices.Contains([]string{"mantik-owned-paper-evaluation-assessment-v2", "mantik-owned-paper-evaluation-assessment-v3", "mantik-owned-paper-evaluation-assessment-v4"}, r.Schema) || hasCorporate {
			value, e := ownedNativeWarmUp(e.Declaration, s.strategies)
			if e != nil {
				return PaperEvaluationAssessmentCatalog{}, e
			}
			warmup = &value
		}
		var cut *PaperEvaluationCorporateSourceCut
		if hasCorporate {
			value, err := ownedCorporateEvaluationCut(ctx, e, s.corporateSources)
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
			cut = &value
		}
		var portfolio *PaperEvaluationPortfolio
		if hasPortfolio {
			portfolio, err = ownedPortfolioEvaluation(ctx, e, s)
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
		}
		var holdout *PaperEvaluationHoldout
		if hasHoldout {
			holdout, err = ownedEvaluationHoldout(ctx, e, s.strategies, s.retained)
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
		}
		var search *PaperEvaluationSearchHistory
		if r.Schema == "mantik-owned-paper-evaluation-assessment-v11" || r.Schema == "mantik-owned-paper-evaluation-assessment-v12" {
			value, err := ownedSearchHistory(e, s.strategies, r.AssessedAt)
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
			search = &value
		}
		var coverage *PaperEvaluationCorporateCoverage
		if r.Schema == "mantik-owned-paper-evaluation-assessment-v12" {
			value, err := ownedCorporateCoverage(ctx, e, *cut, s)
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
			coverage = &value
		}
		want, err := paperAssessmentWithBenchmarks(r.Command, e, warmup, cut, portfolio, holdout, search, coverage, r.Schema)
		if err != nil || id != r.Command.ID || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) {
			return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentInvalid
		}
		raw, _ := json.Marshal(r)
		bytes += len(raw)
		if bytes > MaxPaperEvaluationAssessmentCatalogBytes {
			return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentCapacity
		}
		out.Records = append(out.Records, clone(r))
	}
	slices.SortFunc(out.Records, func(a, b PaperEvaluationAssessment) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	return out, nil
}

// Validate envelopes before using their clock floor, even on source-writer paths.
func paperEvaluationSourceClock(owner string, all map[string]PaperEvaluationAssessment, at time.Time) error {
	if !ValidReceiverID(owner) || !declarationTime(at) || len(all) > MaxPaperEvaluationAssessments {
		return ErrPaperEvaluationAssessmentInvalid
	}
	for id, r := range all {
		want, err := paperAssessmentWithBenchmarks(r.Command, r.Evidence, r.WarmUp, r.CorporateSourceCut, r.Portfolio, r.Holdout, r.SearchHistory, r.CorporateCoverage, r.Schema)
		if err != nil || id != r.Command.ID || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) {
			return ErrPaperEvaluationAssessmentInvalid
		}
		if !at.After(r.AssessedAt) {
			return ErrPaperEvaluationAssessmentConflict
		}
	}
	return nil
}
func appendPaperEvaluationAssessment(ctx context.Context, owner string, all map[string]PaperEvaluationAssessment, s paperEvaluationSources, c PaperEvaluationAssessmentCommand, now func() time.Time, allocations ownedAllocationJournal) (PaperEvaluationAssessmentReceipt, error) {
	catalog, err := paperEvaluationAssessmentCatalog(ctx, owner, all, s)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	if c.Validate() != nil {
		return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
	}
	if c.ReceiverID != owner {
		return PaperEvaluationAssessmentReceipt{}, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if old.Command != c {
			return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentConflict
		}
		return PaperEvaluationAssessmentReceipt{clone(old), true}, nil
	}
	if len(all) >= MaxPaperEvaluationAssessments {
		return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentCapacity
	}
	if now == nil {
		return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
	}
	at := now()
	// v8 comparisons retain the complete assessment cut. New issuance must
	// stay strictly outside every retained cut; exact retries return above.
	for _, item := range allocations.Records {
		r := item.Record
		if r.Schema != "mantik-owned-allocation-deferral-v8" && r.Schema != "mantik-owned-allocation-deferral-v9" && r.Schema != "mantik-owned-allocation-deferral-v10" && r.Schema != "mantik-owned-allocation-deferral-v11" && r.Schema != "mantik-owned-allocation-deferral-v12" && r.Schema != "mantik-owned-allocation-deferral-v13" && r.Schema != "mantik-owned-allocation-deferral-v14" {
			continue
		}
		unsigned := clone(r)
		unsigned.SHA256 = ""
		if r.Command.ReceiverID != owner || !declarationTime(r.At) || workerplan.Hash(unsigned) != r.SHA256 || !at.After(r.At) {
			return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
		}
	}
	if !declarationTime(at) || at.Before(s.sourceClock) {
		return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
	}
	for _, r := range catalog.Records {
		if at.Before(r.AssessedAt) {
			return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
		}
	}
	for _, source := range s.corporateSources {
		if source.RetainedAt.After(at) {
			return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
		}
	}
	for _, source := range s.referenceSources {
		if source.RetainedAt.After(at) {
			return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
		}
	}
	// New issuance must cover every currently retained source; only historical
	// replay filters later writes. A future source cannot be silently omitted.
	e, err := paperEvaluationEvidence(ctx, c.DeclarationID, s.declarations, s.decisions, s.outcomes, s.retained, at)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	warmup, err := ownedNativeWarmUp(e.Declaration, s.strategies)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	cut, err := ownedCorporateEvaluationCut(ctx, e, s.corporateSources)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	schema := "mantik-owned-paper-evaluation-assessment-v5"
	var portfolio *PaperEvaluationPortfolio
	if s.portfolioEvaluator != nil {
		schema = "mantik-owned-paper-evaluation-assessment-v6"
		portfolio, err = ownedPortfolioEvaluation(ctx, e, s)
		if err != nil {
			return PaperEvaluationAssessmentReceipt{}, err
		}
	}
	var holdout *PaperEvaluationHoldout
	if e.Declaration.Command.Plan.Holdout != nil {
		schema = "mantik-owned-paper-evaluation-assessment-v7"
		holdout, err = ownedEvaluationHoldout(ctx, e, s.strategies, s.retained)
		if err != nil {
			return PaperEvaluationAssessmentReceipt{}, err
		}
	}
	if e.Declaration.Command.Plan.BenchmarkMethod != "" {
		schema = "mantik-owned-paper-evaluation-assessment-v8"
	}
	if e.Declaration.Command.Plan.FeatureCausalityMethod != "" {
		schema = "mantik-owned-paper-evaluation-assessment-v9"
	}
	if e.Declaration.Command.Plan.SampleDesign != nil {
		schema = "mantik-owned-paper-evaluation-assessment-v10"
	}
	var search *PaperEvaluationSearchHistory
	if e.Declaration.Command.Plan.SearchMultiplicity != nil {
		schema = "mantik-owned-paper-evaluation-assessment-v11"
		// The registered search cut cannot silently omit a strategy that already
		// exists; later registrations must be strictly after this assessment.
		for _, strategy := range s.strategies.Records {
			if strategy.RegisteredAt.After(at) {
				return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
			}
		}
		value, err := ownedSearchHistory(e, s.strategies, at)
		if err != nil {
			return PaperEvaluationAssessmentReceipt{}, err
		}
		search = &value
	}
	var coverage *PaperEvaluationCorporateCoverage
	if e.Declaration.Command.Plan.CorporateCoverage != nil {
		schema = "mantik-owned-paper-evaluation-assessment-v12"
		value, err := ownedCorporateCoverage(ctx, e, cut, s)
		if err != nil {
			return PaperEvaluationAssessmentReceipt{}, err
		}
		coverage = &value
	}
	record, err := paperAssessmentWithBenchmarks(c, e, &warmup, &cut, portfolio, holdout, search, coverage, schema)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	next := clone(all)
	next[c.ID] = record
	if _, err = paperEvaluationAssessmentCatalog(ctx, owner, next, s); err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	return PaperEvaluationAssessmentReceipt{record, false}, nil
}
func (m *Memory) AssessPaperEvaluation(ctx context.Context, c PaperEvaluationAssessmentCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	s, err := m.paperEvaluationSourcesLocked(de, oe)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	r, err := appendPaperEvaluationAssessment(ctx, m.receiverID, m.evaluationAssessments, s, c, now, m.ownedAllocations)
	if err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return PaperEvaluationAssessmentReceipt{}, err
	}
	if !r.Duplicate {
		m.evaluationAssessments[c.ID] = clone(r.Record)
	}
	return r, nil
}
func (m *Memory) PaperEvaluationAssessments(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	s, err := m.paperEvaluationSourcesLocked(de, oe)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	return paperEvaluationAssessmentCatalog(ctx, m.receiverID, m.evaluationAssessments, s)
}
func readPaperEvaluationAssessments(ctx context.Context, tx pgx.Tx) (map[string]PaperEvaluationAssessment, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_evaluation_assessments ORDER BY id LIMIT $1", MaxPaperEvaluationAssessments+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]PaperEvaluationAssessment{}
	bytes := 0
	for rows.Next() {
		var id string
		var raw []byte
		var r PaperEvaluationAssessment
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		bytes += len(raw)
		if bytes > MaxPaperEvaluationAssessmentCatalogBytes || len(all) >= MaxPaperEvaluationAssessments {
			return nil, ErrPaperEvaluationAssessmentCapacity
		}
		if workerplan.ReadBoundedShape(raw, &r, MaxPaperEvaluationAssessmentBytes) != nil || id != r.Command.ID {
			return nil, ErrPaperEvaluationAssessmentInvalid
		}
		all[id] = r
	}
	return all, rows.Err()
}
func (p *Postgres) paperEvaluationAssessmentAccess(ctx context.Context, write bool, c PaperEvaluationAssessmentCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, PaperEvaluationAssessmentReceipt, error) {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		opts = pgx.TxOptions{}
	}
	tx, err := p.pool.BeginTx(ctx, opts)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	s, err := readPaperEvaluationSources(ctx, tx, write, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
	}
	all, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
	}
	var receipt PaperEvaluationAssessmentReceipt
	if write {
		allocations, e := readOwnedAllocationJournal(ctx, tx)
		if e != nil {
			return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, e
		}
		receipt, err = appendPaperEvaluationAssessment(ctx, s.declarations.ReceiverID, all, s, c, now, allocations)
		if err != nil {
			return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
		}
		if !receipt.Duplicate {
			raw, e := json.Marshal(receipt.Record)
			if e != nil {
				return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, e
			}
			if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_evaluation_assessments(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
				return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
			}
			all[c.ID] = receipt.Record
		}
	}
	catalog, err := paperEvaluationAssessmentCatalog(ctx, s.declarations.ReceiverID, all, s)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperEvaluationAssessmentCatalog{}, PaperEvaluationAssessmentReceipt{}, err
	}
	return catalog, receipt, nil
}
func (p *Postgres) AssessPaperEvaluation(ctx context.Context, c PaperEvaluationAssessmentCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentReceipt, error) {
	if c.Validate() != nil {
		return PaperEvaluationAssessmentReceipt{}, ErrPaperEvaluationAssessmentInvalid
	}
	_, r, err := p.paperEvaluationAssessmentAccess(ctx, true, c, now, de, oe)
	return r, err
}
func (p *Postgres) PaperEvaluationAssessments(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	c, _, err := p.paperEvaluationAssessmentAccess(ctx, false, PaperEvaluationAssessmentCommand{}, nil, de, oe)
	return c, err
}

var _ PaperEvaluationAssessmentStore = (*Memory)(nil)
var _ PaperEvaluationAssessmentStore = (*Postgres)(nil)
