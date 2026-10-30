package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestSwitchyardEvaluationCLIPrivateManifestAndNoNetworkOrWrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	var status, calls atomic.Int32
	status.Store(200)
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
	model := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if status.Load() != 200 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private diagnostic"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": extraction.SwitchyardModel, "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	})))
	defer model.Close()
	capture := func(name string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		_, e := extraction.RunSwitchyard(ctx, news.FixtureArticle(), "event", extraction.SwitchyardConfig{Endpoint: model.URL, Model: extraction.SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r extraction.SwitchyardReceipt) error { return retainLocalReceipt(dir, extraction.LocalReceipt(r)) })
		if e != nil {
			t.Fatal(e)
		}
		return dir
	}
	good := capture("good")
	status.Store(503)
	failed := capture("failed")
	unknown := filepath.Join(root, "unknown")
	if e := os.Mkdir(unknown, 0700); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(good, "prepared.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(unknown, "prepared.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	m := switchyardEvaluationManifest{Schema: switchyardEvaluationManifestSchema, Cases: []switchyardEvaluationManifestCase{{"good", good, f}, {"failed", failed, f}, {"unknown", unknown, f}}}
	write := func(name string, m any, mode os.FileMode) string {
		t.Helper()
		b, _ := json.Marshal(m)
		p := filepath.Join(root, name)
		if e := os.WriteFile(p, b, mode); e != nil {
			t.Fatal(e)
		}
		if e := os.Chmod(p, mode); e != nil {
			t.Fatal(e)
		}
		return p
	}
	path := write("manifest.json", m, 0600)
	before := map[string]string{}
	for _, dir := range []string{good, failed, unknown} {
		entries, e := os.ReadDir(dir)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			p := filepath.Join(dir, entry.Name())
			b, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			before[p] = string(b)
		}
	}
	for _, entry := range m.Cases {
		p, f, e := loadSwitchyardReceipts(entry.CaptureDir)
		if e != nil {
			t.Fatalf("fixture load %s: %v", entry.ID, e)
		}
		if _, e = extraction.InspectSwitchyard(ctx, p, f); e != nil {
			t.Fatalf("fixture inspection %s: %v", entry.ID, e)
		}
	}
	var out, errs bytes.Buffer
	args := []string{"news-switchyard-evaluate", "--manifest", path}
	if e := Run(ctx, args, &out, &errs); e != nil {
		t.Fatal(e)
	}
	var r extraction.SwitchyardEvaluation
	if json.Unmarshal(out.Bytes(), &r) != nil || r.Counts.Cases != 3 || r.Counts.FinishedCases != 2 || r.Counts.UnknownCases != 1 || r.Counts.Compared.ExactCases != 1 || r.Cases[2].Comparison != nil || r.Cases[1].Comparison.Actual.ReasonCode != "extractor_unavailable" || calls.Load() != 2 {
		t.Fatal("incorrect complete local evaluation")
	}
	first := append([]byte(nil), out.Bytes()...)
	model.Close()
	out.Reset()
	if e := Run(ctx, args, &out, &errs); e != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("offline replay changed", e)
	}
	for p, b := range before {
		after, e := os.ReadFile(p)
		if e != nil || string(after) != b {
			t.Fatal("evaluation rewrote capture")
		}
	}
	for _, flags := range [][]string{{}, {"--manifest", path, "extra"}, {"--manifest", path, "--api-url", model.URL}, {"--manifest", path, "--only-passed"}, {"--manifest", "relative"}, {"--manifest", root}} {
		out.Reset()
		if e := Run(ctx, append([]string{"news-switchyard-evaluate"}, flags...), &out, &errs); e == nil || out.Len() != 0 {
			t.Fatal("unsafe arguments emitted partial report", e)
		}
	}
	for name, mutate := range map[string]func(*switchyardEvaluationManifest){
		"schema":           func(m *switchyardEvaluationManifest) { m.Schema = "unknown" },
		"empty":            func(m *switchyardEvaluationManifest) { m.Cases = nil },
		"duplicate":        func(m *switchyardEvaluationManifest) { m.Cases[1].ID = m.Cases[0].ID },
		"snapshot":         func(m *switchyardEvaluationManifest) { m.Cases[1].CaptureDir = m.Cases[0].CaptureDir },
		"bad-gold":         func(m *switchyardEvaluationManifest) { m.Cases[2].Expected.Claims = nil },
		"relative-capture": func(m *switchyardEvaluationManifest) { m.Cases[2].CaptureDir = "relative" },
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			var copy switchyardEvaluationManifest
			json.Unmarshal(b, &copy)
			mutate(&copy)
			p := write(name+".json", copy, 0600)
			out.Reset()
			if e := Run(ctx, []string{"news-switchyard-evaluate", "--manifest", p}, &out, &errs); e == nil || out.Len() != 0 {
				t.Fatal("invalid manifest emitted partial report", e)
			}
		})
	}
	// Manifest files share the capture loader's ownership, mode and file guards.
	public := write("public.json", m, 0644)
	link := filepath.Join(root, "link.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	fifo := filepath.Join(root, "fifo.json")
	if e := syscall.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	big := filepath.Join(root, "big.json")
	file, e := os.OpenFile(big, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	file.Truncate(extraction.MaxEvaluationBytes + 1)
	file.Close()
	for _, p := range []string{public, link, fifo, big} {
		out.Reset()
		if e := Run(ctx, []string{"news-switchyard-evaluate", "--manifest", p}, &out, &errs); e == nil || out.Len() != 0 {
			t.Fatal("unsafe manifest accepted", p, e)
		}
	}
	for i, raw := range []string{`{"schema":"x","schema":"y"}`, `{"Schema":"x"}`, `{"schema":"x","secret-do-not-echo":true}`, `{} {}`} {
		p := filepath.Join(root, strings.Repeat("j", i+1)+".json")
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		out.Reset()
		if e := Run(ctx, []string{"news-switchyard-evaluate", "--manifest", p}, &out, &errs); e == nil || out.Len() != 0 || strings.Contains(e.Error(), "secret-do-not-echo") {
			t.Fatal("ambiguous JSON accepted or leaked", e)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	out.Reset()
	if e := Run(cancelled, args, &out, &errs); e != context.Canceled || out.Len() != 0 {
		t.Fatal("cancelled evaluation emitted report")
	}
	if calls.Load() != 2 {
		t.Fatal("evaluation contacted model endpoint")
	}
}
