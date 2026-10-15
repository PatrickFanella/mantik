package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestActivityAPI(t *testing.T) {
	m := storage.NewMemory()
	if _, err := m.Ingest(context.Background(), workflow.Fixtures()[0]); err != nil {
		t.Fatal(err)
	}
	h := Handler(m, "activity-token")
	for _, test := range []struct {
		query, token string
		status       int
	}{
		{"", "", 401}, {"?limit=1", "activity-token", 200}, {"?through=0", "activity-token", 200},
		{"?limit=101", "activity-token", 400}, {"?after=-1", "activity-token", 400}, {"?limit=1&limit=2", "activity-token", 400},
		{"?through=99999", "activity-token", 400}, {"?after=1&through=0", "activity-token", 400}, {"?limit=", "activity-token", 400},
		{"?unknown=1", "activity-token", 400}, {"?after=%xx", "activity-token", 400},
	} {
		r := httptest.NewRequest("GET", "/v1/activity"+test.query, nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.query, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var p domain.ActivityPage
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Items) > 1 {
				t.Fatal("page exceeds requested bound")
			}
		}
	}
}
