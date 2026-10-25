package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type sourceReadFailure struct {
	*storage.Memory
	err   error
	reads int
}

func (s *sourceReadFailure) SocialHistory(context.Context) (socialwatch.Catalog, error) {
	s.reads++
	return socialwatch.Catalog{}, s.err
}

type sourceReadAbsent struct{ storage.Store }

func TestRetainedRecordingReadGuards(t *testing.T) {
	for _, prefix := range []string{"/v1/social-recordings/", "/v1/copy-recordings/"} {
		s := &sourceReadFailure{Memory: storage.NewMemory(), err: errors.New("owner unavailable")}
		h := HandlerWithClock(s, "fixture-source-read", WallClock{})
		for _, tc := range []struct {
			suffix, body string
			length       int64
			transfer     []string
		}{
			{"bad!", "", 0, nil}, {"record?", "", 0, nil}, {"record?at=future", "", 0, nil},
			{"record", "{}", 2, nil}, {"record", "{}", -1, nil}, {"record", "", 0, []string{"chunked"}},
		} {
			req := httptest.NewRequest("GET", prefix+tc.suffix, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture-source-read")
			req.ContentLength, req.TransferEncoding = tc.length, tc.transfer
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 422 || s.reads != 0 {
				t.Fatal(w.Code, s.reads, w.Body.String())
			}
		}
		for _, e := range []error{errors.New("owner unavailable"), socialwatch.ErrInvalid, storage.ErrReceiverMismatch} {
			s.err = e
			req := httptest.NewRequest("GET", prefix+"record", nil)
			req.Header.Set("Authorization", "Bearer fixture-source-read")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 503 || strings.Contains(w.Body.String(), e.Error()) {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		req := httptest.NewRequest("GET", prefix+"record", nil)
		before := s.reads
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 || s.reads != before {
			t.Fatal(w.Code, s.reads)
		}
		missing := HandlerWithClock(sourceReadAbsent{storage.NewMemory()}, "fixture-source-read", WallClock{})
		req.Header.Set("Authorization", "Bearer fixture-source-read")
		w = httptest.NewRecorder()
		missing.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
