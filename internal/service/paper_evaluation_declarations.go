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

func paperEvaluationDeclarationRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationDeclarationStore)
	if !ok {
		return
	}
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, storage.ErrPaperEvaluationDeclarationInvalid):
			write(w, 422, map[string]string{"error": "invalid paper evaluation declaration"})
		case errors.Is(err, storage.ErrPaperEvaluationDeclarationConflict), errors.Is(err, storage.ErrPaperEvaluationDeclarationCapacity), errors.Is(err, storage.ErrReceiverMismatch):
			write(w, 409, map[string]string{"error": err.Error()})
		case errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound):
			write(w, 404, map[string]string{"error": err.Error()})
		default:
			write(w, 503, map[string]string{"error": "paper evaluation declaration storage unavailable"})
		}
	}
	mux.HandleFunc("POST /v1/paper-evaluation-declarations", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(w, storage.ErrPaperEvaluationDeclarationInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxPaperEvaluationDeclarationCommandBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.PaperEvaluationDeclarationCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxPaperEvaluationDeclarationCommandBytes) != nil || c.Validate() != nil {
			fail(w, storage.ErrPaperEvaluationDeclarationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.DeclarePaperEvaluation(ctx, c, clock.Now)
		if err != nil {
			fail(w, err)
			return
		}
		status := 201
		if receipt.Duplicate {
			status = 200
		}
		write(w, status, receipt)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.RawQuery != "" || r.URL.ForceQuery || id != "" && !paperaccount.ID.MatchString(id) {
			fail(w, storage.ErrPaperEvaluationDeclarationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.PaperEvaluationDeclarations(ctx)
		if err != nil {
			fail(w, err)
			return
		}
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, record := range all.Records {
			if record.Command.ID == id {
				write(w, 200, record)
				return
			}
		}
		fail(w, storage.ErrPaperEvaluationDeclarationNotFound)
	}
	mux.HandleFunc("GET /v1/paper-evaluation-declarations", read)
	mux.HandleFunc("GET /v1/paper-evaluation-declarations/{id}", read)
}

const MaxPaperEvaluationDeclarationCommandBytes = 16384
