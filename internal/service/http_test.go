package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestObservationAPI(t *testing.T) {
	h := Handler(storage.NewMemory(), "test-token")
	o := workflow.Fixtures()[0]
	raw, _ := json.Marshal(o)
	for _, test := range []struct {
		body   []byte
		token  string
		status int
	}{
		{raw, "", 401}, {raw, "test-token", 201}, {raw, "test-token", 200}, {append(raw, []byte(" {}")...), "test-token", 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/observations", bytes.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("got %d, want %d: %s", w.Code, test.status, w.Body.String())
		}
	}
	o.PriceCents++
	raw, _ = json.Marshal(o)
	r := httptest.NewRequest(http.MethodPost, "/v1/observations", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("changed observation: %d", w.Code)
	}
}
