package storage_test

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"testing"
)

func jsonBytes(v any) []byte { raw, _ := json.Marshal(v); return raw }
func reportedFixture(t *testing.T, j externalplan.Job) map[string]any {
	t.Helper()
	results := []any{}
	for index, m := range j.NativeReference.Results {
		metrics := map[string]int64{}
		for name, value := range metricValues(m.Metrics) {
			metrics[name] = *value
		}
		if index == 3 {
			metrics["actions"] = 1
		}
		compared := map[string]any{}
		for name, value := range metrics {
			if index == 3 && name == "actions" {
				continue
			}
			reference := *metricValues(m.Metrics)[name]
			compared[name] = map[string]any{"native": reference, "external": value, "delta": value - reference, "matches": value == reference}
		}
		noncomparable := []string{}
		if index == 3 {
			noncomparable = []string{"actions"}
		}
		trades := []any{}
		if index == 0 {
			trades = append(trades, map[string]any{"size": 10, "entry_bar": 2, "entry_price_cents": 10000, "exit_bar": 4, "exit_price_cents": 9400})
		}
		results = append(results, map[string]any{"method_id": m.MethodID, "method_sha256": m.MethodSHA256, "metrics": metrics, "simulated_trades": trades, "comparisons": compared, "noncomparable_metrics": noncomparable})
	}
	return map[string]any{"schema": externalplan.ResultSchema, "tool": externalplan.Tool, "library": "backtesting", "library_version": externalplan.LibraryVersion, "python_version": "3.13.9", "recipe_sha256": externalplan.RecipeSHA, "dataset_sha256": externalplan.DatasetSHA, "conversion": "quote_to_flat_ohlc_with_copied_boundary_bars_v1", "warmup_copy_bars": 1, "settlement_copy_bars": 1, "trade_on_close": true, "finalize_trades": false, "commission": 0, "spread": 0, "margin": 1, "status": "compared", "results": results, "authority": "synthetic_external_fixture_comparison_only", "qualification_credit": false, "image_identity_verified": false, "isolation_verified": false, "all_comparable_metrics_match": true, "artifacts": map[string]string{"dataset": j.Inputs[0].SHA256, "recipe": j.Inputs[1].SHA256, "native": j.Inputs[2].SHA256}, "adapter_sha256": j.AdapterSHA256, "requirements_sha256": j.RequirementsSHA256}
}
func reportedBytes(value map[string]any) []byte {
	delete(value, "sha256")
	body := jsonBytes(value)
	hash := workerplan.Digest(body)
	return append(body[:len(body)-1], []byte(`,"sha256":"`+hash+`"}`)...)
}
func metricValues(m externalplan.Metrics) map[string]*int64 {
	return map[string]*int64{"observations": m.Observations, "actions": m.Actions, "realized_pnl_cents": m.Realized, "marked_pnl_cents": m.Marked}
}
