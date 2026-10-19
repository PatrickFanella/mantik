package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func revenueQuoteFixture() QuoteResponse {
	q := quoteFixture()
	q.Claims[0].Code = "reported_revenue"
	return q
}
func revenueExpectedFixture() Response {
	q := FixtureResponse()
	q.Claims[0].Code = "reported_revenue"
	return q
}

func TestRevenueProfileRetainsAndReplaysEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		change      func(*QuoteResponse)
		disposition string
		outcome     string
	}{
		{"extracted", 200, func(*QuoteResponse) {}, "response_complete", "extracted"},
		{"wrong-code", 200, func(q *QuoteResponse) { q.Claims[0].Code = "revenue_report" }, "profile_policy_rejected", "abstain"},
		{"disputed", 200, func(q *QuoteResponse) { q.Claims[0].Assessment = "disputed" }, "profile_policy_rejected", "abstain"},
		{"multiple", 200, func(q *QuoteResponse) { c := copyJSON(q.Claims[0]); c.ID = "second"; q.Claims = append(q.Claims, c) }, "profile_policy_rejected", "abstain"},
		{"no-revenue", 200, func(q *QuoteResponse) {
			q.Outcome = "abstain"
			q.ReasonCode = "no_relevant_claims"
			q.Claims = []QuoteClaim{}
		}, "response_complete", "abstain"},
		{"ambiguous", 200, func(q *QuoteResponse) {
			q.Outcome = "abstain"
			q.ReasonCode = "ambiguous_source"
			q.Claims = []QuoteClaim{}
		}, "response_complete", "abstain"},
		{"unsupported-quote", 200, func(q *QuoteResponse) { q.Claims[0].Evidence[0].Quote = "not in source" }, "quote_anchoring_rejected", "abstain"},
		{"http-failed", 503, func(*QuoteResponse) {}, "http_failed", "abstain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := revenueQuoteFixture()
			tc.change(&q)
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid request")
				}
				if len(req) != 6 || req["max_tokens"] != float64(1024) || req["reasoning_effort"] != "low" {
					t.Error("gateway bounds or extra options", req)
				}
				messages := req["messages"].([]any)
				system := messages[0].(map[string]any)["content"].(string)
				if system != revenueInstructions || !strings.Contains(system, "title") || !strings.Contains(system, `"reported_revenue"`) {
					t.Error("profile prompt missing")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(localReply(SwitchyardModel, string(quoteJSON(q)), "stop"))
			}))
			defer model.Close()
			var receipts []SwitchyardReceipt
			a := diagnosticSource()
			config := SwitchyardConfig{Endpoint: model.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low", Profile: RevenueProfile}
			f, err := RunSwitchyard(context.Background(), a, "event", config, "", func(r SwitchyardReceipt) error { receipts = append(receipts, copyJSON(r)); return nil })
			if err != nil || len(receipts) != 2 || calls.Load() != 1 || f.Disposition != tc.disposition || f.Result.Outcome != tc.outcome || f.Schema != RevenueSchema || f.PromptVersion != RevenuePromptVersion || f.QualificationCredit || f.Result.QualificationCredit {
				t.Fatal("profile outcome", err, f.Disposition, f.Result)
			}
			p := receipts[0]
			model.Close()
			for _, finished := range []*SwitchyardReceipt{nil, &f} {
				inspection, err := InspectSwitchyard(context.Background(), p, finished)
				if err != nil || inspection.Schema != RevenueInspectionSchema || inspection.ContractSHA256 != hash(captureContract(a, RevenueSchema)) || inspection.ExecutionAttested {
					t.Fatal("replay", err, inspection)
				}
				if _, err := RetainSwitchyard(context.Background(), "receiver-abcdefghijklmnop", "profile", p, finished, time.Now()); err == nil {
					t.Fatal("profile imported")
				}
			}
			cohort := SwitchyardCorpus{Schema: SwitchyardCorpusSchema, Cases: []SwitchyardEvaluationInput{{ID: tc.name, Prepared: p, Finished: &f, Expected: revenueExpectedFixture()}, {ID: "unknown", Prepared: p, Expected: revenueExpectedFixture()}}}
			eval, err := EvaluateSwitchyard(context.Background(), cohort)
			if err != nil || eval.Counts.Cases != 2 || eval.Counts.UnknownCases != 1 {
				t.Fatal("cohort", err)
			}
			again, err := EvaluateSwitchyard(context.Background(), cohort)
			x, _ := json.Marshal(eval)
			y, _ := json.Marshal(again)
			if err != nil || !bytes.Equal(x, y) {
				t.Fatal("nondeterministic")
			}
			genericP, genericF := switchyardAdmissionReceipts(t, 200, localReply(SwitchyardModel, string(quoteJSON(quoteFixture())), "stop"))
			mixed := copyJSON(cohort)
			mixed.Cases = append(mixed.Cases, SwitchyardEvaluationInput{ID: "generic", Prepared: genericP, Finished: &genericF, Expected: FixtureResponse()})
			if _, err := EvaluateSwitchyard(context.Background(), mixed); err == nil {
				t.Fatal("profile mixed with generic contract")
			}
			bad := copyJSON(cohort)
			bad.Cases[0].Expected.Claims[0].Code = "revenue_report"
			if _, err := EvaluateSwitchyard(context.Background(), bad); err == nil {
				t.Fatal("wrong-code expectation accepted")
			}
			bad = copyJSON(cohort)
			bad.Cases[1].Expected.Claims[0].Assessment = "disputed"
			if _, err := EvaluateSwitchyard(context.Background(), bad); err == nil {
				t.Fatal("invalid unknown-completion annotation accepted")
			}
			for _, change := range []func(*SwitchyardReceipt){func(r *SwitchyardReceipt) { r.Config.Profile = "" }, func(r *SwitchyardReceipt) { r.PromptVersion = LocalPromptVersion }, func(r *SwitchyardReceipt) { r.Schema = SwitchyardDiagnosticSchema }} {
				changed := copyJSON(p)
				change(&changed)
				if _, err := InspectSwitchyard(context.Background(), changed, nil); err == nil {
					t.Fatal("profile provenance relabeled")
				}
			}
		})
	}
}

func TestRevenueProfileRefusesUnsupportedContextBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	config := SwitchyardConfig{Endpoint: server.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low", Profile: RevenueProfile}
	for _, mutate := range []func(*SwitchyardConfig){func(c *SwitchyardConfig) { c.Profile = "unknown" }, func(c *SwitchyardConfig) { c.MaxTokens = 512 }} {
		c := config
		mutate(&c)
		if _, err := RunSwitchyard(context.Background(), diagnosticSource(), "event", c, "", func(SwitchyardReceipt) error { return nil }); err == nil {
			t.Fatal("invalid profile accepted")
		}
	}
	if _, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", config, "", func(SwitchyardReceipt) error { return nil }); err == nil {
		t.Fatal("exact article accepted for revenue diagnostic")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid context sent request")
	}
	if LocalConfig(config).Validate() == nil {
		t.Fatal("local model accepted profile")
	}
}

func TestRevenueProfileDoesNotAttestSemanticSupport(t *testing.T) {
	// Structural vocabulary enforcement cannot recognize an invented assertion
	// whose quote exists. This fixture keeps that limitation explicit.
	raw := revenueExpectedFixture()
	raw.Claims[0].Assertion = "The issuer reported an unsupported amount."
	result, err := evaluateExpectedContract(context.Background(), diagnosticSource(), "event", raw, captureContract(diagnosticSource(), RevenueSchema))
	if err != nil || result.Outcome != "extracted" || result.QualificationCredit {
		t.Fatal("structural check claimed semantic authority", err)
	}
}
