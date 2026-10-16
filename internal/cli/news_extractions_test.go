package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestRetainedExtractionCLIAndStrictHTTP(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	server := httptest.NewServer(service.Handler(s, "extraction-test-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "extraction-test-token")
	dir := t.TempDir()
	article, fixture := filepath.Join(dir, "article.json"), filepath.Join(dir, "model.bin")
	raw, _ := json.Marshal(news.FixtureArticle())
	if e := os.WriteFile(article, raw, 0600); e != nil {
		t.Fatal(e)
	}
	model := []byte{0xff, 0xfe}
	if e := os.WriteFile(fixture, model, 0600); e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	run := func(args ...string) extraction.Attempt {
		t.Helper()
		out.Reset()
		if e := Run(ctx, append(args, "--api-url", server.URL), &out, &errs); e != nil {
			t.Fatal(e)
		}
		var a extraction.Attempt
		if json.Unmarshal(out.Bytes(), &a) != nil || a.Validate() != nil {
			t.Fatal("invalid CLI receipt")
		}
		return a
	}
	startArgs := []string{"news-extraction-start", "--id", "one", "--receiver-id", receiver.ID, "--article", article, "--event-key", "event"}
	pending := run(startArgs...)
	if pending.Status != "pending" {
		t.Fatal(pending)
	}
	done := run("news-extraction-complete", "--id", "one", "--receiver-id", receiver.ID, "--model-fixture", fixture)
	if done.Status != "completed" || done.Result.Outcome != "abstain" || done.Result.Artifact.RawBase64 != base64.StdEncoding.EncodeToString(model) {
		t.Fatal(done)
	}
	if retry := run(startArgs...); !reflect.DeepEqual(retry, done) {
		t.Fatal("start retry lost completion")
	}
	if got := run("news-extraction", "--id", "one"); !reflect.DeepEqual(got, done) {
		t.Fatal("targeted read changed receipt")
	}
	out.Reset()
	if e := Run(ctx, []string{"news-extractions", "--api-url", server.URL}, &out, &errs); e != nil {
		t.Fatal(e)
	}
	var h extraction.History
	if json.Unmarshal(out.Bytes(), &h) != nil || h.Validate() != nil || h.AttemptCount != 1 {
		t.Fatal("incomplete history")
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
		auth               bool
	}{
		{"GET", "/v1/news-extractions", "", 401, false},
		{"GET", "/v1/news-extractions?status=completed", "", 422, true},
		{"GET", "/v1/news-extractions/unknown", "", 404, true},
		{"POST", "/v1/news-extractions", `{"receiver_id":"x","receiver_id":"y"}`, 400, true},
		{"POST", "/v1/news-extractions", `{"Receiver_ID":"x"}`, 400, true},
		{"POST", "/v1/news-extractions", `{"contract":{}}`, 400, true},
		{"POST", "/v1/news-extractions/one/complete", `{"receiver_id":"x","result":{}}`, 400, true},
		{"POST", "/v1/news-extractions/one/complete", `{"receiver_id":"x","artifact_base64":null}`, 422, true},
		{"POST", "/v1/news-extractions/one/complete", `{"receiver_id":"x"}`, 422, true},
		{"POST", "/v1/news-extractions/one/complete", `{"receiver_id":"x","artifact_base64":"/w==\n"}`, 422, true},
		{"POST", "/v1/news-extractions/one/complete", `{"receiver_id":"x","artifact_base64":"wrong"}`, 422, true},
	} {
		req, e := http.NewRequest(tc.method, server.URL+tc.path, bytes.NewBufferString(tc.body))
		if e != nil {
			t.Fatal(e)
		}
		if tc.auth {
			req.Header.Set("Authorization", "Bearer extraction-test-token")
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatal(tc.path, tc.body, res.StatusCode, tc.status)
		}
	}
	// A hash-valid-looking but altered server result must not reach CLI output.
	bad := done
	bad.Result.QualificationCredit = true
	corrupt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(bad) }))
	defer corrupt.Close()
	out.Reset()
	if e := Run(ctx, []string{"news-extraction", "--id", "one", "--api-url", corrupt.URL}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("corrupt result printed")
	}
}
