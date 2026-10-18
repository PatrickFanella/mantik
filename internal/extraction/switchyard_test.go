package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestSwitchyardExtractionAndOfflineReplay(t *testing.T) {
	content, _ := json.Marshal(quoteFixture())
	for _, tc := range []struct {
		name                                string
		status                              int
		model, finish, content, disposition string
	}{
		{"extracted", 200, SwitchyardModel, "stop", string(content), "response_complete"},
		{"abstain", 200, SwitchyardModel, "stop", `{"outcome":"abstain","reason_code":"insufficient_source","claims":[]}`, "response_complete"},
		{"wrong-route", 200, "openai/other", "stop", string(content), "invalid_envelope"},
		{"truncated", 200, SwitchyardModel, "length", string(content), "incomplete_completion"},
		{"invalid-quotes", 200, SwitchyardModel, "stop", `{}`, "quote_anchoring_rejected"},
		{"provider-failure", 503, SwitchyardModel, "stop", string(content), "http_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var saved []SwitchyardReceipt
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if len(saved) != 1 || saved[0].Status != "prepared" || r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer gateway-token" {
					t.Error("intent/transport contract")
				}
				var request map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&request) != nil || string(request["model"]) != `"openai/gpt-6.1-sol"` || string(request["reasoning_effort"]) != `"low"` || string(request["max_tokens"]) != "1024" || request["temperature"] != nil || request["tools"] != nil {
					t.Error("selected model/request bounds")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(localReply(tc.model, tc.content, tc.finish))
			}))
			config := SwitchyardConfig{Endpoint: server.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}
			r, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", config, "gateway-token", func(r SwitchyardReceipt) error { saved = append(saved, copyJSON(r)); return nil })
			server.Close()
			if err != nil || len(saved) != 2 || calls.Load() != 1 || r.Disposition != tc.disposition || r.Schema != SwitchyardSchema || r.Authority != "switchyard_model_claim_candidates_only" || r.QualificationCredit {
				t.Fatal(err, r.Disposition, len(saved), calls.Load())
			}
			unknown, err := InspectSwitchyard(context.Background(), saved[0], nil)
			if err != nil || unknown.Disposition != "completion_unknown" || unknown.ExecutionAttested {
				t.Fatal("unknown", err, unknown)
			}
			report, err := InspectSwitchyard(context.Background(), saved[0], &saved[1])
			if err != nil || report.Schema != SwitchyardInspectionSchema || report.Disposition != tc.disposition || report.ExecutionAttested || report.Authority != "retained_switchyard_bytes_consistency_only" {
				t.Fatal("offline replay", err, report)
			}
			capture, err := RetainSwitchyard(context.Background(), "receiver-abcdefghijklmnop", tc.name, saved[0], &saved[1], time.Now())
			if err != nil || capture.Validate() != nil || capture.ExecutionAttested || capture.QualificationCredit || capture.Inspection.Disposition != tc.disposition {
				t.Fatal("retained outcome", err)
			}
			p, f := LocalReceipt(saved[0]), LocalReceipt(saved[1])
			if _, err := InspectLocal(context.Background(), p, &f); !errors.Is(err, ErrLocalVersion) {
				t.Fatal("provider capture entered local path", err)
			}
			for _, receipt := range saved {
				raw, _ := json.Marshal(receipt)
				if strings.Contains(string(raw), "gateway-token") {
					t.Fatal("credential retained")
				}
			}
			// Resealing a changed request setting or checked result cannot make it
			// consistent with the pinned prepared request and replayed response.
			for _, mutate := range []func(*LocalReceipt){func(f *LocalReceipt) { f.Config.ReasoningEffort = "medium" }, func(f *LocalReceipt) { f.Result.ReasonCode = "ambiguous_source" }} {
				bad := copyJSON(f)
				mutate(&bad)
				bad = sealLocal(bad)
				converted := SwitchyardReceipt(bad)
				if _, err := InspectSwitchyard(context.Background(), saved[0], &converted); err == nil {
					t.Fatal("accepted forged finish")
				}
			}
		})
	}
}

func TestSwitchyardPolicyAndPersistenceFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(localReply(SwitchyardModel, `{}`, "stop"))
	}))
	defer server.Close()
	config := SwitchyardConfig{Endpoint: server.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}
	for _, mutate := range []func(*SwitchyardConfig){func(c *SwitchyardConfig) { c.Model = "openai/gpt-5.4-mini" }, func(c *SwitchyardConfig) { c.Model = "ollama/fixture" }, func(c *SwitchyardConfig) { c.Endpoint = "https://api.openai.com" }, func(c *SwitchyardConfig) { c.ReasoningEffort = "none" }, func(c *SwitchyardConfig) { c.MaxTokens = 4097 }} {
		bad := config
		mutate(&bad)
		if _, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", bad, "", func(SwitchyardReceipt) error { return nil }); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	if LocalConfig(config).Validate() == nil {
		t.Fatal("OpenAI route accepted as local")
	}
	if _, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", config, "", nil); err == nil {
		t.Fatal("nil sink")
	}
	if _, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", config, "", func(SwitchyardReceipt) error { return errors.New("private disk failure") }); err == nil || strings.Contains(err.Error(), "private disk failure") {
		t.Fatal("initial sink failure", err)
	}
	if calls.Load() != 0 {
		t.Fatal("request before retained intent")
	}
	n := 0
	_, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", config, "", func(r SwitchyardReceipt) error {
		n++
		if n == 2 {
			return errors.New("disk failure")
		}
		r.Config.Model = "mutated"
		return nil
	})
	if err == nil || n != 2 || calls.Load() != 1 {
		t.Fatal("final write retry or receipt alias", err, n, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = RunSwitchyard(ctx, news.FixtureArticle(), "event", config, "", func(SwitchyardReceipt) error { return nil })
	if err == nil || calls.Load() != 1 {
		t.Fatal("cancelled request sent", err)
	}
}
