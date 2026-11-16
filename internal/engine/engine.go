// Package engine runs one strategy through trading days. On each session it
// asks the strategy for target weights from a point-in-time view, fills them
// at the next session's open with fees and slippage, applies splits and
// dividends to the holdings, and records cash, positions, equity and the
// daily return. The same code is meant to drive paper trading later.
package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// Scale is the number of decimal places results are rounded to when rendered.
// Arithmetic is exact until then.
const Scale = 12

// Version binds the accounting convention independently of strategy identity.
const Version = "daily-equity-receivables-v2"

var (
	ErrInvalidConfig = errors.New("invalid engine config")

	newYork      = mustZone("America/New_York")
	plainDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
)

func mustZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return zone
}

// CostModel prices a fill. Slippage moves the fill price against the trade by
// SlippageBps of the open; the fee is FeeBps of the fill's notional. Version
// names the model, so a result records which costs produced it.
type CostModel = executioncost.Model

// DefaultCost is the first cost model: no commission, as on Alpaca, and five
// basis points of slippage each way.
var DefaultCost = executioncost.Default()

// Config is one run. The engine reads bars through Source; the strategy sees
// only the views the engine builds. Sessions are the bar dates of Assets whose
// decision time falls within [Start, End]. The book opens at Start. Deposits,
// when set, schedule cash flows from then on.
type Config struct {
	Strategy    strategy.Strategy
	Source      strategy.BarSource
	Assets      []string
	Start, End  time.Time
	InitialCash string
	Cost        CostModel
	Deposits    *DepositSchedule
	Account     account.Type
	// Unlevered restricts targets to long-only gross <=1 and clips buys to
	// settled local cash including costs. Both account types then obey the same
	// funding rule; a fully invested target cannot borrow merely to pay slippage.
	Unlevered bool
	// SettlementDate overrides the default US equity T+1 calendar, for
	// exceptional clearing closures. It must return a later UTC civil date.
	SettlementDate func(time.Time) time.Time
}

// Position is a holding in shares, rendered to Scale places.
type Position struct {
	Asset  string `json:"asset"`
	Shares string `json:"shares"`
}

// Fill is one executed trade. DecidedOn is the session whose decision it
// carries out; Date is the later session whose open it filled at.
type Fill struct {
	Date      string `json:"date"`
	DecidedOn string `json:"decided_on"`
	Asset     string `json:"asset"`
	Shares    string `json:"shares"` // negative sells
	Open      string `json:"open"`
	Price     string `json:"price"`
	Fee       string `json:"fee"`
}

// Day is the book at one session's close. Deposited is the cash deposited
// before this session's open. Return is the session's time-weighted return,
// split at the open: the previous close to the book's value at the open, then
// that value plus the deposits to the close. TWR chains the returns from the
// start of the run, so deposits never count as profit.
type Day struct {
	Date       string        `json:"date"`
	DecidedAt  time.Time     `json:"decided_at"`
	Cash       string        `json:"cash"`
	Receivable string        `json:"dividend_receivable"`
	Equity     string        `json:"equity"`
	Deposited  string        `json:"deposited"`
	Return     string        `json:"return"`
	TWR        string        `json:"twr"`
	Positions  []Position    `json:"positions"`
	Decided    bool          `json:"decided"`
	Account    AccountStatus `json:"account"`
}

// AccountStatus is the capped book's cash and margin readback at the close.
type AccountStatus struct {
	EffectiveType        account.Type `json:"effective_type"`
	SettledCash          string       `json:"settled_cash"`
	UnsettledCash        string       `json:"unsettled_cash"`
	OvernightBuyingPower string       `json:"overnight_buying_power"`
	Maintenance          string       `json:"maintenance"`
	MarginExcess         string       `json:"margin_excess"`
	MaintenanceShortfall string       `json:"maintenance_shortfall"`
}

// Refusal is an attempted fill rejected by the account rules. The decision
// is consumed, so a later attempt needs a new strategy decision.
type Refusal struct {
	Date      string       `json:"date"`
	DecidedOn string       `json:"decided_on"`
	Asset     string       `json:"asset"`
	Shares    string       `json:"shares"`
	Code      account.Code `json:"code"`
}

// Result is a whole run. Pending holds the last decision's targets, which
// have no later session to fill at.
type Result struct {
	Version     string            `json:"engine_version"`
	StrategyID  string            `json:"strategy_id"`
	Cost        CostModel         `json:"cost"`
	Start       time.Time         `json:"start"`
	End         time.Time         `json:"end"`
	InitialCash string            `json:"initial_cash"`
	Days        []Day             `json:"days"`
	Fills       []Fill            `json:"fills"`
	Deposits    []Deposit         `json:"deposits"`
	Pending     []strategy.Target `json:"pending"`
	Account     account.Type      `json:"account"`
	Unlevered   bool              `json:"unlevered"`
	Refusals    []Refusal         `json:"refusals"`
}

type session struct {
	date string
	day  time.Time
	at   time.Time // decision time: 16:30 New York on the session date
}

type bar struct{ open, close *big.Rat }

type action struct {
	kind          string
	ex, pay       time.Time
	amount, ratio *big.Rat
}

type owed struct {
	pay  time.Time
	cash *big.Rat
}

// Run steps the configured sessions in order. For each session it applies
// corporate actions to the holdings, pays dividends due, fills the previous
// decision at this session's open, marks the book at the close, and then asks
// the strategy for its next decision. No fill uses a price from the session
// whose decision it carries out.
func Run(ctx context.Context, cfg Config) (Result, error) {
	return run(ctx, cfg, nil)
}

func run(ctx context.Context, cfg Config, record func(Day) error) (Result, error) {
	spec := cfg.Strategy.Spec()
	id, err := spec.ID()
	if err != nil {
		return Result{}, err
	}
	fee, slip, err := cfg.Cost.Parse()
	if err != nil {
		return Result{}, fmt.Errorf("%w: cost model: %w", ErrInvalidConfig, err)
	}
	cash, ok := decimal(cfg.InitialCash)
	if !ok || cash.Sign() <= 0 || cfg.Start.IsZero() || cfg.End.Before(cfg.Start) || len(cfg.Assets) == 0 || !cfg.Account.Valid() {
		return Result{}, ErrInvalidConfig
	}
	assets := slices.Clone(cfg.Assets)
	slices.Sort(assets)
	if len(slices.Compact(slices.Clone(assets))) != len(assets) {
		return Result{}, fmt.Errorf("%w: repeated asset", ErrInvalidConfig)
	}

	// The engine itself reads as of End to lay out the sessions, raw prices
	// and corporate actions. Strategies only ever see views as of their own
	// decision times.
	bars := map[string]map[string]bar{}
	actions := map[string][]action{}
	dates := map[string]bool{}
	for _, a := range assets {
		s, err := cfg.Source.AdjustedBars(ctx, a, cfg.End)
		if err != nil {
			return Result{}, fmt.Errorf("bars for %s: %w", a, err)
		}
		bars[a] = map[string]bar{}
		for _, b := range s.Bars {
			o, ok1 := decimal(b.Raw.Open)
			c, ok2 := decimal(b.Raw.Close)
			if !ok1 || !ok2 || o.Sign() <= 0 || c.Sign() <= 0 {
				return Result{}, fmt.Errorf("%s bar %s has an invalid raw price", a, b.Date)
			}
			bars[a][b.Date] = bar{o, c}
			dates[b.Date] = true
		}
		if actions[a], err = parseActions(s.Applied); err != nil {
			return Result{}, fmt.Errorf("actions for %s: %w", a, err)
		}
	}
	sessions := []session{}
	for date := range dates {
		day, _ := time.Parse(time.DateOnly, date)
		if at := decisionTime(day); !at.Before(cfg.Start) && !at.After(cfg.End) {
			sessions = append(sessions, session{date: date, day: day, at: at})
		}
	}
	slices.SortFunc(sessions, func(a, b session) int { return strings.Compare(a.date, b.date) })

	scheduled := []Deposit{}
	if cfg.Deposits != nil {
		if scheduled, err = cfg.Deposits.Due(cfg.Start, cfg.End); err != nil {
			return Result{}, err
		}
	}

	kind := cfg.Account
	if kind == "" {
		kind = account.Cash
	}
	funds := account.New(kind, cash)
	settlementDate := cfg.SettlementDate
	if settlementDate == nil {
		settlementDate = account.SettlementDate
	}
	out := Result{StrategyID: id, Cost: cfg.Cost, Start: cfg.Start.UTC(), End: cfg.End.UTC(), InitialCash: render(cash), Days: []Day{}, Fills: []Fill{}, Deposits: []Deposit{}, Pending: []strategy.Target{}, Account: kind, Refusals: []Refusal{}}
	out.Unlevered = cfg.Unlevered
	out.Version = Version
	growth := big.NewRat(1, 1) // chained time-weighted growth
	shares := map[string]*big.Rat{}
	last := map[string]*big.Rat{} // last raw close seen per asset
	pending := map[string]*big.Rat{}
	decidedOn, decidedAt := "", time.Time{}
	dividends := []owed{}
	var prior *time.Time
	equity := new(big.Rat).Set(cash)
	book := strategy.Book{}
	for _, s := range sessions {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		funds.Advance(s.day)
		// Corporate actions since the previous session, in ex-date order with
		// dividends first on a shared date: a dividend is earned on the shares
		// held before its ex-date, which includes any earlier split in the gap.
		// Nothing is held before the first session.
		for _, a := range assets {
			for _, c := range actions[a] {
				if prior == nil || !c.ex.After(*prior) || c.ex.After(s.day) {
					continue
				}
				held := shares[a]
				switch {
				case c.kind == "cash_dividend" && held != nil:
					dividends = append(dividends, owed{c.pay, quantize(new(big.Rat).Mul(held, c.amount))})
				case c.kind == "split":
					if held != nil {
						shares[a] = quantize(held.Mul(held, c.ratio))
					}
					if l := last[a]; l != nil {
						last[a] = quantize(l.Quo(l, c.ratio))
					}
				}
			}
		}
		kept := dividends[:0]
		for _, d := range dividends {
			if d.pay.After(s.day) {
				kept = append(kept, d)
				continue
			}
			funds.AddCash(d.cash)
		}
		dividends = kept
		receivable := new(big.Rat)
		for _, d := range dividends {
			receivable.Add(receivable, d.cash)
		}
		cash = funds.Cash()

		// Deposits due by this session's open arrive first, as settled cash.
		// Each scheduled deposit is applied once, at the first session open at
		// or after its scheduled time, and keeps that time as its effective
		// time.
		deposited := new(big.Rat)
		for len(scheduled) > 0 && !scheduled[0].EffectiveAt.After(openTime(s.day)) {
			d := scheduled[0]
			scheduled = scheduled[1:]
			amount, _ := decimal(d.Amount)
			deposited.Add(deposited, amount)
			d.AppliedOn = s.date
			out.Deposits = append(out.Deposits, d)
		}
		// The book's value at the open, before the deposits, priced with opens
		// where available. It splits the session's return at the deposit.
		atOpen := new(big.Rat).Set(cash)
		for _, a := range assets {
			if held := shares[a]; held != nil {
				p := last[a]
				if b, ok := bars[a][s.date]; ok {
					p = b.open
				}
				atOpen.Add(atOpen, new(big.Rat).Mul(held, p))
			}
		}
		funds.AddCash(deposited)
		cash = funds.Cash()
		// Earned dividends are assets/liabilities from ex-date. They enter TWR
		// valuation before a deposit, but cannot fund orders until payment.
		valuationOpen := new(big.Rat).Add(atOpen, receivable)

		// Fill the previous decision at this session's open, sized from the
		// equity at the open including the deposits.
		if len(pending) > 0 {
			open := new(big.Rat).Add(atOpen, deposited)
			if fillAt := openTime(s.day); !fillAt.After(decidedAt) {
				return Result{}, fmt.Errorf("fill at %s is not after its decision at %s", fillAt, decidedAt)
			}
			settlesOn := settlementDate(s.day)
			if !settlesOn.After(s.day) || !settlesOn.Equal(time.Date(settlesOn.Year(), settlesOn.Month(), settlesOn.Day(), 0, 0, 0, 0, time.UTC)) {
				return Result{}, fmt.Errorf("%w: settlement date", ErrInvalidConfig)
			}
			// Reduce existing positions before opening new ones. Ties use asset
			// order. Cash sales still cannot fund another buy until settlement.
			fillOrder := slices.Clone(assets)
			slices.SortStableFunc(fillOrder, func(a, b string) int {
				return cmp.Compare(reductionOrder(a, pending, shares, bars, s.date, open), reductionOrder(b, pending, shares, bars, s.date, open))
			})
			for _, a := range fillOrder {
				w, want := pending[a]
				b, ok := bars[a][s.date]
				if !want || !ok {
					continue
				}
				delete(pending, a)
				held := shares[a]
				if held == nil {
					held = new(big.Rat)
				}
				target := truncateShares(new(big.Rat).Quo(new(big.Rat).Mul(w, open), b.open))
				delta := new(big.Rat).Sub(target, held)
				if delta.Sign() == 0 {
					continue
				}
				price := executioncost.Price(b.open, slip, delta.Sign() > 0)
				notional := new(big.Rat).Mul(delta, price)
				if cfg.Unlevered && delta.Sign() > 0 {
					available := funds.Settled()
					unitCost := new(big.Rat).Mul(price, new(big.Rat).Add(big.NewRat(1, 1), fee))
					affordable := truncateShares(new(big.Rat).Quo(available, unitCost))
					if affordable.Sign() <= 0 {
						continue
					}
					if delta.Cmp(affordable) > 0 {
						delta = affordable
						target = new(big.Rat).Add(held, delta)
						notional.Mul(delta, price)
					}
				}
				cost := quantize(new(big.Rat).Mul(new(big.Rat).Abs(notional), fee))
				positions := markedPositions(assets, shares, last, bars, s.date)
				trade := account.Trade{Asset: a, Quantity: delta, Price: price, Fee: cost}
				// Reserve unpaid short-dividend liabilities during admission.
				// Positive receivables never grant buying power before payment.
				if receivable.Sign() < 0 {
					funds.AddCash(receivable)
				}
				code := funds.Execute(positions, trade, account.Overnight, s.day, settlesOn)
				if receivable.Sign() < 0 {
					funds.AddCash(new(big.Rat).Neg(receivable))
				}
				if code != "" {
					out.Refusals = append(out.Refusals, Refusal{Date: s.date, DecidedOn: decidedOn, Asset: a, Shares: render(delta), Code: code})
					continue
				}
				cash = funds.Cash()
				shares[a] = target
				out.Fills = append(out.Fills, Fill{Date: s.date, DecidedOn: decidedOn, Asset: a, Shares: render(delta), Open: render(b.open), Price: render(price), Fee: render(cost)})
				book.Fills = append(book.Fills, strategy.Fill{Asset: a, Time: openTime(s.day), Quantity: render(delta), Price: render(price)})
			}
		}

		// Mark the book at the close.
		for _, a := range assets {
			if b, ok := bars[a][s.date]; ok {
				last[a] = new(big.Rat).Set(b.close)
			}
		}
		next := new(big.Rat).Set(cash)
		next.Add(next, receivable)
		positions := []Position{}
		for _, a := range assets {
			if held := shares[a]; held != nil && held.Sign() != 0 {
				next.Add(next, new(big.Rat).Mul(held, last[a]))
				positions = append(positions, Position{Asset: a, Shares: render(held)})
			}
		}
		// Time-weighted: the return from the previous close to the open,
		// before the deposits, chained with the return from the open, with the
		// deposits, to the close.
		growthToday := big.NewRat(1, 1)
		if equity.Sign() > 0 {
			growthToday.Quo(valuationOpen, equity)
		}
		if base := new(big.Rat).Add(valuationOpen, deposited); base.Sign() > 0 {
			growthToday.Mul(growthToday, new(big.Rat).Quo(next, base))
		}
		ret := quantize(growthToday.Sub(growthToday, big.NewRat(1, 1)))
		growth = quantize(growth.Mul(growth, new(big.Rat).Add(ret, big.NewRat(1, 1))))
		equity = next
		twr := new(big.Rat).Sub(growth, big.NewRat(1, 1))
		day := Day{Date: s.date, DecidedAt: s.at.UTC(), Cash: render(cash), Equity: render(equity), Deposited: render(deposited), Return: render(ret), TWR: render(twr), Positions: positions}
		day.Receivable = render(receivable)
		if receivable.Sign() < 0 {
			funds.AddCash(receivable)
		}
		status := funds.Status(markedPositions(assets, shares, last, nil, ""))
		if receivable.Sign() < 0 {
			funds.AddCash(new(big.Rat).Neg(receivable))
		}
		day.Account = AccountStatus{EffectiveType: status.EffectiveType, SettledCash: render(funds.Settled()), UnsettledCash: render(new(big.Rat).Sub(cash, funds.Settled())), OvernightBuyingPower: render(status.OvernightBuyingPower), Maintenance: render(status.Maintenance), MarginExcess: render(status.Excess), MaintenanceShortfall: render(status.Shortfall)}

		if record != nil {
			if err := record(day); err != nil {
				return Result{}, err
			}
		}

		// Decide for the next session, once the warm-up is covered.
		book.Positions = book.Positions[:0]
		for _, p := range positions {
			book.Positions = append(book.Positions, strategy.Position{Asset: p.Asset, Quantity: p.Shares})
		}
		view, err := strategy.Build(ctx, spec, cfg.Source, s.at, assets, book)
		if err != nil {
			return Result{}, fmt.Errorf("view for %s: %w", s.date, err)
		}
		if warm(view, assets, spec.Warmup) {
			d, err := strategy.Decide(cfg.Strategy, view)
			if err != nil {
				return Result{}, fmt.Errorf("decision for %s: %w", s.date, err)
			}
			if d.KeepPositions {
				book.State = d.State
				day.Decided = true
				out.Days = append(out.Days, day)
				p := s.day
				prior = &p
				continue
			}
			// A decision is the complete target set: held assets it leaves out
			// go to zero.
			pending = map[string]*big.Rat{}
			for _, a := range assets {
				if held := shares[a]; held != nil && held.Sign() != 0 {
					pending[a] = new(big.Rat)
				}
			}
			for _, t := range d.Targets {
				w, _ := new(big.Rat).SetString(t.Weight)
				pending[t.Asset] = w
			}
			if cfg.Unlevered {
				gross := new(big.Rat)
				for _, w := range pending {
					if w.Sign() < 0 {
						return Result{}, fmt.Errorf("%w: unlevered short target", ErrInvalidConfig)
					}
					gross.Add(gross, w)
				}
				if gross.Cmp(big.NewRat(1, 1)) > 0 {
					return Result{}, fmt.Errorf("%w: unlevered target gross exceeds one", ErrInvalidConfig)
				}
			}
			book.State = d.State
			decidedOn, decidedAt, day.Decided = s.date, s.at, true
		}
		out.Days = append(out.Days, day)
		p := s.day
		prior = &p
	}
	for _, a := range assets {
		if w, ok := pending[a]; ok {
			out.Pending = append(out.Pending, strategy.Target{Asset: a, Weight: render(w)})
		}
	}
	return out, nil
}

func markedPositions(assets []string, shares, last map[string]*big.Rat, bars map[string]map[string]bar, date string) []account.Position {
	positions := make([]account.Position, 0, len(assets))
	for _, a := range assets {
		q, p := shares[a], last[a]
		if q == nil {
			q = new(big.Rat)
		}
		if b, ok := bars[a][date]; ok {
			p = b.open
		}
		// A never-held asset without a bar has no value or requirement.
		if p == nil {
			p = big.NewRat(1, 1)
		}
		positions = append(positions, account.Position{Asset: a, Quantity: q, Price: p})
	}
	return positions
}

func reductionOrder(a string, pending, shares map[string]*big.Rat, bars map[string]map[string]bar, date string, equity *big.Rat) int {
	w, want := pending[a]
	b, available := bars[a][date]
	held := shares[a]
	if !want || !available || held == nil || held.Sign() == 0 {
		return 1
	}
	target := truncateShares(new(big.Rat).Quo(new(big.Rat).Mul(w, equity), b.open))
	if target.Sign()*held.Sign() >= 0 && new(big.Rat).Abs(target).Cmp(new(big.Rat).Abs(held)) < 0 {
		return 0
	}
	return 1
}

// warm reports whether every asset has at least the warm-up number of bars.
func warm(v strategy.View, assets []string, n int) bool {
	for _, a := range assets {
		if len(v.Bars(a)) < max(n, 1) {
			return false
		}
	}
	return true
}

func parseActions(in []marketdata.CorporateAction) ([]action, error) {
	out := []action{}
	for _, c := range in {
		ex, err := time.Parse(time.DateOnly, c.ExDate)
		if err != nil {
			return nil, err
		}
		a := action{kind: c.Kind, ex: ex}
		switch c.Kind {
		case "cash_dividend":
			pay, err := time.Parse(time.DateOnly, c.PayDate)
			amount, ok := decimal(c.Amount)
			if err != nil || !ok {
				return nil, errors.New("invalid dividend")
			}
			a.pay, a.amount = pay, amount
		case "split":
			from, ok1 := decimal(c.From)
			to, ok2 := decimal(c.To)
			if !ok1 || !ok2 || from.Sign() == 0 {
				return nil, errors.New("invalid split")
			}
			a.ratio = to.Quo(to, from)
		default:
			return nil, fmt.Errorf("unsupported action %q", c.Kind)
		}
		out = append(out, a)
	}
	// Ex-date order, with dividends before splits on the same date.
	slices.SortStableFunc(out, func(a, b action) int {
		return cmp.Or(a.ex.Compare(b.ex), cmp.Compare(kindOrder(a.kind), kindOrder(b.kind)))
	})
	return out, nil
}

func kindOrder(kind string) int {
	if kind == "cash_dividend" {
		return 0
	}
	return 1
}

// decisionHour and decisionMinute set when a session's decision is made: 16:30 New York, half an
// hour after the regular close and past the SIP feed's 15-minute embargo. A
// fixed clock keeps decision times independent of later bar corrections. A bar
// that becomes available after this time is not in that session's view.
const decisionHour, decisionMinute = 16, 30

func decisionTime(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), decisionHour, decisionMinute, 0, 0, newYork).UTC()
}

// openTime is the session's 09:30 New York open, when its fills happen.
func openTime(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 9, 30, 0, 0, newYork).UTC()
}

func decimal(s string) (*big.Rat, bool) {
	if !plainDecimal.MatchString(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// stateScale is the fixed precision of the engine's own state: cash, shares,
// prices after splits and the chained return are rounded to 18 places after
// every step, half away from zero. Exact fractions would grow without bound
// over a long run; 18 places stay far below the 12 places results render to.
var stateScale = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func quantize(r *big.Rat) *big.Rat {
	n := new(big.Int).Mul(r.Num(), stateScale)
	q, m := new(big.Int).QuoRem(n, r.Denom(), new(big.Int))
	if new(big.Int).Abs(new(big.Int).Lsh(m, 1)).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(int64(n.Sign())))
	}
	return new(big.Rat).SetFrac(q, stateScale)
}

// render rounds to Scale places, half away from zero, without trailing zeros.
func render(r *big.Rat) string {
	s := r.FloatString(Scale)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}

// Truncate order quantities toward zero so rounding cannot create a cash debit
// on an otherwise fully funded, zero-cost buy.
func truncateShares(r *big.Rat) *big.Rat {
	n := new(big.Int).Mul(r.Num(), stateScale)
	return new(big.Rat).SetFrac(n.Quo(n, r.Denom()), stateScale)
}
