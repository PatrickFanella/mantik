package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestRetainedReviewCLI(t *testing.T) {
	s := storage.NewMemory()
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler(s, "review-cli-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "review-cli-token")
	var out bytes.Buffer
	run := func(args ...string) review.View {
		t.Helper()
		out.Reset()
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(err)
		}
		var view review.View
		if err = json.Unmarshal(out.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	started := run("news-review-start", "--id", "review-one", "--receiver-id", catalog.ReceiverID, "--case", receipt.Matches[0].CaseID, "--version", "1")
	if started.Attempt.Status != "pending" {
		t.Fatal(started)
	}
	fixture := filepath.Join(t.TempDir(), "model.json")
	if err = os.WriteFile(fixture, []byte(`[{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	complete := run("news-review-complete", "--id", "review-one", "--receiver-id", catalog.ReceiverID, "--model-fixture", fixture)
	if !complete.ReviewApplicable || complete.Attempt.Result.Outcome != "abstain" {
		t.Fatal(complete)
	}
	actual := run("news-review", "--id", "review-one")
	if actual.Attempt.Result.ID != complete.Attempt.Result.ID {
		t.Fatal("readback changed result")
	}
	methodArgs := []string{"news-review-start", "--id", "method-review", "--receiver-id", catalog.ReceiverID, "--case", receipt.Matches[0].CaseID, "--version", "1", "--type", investigation.MethodTypeID}
	method := run(methodArgs...)
	if method.Attempt.Profile == nil || method.Attempt.Profile.Validate() != nil || method.Attempt.Profile.Contract.TypeID != investigation.MethodTypeID {
		t.Fatal(method)
	}
	methodDone := run("news-review-complete", "--id", "method-review", "--receiver-id", catalog.ReceiverID, "--model-fixture", fixture)
	if methodDone.Attempt.Result.Schema != investigation.MethodSchema || methodDone.Attempt.Result.ProfileSHA256 != method.Attempt.Profile.SHA256 || len(methodDone.Attempt.Result.Checks) != 6 {
		t.Fatal(methodDone)
	}
	if retry := run(methodArgs...); !reflect.DeepEqual(retry, methodDone) {
		t.Fatal("CLI changed profile retry")
	}
	body := `{"receiver_id":"` + catalog.ReceiverID + `","request_id":"http-profile","case_id":"` + receipt.Matches[0].CaseID + `","version":1,"type_id":"news-method-review-v1"}`
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer review-cli-token")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want)
		}
	}
	out.Reset()
	if err = Run(ctx, []string{"news-reviews", "--case", receipt.Matches[0].CaseID, "--api-url", server.URL}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var history review.History
	if err = json.Unmarshal(out.Bytes(), &history); err != nil || history.Validate() != nil || history.AttemptCount != 2 || history.OutcomeCounts["abstain"] != 2 || history.Attempts[0].ResultID == "" {
		t.Fatal(history, err)
	}
	historyPath := "/v1/news-cases/" + receipt.Matches[0].CaseID + "/reviews"
	call("GET", historyPath, "", false, 401)
	call("GET", historyPath+"?best=true", "", true, 422)
	call("GET", historyPath+"?limit=1", "", true, 422)
	call("POST", historyPath, `{}`, true, 405)
	call("GET", "/v1/news-cases/news-case-"+strings.Repeat("0", 64)+"/reviews", "", true, 404)
	for _, args := range [][]string{{"news-reviews"}, {"news-reviews", "--case", "invalid"}, {"news-reviews", "--case", receipt.Matches[0].CaseID, "--api-url", "https://example.invalid"}} {
		out.Reset()
		if err = Run(ctx, args, &out, &out); err == nil {
			t.Fatal("invalid history arguments accepted")
		}
	}
	call("POST", "/v1/news-reviews", body, false, 401)
	call("POST", "/v1/news-reviews", strings.Replace(body, `"type_id":`, `"profile":{},"type_id":`, 1), true, 400)
	call("POST", "/v1/news-reviews", strings.Replace(body, `"type_id":`, `"type_id":"news-review-v1","type_id":`, 1), true, 400)
	call("POST", "/v1/news-reviews", strings.Replace(body, `"type_id":`, `"Type_ID":`, 1), true, 400)
	call("POST", "/v1/news-reviews", strings.Replace(body, `news-method-review-v1`, `unknown`, 1), true, 422)
	call("POST", "/v1/news-reviews/method-review/complete", `{"receiver_id":"`+catalog.ReceiverID+`","type_id":"news-review-v1","steps":[]}`, true, 400)

	for _, args := range [][]string{{"news-review"}, {"news-review-start", "--id", "empty"}, {"news-review-complete", "--id", "review-one", "--model-fixture", fixture}} {
		out.Reset()
		if err = Run(ctx, args, &out, &out); err == nil {
			t.Fatal("incomplete arguments accepted")
		}
	}
}

func TestReviewHistoryCLIRejectsCorruptReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":"mantik-case-review-history-v1","attempts":[],"qualification_credit":true}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"news-reviews", "--case", "news-case-" + strings.Repeat("0", 64), "--api-url", server.URL}, &out, &out); err == nil || out.Len() != 0 {
		t.Fatal("corrupt report emitted", err)
	}
}
