package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type exceptionOnlyReadback struct{ *storage.Memory }

func (s exceptionOnlyReadback) Snapshot(context.Context) (domain.Snapshot, error) {
	return domain.Snapshot{}, errors.New("full status must not be loaded")
}

func TestExceptionPageAPI(t *testing.T) {
	h := Handler(exceptionOnlyReadback{storage.NewMemory()}, "exception-token")
	for _, test := range []struct {
		query, token string
		status       int
	}{
		{"", "", 401}, {"", "exception-token", 200}, {"?limit=1", "exception-token", 200},
		{"?limit=101", "exception-token", 400}, {"?limit=0", "exception-token", 400}, {"?after=bad%20cursor", "exception-token", 400},
		{"?limit=1&limit=2", "exception-token", 400}, {"?unknown=1", "exception-token", 400}, {"?after=", "exception-token", 400}, {"?after=%xx", "exception-token", 400},
	} {
		r := httptest.NewRequest("GET", "/v1/exceptions"+test.query, nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.query, w.Code, w.Body.String())
		}
	}
}
