package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestRoutingAPIValidationAndAuthentication(t *testing.T) {
	store := storage.NewMemory()
	handler := Handler(store, "owner-token")
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	d := domain.TriggerDefinition{ID: "api-event-v1", Owner: "operator", CaseKey: "api-case", Behavior: "decide", Condition: domain.Condition{Kind: "event", EventName: "signal"}, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "reopen", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	post := func(path string, value any, token string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := post("/v1/triggers", d, ""); w.Code != 401 {
		t.Fatal("trigger registration bypassed auth")
	}
	if w := post("/v1/triggers", d, "owner-token"); w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	changed := d
	changed.Condition.EventName = "other"
	if w := post("/v1/triggers", changed, "owner-token"); w.Code != 409 {
		t.Fatal("changed definition did not conflict")
	}
	act := d
	act.ID, act.Owner, act.Behavior, act.SourceDecisionID, act.SubjectCaseID, act.RepeatPolicy = "forged-action", "operations", "act", "entry", "subject", "once"
	if w := post("/v1/triggers", act, "owner-token"); w.Code != 422 {
		t.Fatal("operator action authority admitted")
	}
	i := domain.RoutingInput{ID: "api-input", Kind: "event", Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, EventName: "signal"}
	w := post("/v1/routing-inputs", i, "owner-token")
	if w.Code != 200 {
		t.Fatalf("route: %d %s", w.Code, w.Body.String())
	}
	var receipt domain.RoutingReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	caseID := receipt.Matches[0].CaseID
	w = post("/v1/routing-inputs", i, "owner-token")
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || !receipt.Duplicate {
		t.Fatal("API retry did not retain original routing receipt")
	}
	if w := post("/v1/routing-cases/"+caseID+"/close", map[string]any{"owner": "operator", "at": at.Add(time.Second)}, "owner-token"); w.Code != 200 {
		t.Fatal("case closure failed")
	}
	if w := post("/v1/routing-cases/unknown/close", map[string]any{"owner": "operator", "at": at}, "owner-token"); w.Code != 404 {
		t.Fatal("missing case closure was accepted")
	}
	if w := post("/v1/routing-inputs", map[string]string{"unexpected": "field"}, "owner-token"); w.Code != 400 {
		t.Fatal("unknown input fields accepted")
	}
}
