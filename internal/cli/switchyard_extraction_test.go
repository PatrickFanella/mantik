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

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestSwitchyardCLIPrivateEvidenceAndNoImplicitRetry(t *testing.T) {
	root := t.TempDir()
	article := filepath.Join(root, "article.json")
	raw, _ := json.Marshal(news.FixtureArticle())
	if err := os.WriteFile(article, raw, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "attempt")
	var calls atomic.Int32
	response := extraction.FixtureResponse()
	quoted := extraction.QuoteResponse{Outcome: response.Outcome, ReasonCode: response.ReasonCode, Claims: []extraction.QuoteClaim{}}
	for _, c := range response.Claims {
		q := extraction.QuoteClaim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []extraction.Quote{}}
		for _, e := range c.Evidence {
			q.Evidence = append(q.Evidence, extraction.Quote{Quote: e.Quote})
		}
		quoted.Claims = append(quoted.Claims, q)
	}
	content, _ := json.Marshal(quoted)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := os.Stat(filepath.Join(dir, "prepared.json")); err != nil {
			t.Error("request before prepared file")
		}
		if r.Header.Get("Authorization") != "Bearer switchyard-private-token" {
			t.Error("missing token")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "openai/gpt-6.1-sol", "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	}))
	defer server.Close()
	t.Setenv("MANTIK_SWITCHYARD_MODEL_TOKEN", "switchyard-private-token")
	args := []string{"news-extract-switchyard", "--article", article, "--endpoint", server.URL, "--model", "openai/gpt-6.1-sol", "--event-key", "event", "--output-dir", dir}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var receipt extraction.LocalReceipt
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt.Result.Outcome != "extracted" || receipt.QualificationCredit || calls.Load() != 1 {
		t.Fatal("wrong local receipt", out.String())
	}
	if receipt.Schema != extraction.SwitchyardSchema || receipt.ModelArtifact == nil || receipt.Anchoring == nil || receipt.ModelArtifact.SHA256 != receipt.Anchoring.ModelArtifactSHA256 || receipt.Anchoring.CandidateArtifactSHA256 != receipt.Result.Artifact.SHA256 {
		t.Fatal("CLI lost quote derivation provenance")
	}
	for _, name := range []string{"prepared.json", "finished.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("receipt permissions", err)
		}
		b, err := os.ReadFile(path)
		if err != nil || bytes.Contains(b, []byte("switchyard-private-token")) {
			t.Fatal("receipt missing or token leaked", err)
		}
		if name == "finished.json" {
			var saved extraction.LocalReceipt
			if json.Unmarshal(b, &saved) != nil || saved.SHA256 != receipt.SHA256 {
				t.Fatal("output differs from retained result")
			}
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
	server.Close()
	out.Reset()
	if err := Run(context.Background(), []string{"news-switchyard-inspect", "--dir", dir}, &out, &errs); err != nil {
		t.Fatal("offline inspection", err)
	}
	first := append([]byte(nil), out.Bytes()...)
	out.Reset()
	if err := Run(context.Background(), []string{"news-switchyard-inspect", "--dir", dir}, &out, &errs); err != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("offline replay differs", err)
	}
	var report extraction.SwitchyardInspection
	if json.Unmarshal(first, &report) != nil || report.Schema != extraction.SwitchyardInspectionSchema || report.ExecutionAttested || report.QualificationCredit {
		t.Fatal("inspection authority")
	}
	out.Reset()
	if err := Run(context.Background(), []string{"news-local-inspect", "--dir", dir}, &out, &errs); err == nil || out.Len() != 0 {
		t.Fatal("provider receipts accepted by local inspector", err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "prepared.json"))
	out.Reset()
	if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 || calls.Load() != 1 {
		t.Fatal("existing output retried", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "prepared.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing intent overwritten")
	}
	for _, flags := range [][]string{{}, {"--article", article, "--endpoint", server.URL, "--model", "cloud/model", "--event-key", "event", "--output-dir", filepath.Join(root, "invalid")}, {"--article", article, "--endpoint", server.URL, "--model", "openai/gpt-6.1-sol", "--event-key", "event", "--output-dir", "relative"}, {"--api-url", "http://127.0.0.1:41890"}} {
		out.Reset()
		if err := Run(context.Background(), append([]string{"news-extract-switchyard"}, flags...), &out, &errs); err == nil || out.Len() != 0 || calls.Load() != 1 {
			t.Fatal("bad flags sent request", err)
		}
	}
	if strings.Contains(errs.String(), "switchyard-private-token") {
		t.Fatal("token echoed")
	}
}
