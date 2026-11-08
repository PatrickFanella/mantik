package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const protectiveReplayMethod = "first-fill-clock-shared-protective-counterfactual-v1"

type PaperEvaluationProtectiveReplay struct {
	Schema                      string                             `json:"schema"`
	Method                      string                             `json:"method"`
	EntryEvidence               PaperEvaluationEntryReplay         `json:"entry_evidence"`
	Variants                    []PaperEvaluationProtectiveVariant `json:"variants"`
	Authority                   string                             `json:"authority"`
	PortfolioEconomicsAvailable bool                               `json:"portfolio_economics_available"`
	AdmissionAvailable          bool                               `json:"admission_available"`
	ExecutionAvailable          bool                               `json:"execution_available"`
	QualificationCredit         bool                               `json:"qualification_credit"`
	SHA256                      string                             `json:"sha256"`
}
type PaperEvaluationProtectiveVariant struct {
	StrategyID string                          `json:"strategy_id"`
	Status     string                          `json:"status"`
	Base       *PaperEvaluationProtectiveModel `json:"base,omitempty"`
	Stress     *PaperEvaluationProtectiveModel `json:"stress,omitempty"`
	Exclusions []PaperEvaluationEntryExclusion `json:"exclusions"`
}
type PaperEvaluationProtectiveModel struct {
	CutAt           time.Time                           `json:"cut_at"`
	EntryBook       paperfill.Book                      `json:"entry_book"`
	EntryProjection paperfill.BookProjection            `json:"entry_projection"`
	ExitBook        *paperfill.Book                     `json:"exit_book,omitempty"`
	ExitProjection  *paperfill.BookProjection           `json:"exit_projection,omitempty"`
	Positions       []PaperEvaluationProtectivePosition `json:"positions"`
}
type PaperEvaluationProtectivePosition struct {
	DecisionID           string    `json:"decision_id"`
	DecisionSHA256       string    `json:"decision_sha256"`
	FirstFillAt          time.Time `json:"first_fill_at"`
	HoldingDeadline      time.Time `json:"holding_deadline"`
	TriggerAt            time.Time `json:"trigger_at"`
	TriggerQuoteID       string    `json:"trigger_quote_id,omitempty"`
	TriggerReason        string    `json:"trigger_reason"`
	EnteredQuantity      int64     `json:"entered_quantity"`
	ClosedQuantity       int64     `json:"closed_quantity"`
	UnclosedQuantity     int64     `json:"unclosed_quantity"`
	EntryDebitCents      int64     `json:"entry_debit_cents"`
	ExitNetProceedsCents int64     `json:"exit_net_proceeds_cents"`
	ClosedNetCents       *int64    `json:"closed_net_cents,omitempty"`
	Status               string    `json:"status"`
}

func inspectEvaluationProtectiveReplay(ctx context.Context, s storage.PaperEvaluationEntrySources) (PaperEvaluationProtectiveReplay, error) {
	entry, err := inspectEvaluationEntryReplay(ctx, s)
	if err != nil {
		return PaperEvaluationProtectiveReplay{}, err
	}
	out := PaperEvaluationProtectiveReplay{Schema: "mantik-owned-evaluation-protective-replay-v1", Method: protectiveReplayMethod, EntryEvidence: entry, Variants: []PaperEvaluationProtectiveVariant{}, Authority: "service_owned_protective_counterfactual_only"}
	for _, v := range entry.Variants {
		result := PaperEvaluationProtectiveVariant{StrategyID: v.StrategyID, Status: v.Status, Exclusions: []PaperEvaluationEntryExclusion{}}
		if v.Base != nil && v.Stress != nil {
			sources := []storage.PaperEvaluationEntrySource{}
			markets := []PaperOutcomeMarketEvidence{}
			for i, r := range s.Rows {
				if r.Strategy.Command.Definition.ID == v.StrategyID {
					sources = append(sources, r)
					markets = append(markets, *entry.Rows[i].Market)
				}
			}
			commands, cut, excluded, err := protectiveReplayQuotes(ctx, sources, markets)
			if err != nil {
				return PaperEvaluationProtectiveReplay{}, err
			}
			result.Exclusions = excluded
			result.Base, err = replayProtectiveModel(ctx, sources, commands, cut, *s.Policy.Command.ExecutionModel, false)
			if err == nil {
				result.Stress, err = replayProtectiveModel(ctx, sources, commands, cut, s.Evidence.Declaration.Command.Plan.StressModel, true)
			}
			result.Status = "protective_counterfactual_only_portfolio_unavailable"
			if errors.Is(err, paperfill.ErrCapacity) {
				result.Base = nil
				result.Stress = nil
				result.Status = "protective_replay_capacity_exceeded"
			} else if err != nil {
				return PaperEvaluationProtectiveReplay{}, err
			}
		}
		out.Variants = append(out.Variants, result)
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > storage.MaxPaperEvaluationEvidenceBytes {
		return PaperEvaluationProtectiveReplay{}, storage.ErrPaperEvaluationEvidenceInvalid
	}
	return out, nil
}

// Union only the actual on-time outcome cuts already selected in the atomic
// owner snapshot. No query can advance this cut or supply another quote path.
func protectiveReplayQuotes(ctx context.Context, sources []storage.PaperEvaluationEntrySource, markets []PaperOutcomeMarketEvidence) ([]paperfill.BookCommand, time.Time, []PaperEvaluationEntryExclusion, error) {
	definition := sources[0].Strategy.Command.Definition
	first, cut := sources[0].Decision.FinalizedAt.Add(time.Nanosecond), time.Time{}
	for _, s := range sources {
		if s.Decision.FinalizedAt.Add(time.Nanosecond).Before(first) {
			first = s.Decision.FinalizedAt.Add(time.Nanosecond)
		}
	}
	byID := map[string]PaperOutcomeMarketRow{}
	for _, m := range markets {
		if m.Provider != "alpaca" || m.Feed != definition.Feed {
			return nil, time.Time{}, nil, storage.ErrPaperEvaluationEvidenceInvalid
		}
		if m.EvaluatedAt.After(cut) {
			cut = m.EvaluatedAt
		}
		for _, r := range m.Rows {
			r.SelectedForwardQuote = false
			r.TemporalStatus = ""
			if old, ok := byID[r.InputID]; ok && !reflect.DeepEqual(old, r) {
				return nil, time.Time{}, nil, storage.ErrPaperEvaluationEvidenceInvalid
			}
			byID[r.InputID] = r
		}
	}
	ordered := []PaperOutcomeMarketRow{}
	for _, r := range byID {
		ordered = append(ordered, r)
	}
	slices.SortFunc(ordered, func(a, b PaperOutcomeMarketRow) int {
		if c := a.AvailableAt.Compare(b.AvailableAt); c != 0 {
			return c
		}
		if c := a.OccurredAt.Compare(b.OccurredAt); c != 0 {
			return c
		}
		return strings.Compare(a.InputID, b.InputID)
	})
	commands := []paperfill.BookCommand{}
	excluded := []PaperEvaluationEntryExclusion{}
	for _, r := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, time.Time{}, nil, err
		}
		reason := ""
		switch {
		case r.AvailableAt.IsZero():
			reason = "source_availability_unavailable"
		case r.AvailableAt.Before(first) || r.AvailableAt.After(cut):
			reason = "outside_protective_source_cut"
		case r.Quote == nil || !r.Quote.ShareQuantitiesAvailable:
			reason = "usable_share_dimensions_unavailable"
		case r.Quote.Symbol != definition.Symbol || r.EvidenceClass != definition.InputEvidenceClass:
			reason = "strategy_quote_scope_mismatch"
		case r.Quote.BidShares > paperfill.MaxQuantity || r.Quote.AskShares > paperfill.MaxQuantity:
			reason = "fill_method_quantity_range_exceeded"
		case r.AvailableAt.Sub(r.OccurredAt) > time.Duration(definition.Safety.MaxQuoteAgeSeconds)*time.Second:
			reason = "strategy_quote_age_exceeded"
		case r.SessionEvidence == nil || !r.SessionEvidence.Evidence.Quote.RegularSessionEligible || r.SessionEvidence.Evidence.Session.Status != "open":
			reason = "regular_session_evidence_unavailable"
		}
		if reason != "" {
			excluded = append(excluded, PaperEvaluationEntryExclusion{r.InputID, reason})
			continue
		}
		d := r.Quote
		q := paperfill.Quote{ID: "quote-" + workerplan.Hash(r.InputID), SourceSHA256: workerplan.Hash(r), Symbol: d.Symbol, Currency: d.Currency, Quality: "usable", EvidenceClass: r.EvidenceClass, OccurredAt: d.OccurredAt, ReceivedAt: r.AvailableAt, BidCents: d.BidCents, AskCents: d.AskCents, BidQuantity: d.BidShares, AskQuantity: d.AskShares}
		commands = append(commands, paperfill.BookCommand{ID: q.ID, At: r.AvailableAt, Quote: &q})
	}
	return commands, cut, excluded, nil
}

// The entry pass seals partials when protection triggers. Exit orders are then
// replayed together against the same unchanged quotes, sharing bid liquidity.
// Holding clocks come only from modeled fills, never proposal finalization.
func replayProtectiveModel(ctx context.Context, sources []storage.PaperEvaluationEntrySource, quotes []paperfill.BookCommand, cut time.Time, model paperfill.Model, stress bool) (*PaperEvaluationProtectiveModel, error) {
	if len(sources) == 0 || len(sources) > paperfill.MaxBookOrders || cut.IsZero() {
		return nil, paperfill.ErrInvalid
	}
	orders := []paperfill.Order{}
	byID := map[string]storage.PaperEvaluationEntrySource{}
	for _, s := range sources {
		cost := s.BaseCost
		if stress {
			cost = s.StressCost
		}
		if cost == nil {
			return nil, paperfill.ErrInvalid
		}
		d := s.Strategy.Command.Definition
		orders = append(orders, paperfill.Order{ID: s.Decision.Command.ID, Symbol: d.Symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: cost.Quantity, LimitCents: cost.LimitCents, MaxDebitCents: cost.MaxDebitCents, PlacedAt: s.Decision.FinalizedAt.Add(time.Nanosecond), ExpiresAt: cost.ValidUntil, Mode: "replay"})
		byID[s.Decision.Command.ID] = s
	}
	slices.SortFunc(orders, func(a, b paperfill.Order) int { return strings.Compare(a.ID, b.ID) })
	book, err := paperfill.NewOccurrenceBook(model, orders)
	if err != nil {
		return nil, err
	}
	appender, err := paperfill.NewBookAppender(ctx, book)
	if err != nil {
		return nil, err
	}
	projection, err := paperfill.ReplayBook(ctx, book)
	if err != nil {
		return nil, err
	}
	positions := map[string]*PaperEvaluationProtectivePosition{}
	expiries := map[string]bool{}
	// A duplicate command appends nothing, so the latest entry keeps its outcomes.
	latest := []paperfill.BookOrderOutcome{}
	apply := func(c paperfill.BookCommand) error {
		p, receipt, e := appender.Apply(ctx, c)
		if e != nil {
			return e
		}
		projection = p
		if !receipt.Duplicate {
			latest = receipt.Outcomes
		}
		return nil
	}
	trigger := func(id, reason, quoteID string, at time.Time) error {
		p := positions[id]
		if p == nil || !p.TriggerAt.IsZero() {
			return nil
		}
		for _, o := range projection.Orders {
			if o.OrderID == id && o.Projection.Status == "working" {
				if err := apply(paperfill.BookCommand{ID: "seal-" + workerplan.Hash(id), At: at, CancelOrderID: id}); err != nil {
					return err
				}
				break
			}
		}
		p.TriggerAt = at
		p.TriggerReason = reason
		p.TriggerQuoteID = quoteID
		return nil
	}
	// Timers precede quote events at the same clock. Sorting equal-clock IDs makes
	// cancellation order deterministic, including multiple partial positions.
	advance := func(until time.Time) error {
		for {
			next := time.Time{}
			for _, o := range orders {
				if !expiries[o.ID] && !o.ExpiresAt.After(until) && (next.IsZero() || o.ExpiresAt.Before(next)) {
					next = o.ExpiresAt
				}
			}
			for _, p := range positions {
				if p.TriggerAt.IsZero() && !p.HoldingDeadline.After(until) && (next.IsZero() || p.HoldingDeadline.Before(next)) {
					next = p.HoldingDeadline
				}
			}
			if next.IsZero() {
				return nil
			}
			expired := false
			for _, o := range orders {
				if !expiries[o.ID] && o.ExpiresAt.Equal(next) {
					expiries[o.ID] = true
					expired = true
				}
			}
			if expired {
				if err := apply(paperfill.BookCommand{ID: "expiry-" + workerplan.Hash(next), At: next}); err != nil {
					return err
				}
			}
			for _, o := range orders {
				p := positions[o.ID]
				if p != nil && p.TriggerAt.IsZero() && !p.HoldingDeadline.After(next) {
					if err := trigger(o.ID, "max_holding_elapsed", "", next); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, q := range quotes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if q.Quote == nil || q.At.After(cut) {
			return nil, paperfill.ErrInvalid
		}
		if err := advance(q.At); err != nil {
			return nil, err
		}
		for _, o := range orders {
			p := positions[o.ID]
			if p == nil || !p.TriggerAt.IsZero() || !q.Quote.OccurredAt.After(p.FirstFillAt) || q.At.Sub(q.Quote.OccurredAt) > time.Duration(model.MaxQuoteAgeSeconds)*time.Second {
				continue
			}
			safety := byID[o.ID].Strategy.Command.Definition.Safety
			reason := ""
			if q.Quote.BidCents <= safety.StopCents {
				reason = "stop_reached"
			} else if q.Quote.BidCents >= safety.TargetCents {
				reason = "target_reached"
			}
			if reason != "" {
				if err := trigger(o.ID, reason, q.Quote.ID, q.At); err != nil {
					return nil, err
				}
			}
		}
		if err := apply(q); err != nil {
			return nil, err
		}
		for _, result := range latest {
			f := result.Outcome.Fill
			if f == nil {
				continue
			}
			p := positions[f.OrderID]
			if p == nil {
				holding := byID[f.OrderID].Strategy.Command.Definition.Safety.MaxHoldingSeconds
				if holding < 1 {
					return nil, paperfill.ErrInvalid
				}
				p = &PaperEvaluationProtectivePosition{DecisionID: f.OrderID, DecisionSHA256: byID[f.OrderID].Decision.SHA256, FirstFillAt: f.At, HoldingDeadline: f.At.Add(time.Duration(holding) * time.Second), Status: "open_at_source_cut"}
				positions[f.OrderID] = p
			}
			p.EnteredQuantity += f.Quantity
			p.EntryDebitCents += f.DebitCents
		}
	}
	if err := advance(cut); err != nil {
		return nil, err
	}
	// A cut command reflects only the actual retained horizon, not future expiry.
	if err := apply(paperfill.BookCommand{ID: "source-cut", At: cut}); err != nil {
		return nil, err
	}
	result := &PaperEvaluationProtectiveModel{CutAt: cut, EntryBook: appender.Book(), EntryProjection: projection, Positions: []PaperEvaluationProtectivePosition{}}
	exits := []paperfill.Order{}
	for _, o := range orders {
		p := positions[o.ID]
		if p == nil {
			continue
		}
		if !p.TriggerAt.IsZero() {
			placed := p.TriggerAt.Add(time.Nanosecond)
			if placed.After(cut) {
				continue
			}
			exits = append(exits, paperfill.Order{ID: o.ID, Symbol: o.Symbol, Currency: o.Currency, Side: "sell", Kind: "market", Quantity: p.EnteredQuantity, PlacedAt: placed, ExpiresAt: placed.Add(24 * time.Hour), Mode: "replay"})
		}
	}
	if len(exits) > 0 {
		exitBook, err := paperfill.NewOccurrenceBook(model, exits)
		if err != nil {
			return nil, err
		}
		exitProjection, err := paperfill.ReplayBook(ctx, exitBook)
		if err != nil {
			return nil, err
		}
		exitAppender, err := paperfill.NewBookAppender(ctx, exitBook)
		if err != nil {
			return nil, err
		}
		for _, q := range quotes {
			if q.At.Before(exitProjection.LastClock) {
				continue
			}
			exitProjection, _, err = exitAppender.Apply(ctx, q)
			if err != nil {
				return nil, err
			}
		}
		if !cut.Before(exitProjection.LastClock) {
			exitProjection, _, err = exitAppender.Apply(ctx, paperfill.BookCommand{ID: "source-cut", At: cut})
			if err != nil {
				return nil, err
			}
		}
		exitBook = exitAppender.Book()
		result.ExitBook = &exitBook
		result.ExitProjection = &exitProjection
		for _, o := range exitProjection.Orders {
			p := positions[o.OrderID]
			p.ClosedQuantity = o.Projection.FilledQuantity
			p.ExitNetProceedsCents = o.Projection.NetProceedsCents
		}
	}
	for _, o := range orders {
		p := positions[o.ID]
		if p == nil {
			continue
		}
		p.UnclosedQuantity = p.EnteredQuantity - p.ClosedQuantity
		if p.UnclosedQuantity == 0 {
			p.Status = "modeled_fully_closed"
			net := p.ExitNetProceedsCents - p.EntryDebitCents
			p.ClosedNetCents = &net
		} else if !p.TriggerAt.IsZero() {
			p.Status = "exit_unfilled_or_partial_at_source_cut"
		}
		result.Positions = append(result.Positions, *p)
	}
	return result, nil
}

func paperEvaluationProtectiveReplayRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationEntrySourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-evaluation-declarations/{id}/protective-replay", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid protective replay query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		s, err := backend.PaperEvaluationEntrySources(ctx, id, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound) {
			write(w, 404, map[string]string{"error": "paper evaluation declaration not found"})
			return
		}
		if err == nil {
			var out PaperEvaluationProtectiveReplay
			out, err = inspectEvaluationProtectiveReplay(ctx, s)
			if err == nil {
				write(w, 200, out)
				return
			}
		}
		write(w, 503, map[string]string{"error": "owned protective replay unavailable"})
	})
}
