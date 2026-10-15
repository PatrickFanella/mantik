package trial

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"strings"
	"testing"
	"time"
)

func TestNativeRunResultBoundary(t *testing.T) {
	c, p, at := fixture(t)
	registration, err := Register(c, p, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	a, err := StartAttempt(AttemptCommand{ReceiverID: c.ReceiverID, RequestID: "intent", TrialID: registration.ID}, registration, at)
	if err != nil {
		t.Fatal(err)
	}
	run, err := BeginRun(a, registration, `{"fixture":true}`, "owner", at)
	if err != nil || run.Validate() != nil {
		t.Fatal(run, err)
	}
	methods := []map[string]string{}
	for _, m := range append(append([]experimentplan.MethodPin{}, registration.Recipe.Methods...), registration.Recipe.Benchmarks...) {
		methods = append(methods, map[string]string{"method_id": m.ID, "method_sha256": m.SHA256})
	}
	base := map[string]any{"id": run.NativeAttemptID, "recipe_sha256": run.RecipeSHA256, "input_sha256": run.InputSHA256, "status": "completed", "error_code": "", "evidence_class": "synthetic_unqualified", "qualified": false, "results": methods}
	raw, _ := json.Marshal(base)
	completed, err := FinishRun(run, registration, "owner", string(raw), at.Add(time.Second))
	if err != nil || !completed.DeadlineMet || completed.Validate() != nil {
		t.Fatal(completed, err)
	}
	late, err := FinishRun(run, registration, "owner", string(raw), run.Deadline)
	if err != nil || late.DeadlineMet || late.Validate() != nil {
		t.Fatal("late result erased or presented in time", err)
	}
	for _, field := range []string{"id", "recipe_sha256", "input_sha256", "evidence_class", "qualified", "results", "status"} {
		var altered map[string]any
		_ = json.Unmarshal(raw, &altered)
		switch field {
		case "qualified":
			altered[field] = true
		case "results":
			altered[field] = []any{}
		default:
			altered[field] = "untrusted"
		}
		bad, _ := json.Marshal(altered)
		if _, err := FinishRun(run, registration, "owner", string(bad), at); err == nil {
			t.Fatal("invalid result accepted", field)
		}
	}
	if _, err := FinishRun(run, registration, "owner", strings.Repeat("x", MaxArtifactBytes+1), at); err == nil {
		t.Fatal("oversized result accepted")
	}
	if _, err := ResolveRun(run, run.Deadline.Add(-time.Nanosecond)); err == nil {
		t.Fatal("early resolution")
	}
	incomplete, err := ResolveRun(run, run.Deadline)
	if err != nil || incomplete.Status != "incomplete" || incomplete.Validate() != nil {
		t.Fatal(incomplete, err)
	}
	if _, err := FinishRun(incomplete, registration, "owner", string(raw), run.Deadline); err == nil {
		t.Fatal("resolved invocation changed history")
	}
}
