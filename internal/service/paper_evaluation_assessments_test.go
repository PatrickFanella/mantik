package service

import (
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaperEvaluationAssessmentRejectsUnknownLengthBody(t *testing.T) {
	mux := http.NewServeMux()
	paperEvaluationAssessmentRoutes(mux, storage.NewMemory(), WallClock{})
	for _, path := range []string{"/v1/paper-evaluation-assessments", "/v1/paper-evaluation-assessments/receipt", "/v1/paper-evaluation-assessments/receipt/corporate-sources"} {
		r := httptest.NewRequest("GET", path, strings.NewReader(`{"at":"future"}`))
		r.Proto = "HTTP/2.0"
		r.ProtoMajor = 2
		r.ContentLength = -1
		r.TransferEncoding = nil
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
