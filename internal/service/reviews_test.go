package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestRetainedReviewAPI(t *testing.T) {
	s := storage.NewMemory()
	ctx := context.Background()
	r := news.Fixture()
	clock := &fakeClock{at: r.ReceivedAt.Add(time.Hour)}
	catalog, _ := s.NewsCases(ctx)
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := receipt.Matches[0].CaseID
	h := HandlerWithClock(s, "review-test-token", clock)
	call := func(method, path string, body any, auth bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if auth {
			req.Header.Set("Authorization", "Bearer review-test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	body := map[string]any{"receiver_id": catalog.ReceiverID, "request_id": "first", "case_id": caseID, "version": 1}
	if w := call("POST", "/v1/news-reviews", body, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	body["pin"] = map[string]string{"risk_policy": "widen"}
	if w := call("POST", "/v1/news-reviews", body, true); w.Code != 400 {
		t.Fatal("client pin accepted", w.Code)
	}
	delete(body, "pin")
	w := call("POST", "/v1/news-reviews", body, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var started review.View
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	if started.Attempt.Pin.Subject.CaseID != caseID || started.ReviewApplicable {
		t.Fatal(started)
	}
	finish := map[string]any{"receiver_id": catalog.ReceiverID, "steps": []map[string]any{{"kind": "proposal", "proposal": map[string]any{"outcome": "abstain", "reason_codes": []string{"insufficient_evidence"}, "uncertainty": "unverified"}}}}
	finish["result"] = map[string]string{"authority": "execute"}
	if w = call("POST", "/v1/news-reviews/first/complete", finish, true); w.Code != 400 {
		t.Fatal("client result accepted", w.Code)
	}
	delete(finish, "result")
	if w = call("POST", "/v1/news-reviews/first/complete", finish, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var completed review.View
	_ = json.Unmarshal(w.Body.Bytes(), &completed)
	if completed.Attempt.Status != "completed" || !completed.ReviewApplicable || completed.Attempt.Result.Authority != "research_only_unqualified" || completed.Attempt.Result.QualificationCredit {
		t.Fatal(completed)
	}
	r.SourceID = "new-source"
	if _, err = s.AdmitNews(ctx, catalog.ReceiverID, r, clock.Now); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/v1/news-reviews/first", nil, true)
	_ = json.Unmarshal(w.Body.Bytes(), &completed)
	if w.Code != 200 || completed.EvidenceCurrent || completed.ReviewApplicable {
		t.Fatal("stale readback", w.Code)
	}
	if w = call("GET", "/v1/news-reviews/first", nil, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = call("GET", "/v1/news-reviews/missing", nil, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	body["receiver_id"] = "receiver-abcdefghijklmnop"
	body["request_id"] = "wrong"
	if w = call("POST", "/v1/news-reviews", body, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
}
