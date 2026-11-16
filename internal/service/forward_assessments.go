package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// ForwardAssessmentService has no model or broker client. Collection runs
// through the compiled strategy runner's distinct qualification execution mode.
type ForwardAssessmentService struct {
	Store forward.AssessmentStore
	Clock Clock
}

func (s ForwardAssessmentService) Assess(ctx context.Context, id string) (forward.Assessment, error) {
	return s.Store.AssessForward(ctx, id, s.Clock.Now)
}
func forwardAssessmentRoutes(mux *http.ServeMux, store storage.Store, clock Clock, token string) {
	s, ok := store.(forward.AssessmentStore)
	if !ok {
		return
	}
	fail := func(w http.ResponseWriter, e error) {
		status := 422
		if errors.Is(e, forward.ErrNotFound) {
			status = 404
		}
		if errors.Is(e, forward.ErrConflict) {
			status = 409
		}
		write(w, status, map[string]string{"error": http.StatusText(status)})
	}
	for _, path := range []string{"POST /v1/forward-assessment-plans", "GET /v1/forward-assessment-plans/{id}", "POST /v1/forward-assessment-plans/{id}/assess"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
				write(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				fail(w, forward.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
			defer cancel()
			switch r.Pattern {
			case "POST /v1/forward-assessment-plans":
				r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
				defer r.Body.Close()
				raw, e := io.ReadAll(r.Body)
				var p forward.AssessmentPlan
				if e != nil || workerplan.ReadBoundedShape(raw, &p, 128<<10) != nil {
					fail(w, forward.ErrInvalid)
					return
				}
				v, e := s.DeclareForwardAssessment(ctx, p, clock.Now)
				if e != nil {
					fail(w, e)
					return
				}
				write(w, 200, v)
			case "GET /v1/forward-assessment-plans/{id}":
				v, e := s.ForwardAssessmentPlan(ctx, r.PathValue("id"))
				if e != nil {
					fail(w, e)
					return
				}
				write(w, 200, v)
			default:
				r.Body = http.MaxBytesReader(w, r.Body, 1)
				defer r.Body.Close()
				raw, e := io.ReadAll(r.Body)
				if e != nil || len(raw) != 0 {
					fail(w, forward.ErrInvalid)
					return
				}
				v, e := (ForwardAssessmentService{Store: s, Clock: clock}).Assess(ctx, r.PathValue("id"))
				if e != nil {
					fail(w, e)
					return
				}
				write(w, 200, v)
			}
		})
	}
}
