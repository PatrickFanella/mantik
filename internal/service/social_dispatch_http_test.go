package service

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The source here is manufactured test material, never provider authentication.
func TestOwnedSocialDispatchHTTP(t *testing.T) {
	s := storage.NewMemory()
	ctx := t.Context()
	cat, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	clock := &fakeClock{at: at}
	owner := cat.ReceiverID
	policy := socialwatch.PolicyCommand{ID: "http-watch-create", ReceiverID: owner, Policy: socialwatch.Policy{ID: "http-watch", Provider: "recorded", AccountID: "account-123", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	pr, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	source := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "account-123", PostID: "post-1"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Manufactured SPY assertion", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(source)
	capture := socialwatch.CaptureCommand{ID: "http-source", ReceiverID: owner, WatchID: policy.Policy.ID, PolicySHA256: pr.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: true}
	cr, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	req := cr.Entry.Capture.ResearchRequest
	c := storage.SocialResearchDispatchCommand{ReceiverID: owner, RequestID: req.ID, RequestSHA256: req.SHA256}
	body, _ := json.Marshal(c)
	h := HandlerWithClock(s, "dispatch-test-token", clock)
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer dispatch-test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/v1/social-research-dispatches", string(body), false); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, raw := range []string{`{}`, `{"receiver_id":"bad"}`, strings.TrimSuffix(string(body), "}") + `,"admission_available":true}`} {
		if w := call("POST", "/v1/social-research-dispatches", raw, true); w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call("POST", "/v1/social-research-dispatches", string(body), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var first storage.SocialResearchDispatchReceipt
	if err = json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Record.Links) != 1 || first.Record.AdmissionAvailable || first.Record.ExecutionAvailable || first.Record.ProviderAccessVerified || first.Record.IndependentEvidenceCredit || first.Record.QualificationCredit {
		t.Fatal(first)
	}
	if w = call("POST", "/v1/social-research-dispatches", string(body), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var duplicate storage.SocialResearchDispatchReceipt
	_ = json.Unmarshal(w.Body.Bytes(), &duplicate)
	if !duplicate.Duplicate || duplicate.Record.SHA256 != first.Record.SHA256 {
		t.Fatal(duplicate)
	}
	if w = call("GET", "/v1/social-research-dispatches", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), first.Record.SHA256) {
		t.Fatal(w.Code, w.Body.String())
	}
	link := first.Record.Links[0]
	if w = call("GET", "/v1/cases/"+link.CaseID, "", true); w.Code != 200 || !strings.Contains(w.Body.String(), link.InputID) || !strings.Contains(w.Body.String(), "owned-social-research") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("GET", "/v1/social-research-dispatches?", "", true); w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
}
