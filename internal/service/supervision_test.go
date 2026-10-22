package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func supervisionRequest(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func toolBody(tool string, args any) string {
	b, _ := json.Marshal(supervisionCall{Tool: tool, Arguments: mustSupervisionJSON(args)})
	return string(b)
}
func mustSupervisionJSON(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

func TestSupervisionResearchOwnerRetryAndInspection(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	fixture := news.Fixture()
	clock := newFakeClock(fixture.ReceivedAt.Add(time.Hour))
	catalog, _ := s.NewsCases(ctx)
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, fixture, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	h := HandlerWithClock(s, "operator", clock)
	args := researchRequestArguments{ReceiverID: catalog.ReceiverID, RequestID: "operator-research", CaseID: receipt.Matches[0].CaseID, Version: 1}
	call := toolBody("request_research", args)
	if w := supervisionRequest(h, "POST", "/v1/supervision/calls", call, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	first := supervisionRequest(h, "POST", "/v1/supervision/calls", call, "operator")
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var view review.View
	if err := json.Unmarshal(first.Body.Bytes(), &view); err != nil || view.Attempt.Pin.Subject.CaseID != args.CaseID || view.ReviewApplicable {
		t.Fatal(err, view)
	}
	direct := supervisionRequest(h, "POST", "/v1/news-reviews", string(mustSupervisionJSON(args)), "operator")
	if direct.Code != 200 || !bytes.Equal(first.Body.Bytes(), direct.Body.Bytes()) {
		t.Fatal("owner retry diverged", direct.Body.String())
	}
	read := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("inspect_record", inspectArguments{Kind: "research_review", ID: args.RequestID}), "operator")
	if read.Code != 200 || !bytes.Equal(read.Body.Bytes(), direct.Body.Bytes()) {
		t.Fatal("inspection diverged", read.Code, read.Body.String())
	}
	recordRead := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("inspect_record", inspectArguments{Kind: "news_record", ID: receipt.RecordKey}), "operator")
	recordDirect := supervisionRequest(h, "GET", "/v1/news-recordings/"+receipt.RecordKey, "", "operator")
	if recordRead.Code != 200 || !bytes.Equal(recordRead.Body.Bytes(), recordDirect.Body.Bytes()) {
		t.Fatal("retained source read diverged", recordRead.Code, recordRead.Body.String())
	}
	args.ReceiverID = "receiver-abcdefghijklmnop"
	args.RequestID = "wrong-owner"
	if w := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("request_research", args), "operator"); w.Code != 409 {
		t.Fatal("receiver check lost", w.Code)
	}
	args.ReceiverID = catalog.ReceiverID
	args.RequestID = "stale-version"
	args.Version = 2
	if w := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("request_research", args), "operator"); w.Code != 409 {
		t.Fatal("evidence version check lost", w.Code, w.Body.String())
	}
}

func TestSupervisionRejectsUnboundedOrInventedAuthority(t *testing.T) {
	h := Handler(storage.NewMemory(), "operator")
	bad := []string{
		`{"tool":"request_research","tool":"inspect_record","arguments":{}}`,
		`{"tool":"request_research","arguments":{"version":1,"version":2}}`,
		`{"tool":"inspect_record","arguments":{"kind":"decision","id":"../status"}}`,
		`{"tool":"inspect_record","arguments":{"kind":"decision","id":"a%2fb"}}`,
		`{"tool":"inspect_record","arguments":{"kind":"decision","id":"a?x=y"}}`,
		`{"tool":"inspect_record","arguments":{"kind":"balances","id":"one"}}`,
		`{"tool":"inspect_record","arguments":{"kind":"news_case","id":"one"}}`,
		`{"tool":"recheck_exception","arguments":{"exception_id":"one","request_id":"x","operation":"retry-protection"}}`,
		`{"tool":"request_research","arguments":{"admission":true}}`,
		`{"tool":"request_research","arguments":null}`,
		`{"tool":"request_research","arguments":{}} {}`,
		`{"tool":"execute","arguments":{}}`,
		`{"tool":"inspect_record","arguments":{},"url":"https://example.com"}`,
		strings.Repeat("x", maxSupervisionCallBytes+1),
	}
	for _, body := range bad {
		if w := supervisionRequest(h, "POST", "/v1/supervision/calls", body, "operator"); w.Code != 422 {
			t.Fatalf("accepted %q: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/supervision/calls?", "/v1/supervision/calls?x=1"} {
		if w := supervisionRequest(h, "POST", path, `{}`, "operator"); w.Code != 422 {
			t.Fatal(w.Code)
		}
	}
	if w := supervisionRequest(h, "GET", "/v1/supervision/tools", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := supervisionRequest(h, "GET", "/v1/supervision/tools?", "", "operator"); w.Code != 422 {
		t.Fatal(w.Code)
	}
	// A store without optional owners must not advertise their write tools.
	minimal := Handler(&outageStore{Store: storage.NewMemory()}, "operator")
	w := supervisionRequest(minimal, "GET", "/v1/supervision/tools", "", "operator")
	if w.Code != 200 || strings.Contains(w.Body.String(), "request_research") {
		t.Fatal(w.Body.String())
	}
	if w = supervisionRequest(minimal, "POST", "/v1/supervision/calls", toolBody("request_research", researchRequestArguments{}), "operator"); w.Code != 422 {
		t.Fatal(w.Code)
	}
}

func TestSupervisionRecheckRetainsOwnerReceiptWithoutFill(t *testing.T) {
	ctx := context.Background()
	s := storage.NewQueuedMemory()
	q := workflow.Fixtures()[1]
	if _, err := s.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(q.ReceivedAt.Add(31 * time.Second))
	if err := RunActionCycle(ctx, s, "worker", clock, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"action_delivery", "protective_monitoring"} {
		if err := s.ReportWorker(ctx, domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: capability, WorkerID: "worker-" + capability, At: clock.Now(), IntervalMillis: 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	h := HandlerWithClock(s, "operator", clock)
	w := supervisionRequest(h, "GET", "/v1/operations", "", "operator")
	var ops domain.OperationsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &ops); err != nil || len(ops.Exceptions) != 1 {
		t.Fatal(err, w.Body.String())
	}
	id := ops.Exceptions[0].ID
	body := toolBody("recheck_exception", recheckArguments{ExceptionID: id, RequestID: "operator-recheck"})
	first := supervisionRequest(h, "POST", "/v1/supervision/calls", body, "operator")
	retry := supervisionRequest(h, "POST", "/v1/operations/exceptions/"+id+"/recover", `{"request_id":"operator-recheck","operation":"recheck"}`, "operator")
	var initial, repeated domain.RecoveryReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &repeated); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || retry.Code != 200 || initial.Duplicate || !repeated.Duplicate || initial.Attempt != repeated.Attempt || initial.ExceptionID != repeated.ExceptionID {
		t.Fatal(first.Code, first.Body.String(), retry.Body.String())
	}
	final, err := s.Snapshot(ctx)
	if err != nil || final.State.Case.Entry != nil {
		t.Fatal("recheck created fill", err)
	}
}

func TestSupervisionWatchPolicyCASAndRestrictedBehavior(t *testing.T) {
	s := storage.NewMemory()
	h := HandlerWithClock(s, "operator", newFakeClock(time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)))
	history, err := s.SocialHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := socialwatch.PolicyCommand{ID: "create-watch", ReceiverID: history.ReceiverID, Policy: socialwatch.Policy{ID: "watch-one", Provider: "recorded", AccountID: "account-one", Symbols: []string{"AAPL"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	first := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("change_watch_policy", c), "operator")
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	retry := supervisionRequest(h, "POST", "/v1/social-watches", string(mustSupervisionJSON(c)), "operator")
	if retry.Code != 200 {
		t.Fatal(retry.Code, retry.Body.String())
	}
	read := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("inspect_record", inspectArguments{Kind: "watch", ID: c.Policy.ID}), "operator")
	direct := supervisionRequest(h, "GET", "/v1/social-watches/"+c.Policy.ID, "", "operator")
	if read.Code != 200 || !bytes.Equal(read.Body.Bytes(), direct.Body.Bytes()) {
		t.Fatal(read.Code, read.Body.String())
	}
	c.ID = "stale-update"
	c.Policy.Enabled = false
	if w := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("change_watch_policy", c), "operator"); w.Code != 409 {
		t.Fatal("CAS lost", w.Code, w.Body.String())
	}
	c.ID = "forbidden-rule"
	c.Policy.Behavior = "configured_rule"
	c.Policy.RuleID = "some-rule"
	if w := supervisionRequest(h, "POST", "/v1/supervision/calls", toolBody("change_watch_policy", c), "operator"); w.Code != 422 {
		t.Fatal("rule activation accepted", w.Code)
	}
	after, err := s.SocialHistory(context.Background())
	if err != nil || len(after.History) != 1 {
		t.Fatal(err, after)
	}
}
