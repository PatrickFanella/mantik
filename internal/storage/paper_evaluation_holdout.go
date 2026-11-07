package storage

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperHoldoutMethod = "predeclared-purged-label-availability-holdout-v1"
const PaperHoldoutAssessmentRecipe = "native-predeclared-holdout-necessary-checks-v7"

// These are prospective partition choices, never supplied sample counts or results.
type PaperEvaluationHoldoutPlan struct {
	Method                     string    `json:"method"`
	SplitAt                    time.Time `json:"split_at"`
	EmbargoSeconds             int64     `json:"embargo_seconds"`
	MinimumTrainingOccurrences int       `json:"minimum_training_occurrences"`
	MinimumHoldoutOccurrences  int       `json:"minimum_holdout_occurrences"`
}

func validHoldoutRecipe(c PaperEvaluationDeclarationCommand) bool {
	h := c.Plan.Holdout
	expected := PaperHoldoutEvaluationRecipe
	if c.Plan.BenchmarkMethod != "" {
		if c.Plan.BenchmarkMethod != papereconomics.BenchmarkMethod {
			return false
		}
		expected = PaperBenchmarkEvaluationRecipe
	}
	if c.Plan.FeatureCausalityMethod != "" {
		if c.Plan.FeatureCausalityMethod != PaperFeatureCausalityMethod || c.Plan.BenchmarkMethod == "" {
			return false
		}
		expected = PaperCausalEvaluationRecipe
	}
	if c.Plan.SampleDesign != nil {
		p := c.Plan.SampleDesign
		if p.Method != PaperDisjointSampleMethod || p.MinimumDisjointOccurrences < c.Plan.MinIndependentDecisions || p.MinimumDisjointOccurrences > 1000000 || c.Plan.FeatureCausalityMethod != PaperFeatureCausalityMethod {
			return false
		}
		expected = PaperSampleDesignEvaluationRecipe
	}
	if c.Plan.SearchMultiplicity != nil {
		if !validSearchPlan(c) {
			return false
		}
		expected = PaperSearchHistoryEvaluationRecipe
	}
	if c.Plan.CorporateCoverage != nil {
		if !validCoveragePlan(c) {
			return false
		}
		expected = PaperCoverageEvaluationRecipe
	}
	if c.Plan.TotalReturnMethod != "" {
		if c.Plan.TotalReturnMethod != papereconomics.TotalReturnMethod || c.Plan.CorporateCoverage == nil {
			return false
		}
		expected = PaperTotalReturnEvaluationRecipe
	}
	if c.Plan.Sensitivity != nil {
		if !validSensitivityPlan(c) {
			return false
		}
		expected = PaperSensitivityEvaluationRecipe
	}
	if c.Plan.StatisticalTests != nil {
		if !validStatisticalPlan(c) {
			return false
		}
		expected = PaperStatisticalEvaluationRecipe
	}
	if c.Plan.DrawdownMethod != "" {
		if c.Plan.DrawdownMethod != PaperDrawdownMethod || c.Plan.StatisticalTests == nil {
			return false
		}
		expected = PaperDrawdownEvaluationRecipe
	}
	if h == nil {
		return c.Plan.BenchmarkMethod == "" && c.Recipe == PaperEvaluationRecipe
	}
	if c.Recipe != expected || h.Method != PaperHoldoutMethod || !declarationTime(h.SplitAt) || h.EmbargoSeconds < 0 || h.EmbargoSeconds > 86400 || h.MinimumTrainingOccurrences < 2 || h.MinimumTrainingOccurrences > 1000000 || h.MinimumHoldoutOccurrences < 2 || h.MinimumHoldoutOccurrences > 1000000 {
		return false
	}
	embargo := time.Duration(h.EmbargoSeconds) * time.Second
	return h.SplitAt.Add(-embargo).After(c.Plan.WindowStart) && h.SplitAt.Add(embargo).Before(c.Plan.WindowEnd)
}

type PaperEvaluationHoldoutRow struct {
	FeatureInputSHA256 string    `json:"feature_input_sha256"`
	FeatureOccurredAt  time.Time `json:"feature_occurred_at"`
	FeatureAvailableAt time.Time `json:"feature_available_at"`
	DecisionID         string    `json:"decision_id"`
	DecisionSHA256     string    `json:"decision_sha256"`
	StrategySHA256     string    `json:"strategy_sha256"`
	HorizonAt          time.Time `json:"horizon_at"`
	LabelAvailableAt   time.Time `json:"label_available_at"`
	OutcomeSHA256      []string  `json:"outcome_sha256"`
	Partition          string    `json:"partition"`
}
type PaperEvaluationHoldoutVariant struct {
	StrategyID                   string `json:"strategy_id"`
	TrainingOccurrenceUpperBound int    `json:"training_occurrence_upper_bound"`
	HoldoutOccurrenceUpperBound  int    `json:"holdout_occurrence_upper_bound"`
	Purged                       int    `json:"purged"`
	Status                       string `json:"status"`
	ReasonCode                   string `json:"reason_code"`
}
type PaperEvaluationHoldout struct {
	Method                          string                          `json:"method"`
	DeclarationSHA256               string                          `json:"declaration_sha256"`
	EvidenceSHA256                  string                          `json:"evidence_sha256"`
	Plan                            PaperEvaluationHoldoutPlan      `json:"plan"`
	Rows                            []PaperEvaluationHoldoutRow     `json:"rows"`
	Variants                        []PaperEvaluationHoldoutVariant `json:"variants"`
	Authority                       string                          `json:"authority"`
	IndependenceEstablished         bool                            `json:"independence_established"`
	OutOfSamplePerformanceAvailable bool                            `json:"out_of_sample_performance_available"`
	AdmissionAvailable              bool                            `json:"admission_available"`
	ExecutionAvailable              bool                            `json:"execution_available"`
	QualificationCredit             bool                            `json:"qualification_credit"`
	SHA256                          string                          `json:"sha256"`
}

// The clock-floor path validates this envelope only. Actual catalogue replay
// regenerates every horizon and availability pin from the original source owners.
func holdoutFromRows(e PaperEvaluationEvidence, pins []PaperEvaluationHoldoutRow) (PaperEvaluationHoldout, error) {
	if e.Declaration.Command.Plan.Holdout == nil || !validHoldoutRecipe(e.Declaration.Command) || pins == nil {
		return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
	}
	plan := *e.Declaration.Command.Plan.Holdout
	out := PaperEvaluationHoldout{Method: PaperHoldoutMethod, DeclarationSHA256: e.Declaration.SHA256, EvidenceSHA256: e.SHA256, Plan: plan, Rows: []PaperEvaluationHoldoutRow{}, Variants: []PaperEvaluationHoldoutVariant{}, Authority: "owner_derived_predeclared_temporal_partition_only"}
	counts := map[string]*PaperEvaluationHoldoutVariant{}
	for _, v := range e.Declaration.Command.Plan.Variants {
		counts[v.ID] = &PaperEvaluationHoldoutVariant{StrategyID: v.ID}
	}
	selected := 0
	before := plan.SplitAt.Add(-time.Duration(plan.EmbargoSeconds) * time.Second)
	after := plan.SplitAt.Add(time.Duration(plan.EmbargoSeconds) * time.Second)
	for _, row := range e.Rows {
		if row.Status != "selected_research_evidence" {
			continue
		}
		if selected >= len(pins) {
			return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
		}
		p := clone(pins[selected])
		selected++
		if !decisionPin.MatchString(p.FeatureInputSHA256) || !declarationTime(p.FeatureOccurredAt) || !declarationTime(p.FeatureAvailableAt) || p.FeatureOccurredAt.Before(e.Declaration.Command.Plan.WindowStart) || p.FeatureOccurredAt.After(p.FeatureAvailableAt) || p.FeatureAvailableAt.After(row.Decision.FinalizedAt) || p.DecisionID != row.Decision.Command.ID || p.DecisionSHA256 != row.Decision.SHA256 || p.StrategySHA256 != row.Decision.Command.StrategySHA256 || !p.HorizonAt.After(row.Decision.FinalizedAt) || !declarationTime(p.HorizonAt) || len(p.OutcomeSHA256) != len(row.Outcomes) {
			return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
		}
		labelAt := time.Time{}
		for i, o := range row.Outcomes {
			if p.OutcomeSHA256[i] != o.SHA256 {
				return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
			}
			// A label is not available for training before its retained outcome
			// exists, even if the underlying quote occurred earlier.
			if o.Evaluation.Status == "observed" {
				labelAt = later(labelAt, later(o.EvaluatedAt, o.Evaluation.AvailableAt))
			}
		}
		if !p.LabelAvailableAt.Equal(labelAt) {
			return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
		}
		p.Partition = "purged_split_or_embargo_overlap"
		if labelAt.IsZero() {
			p.Partition = "outcome_unavailable"
		} else if row.Decision.FinalizedAt.Before(plan.SplitAt) && p.HorizonAt.Before(before) && labelAt.Before(before) {
			p.Partition = "training"
		} else if !row.Decision.FinalizedAt.Before(after) && !p.FeatureOccurredAt.Before(after) && !p.FeatureAvailableAt.Before(after) {
			p.Partition = "holdout"
		}
		v := counts[row.Decision.Command.StrategyID]
		if v == nil {
			return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
		}
		switch p.Partition {
		case "training":
			v.TrainingOccurrenceUpperBound++
		case "holdout":
			v.HoldoutOccurrenceUpperBound++
		default:
			v.Purged++
		}
		out.Rows = append(out.Rows, p)
	}
	if selected != len(pins) {
		return PaperEvaluationHoldout{}, ErrPaperEvaluationAssessmentInvalid
	}
	for _, declared := range e.Declaration.Command.Plan.Variants {
		v := *counts[declared.ID]
		v.Status = "pending"
		v.ReasonCode = "predeclared_outcome_window_not_closed"
		if e.Phase == "outcome_deadline_elapsed" {
			v.Status = "unavailable"
			v.ReasonCode = "partition_counts_do_not_establish_independence_or_holdout_performance"
			if v.TrainingOccurrenceUpperBound < plan.MinimumTrainingOccurrences || v.HoldoutOccurrenceUpperBound < plan.MinimumHoldoutOccurrences {
				v.Status = "failed"
				v.ReasonCode = "purged_partition_occurrence_upper_bound_below_predeclared_minimum"
			}
		}
		out.Variants = append(out.Variants, v)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

func later(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func ownedEvaluationHoldout(ctx context.Context, e PaperEvaluationEvidence, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision) (*PaperEvaluationHoldout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pins := []PaperEvaluationHoldoutRow{}
	for _, row := range e.Rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row.Status != "selected_research_evidence" {
			continue
		}
		strategy, err := paperstrategy.Select(strategies, row.Decision.Command.StrategyID)
		if err != nil || strategy.SHA256 != row.Decision.Command.StrategySHA256 {
			return nil, ErrPaperEvaluationAssessmentInvalid
		}
		basis, ok := retained[row.Decision.Command.ID]
		if !ok || basis.Record.SHA256 != row.Decision.SHA256 || basis.Basis.Feed.Routing == nil {
			return nil, ErrPaperEvaluationAssessmentInvalid
		}
		feature, ok := basis.Basis.Feed.Routing.Inputs[row.QuoteInputID]
		if !ok || feature.Input.Provenance == nil {
			return nil, ErrPaperEvaluationAssessmentInvalid
		}
		publishedAt, err := holdoutFeaturePublishedAt(ctx, basis.Basis.Feed, row.Decision, feature.Input)
		if err != nil {
			return nil, err
		}
		available := later(feature.Input.ReceivedAt, feature.Input.Provenance.AvailableAt)
		available = later(available, publishedAt)
		if admission, ok := basis.Basis.Feed.Collectors.Admissions[row.QuoteInputID]; ok {
			available = later(available, admission.At)
		}
		p := PaperEvaluationHoldoutRow{FeatureInputSHA256: feedcollector.InputDigest(feature.Input), FeatureOccurredAt: feature.Input.OccurredAt, FeatureAvailableAt: available, DecisionID: row.Decision.Command.ID, DecisionSHA256: row.Decision.SHA256, StrategySHA256: strategy.SHA256, HorizonAt: row.Decision.FinalizedAt.Add(time.Duration(strategy.Command.Definition.Safety.MaxHoldingSeconds) * time.Second), OutcomeSHA256: []string{}}
		for _, o := range row.Outcomes {
			p.OutcomeSHA256 = append(p.OutcomeSHA256, o.SHA256)
			if o.Evaluation.Status == "observed" {
				p.LabelAvailableAt = later(p.LabelAvailableAt, later(o.EvaluatedAt, o.Evaluation.AvailableAt))
			}
		}
		pins = append(pins, p)
	}
	h, err := holdoutFromRows(e, pins)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func paperAssessmentWithHoldout(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, w *PaperEvaluationWarmUp, cut *PaperEvaluationCorporateSourceCut, portfolio *PaperEvaluationPortfolio, h *PaperEvaluationHoldout, schema string) (PaperEvaluationAssessment, error) {
	if schema != "mantik-owned-paper-evaluation-assessment-v7" {
		if h != nil || e.Declaration.Command.Plan.Holdout != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return paperAssessmentWithPortfolio(c, e, w, cut, portfolio, schema)
	}
	if h == nil || portfolio == nil {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	want, err := holdoutFromRows(e, h.Rows)
	if err != nil || workerplan.Hash(want) != workerplan.Hash(*h) {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	base := "mantik-owned-paper-evaluation-assessment-v5"
	if portfolio != nil {
		base = "mantik-owned-paper-evaluation-assessment-v6"
	}
	r, err := paperAssessmentWithPortfolio(c, e, w, cut, portfolio, base)
	if err != nil {
		return PaperEvaluationAssessment{}, err
	}
	r.Schema = schema
	r.Recipe = PaperHoldoutAssessmentRecipe
	r.Holdout = &want
	for i, check := range r.Checks {
		if check.Name != "out_of_sample" {
			continue
		}
		for _, v := range want.Variants {
			if check.StrategyID == v.StrategyID {
				r.Checks[i].Status = v.Status
				r.Checks[i].ReasonCode = v.ReasonCode
				if v.Status == "failed" {
					r.Status = "blocked"
				}
			}
		}
	}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
	}
	return r, nil
}

// The service already normalized and digest-checked the input during source
// owner replay. Storage resolves its immutable publication envelope here, using
// the low-level recording contract without a collector/service dependency.
func holdoutFeaturePublishedAt(ctx context.Context, feed FeedEvidenceSnapshot, decision PaperDecisionRecord, input domain.RoutingInput) (time.Time, error) {
	if input.Provenance == nil || input.Kind != "quote" {
		return time.Time{}, ErrPaperEvaluationAssessmentInvalid
	}
	selected := time.Time{}
	matches := 0
	for _, publication := range feed.Records {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		if publication.RegistrationID != decision.Command.RegistrationID {
			continue
		}
		if publication.PublishedAt.IsZero() || publication.PublishedAt.After(decision.FinalizedAt) {
			return time.Time{}, ErrPaperEvaluationAssessmentInvalid
		}
		var recording alpacarecord.Recording
		if json.Unmarshal(publication.Recording, &recording) != nil {
			return time.Time{}, ErrPaperEvaluationAssessmentInvalid
		}
		if recording.SHA256 != input.Provenance.SHA256 || recording.RawRef != input.Provenance.RawRef || recording.Revision != input.Provenance.Revision || input.Provenance.RecordKey != recording.CaptureID+"-"+input.Symbol+"-quote" {
			continue
		}
		if alpacarecord.ValidateBase(recording) != nil || recording.Capability != "latest_quotes" || !slices.Contains(recording.Symbols, input.Symbol) || input.Provenance.SourceSymbol != input.Symbol || recording.EvidenceClass != input.EvidenceClass || recording.AccessRef != input.Provenance.AccessRef || !recording.ReceivedAt.Equal(input.ReceivedAt) || input.Source != "alpaca-"+strings.ReplaceAll(recording.Feed, "_", "-") || publication.PublishedAt.Before(recording.ReceivedAt) {
			return time.Time{}, ErrPaperEvaluationAssessmentInvalid
		}
		matches++
		selected = publication.PublishedAt
	}
	if matches != 1 {
		return time.Time{}, ErrPaperEvaluationAssessmentInvalid
	}
	return selected, nil
}
