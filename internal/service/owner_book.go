package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// OwnerBookReceipt separates the immutable action receipt from current controls.
// An exact retry after another worker halts again must show that newer halt.
type OwnerBookReceipt struct {
	ActionID       string
	AppliedVersion int64
	Current        account.OwnerBookView
}

func ownerBookRoutes(mux *http.ServeMux, s storage.CappedBookStore, clock Clock, token string) {
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if token == "" {
			write(w, 503, map[string]string{"error": "owner authentication is not configured"})
			return false
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			write(w, 401, map[string]string{"error": "owner authentication required"})
			return false
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			write(w, 400, map[string]string{"error": "query parameters are not supported"})
			return false
		}
		return true
	}
	fail := func(w http.ResponseWriter, err error) {
		status := 422
		if errors.Is(err, account.ErrBookConflict) {
			status = 409
		} else if errors.Is(err, account.ErrBookNotFound) {
			status = 404
		}
		write(w, status, map[string]string{"error": http.StatusText(status)})
	}
	mux.HandleFunc("GET /v1/capped-books/{book}/owner-view", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		b, err := s.CappedBook(ctx, r.PathValue("book"))
		if err != nil {
			fail(w, err)
			return
		}
		v, err := account.ReadOwnerBook(b, clock.Now().UTC())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("POST /v1/capped-books/{book}/owner-controls/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var in account.OwnerBookAction
		if err != nil || workerplan.ReadBoundedShape(raw, &in, 16<<10) != nil || in.Validate(r.PathValue("book"), r.PathValue("id")) != nil {
			write(w, 400, map[string]string{"error": "invalid confirmed owner action"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		receipt, err := applyOwnerBookAction(ctx, s, r.PathValue("book"), r.PathValue("id"), in, clock.Now().UTC().Truncate(time.Microsecond))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, receipt)
	})
}

func applyOwnerBookAction(ctx context.Context, s storage.CappedBookStore, book, id string, in account.OwnerBookAction, at time.Time) (OwnerBookReceipt, error) {
	b, err := s.CappedBook(ctx, book)
	if err != nil {
		return OwnerBookReceipt{}, err
	}
	var cmd *account.BookCommand
	applied := int64(0)
	var sequence int64
	for _, event := range b.Events {
		sequence += account.CappedCommandCount(event)
		if event.ID == id {
			if event.OwnerAction == nil || !reflect.DeepEqual(*event.OwnerAction, in) {
				return OwnerBookReceipt{}, account.ErrBookConflict
			}
			copy := event
			cmd = &copy
			applied = sequence
			break
		}
	}
	if cmd == nil {
		if in.ExpectedVersion != b.Version {
			return OwnerBookReceipt{}, account.ErrBookConflict
		}
		c := account.BookCommand{ID: id, At: at, Kind: "risk_control", RiskControl: &risk.Control{Action: in.Action, StrategyID: in.StrategyID}, OwnerAction: &in}
		if in.Action == "deposit" {
			c.Kind, c.RiskControl, c.Amount, c.EffectiveAt = "deposit", nil, in.Amount, in.EffectiveAt
		} else if in.Action == "owner_clear_halt" {
			// Refresh risk from complete current local valuation before the explicit
			// baseline reset. Stale or missing marks refuse clear, never assume equity.
			c.Marks, err = account.CurrentPortfolioMarks(b, at)
			if err != nil {
				return OwnerBookReceipt{}, err
			}
		} else {
			roster, e := account.PortfolioRoster(b)
			if e != nil {
				return OwnerBookReceipt{}, e
			}
			found := false
			for _, sleeve := range roster {
				if sleeve.StrategyID == in.StrategyID {
					found = true
				}
			}
			if !found {
				return OwnerBookReceipt{}, errors.New("strategy is not retained by this book")
			}
		}
		cmd = &c
		applied = b.Version + 1
	}
	// Storage is a trusted internal boundary. No new version is substituted on
	// conflict: a concurrently changed book needs fresh owner confirmation.
	if _, err = s.ApplyCappedBook(ctx, book, in.ExpectedVersion, *cmd); err != nil {
		return OwnerBookReceipt{}, err
	}
	current, err := s.CappedBook(ctx, book)
	if err != nil {
		return OwnerBookReceipt{}, err
	}
	if len(current.Events) > 0 && current.Events[len(current.Events)-1].At.After(at) {
		at = current.Events[len(current.Events)-1].At
	}
	view, err := account.ReadOwnerBook(current, at)
	return OwnerBookReceipt{ActionID: id, AppliedVersion: applied, Current: view}, err
}
