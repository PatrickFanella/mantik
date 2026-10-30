package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestLocalCaptureCLIAndStrictHTTP(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	api := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(s, "local-capture-token")))
	defer api.Close()
	t.Setenv("MANTIK_API_TOKEN", "local-capture-token")
	var calls atomic.Int32
	model := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte("private diagnostic"))
	})))
	root := t.TempDir()
	dir := filepath.Join(root, "finished")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	_, e := extraction.RunLocal(ctx, news.FixtureArticle(), "event", extraction.LocalConfig{Endpoint: model.URL, Model: "ollama/fixture", MaxTokens: 512}, "", func(r extraction.LocalReceipt) error { return retainLocalReceipt(dir, r) })
	model.Close()
	if e != nil {
		t.Fatal(e)
	}
	p, f, e := loadLocalReceipts(dir)
	if e != nil {
		t.Fatal(e)
	}
	unknown := filepath.Join(root, "unknown")
	if e = os.Mkdir(unknown, 0700); e != nil {
		t.Fatal(e)
	}
	if e = retainLocalReceipt(unknown, p); e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	run := func(args ...string) extraction.LocalCapture {
		t.Helper()
		out.Reset()
		if e := Run(ctx, append(args, "--api-url", api.URL), &out, &errs); e != nil {
			t.Fatal(e)
		}
		var r extraction.LocalCapture
		if json.Unmarshal(out.Bytes(), &r) != nil || r.Validate() != nil {
			t.Fatal("invalid capture output")
		}
		return r
	}
	args := []string{"news-local-import", "--id", "one", "--receiver-id", receiver.ID, "--dir", dir}
	done := run(args...)
	if done.Inspection.State != "finished" || done.Finished.Result.Outcome != "abstain" {
		t.Fatal(done)
	}
	if retry := run(args...); !reflect.DeepEqual(retry, done) {
		t.Fatal("retry changed import")
	}
	if got := run("news-local-capture", "--id", "one"); !reflect.DeepEqual(got, done) {
		t.Fatal("read changed evidence")
	}
	pending := run("news-local-import", "--id", "unknown", "--receiver-id", receiver.ID, "--dir", unknown)
	if pending.Inspection.State != "prepared_only" || pending.Inspection.Outcome != "" {
		t.Fatal("invented completion")
	}
	out.Reset()
	if e = Run(ctx, []string{"news-local-captures", "--api-url", api.URL}, &out, &errs); e != nil {
		t.Fatal(e)
	}
	var h extraction.LocalCaptureHistory
	if json.Unmarshal(out.Bytes(), &h) != nil || h.Validate() != nil || h.CaptureCount != 2 {
		t.Fatal("invalid history")
	}
	body := struct {
		ReceiverID string                   `json:"receiver_id"`
		RequestID  string                   `json:"request_id"`
		Prepared   extraction.LocalReceipt  `json:"prepared"`
		Finished   *extraction.LocalReceipt `json:"finished,omitempty"`
	}{receiver.ID, "one", p, f}
	raw, _ := json.Marshal(body)
	for _, tc := range []struct {
		method, path, body string
		status             int
		auth               bool
	}{
		{"GET", "/v1/news-local-captures", "", 401, false},
		{"GET", "/v1/news-local-captures?state=finished", "", 422, true},
		{"GET", "/v1/news-local-captures/absent", "", 404, true},
		{"GET", "/v1/news-local-captures/one?extra=1", "", 422, true},
		{"POST", "/v1/news-local-captures", `{"receiver_id":"x","receiver_id":"y"}`, 400, true},
		{"POST", "/v1/news-local-captures", `{"Receiver_ID":"x"}`, 400, true},
		{"POST", "/v1/news-local-captures", `{"qualification_credit":true}`, 400, true},
		{"POST", "/v1/news-local-captures", string(raw) + "{}", 400, true},
		{"POST", "/v1/news-local-captures", `{}`, 422, true},
	} {
		req, e := http.NewRequest(tc.method, api.URL+tc.path, bytes.NewBufferString(tc.body))
		if e != nil {
			t.Fatal(e)
		}
		if tc.auth {
			req.Header.Set("Authorization", "Bearer local-capture-token")
		}
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatal(tc.path, response.StatusCode, tc.status)
		}
	}
	// A finished capture cannot rewrite an immutable unknown import.
	out.Reset()
	if e = Run(ctx, []string{"news-local-import", "--id", "unknown", "--receiver-id", receiver.ID, "--dir", dir, "--api-url", api.URL}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("unknown overwritten")
	}
	// Malformed or unsafe local files fail before the service is contacted.
	if e = os.WriteFile(filepath.Join(unknown, "finished.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e = Run(ctx, []string{"news-local-import", "--id", "bad", "--receiver-id", receiver.ID, "--dir", unknown, "--api-url", api.URL}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("malformed completion accepted")
	}
	// Caller-reported local captures have no route into recorded-fixture approval.
	if _, e = s.Extraction(ctx, "one"); e == nil {
		t.Fatal("import became recorded fixture")
	}
	corrupt := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bad := done
		bad.ExecutionAttested = true
		_ = json.NewEncoder(w).Encode(bad)
	})))
	defer corrupt.Close()
	out.Reset()
	if e = Run(ctx, []string{"news-local-capture", "--id", "one", "--api-url", corrupt.URL}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("forged service output printed")
	}
	if calls.Load() != 1 {
		t.Fatal("import contacted model endpoint")
	}
}
