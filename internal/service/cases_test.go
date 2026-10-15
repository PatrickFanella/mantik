package service

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestCasesAPI(t *testing.T) {
	h := Handler(storage.NewMemory(), "case-token")
	for _, test := range []struct {
		path, token string
		status      int
	}{
		{"/v1/cases", "", 401}, {"/v1/cases", "case-token", 200}, {"/v1/cases/demo-case-v1", "case-token", 200},
		{"/v1/cases/missing", "case-token", 404}, {"/v1/cases/bad%20id", "case-token", 400},
		{"/v1/cases?limit=1", "case-token", 400}, {"/v1/cases/demo-case-v1?x=1", "case-token", 400},
	} {
		r := httptest.NewRequest("GET", test.path, nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
		if w.Code == 200 && test.path == "/v1/cases" {
			var c domain.CaseCatalog
			if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.Routing == nil {
				t.Fatal("empty catalogue contract")
			}
		}
	}
}
