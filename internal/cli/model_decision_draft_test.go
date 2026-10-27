package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/decisiondraft"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Called by actual Memory and disposable PostgreSQL finalization contracts.
// The model transport is manufactured; it never supplies the owner source.
func modelDecisionDraftContracts(t *testing.T, base string, d storage.PaperDecisionRecord) {
	t.Helper()
	ctx := context.Background()
	var calls atomic.Int32
	var content string
	var status int
	var capture string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer manufactured-model-token" {
			t.Error("model transport escaped configured boundary")
		}
		for _, name := range []string{"source.json", "model-1.json", "model-2.json"} {
			if _, err := os.Stat(filepath.Join(capture, name)); err != nil {
				t.Error("model called before retained intent", name, err)
			}
		}
		var body struct {
			Model     string
			Messages  []struct{ Role, Content string }
			Stream    bool
			MaxTokens int `json:"max_tokens"`
			Tools     json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "ollama/manufactured-model" || body.Stream || body.MaxTokens != 512 || len(body.Tools) != 0 || len(body.Messages) != 2 {
			t.Error("bad model request", err)
		} else {
			var actual storage.PaperDecisionRecord
			if json.Unmarshal([]byte(body.Messages[1].Content), &actual) != nil || !reflect.DeepEqual(actual, d) {
				t.Error("model did not receive actual selected finalized source")
			}
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, "manufactured provider unavailable")
			return
		}
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
		_, _ = w.Write(raw)
	}))
	defer provider.Close()
	t.Setenv("MANTIK_MODEL_TOKEN", "manufactured-model-token")
	root := t.TempDir()
	configPath := filepath.Join(root, "model.json")
	config := decisiondraft.Config{Endpoint: provider.URL, Model: "ollama/manufactured-model", MaxTokens: 512}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := func(review, pin string) []string {
		return []string{"paper-decision-draft", "--api-url", base, "--receiver-id", d.Command.ReceiverID, "--decision-id", d.Command.ID, "--decision-sha256", pin, "--review-id", review, "--capture-dir", capture, "--model-config", configPath}
	}
	for _, tc := range []struct {
		id, prose string
		claims    []storage.DecisionReviewClaim
		want      string
	}{
		{"model-supported", "A retained candidate still requires admission; no execution is established.", []storage.DecisionReviewClaim{{Field: "action", Value: d.Evaluation.Action}}, "claims_consistent"},
		{"model-contradiction", "This unverified draft claims an exit and profit.", []storage.DecisionReviewClaim{{Field: "action", Value: "exit"}, {Field: "realized_profit", Value: "100000"}}, "review_required"},
	} {
		capture = filepath.Join(root, tc.id)
		payload, _ := json.Marshal(decisiondraft.Draft{Explanation: tc.prose, Claims: tc.claims})
		content = string(payload)
		status = 200
		var out, diag bytes.Buffer
		if err := Run(ctx, args(tc.id, d.SHA256), &out, &diag); err != nil {
			t.Fatal(out.String(), diag.String(), err)
		}
		var result struct {
			Review              storage.DecisionReviewReceipt `json:"review"`
			ProseVerified       bool                          `json:"prose_verified"`
			QualificationCredit bool                          `json:"qualification_credit"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Review.Record.Status != tc.want || result.ProseVerified || result.QualificationCredit || result.Review.Record.Command.Explanation != tc.prose || result.Review.Record.ExecutionAvailable || result.Review.Record.AdmissionAvailable {
			t.Fatal(out.String(), err)
		}
		if tc.want == "review_required" {
			var reviewCase storage.DecisionReviewCase
			if err := requestBounded(ctx, base, "/v1/decision-review-cases/"+result.Review.Record.CaseID, nil, &reviewCase, nil, storage.MaxDecisionReviewBytes, true); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, id := range reviewCase.ReviewIDs {
				found = found || id == tc.id
			}
			if !found {
				t.Fatal("contradiction did not join actual review case")
			}
		}
		count := calls.Load()
		out.Reset()
		if err := Run(ctx, args(tc.id, d.SHA256), &out, &diag); err == nil || calls.Load() != count {
			t.Fatal("capture identity retried model request", err)
		}
		originalCapture := capture
		capture = filepath.Join(root, tc.id+"-replacement")
		out.Reset()
		if err := Run(ctx, args(tc.id, d.SHA256), &out, &diag); err == nil || calls.Load() != count {
			t.Fatal("retained review ID triggered another model request", err)
		}
		if _, err := os.Stat(capture); !os.IsNotExist(err) {
			t.Fatal("retained review created replacement acquisition")
		}
		capture = originalCapture
		info, err := os.Stat(capture)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("capture privacy lost", err)
		}
		for _, name := range []string{"source.json", "model-1.json", "model-2.json", "model-3.json", "review-intent.json", "review-response.json", "complete.json"} {
			info, err := os.Stat(filepath.Join(capture, name))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("receipt missing/private", name, err)
			}
		}
	}
	var before storage.DecisionReviewCatalog
	if err := requestBounded(ctx, base, "/v1/paper-decision-reviews", nil, &before, nil, 1<<20, true); err != nil {
		t.Fatal(err)
	}
	capture = filepath.Join(root, "provider-outage")
	status = 503
	var out, diag bytes.Buffer
	if err := Run(ctx, args("model-outage", d.SHA256), &out, &diag); err == nil || !strings.Contains(out.String(), "http_failed") {
		t.Fatal("provider failure not retained", out.String(), err)
	}
	var failed decisiondraft.Receipt
	raw, err := os.ReadFile(filepath.Join(capture, "model-3.json"))
	if err != nil || json.Unmarshal(raw, &failed) != nil || failed.Status != "http_failed" || failed.Response == nil {
		t.Fatal("failed attempt lost", err)
	}
	var after storage.DecisionReviewCatalog
	if err := requestBounded(ctx, base, "/v1/paper-decision-reviews", nil, &after, nil, 1<<20, true); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("outage created review or changed owner", err)
	}
	capture = filepath.Join(root, "wrong-source")
	count := calls.Load()
	out.Reset()
	if err := Run(ctx, args("wrong-source", strings.Repeat("f", 64)), &out, &diag); err == nil || calls.Load() != count {
		t.Fatal("wrong pin reached model", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("wrong source created an acquisition")
	}
	// Existing finalized readbacks still work after model outage; no background
	// model client or service worker was installed by this command.
	if _, err := fetchDecisionInspection(ctx, base, d, "e"); err != nil {
		t.Fatal("outage interrupted deterministic owner inspection", err)
	}
}
