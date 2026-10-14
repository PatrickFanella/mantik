package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestFeedCollectorAPI(t *testing.T) {
	s := storage.NewMemory()
	clock := &fakeClock{at: time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)}
	h := HandlerWithClock(s, "private-test-token", clock)
	request := func(method, path string, body any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if auth {
			r.Header.Set("Authorization", "Bearer private-test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/v1/feed-collectors", nil, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	initial, _ := s.FeedCollectors(context.Background())
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 30}
	c := feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}
	body := map[string]any{"receiver_id": initial.ReceiverID, "command": c}
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body["receiver_id"] = "receiver-abcdefghijklmnop"
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	body["receiver_id"] = initial.ReceiverID
	body["at"] = clock.at
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 400 {
		t.Fatal("client supplied clock admitted", w.Code)
	}
	delete(body, "at")
	c = feedcollector.Command{RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}
	body["command"] = c
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request("GET", "/v1/feed-collectors", nil, true)
	var read struct {
		LeaseStatus               map[string]string `json:"lease_status"`
		ProcessLivenessVerified   bool              `json:"process_liveness_verified"`
		GlobalReadinessConfigured bool              `json:"global_readiness_configured"`
		QualificationCredit       bool              `json:"qualification_credit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.LeaseStatus[r.ID] != "lease_current" || read.ProcessLivenessVerified || read.GlobalReadinessConfigured || read.QualificationCredit {
		t.Fatal(w.Body.String())
	}
	if w := request("GET", "/readyz?scope=feed_coverage", nil, true); w.Code != 503 {
		t.Fatal("lease implied feed ready", w.Code)
	}
	clock.mu.Lock()
	clock.at = clock.at.Add(30 * time.Second)
	clock.mu.Unlock()
	w = request("GET", "/v1/feed-collectors", nil, true)
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || read.LeaseStatus[r.ID] != "expired" {
		t.Fatal(w.Body.String(), err)
	}
	c = feedcollector.Command{RequestID: "renew", Operation: "renew", RegistrationID: r.ID, OwnerID: "worker-one", Generation: 1}
	body["command"] = c
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 409 {
		t.Fatal("expiry bypassed", w.Code)
	}
	c.RegistrationID = "missing"
	body["command"] = c
	if w := request("POST", "/v1/feed-collectors", body, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
