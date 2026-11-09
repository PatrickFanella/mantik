package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func paperRecordArchiveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPaperRecordArchiveInvalid):
		write(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrPaperRecordArchiveNotFound):
		write(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrPaperRecordArchiveConflict), errors.Is(err, storage.ErrPaperRecordArchiveCapacity),
		errors.Is(err, storage.ErrPaperEvaluationAssessmentInvalid), errors.Is(err, storage.ErrPaperEvaluationAssessmentConflict),
		errors.Is(err, storage.ErrDecisionReviewInvalid), errors.Is(err, storage.ErrDecisionReviewConflict):
		write(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		feedArchiveError(w, err)
	}
}

func paperRecordArchiveRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperRecordArchiveStore)
	if !ok {
		return
	}
	readOnly := func(r *http.Request) bool {
		return r.ContentLength == 0 && len(r.TransferEncoding) == 0
	}
	mux.HandleFunc("POST /v1/paper-record-archives", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		defer func() { _ = r.Body.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		var command struct {
			ReceiverID string `json:"receiver_id"`
		}
		if err != nil || workerplan.ReadBoundedShape(raw, &command, 4096) != nil {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		receipt, err := backend.ArchivePaperRecords(ctx, command.ReceiverID, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperRecordArchiveError(w, err)
			return
		}
		status := http.StatusCreated
		if receipt.Duplicate {
			status = http.StatusOK
		}
		write(w, status, receipt)
	})
	mux.HandleFunc("GET /v1/paper-record-archives", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.ForceQuery || !readOnly(r) {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		for key, values := range q {
			if (key != "after" && key != "limit") || len(values) != 1 {
				paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
				return
			}
		}
		after, limit := int64(0), 16
		if v, ok := q["after"]; ok {
			after, err = strconv.ParseInt(v[0], 10, 64)
		}
		if err == nil {
			if v, ok := q["limit"]; ok {
				limit, err = strconv.Atoi(v[0])
			}
		}
		if err != nil {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		page, err := backend.PaperRecordArchives(ctx, after, limit)
		if err != nil {
			paperRecordArchiveError(w, err)
			return
		}
		write(w, http.StatusOK, page)
	})
	mux.HandleFunc("GET /v1/paper-record-archives/{sequence}", func(w http.ResponseWriter, r *http.Request) {
		sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
		if err != nil || r.URL.RawQuery != "" || r.URL.ForceQuery || !readOnly(r) {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		a, err := backend.PaperRecordArchive(ctx, sequence, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperRecordArchiveError(w, err)
			return
		}
		write(w, http.StatusOK, a)
	})
	mux.HandleFunc("GET /v1/paper-record-archives/source", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 2 || len(q["kind"]) != 1 || len(q["id"]) != 1 || !readOnly(r) {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		record, err := backend.PaperArchivedRecord(ctx, q.Get("kind"), q.Get("id"), evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperRecordArchiveError(w, err)
			return
		}
		write(w, http.StatusOK, record)
	})
	if audit, ok := store.(storage.PaperRecordArchiveAuditStore); ok {
		mux.HandleFunc("GET /v1/paper-record-archives/audit", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery || !readOnly(r) {
				paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
			defer cancel()
			result, err := audit.AuditPaperRecordArchives(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				paperRecordArchiveError(w, err)
				return
			}
			write(w, http.StatusOK, result)
		})
	}
}
