package service

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func ownedAllocationHistoryRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.OwnedAllocationHistoryStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/allocation-history", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		for key, values := range q {
			if (key != "receiver_id" && key != "after" && key != "limit") || len(values) != 1 {
				ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
				return
			}
		}
		query := storage.OwnedAllocationHistoryQuery{ReceiverID: q.Get("receiver_id"), After: q.Get("after"), Limit: 16}
		if v, exists := q["limit"]; exists {
			query.Limit, err = strconv.Atoi(v[0])
		}
		if err != nil || query.Validate() != nil {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		page, err := backend.OwnedAllocationHistory(ctx, query, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAllocationError(w, err)
			return
		}
		write(w, http.StatusOK, page)
	})
}
