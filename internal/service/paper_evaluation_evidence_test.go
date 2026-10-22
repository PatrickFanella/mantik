package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type evaluationEvidenceBodyStore struct {
	*storage.Memory
	called bool
}

func (s *evaluationEvidenceBodyStore) PaperEvaluationEvidence(context.Context, string, func() time.Time, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperEvaluationEvidence, error) {
	s.called = true
	return storage.PaperEvaluationEvidence{}, storage.ErrPaperEvaluationEvidenceInvalid
}
func TestPaperEvaluationEvidenceRejectsUnknownLengthHTTP2Body(t *testing.T) {
	s := &evaluationEvidenceBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperEvaluationEvidenceRoutes(mux, s, WallClock{})
	r := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/evidence", strings.NewReader(`{"at":"future"}`))
	r.Proto = "HTTP/2.0"
	r.ProtoMajor = 2
	r.ProtoMinor = 0
	r.ContentLength = -1
	r.TransferEncoding = nil
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 422 || s.called {
		t.Fatal("unknown-length body reached owner evidence read", w.Code, s.called)
	}
}
