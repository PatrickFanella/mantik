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

// Entry-only counterfactuals share displayed liquidity within each variant.
// Variants are alternative studies, never summed into portfolio performance.
type PaperEvaluationEntryReplay struct {
	Schema                      string                              `json:"schema"`
	Evidence                    storage.PaperEvaluationEvidence     `json:"evidence"`
	PolicySHA256                string                              `json:"policy_sha256"`
	Method                      string                              `json:"method"`
	Rows                        []PaperEvaluationEntryReplayRow     `json:"rows"`
	Variants                    []PaperEvaluationEntryVariantReplay `json:"variants"`
	Authority                   string                              `json:"authority"`
	PortfolioEconomicsAvailable bool                                `json:"portfolio_economics_available"`
	AdmissionAvailable          bool                                `json:"admission_available"`
	ExecutionAvailable          bool                                `json:"execution_available"`
	QualificationCredit         bool                                `json:"qualification_credit"`
	SHA256                      string                              `json:"sha256"`
}
type PaperEvaluationEntryReplayRow struct {
	DecisionID     string                      `json:"decision_id"`
	DecisionSHA256 string                      `json:"decision_sha256"`
	StrategyID     string                      `json:"strategy_id"`
	Status         string                      `json:"status"`
	Market         *PaperOutcomeMarketEvidence `json:"market,omitempty"`
	BaseCost       *storage.OwnedExecutionCost `json:"base_cost,omitempty"`
	StressCost     *storage.OwnedExecutionCost `json:"stress_cost,omitempty"`
}
type PaperEvaluationEntryVariantReplay struct {
	StrategyID string                           `json:"strategy_id"`
	Status     string                           `json:"status"`
	Base       *PaperEvaluationEntryModelReplay `json:"base,omitempty"`
	Stress     *PaperEvaluationEntryModelReplay `json:"stress,omitempty"`
	Exclusions []PaperEvaluationEntryExclusion  `json:"exclusions"`
}
type PaperEvaluationEntryExclusion struct {
	InputID string `json:"input_id"`
	Reason  string `json:"reason"`
}
type PaperEvaluationEntryModelReplay struct {
	Book       paperfill.Book           `json:"book"`
	Projection paperfill.BookProjection `json:"projection"`
}

func inspectEvaluationEntryReplay(ctx context.Context, s storage.PaperEvaluationEntrySources) (PaperEvaluationEntryReplay, error) {
	out := PaperEvaluationEntryReplay{Schema: "mantik-owned-evaluation-entry-replay-v1", Evidence: s.Evidence, PolicySHA256: s.Policy.SHA256, Method: "predeclared-shared-entry-counterfactual-v1", Rows: []PaperEvaluationEntryReplayRow{}, Variants: []PaperEvaluationEntryVariantReplay{}, Authority: "service_owned_entry_counterfactual_only"}
	for _, r := range s.Rows {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationEntryReplay{}, err
		}
		row := PaperEvaluationEntryReplayRow{DecisionID: r.Decision.Command.ID, DecisionSHA256: r.Decision.SHA256, StrategyID: r.Strategy.Command.Definition.ID, Status: r.Status, BaseCost: r.BaseCost, StressCost: r.StressCost}
		if r.Outcome != nil {
			market, err := inspectPaperOutcomeMarket(ctx, *r.Outcome)
			if err != nil {
				return PaperEvaluationEntryReplay{}, err
			}
			row.Market = &market
		}
		out.Rows = append(out.Rows, row)
	}
	for _, variant := range s.Evidence.Declaration.Command.Plan.Variants {
		result := PaperEvaluationEntryVariantReplay{StrategyID: variant.ID, Status: "no_selected_decisions", Exclusions: []PaperEvaluationEntryExclusion{}}
		sources := []storage.PaperEvaluationEntrySource{}
		markets := []PaperOutcomeMarketEvidence{}
		for i, r := range s.Rows {
			if r.Strategy.Command.Definition.ID == variant.ID {
				sources = append(sources, r)
				if out.Rows[i].Market != nil {
					markets = append(markets, *out.Rows[i].Market)
				}
			}
		}
		if len(sources) > 0 {
			result.Status = "entry_sources_unavailable"
			ready := s.Evidence.Phase == "outcome_deadline_elapsed"
			if !ready {
				result.Status = "pending_outcome_deadline"
			}
			for _, r := range sources {
				if r.Status != "entry_sources_selected" {
					ready = false
				}
			}
			if len(sources) > paperfill.MaxBookOrders {
				result.Status = "entry_order_capacity_exceeded"
				ready = false
			}
			if ready {
				base, stress, exclusions, err := replayEvaluationEntryVariant(ctx, sources, markets, *s.Policy.Command.ExecutionModel, s.Evidence.Declaration.Command.Plan.StressModel)
				if err != nil {
					return PaperEvaluationEntryReplay{}, err
				}
				result.Base, result.Stress, result.Exclusions = base, stress, exclusions
				result.Status = "entry_replay_only_exits_and_portfolio_unavailable"
				if base == nil {
					result.Status = "entry_quote_capacity_exceeded"
				}
			}
		}
		out.Variants = append(out.Variants, result)
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > storage.MaxPaperEvaluationEvidenceBytes {
		return PaperEvaluationEntryReplay{}, storage.ErrPaperEvaluationEvidenceInvalid
	}
	return out, nil
}

func replayEvaluationEntryVariant(ctx context.Context, sources []storage.PaperEvaluationEntrySource, markets []PaperOutcomeMarketEvidence, base, stress paperfill.Model) (*PaperEvaluationEntryModelReplay, *PaperEvaluationEntryModelReplay, []PaperEvaluationEntryExclusion, error) {
	orders := [2][]paperfill.Order{}
	models := [2]paperfill.Model{base, stress}
	first, last := sources[0].Decision.FinalizedAt.Add(time.Nanosecond), sources[0].BaseCost.ValidUntil
	symbol, feed, class := sources[0].Strategy.Command.Definition.Symbol, sources[0].Strategy.Command.Definition.Feed, sources[0].Strategy.Command.Definition.InputEvidenceClass
	maxAge := sources[0].Strategy.Command.Definition.Safety.MaxQuoteAgeSeconds
	for _, r := range sources {
		definition := r.Strategy.Command.Definition
		if definition.Symbol != symbol || definition.Feed != feed || definition.InputEvidenceClass != class || definition.Safety.MaxQuoteAgeSeconds != maxAge {
			return nil, nil, nil, storage.ErrPaperEvaluationEvidenceInvalid
		}
		if r.Decision.FinalizedAt.Add(time.Nanosecond).Before(first) {
			first = r.Decision.FinalizedAt.Add(time.Nanosecond)
		}
		if r.BaseCost.ValidUntil.After(last) {
			last = r.BaseCost.ValidUntil
		}
		for i, cost := range []*storage.OwnedExecutionCost{r.BaseCost, r.StressCost} {
			orders[i] = append(orders[i], paperfill.Order{ID: r.Decision.Command.ID, Symbol: symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: cost.Quantity, LimitCents: cost.LimitCents, MaxDebitCents: cost.MaxDebitCents, PlacedAt: r.Decision.FinalizedAt.Add(time.Nanosecond), ExpiresAt: cost.ValidUntil, Mode: "replay"})
		}
	}
	for i := range orders {
		slices.SortFunc(orders[i], func(a, b paperfill.Order) int { return strings.Compare(a.ID, b.ID) })
	}
	rows := map[string]PaperOutcomeMarketRow{}
	exclusions := []PaperEvaluationEntryExclusion{}
	for _, market := range markets {
		if market.Provider != "alpaca" || market.Feed != feed {
			return nil, nil, nil, storage.ErrPaperEvaluationEvidenceInvalid
		}
		for _, row := range market.Rows {
			// The selected-forward marker is outcome-specific; source identity is not.
			row.SelectedForwardQuote = false
			row.TemporalStatus = ""
			if old, ok := rows[row.InputID]; ok && !reflect.DeepEqual(old, row) {
				return nil, nil, nil, storage.ErrPaperEvaluationEvidenceInvalid
			}
			rows[row.InputID] = row
		}
	}
	ordered := []PaperOutcomeMarketRow{}
	for _, row := range rows {
		ordered = append(ordered, row)
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
	for _, row := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		reason := ""
		switch {
		case row.AvailableAt.IsZero():
			reason = "source_availability_unavailable"
		case row.AvailableAt.Before(first) || !row.AvailableAt.Before(last):
			reason = "outside_entry_replay_window"
		case row.Quote == nil || !row.Quote.ShareQuantitiesAvailable:
			reason = "usable_share_dimensions_unavailable"
		case row.Quote.Symbol != symbol || row.EvidenceClass != class:
			reason = "strategy_quote_scope_mismatch"
		case row.Quote.BidShares > paperfill.MaxQuantity || row.Quote.AskShares > paperfill.MaxQuantity:
			reason = "fill_method_quantity_range_exceeded"
		case row.AvailableAt.Sub(row.OccurredAt) > time.Duration(maxAge)*time.Second:
			reason = "strategy_quote_age_exceeded"
		case row.SessionEvidence == nil || !row.SessionEvidence.Evidence.Quote.RegularSessionEligible || row.SessionEvidence.Evidence.Session.Status != "open":
			reason = "regular_session_evidence_unavailable"
		}
		if reason != "" {
			exclusions = append(exclusions, PaperEvaluationEntryExclusion{row.InputID, reason})
			continue
		}
		q := row.Quote
		// ReceivedAt for the fill method means first usable owner availability,
		// not transport receipt. All original clocks remain in the pinned market row.
		quote := paperfill.Quote{ID: "quote-" + workerplan.Hash(row.InputID), SourceSHA256: workerplan.Hash(row), Symbol: q.Symbol, Currency: q.Currency, Quality: "usable", EvidenceClass: row.EvidenceClass, OccurredAt: q.OccurredAt, ReceivedAt: row.AvailableAt, BidCents: q.BidCents, AskCents: q.AskCents, BidQuantity: q.BidShares, AskQuantity: q.AskShares}
		commands = append(commands, paperfill.BookCommand{ID: quote.ID, At: row.AvailableAt, Quote: &quote})
	}
	// Keep a terminal expiry command within the method's bounded journal.
	if len(commands) >= paperfill.MaxEntries {
		return nil, nil, exclusions, nil
	}
	commands = append(commands, paperfill.BookCommand{ID: "entry-expiry", At: last})
	var results [2]*PaperEvaluationEntryModelReplay
	for i, model := range models {
		b, err := paperfill.NewOccurrenceBook(model, orders[i])
		if err != nil {
			return nil, nil, nil, err
		}
		appender, err := paperfill.NewBookAppender(ctx, b)
		if err != nil {
			return nil, nil, nil, err
		}
		var projection paperfill.BookProjection
		for _, command := range commands {
			var err error
			projection, _, err = appender.Apply(ctx, command)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		results[i] = &PaperEvaluationEntryModelReplay{Book: appender.Book(), Projection: projection}
	}
	return results[0], results[1], exclusions, nil
}
func paperEvaluationEntryReplayRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationEntrySourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-evaluation-declarations/{id}/entry-replay", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid entry replay query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		source, err := backend.PaperEvaluationEntrySources(ctx, id, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound) {
			write(w, 404, map[string]string{"error": "paper evaluation declaration not found"})
			return
		}
		if err == nil {
			var out PaperEvaluationEntryReplay
			out, err = inspectEvaluationEntryReplay(ctx, source)
			if err == nil {
				write(w, 200, out)
				return
			}
		}
		write(w, 503, map[string]string{"error": "owned entry replay unavailable"})
	})
}
