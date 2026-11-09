package service

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func paperHistoryRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.PaperHistoryStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-history", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		for key, values := range q {
			if (key != "receiver_id" && key != "kind" && key != "after" && key != "limit") || len(values) != 1 {
				paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
				return
			}
		}
		query := storage.PaperHistoryQuery{ReceiverID: q.Get("receiver_id"), Kind: q.Get("kind"), After: q.Get("after"), Limit: 16}
		if v, exists := q["limit"]; exists {
			query.Limit, err = strconv.Atoi(v[0])
		}
		if err != nil || query.Validate() != nil {
			paperRecordArchiveError(w, storage.ErrPaperRecordArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		page, err := backend.PaperHistory(ctx, query, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperRecordArchiveError(w, err)
			return
		}
		write(w, http.StatusOK, page)
	})
}
