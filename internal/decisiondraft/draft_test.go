package decisiondraft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func manufacturedDecision() storage.PaperDecisionRecord {
	d := storage.PaperDecisionRecord{Schema: "mantik-owned-paper-decision-v1", Command: storage.PaperDecisionCommand{ID: "decision-one", ReceiverID: "receiver-0123456789abcdef", StrategyID: "strategy-one", StrategySHA256: strings.Repeat("a", 64), RegistrationID: "collector-one"}, FinalizedAt: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC), Status: "finalized", Evaluation: storage.PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "manufactured", PriceCents: 10000}, BasisSHA256: strings.Repeat("b", 64), Authority: "service_finalized_research_proposal_only"}
	d.SHA256 = workerplan.Hash(d)
	return d
}
func modelBody(content, reason string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": reason, "message": map[string]any{"role": "assistant", "content": content}}}})
	return string(raw)
}

const goodDraft = `{"explanation":"The retained candidate still requires admission; no executed trade is established.","claims":[{"field":"action","value":"entry_candidate"}]}`

func TestDraftRetainsBeforeRequestAndRejectsInvalidOutcomesWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		raw    string
		want   string
	}{
		{"valid", 200, modelBody(goodDraft, "stop"), "draft_retained"},
		{"unsupported_claim_retained_for_review", 200, modelBody(`{"explanation":"Unverified profit claim.","claims":[{"field":"realized_profit","value":"1000"}]}`, "stop"), "draft_retained"},
		{"http", 403, "private provider error", "http_failed"},
		{"truncated", 200, modelBody(goodDraft, "length"), "draft_invalid"},
		{"duplicate", 200, modelBody(`{"explanation":"one","explanation":"two","claims":[{"field":"action","value":"entry_candidate"}]}`, "stop"), "draft_invalid"},
		{"empty_claims", 200, modelBody(`{"explanation":"draft","claims":[]}`, "stop"), "draft_invalid"},
		{"unknown_authority", 200, modelBody(`{"explanation":"draft","claims":[{"field":"action","value":"entry_candidate"}],"execution_available":true}`, "stop"), "draft_invalid"},
		{"oversize", 200, strings.Repeat("x", MaxResponseBytes+1), "response_incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			history := []Receipt{}
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if len(history) != 2 || history[0].Status != "prepared" || history[1].Status != "request_started" {
					t.Error("request not retained first")
				}
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer manufactured-token" {
					t.Error("wrong request")
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.raw)
			})))
			defer server.Close()
			c := Config{Endpoint: server.URL, Model: "ollama/manufactured-model", MaxTokens: 512}
			d := manufacturedDecision()
			got, err := Generate(context.Background(), c, d, "manufactured-token", func(r Receipt) error { history = append(history, r); return nil })
			if got.Status != test.want || calls.Load() != 1 || len(history) != 3 || got.ProseVerified || got.ExecutionAttested || got.QualificationCredit {
				t.Fatal(got.Status, err, calls.Load(), len(history))
			}
			if (err == nil) != (test.want == "draft_retained") {
				t.Fatal("wrong success state", err)
			}
			raw, e := base64.StdEncoding.DecodeString(got.Request.Base64)
			if e != nil || got.Request.SHA256 != workerplan.Digest(raw) || strings.Contains(string(raw), "manufactured-token") || !strings.Contains(string(raw), d.SHA256) {
				t.Fatal("request lost source or leaked bearer")
			}
			if got.Response == nil || got.Response.SHA256 == "" || got.Response.Complete != (test.want != "response_incomplete") {
				t.Fatal("response bytes not retained")
			}
		})
	}
}
func TestDraftPersistenceFailureAndCancellationPreventTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })))
	defer server.Close()
	c := Config{Endpoint: server.URL, Model: "ollama/manufactured-model", MaxTokens: 512}
	d := manufacturedDecision()
	for _, failureAt := range []int{1, 2} {
		n := 0
		if _, err := Generate(context.Background(), c, d, "", func(Receipt) error {
			n++
			if n == failureAt {
				return errors.New("manufactured persistence refusal")
			}
			return nil
		}); err == nil || calls.Load() != 0 {
			t.Fatal("request escaped persistence refusal")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	history := []Receipt{}
	r, err := Generate(ctx, c, d, "", func(r Receipt) error { history = append(history, r); return nil })
	if err == nil || r.Status != "cancelled_before_request" || len(history) != 2 || calls.Load() != 0 {
		t.Fatal(r.Status, err)
	}
}
