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
	"syscall"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestLocalInspectionCLIReadsRetainedEvidenceWithoutNetworkOrWrites(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "capture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := extraction.FixtureResponse()
	q := extraction.QuoteResponse{Outcome: f.Outcome, ReasonCode: f.ReasonCode, Claims: []extraction.QuoteClaim{}}
	for _, c := range f.Claims {
		claim := extraction.QuoteClaim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []extraction.Quote{}}
		for _, s := range c.Evidence {
			claim.Evidence = append(claim.Evidence, extraction.Quote{Quote: s.Quote})
		}
		q.Claims = append(q.Claims, claim)
	}
	content, _ := json.Marshal(q)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "ollama/fixture", "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	}))
	_, err := extraction.RunLocal(context.Background(), news.FixtureArticle(), "event", extraction.LocalConfig{Endpoint: server.URL, Model: "ollama/fixture", MaxTokens: 512}, "", func(r extraction.LocalReceipt) error { return retainLocalReceipt(dir, r) })
	server.Close()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := os.ReadFile(filepath.Join(dir, "prepared.json"))
	if err != nil {
		t.Fatal(err)
	}
	finished, err := os.ReadFile(filepath.Join(dir, "finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	args := []string{"news-local-inspect", "--dir", dir}
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var report extraction.LocalInspection
	if json.Unmarshal(out.Bytes(), &report) != nil || report.State != "finished" || report.ClaimCount != 1 || report.ExecutionAttested || report.QualificationCredit || report.Authority != "retained_local_bytes_consistency_only" || calls.Load() != 1 {
		t.Fatal("wrong offline report", out.String())
	}
	first := append([]byte(nil), out.Bytes()...)
	out.Reset()
	if err := Run(context.Background(), args, &out, &errs); err != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("unstable offline report", err)
	}
	for name, want := range map[string][]byte{"prepared.json": prepared, "finished.json": finished} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(b, want) {
			t.Fatal("inspection altered evidence")
		}
	}
	// Missing completion is unknown; a present malformed completion is an error.
	clone := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "prepared.json"), prepared, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	pending := clone("pending")
	out.Reset()
	if err := Run(context.Background(), []string{"news-local-inspect", "--dir", pending}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	report = extraction.LocalInspection{}
	if json.Unmarshal(out.Bytes(), &report) != nil || report.State != "prepared_only" || report.Disposition != "completion_unknown" || report.Outcome != "" || report.ClaimCount != 0 {
		t.Fatal("completion invented", out.String())
	}
	cases := []struct {
		name  string
		setup func(string)
	}{
		{"malformed", func(p string) {
			os.WriteFile(filepath.Join(p, "finished.json"), []byte(`{"secret-do-not-echo":true}`), 0600)
		}},
		{"symlink", func(p string) { os.Symlink(filepath.Join(dir, "finished.json"), filepath.Join(p, "finished.json")) }},
		{"fifo", func(p string) { syscall.Mkfifo(filepath.Join(p, "finished.json"), 0600) }},
		{"public-file", func(p string) { os.WriteFile(filepath.Join(p, "finished.json"), finished, 0644) }},
		{"oversized", func(p string) {
			f, err := os.Create(filepath.Join(p, "finished.json"))
			if err != nil {
				t.Fatal(err)
			}
			f.Chmod(0600)
			f.Truncate(extraction.MaxLocalReceiptBytes + 1)
			f.Close()
		}},
		{"legacy", func(p string) {
			var r extraction.LocalReceipt
			json.Unmarshal(prepared, &r)
			r.Schema = "mantik-local-news-extraction-v1"
			b, _ := json.Marshal(r)
			os.WriteFile(filepath.Join(p, "prepared.json"), b, 0600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := clone(tc.name)
			tc.setup(p)
			out.Reset()
			errs.Reset()
			err := Run(context.Background(), []string{"news-local-inspect", "--dir", p}, &out, &errs)
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "secret-do-not-echo") {
				t.Fatal("unsafe/invalid evidence emitted report", err)
			}
		})
	}
	rootLink := filepath.Join(root, "link")
	if err := os.Symlink(dir, rootLink); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{}, {"--dir", "relative"}, {"--dir", rootLink}, {"--dir", dir, "--api-url", server.URL}, {"--dir", dir, "extra"}} {
		out.Reset()
		if err := Run(context.Background(), append([]string{"news-local-inspect"}, flags...), &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid arguments emitted report", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != context.Canceled || out.Len() != 0 {
		t.Fatal("cancelled inspection emitted report")
	}
	if calls.Load() != 1 {
		t.Fatal("inspection called model endpoint")
	}
}
