package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// StrategyReviewService imports bounded evidence and commits deterministic
// reviews. It has no broker/model client and never asks a model to choose risk.
// Owner authentication belongs at the HTTP boundary, outside strategy inputs.
type StrategyReviewService struct{ Store storage.CappedBookStore }

// Review records an exact retry or atomically binds a new review to its current
// journal prefix. A concurrent owner/control/plan write forces a fresh snapshot.
func (s StrategyReviewService) Review(ctx context.Context, bookID, id string, in account.ReviewInput) (account.StrategyReview, error) {
	for attempt := 0; attempt < 4; attempt++ {
		book, e := s.Store.CappedBook(ctx, bookID)
		if e != nil {
			return account.StrategyReview{}, e
		}
		for _, event := range book.Events {
			if event.ID == id {
				if event.Review == nil || event.Kind != "strategy_review" || !reflect.DeepEqual(event.Review.Input, in) {
					return account.StrategyReview{}, account.ErrBookConflict
				}
				return *event.Review, nil
			}
		}
		review, e := account.BuildStrategyReview(book, in)
		if e != nil {
			return review, e
		}
		_, e = s.Store.ApplyCappedBook(ctx, bookID, book.Version, account.BookCommand{ID: id, Kind: "strategy_review", At: in.At, Review: &review})
		if errors.Is(e, account.ErrBookConflict) {
			continue
		}
		return review, e
	}
	return account.StrategyReview{}, account.ErrBookConflict
}

func strategyReviewRoutes(mux *http.ServeMux, store storage.Store, clock Clock, token string) {
	s, ok := store.(storage.CappedBookStore)
	if !ok {
		return
	}
	fail := func(w http.ResponseWriter, e error) {
		status := 422
		if errors.Is(e, account.ErrBookConflict) {
			status = 409
		} else if errors.Is(e, account.ErrBookNotFound) {
			status = 404
		}
		write(w, status, map[string]string{"error": http.StatusText(status)})
	}
	// These routes always require an explicitly configured bearer credential, even
	// when the rest of the development service permits unauthenticated reads.
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if token == "" {
			write(w, 503, map[string]string{"error": "owner authentication is not configured"})
			return false
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			write(w, 400, map[string]string{"error": "query parameters are not supported"})
			return false
		}
		return true
	}
	read := func(w http.ResponseWriter, r *http.Request, v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		defer r.Body.Close()
		raw, e := io.ReadAll(r.Body)
		if e != nil || workerplan.ReadBoundedShape(raw, v, 128<<10) != nil {
			write(w, 400, map[string]string{"error": "invalid bounded JSON"})
			return false
		}
		return true
	}
	mux.HandleFunc("POST /v1/capped-books/{book}/review-declaration", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var d account.ReviewDeclaration
		if !read(w, r, &d) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		b, e := s.CappedBook(ctx, r.PathValue("book"))
		if e != nil {
			fail(w, e)
			return
		}
		id := "review-declare-" + d.ID
		at := clock.Now().UTC().Truncate(time.Microsecond)
		for _, v := range b.Events {
			if v.ID == id {
				at = v.At
			}
		}
		// Bind imported history to the retained gate's exact source and clock. This
		// remains an operator-reviewed import, not independent backtest replication.
		gates, ok := store.(interface {
			StrategyGate(context.Context, string) (strategygate.Record, error)
		})
		if !ok {
			write(w, 503, map[string]string{"error": "gate registry unavailable"})
			return
		}
		g, e := gates.StrategyGate(ctx, d.GateID)
		if e != nil || g.Decision == nil || g.Decision.StrategyID != d.StrategyID || d.Benchmark != g.Declaration.Benchmark || len(d.Backtest) > 0 && (g.Declaration.DataSHA256 != d.BacktestSourceSHA256 || d.BacktestEnd.After(g.Decision.At)) {
			write(w, 422, map[string]string{"error": "frozen comparison differs from retained gate"})
			return
		}
		b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, account.BookCommand{ID: id, Kind: "review_declare", At: at, ReviewDeclaration: &d})
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, b.Events)
	})
	mux.HandleFunc("POST /v1/capped-books/{book}/reviews/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var in account.ReviewInput
		if !read(w, r, &in) {
			return
		}
		if in.At.After(clock.Now()) {
			write(w, 422, map[string]string{"error": "future review clock"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*10*time.Second)
		defer cancel()
		book, e := s.CappedBook(ctx, r.PathValue("book"))
		if e != nil {
			fail(w, e)
			return
		}
		retained := false
		for _, event := range book.Events {
			if event.ID == r.PathValue("id") {
				retained = true
				break
			}
		}
		if !retained && clock.Now().Sub(in.At) > time.Minute {
			write(w, 422, map[string]string{"error": "new review requires a current observation clock"})
			return
		}
		out, e := (StrategyReviewService{s}).Review(ctx, r.PathValue("book"), r.PathValue("id"), in)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, out)
	})
	ownerBookRoutes(mux, s, clock, token)
	mux.HandleFunc("GET /v1/capped-books/{book}/reviews", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		b, e := s.CappedBook(r.Context(), r.PathValue("book"))
		if e != nil {
			fail(w, e)
			return
		}
		out := []account.BookCommand{}
		for _, v := range b.Events {
			if v.Review != nil || v.ReviewDeclaration != nil {
				out = append(out, v)
			}
		}
		write(w, 200, out)
	})
}
