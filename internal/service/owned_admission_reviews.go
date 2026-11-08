package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func ownedAdmissionReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrOwnedAdmissionReviewInvalid):
		write(w, 422, map[string]string{"error": "invalid owned admission review"})
	case errors.Is(err, storage.ErrOwnedAdmissionReviewConflict), errors.Is(err, storage.ErrOwnedAdmissionReviewCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrOwnedAdmissionReviewNotFound):
		write(w, 404, map[string]string{"error": "owned admission review or proposal source not found"})
	default:
		write(w, 503, map[string]string{"error": "owned admission review unavailable"})
	}
}
func ownedAdmissionReviewRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.OwnedAdmissionReviewStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/admission-reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			ownedAdmissionReviewError(w, storage.ErrOwnedAdmissionReviewInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.OwnedAdmissionReviewCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || c.Validate() != nil {
			ownedAdmissionReviewError(w, storage.ErrOwnedAdmissionReviewInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		v, err := backend.ReviewOwnedAdmission(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAdmissionReviewError(w, err)
			return
		}
		code := 201
		if v.Duplicate {
			code = 200
		}
		write(w, code, v)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "" && !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			ownedAdmissionReviewError(w, storage.ErrOwnedAdmissionReviewInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		all, err := backend.OwnedAdmissionReviews(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAdmissionReviewError(w, err)
			return
		}
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, v := range all.Records {
			if v.Command.ID == id {
				write(w, 200, v)
				return
			}
		}
		ownedAdmissionReviewError(w, storage.ErrOwnedAdmissionReviewNotFound)
	}
	mux.HandleFunc("GET /v1/admission-reviews", read)
	mux.HandleFunc("GET /v1/admission-reviews/{id}", read)
}
