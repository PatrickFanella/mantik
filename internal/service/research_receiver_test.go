package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestResearchReceiverAPIBindingAuthenticationAndBoundedReadback(t *testing.T) {
	store := storage.NewMemory()
	handler := Handler(store, "research-token")
	invoke := func(method, path string, body any, token string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := invoke("GET", "/v1/research-receiver", nil, ""); w.Code != 401 {
		t.Fatal("receiver read bypassed authentication")
	}
	receiver, _ := store.ResearchSnapshot(context.Background())
	raw := `{"quotes":{"SPY":{"t":"2026-10-14T15:00:00Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`
	r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "api-receiver", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: time.Date(2026, 10, 14, 15, 0, 1, 0, time.UTC), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-fixture", RawRef: "artifact:receiver-test", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	out, err := alpaca.Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	input := out.Inputs[0]
	body := map[string]any{"receiver_id": "receiver-abcdefghijklmnop", "input": input}
	if w := invoke("POST", "/v1/research-inputs", body, "research-token"); w.Code != 409 {
		t.Fatal("wrong receiver accepted")
	}
	snapshot, _ := store.ResearchSnapshot(context.Background())
	if len(snapshot.Inputs) != 0 {
		t.Fatal("wrong receiver left input")
	}
	body["receiver_id"] = receiver.ID
	if w := invoke("POST", "/v1/research-inputs", body, "research-token"); w.Code != 200 {
		t.Fatalf("guarded input failed: %d %s", w.Code, w.Body.String())
	}
	var selected domain.ResearchSnapshot
	w := invoke("GET", "/v1/research-receiver", nil, "research-token")
	if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || selected.ID != receiver.ID || len(selected.Inputs) != 0 {
		t.Fatal("identity-only read exposed all inputs")
	}
	w = invoke("GET", "/v1/research-receiver?input_id="+input.ID, nil, "research-token")
	if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || len(selected.Inputs) != 1 {
		t.Fatal("targeted readback lost input")
	}
	values := url.Values{}
	for n := 0; n < 17; n++ {
		values.Add("input_id", input.ID)
	}
	if w := invoke("GET", "/v1/research-receiver?"+values.Encode(), nil, "research-token"); w.Code != 422 {
		t.Fatal("unbounded receiver read admitted")
	}
	input.Source, input.Symbol, input.EvidenceClass, input.Provenance, input.ID = "fixture", "DEMO", "synthetic_unqualified", nil, "fixture-fenced"
	body["input"] = input
	if w := invoke("POST", "/v1/research-inputs", body, "research-token"); w.Code != 422 {
		t.Fatal("guarded endpoint accepted fixture input")
	}
	if w := invoke("POST", "/v1/research-inputs", map[string]string{"unexpected": "field"}, "research-token"); w.Code != 400 {
		t.Fatal("unknown guarded fields admitted")
	}
	if w := invoke("GET", "/v1/research-receiver?input_id="+strings.Repeat("a", 129), nil, "research-token"); w.Code != 422 {
		t.Fatal("unbounded identity admitted")
	}
}
