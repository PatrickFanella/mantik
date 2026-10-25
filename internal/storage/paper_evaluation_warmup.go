package storage

import (
	"encoding/json"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// This method describes the compiled predicate's input requirements, not data
// quality, statistical performance, a passing full recipe or an execution grant.
type PaperEvaluationWarmUp struct {
	Method                       string                 `json:"method"`
	Strategies                   []paperstrategy.Record `json:"strategies"`
	RequiredLookbackObservations int                    `json:"required_lookback_observations"`
	Status                       string                 `json:"status"`
	ReasonCode                   string                 `json:"reason_code"`
	SHA256                       string                 `json:"sha256"`
}

// Select only actual owner records pinned by the predeclaration. This also works
// before any decision exists: absence of samples cannot invent a new method or
// erase the compiled stateless predicate's zero-lookback requirement.
func ownedNativeWarmUp(d PaperEvaluationDeclaration, actual paperstrategy.Catalog) (PaperEvaluationWarmUp, error) {
	unsigned := clone(d)
	unsigned.SHA256 = ""
	if !declarationTime(d.DeclaredAt) || workerplan.Hash(unsigned) != d.SHA256 || d.Command.Validate() != nil || actual.Validate() != nil || actual.ReceiverID != d.Command.ReceiverID || !d.Command.Plan.WindowStart.After(d.DeclaredAt) {
		return PaperEvaluationWarmUp{}, ErrPaperEvaluationAssessmentInvalid
	}
	out := PaperEvaluationWarmUp{Method: "native-threshold-stateless-warmup-v1", Strategies: []paperstrategy.Record{}, Status: "not_required", ReasonCode: "compiled_threshold_predicate_has_no_lookback"}
	for _, v := range d.Command.Plan.Variants {
		s, err := paperstrategy.Select(actual, v.ID)
		if err != nil || s.SHA256 != v.SHA256 || s.RegisteredAt.After(d.DeclaredAt) || s.Command.Definition.Method != "native-paper-threshold-v1" {
			return PaperEvaluationWarmUp{}, ErrPaperEvaluationAssessmentInvalid
		}
		out.Strategies = append(out.Strategies, clone(s))
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

// Legacy v1 bytes and semantics remain unchanged. This envelope-only dispatch
// is also used by source-writer clock floors. Actual catalogue replay separately
// rederives the warm-up result from the real strategy owner in the same snapshot.
func paperEvaluationAssessmentRecipe(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, warmup *PaperEvaluationWarmUp, schema string) (PaperEvaluationAssessment, error) {
	r, err := newPaperEvaluationAssessment(c, e)
	if err != nil {
		return PaperEvaluationAssessment{}, err
	}
	switch schema {
	case "mantik-owned-paper-evaluation-assessment-v1":
		if warmup != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return r, nil
	case "mantik-owned-paper-evaluation-assessment-v2", "mantik-owned-paper-evaluation-assessment-v3", "mantik-owned-paper-evaluation-assessment-v4":
		if warmup == nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		actual, err := paperstrategy.CatalogFor(c.ReceiverID, warmup.Strategies)
		if err != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		expected, err := ownedNativeWarmUp(e.Declaration, actual)
		if err != nil || !reflect.DeepEqual(expected, *warmup) {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		r.Schema = schema
		r.Recipe = "native-predeclared-necessary-checks-v2"
		value := clone(expected)
		r.WarmUp = &value
		for i := range r.Checks {
			if r.Checks[i].Name == "warm_up" {
				r.Checks[i].Status = "not_applicable"
				r.Checks[i].ReasonCode = expected.ReasonCode
			}
		}
		if schema == "mantik-owned-paper-evaluation-assessment-v3" || schema == "mantik-owned-paper-evaluation-assessment-v4" {
			r.Recipe = "native-predeclared-necessary-checks-v3"
			calibration := nativeThresholdCalibration(expected)
			r.ForecastCalibration = &calibration
			for i := range r.Checks {
				if r.Checks[i].Name == "forecast_calibration" {
					r.Checks[i].Status = calibration.Status
					r.Checks[i].ReasonCode = calibration.ReasonCode
				}
			}
		}
		if schema == "mantik-owned-paper-evaluation-assessment-v4" {
			r.Recipe = "native-predeclared-necessary-checks-v4"
			applyKnownSourceLeakage(&r)
		}
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(r)
		raw, err := json.Marshal(r)
		if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
		}
		return r, nil
	default:
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
}
