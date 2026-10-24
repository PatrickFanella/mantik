package storage

import "git.subcult.tv/subculture-collective/mantik/internal/workerplan"

// Applicability describes the compiled method's output contract. It does not
// measure calibration, attest a forecast, or grant admission or execution.
type PaperEvaluationForecastCalibration struct {
	Method     string                   `json:"method"`
	Strategies []PaperEvaluationVariant `json:"strategies"`
	OutputKind string                   `json:"output_kind"`
	Status     string                   `json:"status"`
	ReasonCode string                   `json:"reason_code"`
	SHA256     string                   `json:"sha256"`
}

// Only call after ownedNativeWarmUp has validated the actual registry and the
// literal native-paper-threshold-v1 method, pinned before the observation window.
// That predicate returns a boolean threshold signal, never a probability forecast.
// A future probabilistic method must define its own applicable calibration recipe.
func nativeThresholdCalibration(owned PaperEvaluationWarmUp) PaperEvaluationForecastCalibration {
	r := PaperEvaluationForecastCalibration{Method: "native-threshold-no-probability-forecast-v1", Strategies: []PaperEvaluationVariant{}, OutputKind: "boolean_threshold_signal", Status: "not_applicable", ReasonCode: "compiled_threshold_predicate_has_no_probability_forecast"}
	for _, s := range owned.Strategies {
		r.Strategies = append(r.Strategies, PaperEvaluationVariant{ID: s.Command.Definition.ID, SHA256: s.SHA256})
	}
	r.SHA256 = workerplan.Hash(r)
	return r
}
