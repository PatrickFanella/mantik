package storage

import (
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Configuration identity covers every native definition field except its ID.
// Distinct configurations do not establish sensitivity, search completeness,
// independent samples, economic performance or qualification.
type PaperEvaluationVariantConfiguration struct {
	Method              string `json:"method"`
	StrategyID          string `json:"strategy_id"`
	StrategySHA256      string `json:"strategy_sha256"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
}

// Call only with an actual owner-selected and validated registry record. Keep
// this explicit versioned hash representation separate from a strategy digest.
func nativeVariantConfiguration(r paperstrategy.Record) PaperEvaluationVariantConfiguration {
	d := r.Command.Definition
	d.ID = ""
	return PaperEvaluationVariantConfiguration{
		Method: "native-definition-without-id-v1", StrategyID: r.Command.Definition.ID,
		StrategySHA256: r.SHA256, ConfigurationSHA256: workerplan.Hash(d),
	}
}
