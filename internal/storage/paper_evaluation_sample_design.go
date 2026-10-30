package storage

import (
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperSampleDesignEvaluationRecipe = "native-threshold-disjoint-sample-plan-v5"
const PaperSampleDesignAssessmentRecipe = "native-predeclared-disjoint-samples-v10"
const PaperDisjointSampleMethod = "native-closed-observation-windows-v1"

type PaperEvaluationSampleDesignPlan struct {
	Method                     string `json:"method"`
	MinimumDisjointOccurrences int    `json:"minimum_disjoint_occurrences"`
}

type PaperEvaluationSampleWindow struct {
	DecisionID         string    `json:"decision_id"`
	DecisionSHA256     string    `json:"decision_sha256"`
	FeatureInputSHA256 string    `json:"feature_input_sha256"`
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	Status             string    `json:"status"`
}

type PaperEvaluationSampleVariant struct {
	StrategyID         string                        `json:"strategy_id"`
	Windows            []PaperEvaluationSampleWindow `json:"windows"`
	ObservedWindows    int                           `json:"observed_windows"`
	DisjointWindows    int                           `json:"disjoint_windows"`
	OverlappingWindows int                           `json:"overlapping_windows"`
	UnavailableWindows int                           `json:"unavailable_windows"`
	Status             string                        `json:"status"`
	ReasonCode         string                        `json:"reason_code"`
}

type PaperEvaluationSampleDesign struct {
	Method                  string                          `json:"method"`
	DeclarationSHA256       string                          `json:"declaration_sha256"`
	EvidenceSHA256          string                          `json:"evidence_sha256"`
	Plan                    PaperEvaluationSampleDesignPlan `json:"plan"`
	Variants                []PaperEvaluationSampleVariant  `json:"variants"`
	Authority               string                          `json:"authority"`
	IndependenceEstablished bool                            `json:"independence_established"`
	EvaluationComplete      bool                            `json:"evaluation_complete"`
	AdmissionAvailable      bool                            `json:"admission_available"`
	ExecutionAvailable      bool                            `json:"execution_available"`
	QualificationCredit     bool                            `json:"qualification_credit"`
	SHA256                  string                          `json:"sha256"`
}

// Earliest finish selects a maximum-cardinality set of closed, disjoint windows.
// Equal endpoints overlap; equal finishes use start then decision ID as tie-breaks.
// This is a sample-design constraint, not a claim of statistical independence.
func packDisjointSampleWindows(v *PaperEvaluationSampleVariant) {
	slices.SortFunc(v.Windows, func(a, b PaperEvaluationSampleWindow) int {
		if c := a.End.Compare(b.End); c != 0 {
			return c
		}
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return strings.Compare(a.DecisionID, b.DecisionID)
	})
	var last time.Time
	for i := range v.Windows {
		w := &v.Windows[i]
		if w.Status != "eligible_observed_window" {
			v.UnavailableWindows++
			continue
		}
		v.ObservedWindows++
		if !last.IsZero() && !w.Start.After(last) {
			w.Status = "overlapping_observation_window"
			v.OverlappingWindows++
			continue
		}
		w.Status = "selected_disjoint_window"
		v.DisjointWindows++
		last = w.End
	}
}

func evaluateDisjointSampleVariant(v *PaperEvaluationSampleVariant, minimum int, closed bool) {
	packDisjointSampleWindows(v)
	v.Status, v.ReasonCode = "pending", "predeclared_outcome_window_not_closed"
	if closed {
		v.Status, v.ReasonCode = "unavailable", "disjoint_windows_do_not_establish_statistical_independence"
		if v.DisjointWindows < minimum {
			v.Status, v.ReasonCode = "failed", "disjoint_windows_below_predeclared_minimum"
		}
	}
}

// Inputs have already passed strategy-owner and holdout-envelope validation.
// Actual catalogue replay reconstructs those inputs from the retained owners.
func nativeSampleDesign(r PaperEvaluationAssessment) (PaperEvaluationSampleDesign, error) {
	p := r.Evidence.Declaration.Command.Plan.SampleDesign
	if p == nil || !validHoldoutRecipe(r.Evidence.Declaration.Command) || p.Method != PaperDisjointSampleMethod || r.WarmUp == nil || r.Holdout == nil {
		return PaperEvaluationSampleDesign{}, ErrPaperEvaluationAssessmentInvalid
	}
	out := PaperEvaluationSampleDesign{Method: p.Method, DeclarationSHA256: r.Evidence.Declaration.SHA256, EvidenceSHA256: r.Evidence.SHA256, Plan: *p, Variants: []PaperEvaluationSampleVariant{}, Authority: "owner_derived_nonoverlapping_observation_design_only"}
	strategies := map[string]paperstrategy.Record{}
	for _, s := range r.WarmUp.Strategies {
		strategies[s.Command.Definition.ID] = s
	}
	variants := map[string]*PaperEvaluationSampleVariant{}
	for _, v := range r.Evidence.Declaration.Command.Plan.Variants {
		variants[v.ID] = &PaperEvaluationSampleVariant{StrategyID: v.ID, Windows: []PaperEvaluationSampleWindow{}}
	}
	selected := 0
	for _, row := range r.Evidence.Rows {
		if row.Status != "selected_research_evidence" {
			continue
		}
		if selected >= len(r.Holdout.Rows) {
			return PaperEvaluationSampleDesign{}, ErrPaperEvaluationAssessmentInvalid
		}
		h := r.Holdout.Rows[selected]
		selected++
		s, ok := strategies[row.Decision.Command.StrategyID]
		v := variants[row.Decision.Command.StrategyID]
		if !ok || v == nil || s.SHA256 != h.StrategySHA256 || h.DecisionID != row.Decision.Command.ID {
			return PaperEvaluationSampleDesign{}, ErrPaperEvaluationAssessmentInvalid
		}
		end := h.HorizonAt.Add(time.Duration(min(int64(60), s.Command.Definition.Safety.MaxQuoteAgeSeconds)) * time.Second)
		if !declarationTime(end) || h.FeatureOccurredAt.After(end) {
			return PaperEvaluationSampleDesign{}, ErrPaperEvaluationAssessmentInvalid
		}
		status := "eligible_observed_window"
		if h.LabelAvailableAt.IsZero() {
			status = "outcome_unavailable"
		} else if end.After(r.AssessedAt) {
			status = "window_not_closed_at_cut"
		}
		v.Windows = append(v.Windows, PaperEvaluationSampleWindow{DecisionID: h.DecisionID, DecisionSHA256: h.DecisionSHA256, FeatureInputSHA256: h.FeatureInputSHA256, Start: h.FeatureOccurredAt, End: end, Status: status})
	}
	if selected != len(r.Holdout.Rows) {
		return PaperEvaluationSampleDesign{}, ErrPaperEvaluationAssessmentInvalid
	}
	for _, declared := range r.Evidence.Declaration.Command.Plan.Variants {
		v := *variants[declared.ID]
		evaluateDisjointSampleVariant(&v, p.MinimumDisjointOccurrences, r.Evidence.Phase == "outcome_deadline_elapsed")
		out.Variants = append(out.Variants, v)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

func applySampleDesign(r *PaperEvaluationAssessment, design PaperEvaluationSampleDesign) {
	r.SampleDesign = &design
	for i := range r.Checks {
		c := &r.Checks[i]
		if c.Name != "sample_sufficiency" || c.Status == "failed" {
			continue
		}
		for _, v := range design.Variants {
			if v.StrategyID != c.StrategyID {
				continue
			}
			c.Status, c.ReasonCode = v.Status, v.ReasonCode
			if v.Status == "failed" {
				r.Status = "blocked"
			}
		}
	}
}
