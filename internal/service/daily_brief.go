package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

// DailyBriefVersion pins the deterministic read policy, not trading authority.
const DailyBriefVersion = "daily-brief-v1"

// MaxBriefSourceBytes bounds source, HTTP and receipt reads independently of event capacity.
const MaxBriefSourceBytes = 16 << 20

// BriefSource is one detached finalized journal prefix with a server read clock.
type BriefSource struct {
	Book       account.CappedState
	AsOf       time.Time
	CapturedAt time.Time
	Families   map[string]BriefFamilyObservation `json:",omitempty"`
}

// BriefFamilyObservation freezes a separately clocked registry read. It grants
// no entry permission and is unavailable for a preceding historical as-of clock.
type BriefFamilyObservation struct {
	Gate        strategygate.Record
	SearchTotal *int64 `json:",omitempty"`
	Pending     bool
	Unavailable string `json:",omitempty"`
	ObservedAt  time.Time
}

// BriefBenchmark is an owner-imported SPY total-return index pair, retained in the
// immutable local receipt. Source hashes identify original bytes, not attest truth.
type BriefBenchmark struct {
	Symbol, Currency, Method, EvidenceClass      string
	StartAt, EndAt, ReceivedAt                   time.Time
	StartIndex, EndIndex, SourceID, SourceSHA256 string
}

// BriefValue makes absent evidence explicit; unavailable values have no number.
type BriefValue struct {
	Value       string `json:",omitempty"`
	Unavailable string `json:",omitempty"`
}

// BriefStrategy reports retained ownership, controls, sizing and oversight independently.
type BriefStrategy struct {
	SleeveID, StrategyID, Stage, State, Action, ReviewID string
	Budget, Scale, BudgetCeiling                         BriefValue
	FamilyStatus                                         string
	FamilyObservedAt                                     time.Time
	Reasons                                              []string
	PendingOrders                                        []string
	Target                                               *allocation.TargetDecision `json:",omitempty"`
}

// DailyBrief contains no model narrative, execution authority or qualification.
type DailyBrief struct {
	Version, BookID, JournalSHA256                                       string
	SourceSHA256                                                         string
	JournalVersion                                                       int64
	AsOf, ReturnStart                                                    time.Time
	SourceIDs, ValuationSourceIDs                                        []string
	OpeningCapital, Deposits, Cash, SettledCash, Equity, BookTWR, SPYTWR BriefValue
	DepositEvents                                                        []account.BookCommand
	Strategies                                                           []BriefStrategy
	Refusals, IdleCashReasons                                            []string
	Health                                                               *workhealth.Report `json:",omitempty"`
	HealthUnavailable                                                    string             `json:",omitempty"`
	HealthSourceID                                                       string             `json:",omitempty"`
	Halted                                                               bool
}

func unavailable(reason string) BriefValue { return BriefValue{Unavailable: reason} }
func number(v string) BriefValue           { return BriefValue{Value: v} }

var briefDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$`)
var briefHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func briefRat(v string) (*big.Rat, error) {
	if !briefDecimal.MatchString(v) {
		return nil, errors.New("invalid brief decimal")
	}
	n, _ := new(big.Rat).SetString(v)
	return n, nil
}

// BriefHash hashes canonical Go JSON, including frozen clocks and source IDs.
func BriefHash(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

type briefMark struct {
	id           string
	at, received time.Time
	prices       map[string]string
}

func briefMarks(b account.CappedState) []briefMark {
	out := []briefMark{}
	for _, e := range b.Events {
		if e.Review != nil {
			m := e.Review.Input.Mark
			if briefHash.MatchString(m.SourceSHA256) && m.EvidenceClass == "prospective_market_unqualified" && !m.ObservedAt.IsZero() && !m.ReceivedAt.Before(m.ObservedAt) && !m.ReceivedAt.After(e.At) {
				out = append(out, briefMark{e.ID, m.ObservedAt, m.ReceivedAt, m.Prices})
			}
		}
		if e.Incubation != nil { // One coherent quote clock; never combine asynchronous marks.
			byClock := map[time.Time]map[string]string{}
			received := map[time.Time]time.Time{}
			for symbol, q := range e.Incubation.Quotes {
				if symbol != q.Symbol || account.ValidateIncubationQuote(q, e.Incubation.DecidedAt, e.At) != nil {
					continue
				}
				bid, _ := briefRat(q.Bid)
				ask, _ := briefRat(q.Ask)
				if bid == nil || ask == nil {
					continue
				}
				if byClock[q.OccurredAt] == nil {
					byClock[q.OccurredAt] = map[string]string{}
				}
				byClock[q.OccurredAt][symbol] = new(big.Rat).Quo(new(big.Rat).Add(bid, ask), big.NewRat(2, 1)).FloatString(18)
				if q.ReceivedAt.After(received[q.OccurredAt]) {
					received[q.OccurredAt] = q.ReceivedAt
				}
			}
			clocks := []time.Time{}
			for at := range byClock {
				clocks = append(clocks, at)
			}
			slices.SortFunc(clocks, func(a, b time.Time) int { return a.Compare(b) })
			for _, at := range clocks {
				out = append(out, briefMark{e.ID, at, received[at], byClock[at]})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b briefMark) int { return a.at.Compare(b.at) })
	return out
}

// valueBrief uses one coherent retained mark set for every nonzero owned asset,
// including assets outside a current strategy universe and shared sleeve assets.
func valueBrief(cash *big.Rat, positions map[string]*big.Rat, marks []briefMark, at, asof time.Time, exact bool) (*big.Rat, string) {
	held := false
	for _, q := range positions {
		held = held || q.Sign() != 0
	}
	if !held {
		return new(big.Rat).Set(cash), "ledger:cash"
	}
	for i := len(marks) - 1; i >= 0; i-- {
		m := marks[i]
		if m.at.After(at) || m.received.After(asof) || exact && !m.at.Equal(at) || !exact && at.Sub(m.at) > time.Minute {
			continue
		}
		equity := new(big.Rat).Set(cash)
		complete := true
		for asset, q := range positions {
			if q.Sign() == 0 {
				continue
			}
			p, e := briefRat(m.prices[asset])
			if e != nil || p.Sign() <= 0 {
				complete = false
				break
			}
			equity.Add(equity, new(big.Rat).Mul(q, p))
		}
		if complete {
			return equity, m.id
		}
	}
	return nil, ""
}

func briefFinancial(b account.CappedState, asof time.Time, out *DailyBrief) error {
	cash, e := briefRat(b.Config.OpeningCash)
	if e != nil {
		return e
	}
	denom := new(big.Rat).Set(cash)
	growth := big.NewRat(1, 1)
	pos := map[string]*big.Rat{}
	marks := briefMarks(b)
	flows := []account.BookCommand{}
	for _, e := range b.Events {
		if e.Kind == "deposit" || e.Kind == "fill" {
			flows = append(flows, e)
		}
	}
	clock := func(e account.BookCommand) time.Time {
		if e.Fill != nil {
			return e.Fill.At
		}
		return e.EffectiveAt
	}
	slices.SortStableFunc(flows, func(a, b account.BookCommand) int {
		if d := clock(a).Compare(clock(b)); d != 0 {
			return d
		}
		if a.Kind == "deposit" && b.Kind != "deposit" {
			return -1
		}
		if b.Kind == "deposit" && a.Kind != "deposit" {
			return 1
		}
		return 0
	})
	missing := ""
	deposits := new(big.Rat)
	for _, e := range flows {
		if clock(e).After(asof) || clock(e).Before(b.Config.OpenedAt) {
			return errors.New("economic event outside finalized interval")
		}
		if e.Kind == "deposit" {
			amount, err := briefRat(e.Amount)
			if err != nil {
				return err
			}
			deposits.Add(deposits, amount)
			out.DepositEvents = append(out.DepositEvents, account.BookCommand{ID: e.ID, Kind: e.Kind, Amount: e.Amount, At: e.At, EffectiveAt: e.EffectiveAt})
			equity, id := valueBrief(cash, pos, marks, e.EffectiveAt, asof, true)
			if equity == nil {
				missing = "flow_boundary_valuation_unavailable:" + e.ID
			} else if denom.Sign() <= 0 || equity.Sign() <= 0 {
				missing = "nonpositive_flow_boundary_equity"
			} else {
				growth.Mul(growth, new(big.Rat).Quo(equity, denom))
				denom.Add(equity, amount)
				out.ValuationSourceIDs = append(out.ValuationSourceIDs, id)
			}
			cash.Add(cash, amount)
		} else {
			f := e.Fill
			if f == nil {
				return errors.New("missing finalized fill")
			}
			q, qe := briefRat(f.Quantity)
			p, pe := briefRat(f.Price)
			fee, fe := briefRat(f.Fee)
			if qe != nil || pe != nil || fe != nil {
				return errors.New("invalid finalized fill decimal")
			}
			if f.Side == "sell" {
				q.Neg(q)
			}
			if pos[f.Symbol] == nil {
				pos[f.Symbol] = new(big.Rat)
			}
			pos[f.Symbol].Add(pos[f.Symbol], q)
			cash.Sub(cash, new(big.Rat).Mul(q, p))
			cash.Sub(cash, fee)
		}
	}
	out.Deposits = number(deposits.FloatString(9))
	out.Cash = number(cash.FloatString(9))
	equity, id := valueBrief(cash, pos, marks, asof, asof, false)
	if equity == nil {
		out.Equity = unavailable("current_complete_valuation_unavailable")
		out.BookTWR = unavailable("current_complete_valuation_unavailable")
		return nil
	}
	out.Equity = number(equity.FloatString(9))
	out.ValuationSourceIDs = append(out.ValuationSourceIDs, id)
	if missing != "" {
		out.BookTWR = unavailable(missing)
	} else if denom.Sign() <= 0 || equity.Sign() < 0 {
		out.BookTWR = unavailable("nonpositive_return_base")
	} else {
		growth.Mul(growth, new(big.Rat).Quo(equity, denom))
		out.BookTWR = number(growth.Sub(growth, big.NewRat(1, 1)).FloatString(18))
	}
	return nil
}

// BuildDailyBrief reads finalized records only. It never appends a book event,
// observes new risk, fetches a quote, runs a model or treats review returns as TWR.
// The caller must retain source and benchmark with the result before delivery.
func BuildDailyBrief(source BriefSource, benchmark *BriefBenchmark) (DailyBrief, error) {
	raw, e := json.Marshal(source)
	if e != nil {
		return DailyBrief{}, e
	}
	if len(raw) > MaxBriefSourceBytes {
		return DailyBrief{}, errors.New("brief source exceeds bound")
	}
	var frozen BriefSource
	if e = json.Unmarshal(raw, &frozen); e != nil {
		return DailyBrief{}, e
	}
	b := frozen.Book
	if frozen.CapturedAt.IsZero() {
		frozen.CapturedAt = frozen.AsOf
	}
	if frozen.CapturedAt.Before(frozen.AsOf) {
		return DailyBrief{}, errors.New("capture precedes valuation clock")
	}
	if frozen.AsOf.IsZero() {
		return DailyBrief{}, errors.New("invalid finalized brief prefix")
	}
	view, roster, e := account.BriefBookView(b, frozen.AsOf)
	if e != nil {
		return DailyBrief{}, e
	}
	hash, e := BriefHash(b)
	if e != nil {
		return DailyBrief{}, e
	}
	out := DailyBrief{Version: DailyBriefVersion, BookID: b.Config.ID, JournalSHA256: hash, JournalVersion: b.Version, AsOf: frozen.AsOf, ReturnStart: b.Config.OpenedAt, OpeningCapital: number(b.Config.OpeningCash), SettledCash: number(view.SettledCash), Halted: view.Halted, SPYTWR: unavailable("comparable_SPY_total_return_evidence_unavailable"), HealthUnavailable: "retained_book_health_unavailable", Strategies: []BriefStrategy{}, DepositEvents: []account.BookCommand{}, SourceIDs: []string{}, ValuationSourceIDs: []string{}, Refusals: []string{}, IdleCashReasons: []string{}}
	out.SourceSHA256, e = BriefHash(struct {
		Source    BriefSource
		Benchmark *BriefBenchmark
	}{frozen, benchmark})
	if e != nil {
		return out, e
	}
	for _, e := range b.Events {
		out.SourceIDs = append(out.SourceIDs, e.ID)
		if e.Incubation != nil && e.Incubation.Health != nil {
			out.Health = e.Incubation.Health
			out.HealthSourceID = e.ID
			out.HealthUnavailable = "current_health_not_rechecked;retained_observation_only"
		}
	}
	if out.Health != nil {
		if out.Health.At.After(out.AsOf) {
			out.HealthUnavailable = "future_retained_health"
		} else {
			for _, c := range out.Health.Checks {
				if c.Ready && (c.ExpiresAt.IsZero() || !out.AsOf.Before(c.ExpiresAt)) {
					out.HealthUnavailable = "retained_health_expired_or_unbounded"
				}
			}
		}
	}
	if e = briefFinancial(b, frozen.AsOf, &out); e != nil {
		return out, e
	}
	if out.Cash.Value != view.Cash {
		return out, errors.New("financial projection differs from finalized account cash")
	}
	if benchmark != nil {
		v := *benchmark
		start, se := briefRat(v.StartIndex)
		end, ee := briefRat(v.EndIndex)
		if v.Symbol == "SPY" && v.Currency == "USD" && v.Method == "total_return_index" && v.EvidenceClass == "prospective_market_unqualified" && v.StartAt.Equal(out.ReturnStart) && v.EndAt.Equal(out.AsOf) && !v.ReceivedAt.Before(v.EndAt) && !v.ReceivedAt.After(frozen.CapturedAt) && v.SourceID != "" && len(v.SourceID) <= 128 && briefHash.MatchString(v.SourceSHA256) && se == nil && ee == nil && start.Sign() > 0 && end.Sign() > 0 {
			out.SPYTWR = number(new(big.Rat).Sub(new(big.Rat).Quo(end, start), big.NewRat(1, 1)).FloatString(18))
		}
	}
	ids := []string{}
	for id := range roster {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s := roster[id]
		v := BriefStrategy{SleeveID: id, StrategyID: s.StrategyID, Stage: s.Stage, State: s.Stage, Action: "unavailable", Budget: unavailable("retained_sizing_unavailable"), Scale: unavailable("retained_review_unavailable"), BudgetCeiling: unavailable("retained_review_unavailable"), Reasons: []string{}, PendingOrders: []string{}}
		v.FamilyStatus = "unavailable:current_family_fence_not_retained_in_book"
		if f, ok := frozen.Families[id]; ok && !f.ObservedAt.IsZero() && !f.ObservedAt.After(out.AsOf) && f.Gate.Declaration.ID == s.GateID && f.Gate.Declaration.StrategyID == s.StrategyID && f.Gate.Decision != nil && !f.Gate.DeclaredAt.After(out.AsOf) && !f.Gate.Decision.At.After(out.AsOf) {
			v.FamilyObservedAt = f.ObservedAt
			switch {
			case f.Pending:
				v.FamilyStatus = "pending_family"
			case f.Unavailable != "":
				v.FamilyStatus = "unavailable:" + f.Unavailable
			case f.SearchTotal == nil:
				v.FamilyStatus = "unavailable:family_count_missing"
			case *f.SearchTotal != f.Gate.Decision.SearchTotal:
				v.FamilyStatus = "gate_search_history_changed"
			default:
				v.FamilyStatus = "retained_count_matches_gate;not_entry_authority"
			}
			if f.Pending || v.FamilyStatus == "gate_search_history_changed" {
				v.Reasons = append(v.Reasons, v.FamilyStatus)
				out.Refusals = append(out.Refusals, id+":"+v.FamilyStatus)
			}
		}

		familyReasons := slices.Clone(v.Reasons)
		for _, e := range b.Events {
			if e.Incubation != nil {
				for _, t := range e.Incubation.Rebalance.Targets {
					if t.SleeveID == id {
						t := t.Decision
						v.Target = &t
					}
				}
			}
			if e.Rebalance != nil {
				for _, t := range e.Rebalance.Targets {
					if t.SleeveID == id {
						t := t.Decision
						v.Target = &t
					}
				}
			}
			if e.Review != nil && e.Review.SleeveID == id {
				r := e.Review
				v.Action = r.Action
				v.ReviewID = e.ID
				v.Scale = number(r.Scale)
				v.BudgetCeiling = number(r.BudgetCeiling)
				v.Reasons = append(slices.Clone(familyReasons), r.Reasons...)
				if r.Action == "retire" {
					v.State = "retired"
				} else if r.Action == "demote" {
					v.State = "review_quarantine"
				} else if r.Action == "grow" && v.State == "review_quarantine" {
					if scale, err := briefRat(r.Scale); err == nil && scale.Cmp(big.NewRat(1, 1)) >= 0 {
						v.State = v.Stage
					}
				}
			}
		}
		if view.Distribution != nil {
			for _, a := range view.Distribution.Sleeves {
				if a.SleeveID == id {
					v.Budget = number(a.Budget)
					v.Reasons = append(v.Reasons, a.Reason)
					if a.Status != "ready" && a.Status != "" {
						out.Refusals = append(out.Refusals, id+":"+a.Status+":"+a.Reason)
					}
				}
			}
		}
		if view.Risk.Breakers[s.StrategyID] {
			v.State = "owner_paused"
		}
		if view.Risk.Retired[s.StrategyID] {
			v.State = "retired"
		}
		for _, p := range view.Pending {
			if p.SleeveID == id {
				v.PendingOrders = append(v.PendingOrders, p.ID)
			}
		}
		if len(v.PendingOrders) > 0 {
			v.Reasons = append(v.Reasons, "pending_orders_require_reconciliation")
		}
		out.Strategies = append(out.Strategies, v)
	}
	if view.Halted {
		out.Refusals = append(out.Refusals, "risk_hard_halt")
	}
	if view.Risk.Paused {
		out.Refusals = append(out.Refusals, view.Risk.PauseReason)
	}
	if view.Risk.Kill {
		out.Refusals = append(out.Refusals, "risk_kill_switch")
	}
	if view.Risk.ReduceOnly {
		out.Refusals = append(out.Refusals, "risk_reduce_only")
	}
	out.IdleCashReasons = append(out.IdleCashReasons, out.Refusals...)
	if out.Health != nil {
		for name, c := range out.Health.Checks {
			if !c.Ready {
				out.Refusals = append(out.Refusals, "health:"+name+":"+c.Code)
				out.IdleCashReasons = append(out.IdleCashReasons, "health:"+name+":"+c.Code)
			}
		}
	}
	if len(roster) == 0 {
		out.IdleCashReasons = append(out.IdleCashReasons, "no_retained_sleeves")
	}
	if view.Distribution == nil {
		out.IdleCashReasons = append(out.IdleCashReasons, "retained_distribution_unavailable")
	} else {
		out.IdleCashReasons = append(out.IdleCashReasons, "fixed_pool_and_sleeve_caps_leave_cash;cash_is_not_a_target_to_spend")
	}
	for _, v := range out.Strategies {
		if v.State == "owner_paused" || v.State == "retired" {
			out.IdleCashReasons = append(out.IdleCashReasons, v.SleeveID+":"+v.State)
		}
		if len(v.PendingOrders) > 0 {
			out.IdleCashReasons = append(out.IdleCashReasons, v.SleeveID+":pending_reconciliation")
		}
	}
	slices.Sort(out.Refusals)
	slices.Sort(out.IdleCashReasons)
	return out, nil
}

// RenderDailyBrief renders the identical frozen brief for terminal and webhook.
func RenderDailyBrief(w io.Writer, b DailyBrief) error {
	clock := func(at time.Time) string {
		if at.IsZero() {
			return "unavailable"
		}
		return at.Format(time.RFC3339Nano)
	}
	value := func(v BriefValue) string {
		if v.Value != "" {
			return v.Value
		}
		if v.Unavailable == "" {
			return "unavailable"
		}
		return "unavailable (" + v.Unavailable + ")"
	}
	_, e := fmt.Fprintf(w, "Mantik daily brief — %s — %s\nPaper research; unqualified. %s / journal %d / %s\nSource receipt inputs: %s; valuation sources: %v\nSince %s\nEquity USD: %s\nBook TWR: %s; SPY total-return TWR: %s\nOpening capital USD: %s; deposits USD: %s\nCash USD: %s; settled cash USD: %s\n", b.BookID, b.AsOf.Format(time.RFC3339Nano), b.Version, b.JournalVersion, b.JournalSHA256, b.SourceSHA256, b.ValuationSourceIDs, b.ReturnStart.Format(time.RFC3339Nano), value(b.Equity), value(b.BookTWR), value(b.SPYTWR), value(b.OpeningCapital), value(b.Deposits), value(b.Cash), value(b.SettledCash))
	if e != nil {
		return e
	}
	for _, d := range b.DepositEvents {
		if _, e = fmt.Fprintf(w, "Deposit %s: USD %s; effective %s; recorded %s\n", d.ID, d.Amount, d.EffectiveAt.Format(time.RFC3339Nano), d.At.Format(time.RFC3339Nano)); e != nil {
			return e
		}
	}
	for _, s := range b.Strategies {
		if _, e = fmt.Fprintf(w, "Strategy %s / %s: %s; review action %s (%s), applies to later decisions; budget USD %s; scale %s; ceiling USD %s; pending %v; reasons %v; retained target %v; family %s (observed %s)\n", s.SleeveID, s.StrategyID, s.State, s.Action, s.ReviewID, value(s.Budget), value(s.Scale), value(s.BudgetCeiling), s.PendingOrders, s.Reasons, s.Target, s.FamilyStatus, clock(s.FamilyObservedAt)); e != nil {
			return e
		}
	}
	if _, e = fmt.Fprintf(w, "Refusals (retained coverage only): %v\nIdle cash reasons: %v\nHealth: ", b.Refusals, b.IdleCashReasons); e != nil {
		return e
	}
	if b.Health == nil {
		_, e = fmt.Fprintf(w, "unavailable (%s)\n", b.HealthUnavailable)
		return e
	}
	_, e = fmt.Fprintf(w, "retained at %s (%s); current health unavailable: %s\n", b.Health.At.Format(time.RFC3339Nano), b.HealthSourceID, b.HealthUnavailable)
	if e != nil {
		return e
	}
	keys := []string{}
	for k := range b.Health.Checks {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		c := b.Health.Checks[k]
		if _, e = fmt.Fprintf(w, "  %s: %s / %s; observed %s; expires %s; evidence %v\n", k, c.Status, c.Code, clock(c.At), clock(c.ExpiresAt), c.EvidenceRefs); e != nil {
			return e
		}
	}
	keys = keys[:0]
	for k := range b.Health.Work {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		c := b.Health.Work[k]
		if _, e = fmt.Fprintf(w, "  retained work %s: %s / %s; observed %s; expires %s; evidence %v\n", k, c.Status, c.Code, clock(c.At), clock(c.ExpiresAt), c.EvidenceRefs); e != nil {
			return e
		}
	}

	return nil
}
