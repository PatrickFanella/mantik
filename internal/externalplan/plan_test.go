package externalplan

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Job, map[string][]byte) {
	t.Helper()
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	r, err := experiment.DefaultRecipe(d)
	if err != nil {
		t.Fatal(err)
	}
	n, err := experiment.Execute(context.Background(), "external-reference", r, d)
	if err != nil {
		t.Fatal(err)
	}
	draw, _ := json.Marshal(d)
	rraw, _ := json.Marshal(r)
	nraw, _ := json.Marshal(n)
	inputs := map[string][]byte{"dataset.json": draw, "recipe.json": rraw, "native.json": nraw}
	j, err := Declare("external-test", "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), inputs)
	if err != nil {
		t.Fatal(err)
	}
	return j, inputs
}
func encoded(value any) []byte { raw, _ := json.Marshal(value); return raw }
func resultFixture(t *testing.T, j Job) map[string]any {
	t.Helper()
	results := []any{}
	for index, m := range j.NativeReference.Results {
		metrics := map[string]int64{}
		for name, value := range m.Metrics.values() {
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
			reference := *m.Metrics.values()[name]
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
	return map[string]any{"schema": ResultSchema, "tool": Tool, "library": "backtesting", "library_version": LibraryVersion, "python_version": "3.13.9", "recipe_sha256": RecipeSHA, "dataset_sha256": DatasetSHA, "conversion": "quote_to_flat_ohlc_with_copied_boundary_bars_v1", "warmup_copy_bars": 1, "settlement_copy_bars": 1, "trade_on_close": true, "finalize_trades": false, "commission": 0, "spread": 0, "margin": 1, "status": "compared", "results": results, "authority": "synthetic_external_fixture_comparison_only", "qualification_credit": false, "image_identity_verified": false, "isolation_verified": false, "all_comparable_metrics_match": true, "artifacts": map[string]string{"dataset": j.Inputs[0].SHA256, "recipe": j.Inputs[1].SHA256, "native": j.Inputs[2].SHA256}, "adapter_sha256": j.AdapterSHA256, "requirements_sha256": j.RequirementsSHA256}
}
func sealResult(value map[string]any) []byte {
	delete(value, "sha256")
	body := encoded(value)
	hash := workerplan.Digest(body)
	return append(body[:len(body)-1], []byte(`,"sha256":"`+hash+`"}`)...)
}
func TestJobPinsRawArtifactsAndNativeReference(t *testing.T) {
	j, inputs := fixture(t)
	if j.ValidateInputs(inputs) != nil {
		t.Fatal("fixture inputs rejected")
	}
	raw := encoded(j)
	read, err := ReadJob(bytes.NewReader(raw))
	if err != nil || read.SHA256 != j.SHA256 {
		t.Fatal(err)
	}
	inputs["native.json"] = append(inputs["native.json"], ' ')
	if j.ValidateInputs(inputs) == nil {
		t.Fatal("changed raw input accepted")
	}
	for _, change := range []func(*Job){
		func(j *Job) { j.Schema = workerplan.JobSchema }, func(j *Job) { j.Resources.MemoryBytes++ },
		func(j *Job) { j.Inputs[0].Reference = "../dataset.json" }, func(j *Job) { j.Transport = "arbitrary" },
		func(j *Job) { j.QualificationCredit = true }, func(j *Job) { j.NativeReference.Results[0].Metrics.Marked = nil },
		func(j *Job) { j.NativeReference.Results[0].Replay = json.RawMessage(`{}`) },
	} {
		var changed Job
		_ = json.Unmarshal(raw, &changed)
		change(&changed)
		if _, err := Seal(changed); err == nil {
			t.Fatal("unsupported external declaration accepted")
		}
	}
	if _, err := workerplan.ReadJob(bytes.NewReader(raw)); err == nil {
		t.Fatal("external job accepted by native v1")
	}
}
func TestResultBindsPinsComparisonsAndRequiredFields(t *testing.T) {
	j, _ := fixture(t)
	if _, err := ReadResult(bytes.NewReader(sealResult(resultFixture(t, j))), j); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["qualification_credit"] = true },
		func(v map[string]any) { delete(v, "qualification_credit") },
		func(v map[string]any) { v["image_identity_verified"] = true },
		func(v map[string]any) { v["adapter_sha256"] = strings.Repeat("d", 64) },
		func(v map[string]any) { v["all_comparable_metrics_match"] = false },
		func(v map[string]any) { v["artifacts"].(map[string]string)["native"] = strings.Repeat("f", 64) },
		func(v map[string]any) { v["results"] = v["results"].([]any)[:3] },
		func(v map[string]any) {
			v["results"].([]any)[0].(map[string]any)["metrics"].(map[string]int64)["marked_pnl_cents"]++
		},
		func(v map[string]any) {
			v["results"].([]any)[0].(map[string]any)["comparisons"].(map[string]any)["actions"].(map[string]any)["delta"] = 1
		},
		func(v map[string]any) { v["results"].([]any)[3].(map[string]any)["noncomparable_metrics"] = []string{} },
		func(v map[string]any) { v["results"] = nil },
		func(v map[string]any) {
			delete(v["results"].([]any)[0].(map[string]any)["simulated_trades"].([]any)[0].(map[string]any), "exit_bar")
		},
	} {
		value := resultFixture(t, j)
		change(value)
		if _, err := ReadResult(bytes.NewReader(sealResult(value)), j); err == nil {
			t.Fatal("forged comparison accepted")
		}
	}
}
func TestResultRetainsMismatchesAndPartialFailures(t *testing.T) {
	j, _ := fixture(t)
	value := resultFixture(t, j)
	comparison := value["results"].([]any)[0].(map[string]any)
	comparison["metrics"].(map[string]int64)["marked_pnl_cents"]++
	c := comparison["comparisons"].(map[string]any)["marked_pnl_cents"].(map[string]any)
	c["external"] = comparison["metrics"].(map[string]int64)["marked_pnl_cents"]
	c["delta"] = 1
	c["matches"] = false
	value["all_comparable_metrics_match"] = false
	raw := sealResult(value)
	r, err := ReadResult(bytes.NewReader(raw), j)
	if err != nil || r.Status != "compared" || *r.AllMatch {
		t.Fatal(r, err)
	}
	value["status"] = "failed"
	value["error_code"] = "library_execution_failed"
	value["results"] = value["results"].([]any)[:1]
	if _, err := ReadResult(bytes.NewReader(sealResult(value)), j); err != nil {
		t.Fatal("partial failure lost", err)
	}
	value["results"] = []any{}
	if _, err := ReadResult(bytes.NewReader(sealResult(value)), j); err != nil {
		t.Fatal("empty failure lost", err)
	}
}
func TestResultStrictBytesAndBound(t *testing.T) {
	j, _ := fixture(t)
	raw := sealResult(resultFixture(t, j))
	for _, bad := range [][]byte{append(bytes.Clone(raw), raw...), append([]byte(" "), raw...), bytes.Replace(raw, []byte(`"tool":`), []byte(`"Tool":`), 1), bytes.Replace(raw, []byte(`"tool":`), []byte(`"tool":"x","tool":`), 1), bytes.Repeat([]byte(" "), MaxResultBytes+1), bytes.Replace(raw, []byte(`"margin":1`), []byte(`"margin":null`), 1), bytes.Replace(raw, []byte(`"margin":1`), []byte(`"margin":2`), 1)} {
		if _, err := ReadResult(bytes.NewReader(bad), j); err == nil {
			t.Fatal("ambiguous or invalid result accepted")
		}
	}
}
