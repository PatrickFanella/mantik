package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func dailyBriefRoutes(mux *http.ServeMux, store storage.Store, clock Clock, token string) {
	s, ok := store.(storage.BoundedCappedBookStore)
	if !ok {
		return
	}
	// This is an owner read operation, including when development auth is disabled.
	mux.HandleFunc("POST /v1/capped-books/{book}/daily-brief-source", func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			write(w, 503, map[string]string{"error": "owner authentication is not configured"})
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			write(w, 400, map[string]string{"error": "query parameters are not supported"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		defer r.Body.Close()
		raw, e := io.ReadAll(r.Body)
		var in struct{ AsOf time.Time }
		if e != nil || workerplan.ReadBoundedShape(raw, &in, 1024) != nil {
			write(w, 400, map[string]string{"error": "invalid bounded JSON"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*10*time.Second)
		defer cancel()
		b, e := s.CappedBookBounded(ctx, r.PathValue("book"), MaxBriefSourceBytes/2)
		if e != nil {
			if e == account.ErrBookNotFound {
				write(w, 404, map[string]string{"error": "book not found"})
			} else {
				failed(w, e)
			}
			return
		}
		// Read family fences separately and keep their actual observation clocks.
		// No registry match is treated as current trading/admission authority.
		families := map[string]BriefFamilyObservation{}
		familyBytes := 0
		if registry, ok := store.(interface {
			StrategyGate(context.Context, string) (strategygate.Record, error)
			StrategyTrialSearchTotal(context.Context, string) (int64, error)
		}); ok {
			_, roster, err := account.BriefBookView(b, clock.Now())
			if err != nil || len(roster) > 64 {
				write(w, 422, map[string]string{"error": "bounded brief roster unavailable"})
				return
			}
			ids := []string{}
			for id := range roster {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			cache := map[string]BriefFamilyObservation{}
			for _, id := range ids {
				owner := roster[id]
				g, err := registry.StrategyGate(ctx, owner.GateID)
				if err != nil {
					families[id] = BriefFamilyObservation{Unavailable: "retained_gate_unavailable", ObservedAt: clock.Now()}
					continue
				}
				f, found := cache[g.Declaration.Family]
				if !found {
					n, err := registry.StrategyTrialSearchTotal(ctx, g.Declaration.Family)
					f.ObservedAt = clock.Now().UTC()
					if errors.Is(err, strategytrial.ErrFamilyPending) {
						f.Pending = true
					} else if err != nil {
						f.Unavailable = "family_registry_unavailable"
					} else {
						f.SearchTotal = &n
					}
					cache[g.Declaration.Family] = f
				}
				gateRaw, err := json.Marshal(g)
				if err != nil {
					failed(w, err)
					return
				}
				familyBytes += len(gateRaw) + 1024
				if familyBytes > MaxBriefSourceBytes/2-4096 {
					write(w, 422, map[string]string{"error": "brief family evidence exceeds capacity"})
					return
				}
				f.Gate = g
				families[id] = f
			}
		}
		now := clock.Now().UTC()
		if in.AsOf.IsZero() {
			in.AsOf = now
		}
		if in.AsOf.After(now) || in.AsOf.Before(b.Config.OpenedAt) {
			write(w, 422, map[string]string{"error": "invalid brief clock"})
			return
		}
		if len(b.Events) > 0 && b.Events[len(b.Events)-1].At.After(in.AsOf) {
			write(w, 409, map[string]string{"error": "finalized prefix is newer than requested clock"})
			return
		}
		source := BriefSource{Book: b, AsOf: in.AsOf, CapturedAt: now, Families: families}
		// Bound both publication and downstream receipt size; fail closed at capacity.
		if _, e := BuildDailyBrief(source, nil); e != nil {
			write(w, 422, map[string]string{"error": "finalized brief source unavailable or exceeds bound"})
			return
		}
		write(w, 200, source)
	})
}
