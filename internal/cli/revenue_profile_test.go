package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestRevenueProfileCLIFlagsPrivateReplayAndOfflineFence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	a := news.FixtureArticle()
	a.Schema = news.DiagnosticArticleSchema
	a.PublishedAt = time.Time{}
	a.Publication = &news.PublicationPrecision{Precision: "unknown"}
	a.EvidenceClass = "recorded_market_unqualified"
	raw, _ := json.Marshal(a)
	path := filepath.Join(root, "article.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		messages := req["messages"].([]any)
		if !strings.Contains(messages[0].(map[string]any)["content"].(string), "Task profile: reported-revenue-v1") {
			t.Error("missing profile")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "usage": map[string]int{}, "model": extraction.SwitchyardModel, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"outcome":"abstain","reason_code":"no_relevant_claims","claims":[]}`}}}})
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	capture := filepath.Join(root, "capture")
	args := []string{"news-extract-switchyard", "--article", path, "--endpoint", server.URL, "--model", extraction.SwitchyardModel, "--event-key", "revenue", "--profile", extraction.RevenueProfile, "--output-dir", capture}
	if err := Run(ctx, args, &out, &errs); err != nil {
		t.Fatal(err, errs.String())
	}
	if calls.Load() != 1 {
		t.Fatal("request count")
	}
	server.Close()
	var receipt extraction.SwitchyardReceipt
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt.Schema != extraction.RevenueSchema || receipt.Config.Profile != extraction.RevenueProfile {
		t.Fatal("wrong receipt")
	}
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-inspect", "--dir", capture}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var inspection extraction.SwitchyardInspection
	if json.Unmarshal(out.Bytes(), &inspection) != nil || inspection.Schema != extraction.RevenueInspectionSchema {
		t.Fatal("wrong inspection")
	}
	manifest := switchyardEvaluationManifest{Schema: switchyardEvaluationManifestSchema, Cases: []switchyardEvaluationManifestCase{{ID: "revenue", CaptureDir: capture, Expected: extraction.Response{Outcome: "abstain", ReasonCode: "no_relevant_claims", Claims: []extraction.Claim{}}}}}
	raw, _ = json.Marshal(manifest)
	mf := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(mf, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-evaluate", "--manifest", mf}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var result extraction.SwitchyardEvaluation
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Counts.Compared.ExactCases != 1 || result.QualificationCredit {
		t.Fatal("evaluation")
	}
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-import", "--dir", capture, "--receiver-id", "receiver-abcdefghijklmnop", "--id", "profile", "--api-url", server.URL}, &out, &errs); err == nil || out.Len() != 0 {
		t.Fatal("import accepted")
	}
	// Unsupported profile/limit/context is rejected before directory creation.
	for _, tc := range []struct {
		name    string
		profile string
		tokens  string
		article news.Article
	}{{"unknown", "unknown", "1024", a}, {"limit", extraction.RevenueProfile, "512", a}, {"exact", extraction.RevenueProfile, "1024", news.FixtureArticle()}} {
		raw, _ = json.Marshal(tc.article)
		file := filepath.Join(root, tc.name+".json")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, tc.name+"-must-not-exist")
		out.Reset()
		if err := Run(ctx, []string{"news-extract-switchyard", "--article", file, "--event-key", "revenue", "--endpoint", server.URL, "--model", extraction.SwitchyardModel, "--profile", tc.profile, "--max-tokens", tc.tokens, "--output-dir", dir}, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid profile flags accepted", tc.name)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid context created output", tc.name)
		}
	}
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err == nil || calls.Load() != 1 {
		t.Fatal("existing output retried")
	}
}
