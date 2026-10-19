package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestDiagnosticPublicationCLIStaysOfflineAfterCapture(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	a := news.FixtureArticle()
	a.Schema = news.DiagnosticArticleSchema
	a.PublishedAt = time.Time{}
	a.EvidenceClass = "recorded_market_unqualified"
	a.Publication = &news.PublicationPrecision{Precision: "date_only", Date: "2025-05-01"}
	raw, _ := json.Marshal(a)
	article := filepath.Join(root, "article.json")
	if err := os.WriteFile(article, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		if json.NewDecoder(r.Body).Decode(&req) != nil || req["max_tokens"] != float64(1024) || req["reasoning_effort"] != "low" {
			t.Error("request bounds")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "usage": map[string]int{}, "model": extraction.SwitchyardModel, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"outcome":"abstain","reason_code":"ambiguous_source","claims":[]}`}}}})
	}))
	defer model.Close()
	dir := filepath.Join(root, "capture")
	var out, errs bytes.Buffer
	if err := Run(ctx, []string{"news-extract-switchyard", "--article", article, "--event-key", "diagnostic", "--endpoint", model.URL, "--model", extraction.SwitchyardModel, "--output-dir", dir}, &out, &errs); err != nil {
		t.Fatal(err, errs.String())
	}
	if calls.Load() != 1 {
		t.Fatal("request count", calls.Load())
	}
	model.Close()
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-inspect", "--dir", dir}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var inspection extraction.SwitchyardInspection
	if json.Unmarshal(out.Bytes(), &inspection) != nil || inspection.Schema != extraction.SwitchyardDiagnosticInspectionSchema {
		t.Fatal("diagnostic inspection")
	}
	manifest := switchyardEvaluationManifest{Schema: switchyardEvaluationManifestSchema, Cases: []switchyardEvaluationManifestCase{{ID: "diagnostic", CaptureDir: dir, Expected: extraction.Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []extraction.Claim{}}}}}
	raw, _ = json.Marshal(manifest)
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-evaluate", "--manifest", path}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var report extraction.SwitchyardEvaluation
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Counts.Compared.ExactCases != 1 || report.QualificationCredit {
		t.Fatal("evaluation counts", report.Counts)
	}
	var requests atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer service.Close()
	out.Reset()
	if err := Run(ctx, []string{"news-switchyard-import", "--api-url", service.URL, "--receiver-id", "receiver-abcdefghijklmnop", "--id", "diagnostic", "--dir", dir}, &out, &errs); err == nil || out.Len() != 0 || requests.Load() != 0 {
		t.Fatal("diagnostic reached service", err, requests.Load())
	}
}
