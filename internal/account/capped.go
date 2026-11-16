package account

import (
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"slices"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

var (
	// ErrBookConflict means the retained book changed; reload before a new command.
	ErrBookConflict = errors.New("capped book version or command conflicts")
	// ErrBookNotFound means the named local paper book has not been created.
	ErrBookNotFound = errors.New("capped paper book not found")
	bookID          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	bookNumber      = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,9})?$`)
)

// CappedConfig binds this ledger to one expected paper account. Opening capital
// and the deposit schedule belong to this book, independently of broker cash.
type CappedConfig struct {
	ID, BrokerAccountID        string
	Type                       Type
	OpenedAt                   time.Time
	OpeningCash, DepositAmount string
	EveryWeeks                 int
}

// BookOrder reserves a simple day limit order before any broker POST. Fee is
// a conservative total fee reserve. Only these recorded client IDs own fills.
type BookOrder struct {
	ReduceOnly                             bool
	ID, Symbol, Quantity, Limit, Side, Fee string
	AllocationID, SleeveID, StrategyID     string
	At                                     time.Time
}

// BookFill binds one immutable venue activity to its reserved client/venue IDs.
// Raw evidence remains in the existing Alpaca journal; SourceSHA256 binds it.
type BookFill struct {
	ID, OrderID, VenueOrderID, Symbol, Quantity, Price, Fee, Side, SourceSHA256 string
	At                                                                          time.Time
}

// BookCommand is one ordered journal event. At is the local application clock;
// fills and deposits keep their original economic clocks in separate fields.
type BookCommand struct {
	ID, Kind, OrderID, VenueOrderID, Amount string
	At, EffectiveAt                         time.Time
	Order                                   *BookOrder
	Fill                                    *BookFill
	Marks                                   map[string]string
	Rebalance                               *allocation.RebalanceRequest
	RiskControl                             *risk.Control
	Incubation                              *IncubationPlan `json:",omitempty"`
	ModelFill                               *IncubationFill `json:",omitempty"`
}

// CappedState is an append-only replayable ledger. Version is its event count.
type CappedState struct {
	Config  CappedConfig
	Version int64
	Events  []BookCommand
}

// CappedSnapshot reports the book alone. Pending orders consume capacity even
// after an ambiguous submission. No field imports Alpaca's buying power.
type CappedSnapshot struct {
	Cash, SettledCash, Equity string
	Positions                 map[string]string
	Pending                   []BookOrder
	Deposits                  int
	Halted                    bool
	Risk                      risk.State
	Distribution              *allocation.Distribution
}
type openOrder struct {
	order       BookOrder
	venue       string
	filled, fee *big.Rat
	released    bool
}
type bookReplay struct {
	sleeves      map[string]allocation.Sleeve
	targets      map[string]allocation.TargetDecision
	funds        *Book
	positions    map[string]*big.Rat
	orders       map[string]*openOrder
	deposits     int
	halted       bool
	risk         risk.State
	distribution *allocation.Distribution
}

func bookRat(s string) (*big.Rat, error) {
	if !bookNumber.MatchString(s) {
		return nil, errors.New("invalid book decimal")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("invalid book decimal")
	}
	return r, nil
}
func (c CappedConfig) validate() error {
	a, e := bookRat(c.OpeningCash)
	d, de := bookRat(c.DepositAmount)
	if !bookID.MatchString(c.ID) || !bookID.MatchString(c.BrokerAccountID) || !c.Type.Valid() || c.Type == "" || c.OpenedAt.IsZero() || c.OpenedAt.Year() < 1900 || e != nil || de != nil || a.Sign() <= 0 || d.Sign() <= 0 || c.EveryWeeks < 1 || c.EveryWeeks > 52 {
		return errors.New("invalid capped book configuration")
	}
	return nil
}

// NewCappedState creates the declared book without issuing orders or funding the venue.
func NewCappedState(c CappedConfig) (CappedState, error) {
	c.OpenedAt = c.OpenedAt.UTC()
	if e := c.validate(); e != nil {
		return CappedState{}, e
	}
	return CappedState{Config: c, Events: []BookCommand{}}, nil
}
func civil(at time.Time) time.Time {
	z, _ := time.LoadLocation("America/New_York")
	local := at.In(z)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}
func marks(ps map[string]*big.Rat, prices map[string]string) ([]Position, error) {
	out := []Position{}
	keys := []string{}
	for a := range ps {
		keys = append(keys, a)
	}
	slices.Sort(keys)
	for _, a := range keys {
		p, e := bookRat(prices[a])
		if e != nil || p.Sign() <= 0 {
			return nil, errors.New("missing positive book mark")
		}
		out = append(out, Position{Asset: a, Quantity: new(big.Rat).Set(ps[a]), Price: p})
	}
	return out, nil
}
func applyTrade(b *Book, ps map[string]*big.Rat, o BookOrder, qty, price, fee *big.Rat, prices map[string]string, at time.Time) error {
	if _, ok := ps[o.Symbol]; !ok {
		ps[o.Symbol] = new(big.Rat)
	}
	copyPrices := map[string]string{}
	for a, p := range prices {
		copyPrices[a] = p
	}
	copyPrices[o.Symbol] = price.FloatString(9)
	positions, e := marks(ps, copyPrices)
	if e != nil {
		return e
	}
	q := new(big.Rat).Set(qty)
	if o.Side == "sell" {
		q.Neg(q)
	}
	day := civil(at)
	b.Advance(day)
	code := b.Execute(positions, Trade{Asset: o.Symbol, Quantity: q, Price: price, Fee: fee}, Overnight, day, SettlementDate(day))
	if code != "" {
		return errors.New(string(code))
	}
	ps[o.Symbol].Add(ps[o.Symbol], q)
	return nil
}
func clonePositions(ps map[string]*big.Rat) map[string]*big.Rat {
	out := map[string]*big.Rat{}
	for k, v := range ps {
		out[k] = new(big.Rat).Set(v)
	}
	return out
}

// cloneBook preserves unsettled credits while checking reservations on a copy.
func cloneBook(b *Book) *Book {
	c := &Book{kind: b.kind, cash: b.Cash()}
	for _, v := range b.credits {
		c.credits = append(c.credits, credit{v.date, new(big.Rat).Set(v.amount)})
	}
	return c
}

// checkReservations never treats another unfilled order as funding or a
// completed reduction. Fees and adverse limit-to-mark gaps consume capacity
// first. Actual holdings remain in the risk book; only the part of each pending order that exceeds independently
// reserved reductions adds a separate exposure leg. Separate legs cannot net.
func checkReservations(r *bookReplay, prices map[string]string, at time.Time) error {
	b := cloneBook(r.funds)
	ps := clonePositions(r.positions)
	ids := []string{}
	for id := range r.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	capitalReserve := new(big.Rat)
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		q.Sub(q, o.filled)
		if o.released || q.Sign() == 0 {
			continue
		}
		fee, _ := bookRat(o.order.Fee)
		fee.Sub(fee, o.fee)
		if fee.Sign() < 0 {
			return errors.New("fee reserve exceeded")
		}
		capitalReserve.Add(capitalReserve, fee)
		limit, _ := bookRat(o.order.Limit)
		mark, err := bookRat(prices[o.order.Symbol])
		if err != nil || mark.Sign() <= 0 {
			return errors.New("missing positive pending-order mark")
		}
		adverse := new(big.Rat).Sub(limit, mark)
		if o.order.Side == "sell" {
			adverse.Neg(adverse)
		}
		if adverse.Sign() > 0 {
			capitalReserve.Add(capitalReserve, new(big.Rat).Mul(q, adverse))
		}
	}
	b.AddCash(new(big.Rat).Neg(capitalReserve))
	remainingLong, remainingShort := map[string]*big.Rat{}, map[string]*big.Rat{}
	for asset, q := range r.positions {
		if q.Sign() > 0 {
			remainingLong[asset] = new(big.Rat).Set(q)
		} else if q.Sign() < 0 {
			remainingShort[asset] = new(big.Rat).Neg(q)
		}
	}
	reservationMarks := map[string]string{}
	for a, p := range prices {
		reservationMarks[a] = p
	}
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		q.Sub(q, o.filled)
		if o.released || q.Sign() == 0 {
			continue
		}
		price, _ := bookRat(o.order.Limit)
		fee, _ := bookRat(o.order.Fee)
		fee.Sub(fee, o.fee)
		// Each order must also be executable without any other pending fill.
		if err := applyTrade(cloneBook(r.funds), clonePositions(r.positions), o.order, q, price, fee, prices, at); err != nil {
			return err
		}
		reductions := remainingLong
		if o.order.Side == "buy" {
			reductions = remainingShort
		}
		if available := reductions[o.order.Symbol]; available != nil {
			reduce := new(big.Rat).Set(q)
			if reduce.Cmp(available) > 0 {
				reduce.Set(available)
			}
			q.Sub(q, reduce)
			available.Sub(available, reduce)
		}
		if q.Sign() == 0 {
			continue
		}
		leg := o.order
		leg.Symbol = "pending:" + id
		riskPrice := new(big.Rat).Set(price)
		mark, _ := bookRat(prices[o.order.Symbol])
		if riskPrice.Cmp(mark) < 0 {
			riskPrice.Set(mark)
		}
		reservationMarks[leg.Symbol] = riskPrice.FloatString(9)
		if err := applyTrade(b, ps, leg, q, riskPrice, new(big.Rat), reservationMarks, at); err != nil {
			return err
		}
	}
	return nil
}

func replay(s CappedState) (*bookReplay, error) {
	if e := s.Config.validate(); e != nil {
		return nil, e
	}
	if s.Version != int64(len(s.Events)) || len(s.Events) > 4096 {
		return nil, errors.New("invalid book history size")
	}
	cash, _ := bookRat(s.Config.OpeningCash)
	r := &bookReplay{funds: New(s.Config.Type, cash), positions: map[string]*big.Rat{}, orders: map[string]*openOrder{}}
	ids := map[string]bool{}
	last := s.Config.OpenedAt
	financialEvents := []BookCommand{}
	for _, c := range s.Events {
		if c.Incubation != nil && c.Kind != "incubation_plan" || c.ModelFill != nil && c.Kind != "fill" {
			return nil, errors.New("incubation payload differs from event kind")
		}
		if c.ID == "" || len(c.ID) > 128 || ids[c.ID] || c.At.Before(last) || c.At.IsZero() {
			return nil, ErrBookConflict
		}
		ids[c.ID] = true
		last = c.At
		r.funds.Advance(civil(c.At))
		switch c.Kind {
		case "deposit":
			due, e := s.Config.DueDeposits(c.At)
			if e != nil {
				return nil, e
			}
			match := false
			for _, d := range due {
				if d.ID == c.ID && d.Amount == c.Amount && d.EffectiveAt.Equal(c.EffectiveAt) {
					match = true
				}
			}
			if !match {
				return nil, errors.New("unscheduled capital flow")
			}
			financialEvents = append(financialEvents, c)
			if err := rebuildFinancial(r, s.Config, financialEvents, c.At); err != nil {
				return nil, err
			}
			r.deposits++
		case "risk_observe":
			if e := observePortfolio(s.Config, r, c.Marks, c.At); e != nil {
				return nil, e
			}
		case "risk_control":
			if c.RiskControl == nil {
				return nil, errors.New("missing risk control")
			}
			if c.Marks != nil {
				if e := observePortfolio(s.Config, r, c.Marks, c.At); e != nil {
					return nil, e
				}
			}
			var e error
			r.risk, e = risk.ApplyControl(r.risk, *c.RiskControl)
			if e != nil {
				return nil, e
			}
			if c.RiskControl.Action == "owner_clear_halt" {
				r.halted = false
			}
		case "incubation_plan":
			if e := validateIncubationPlan(s, c); e != nil {
				return nil, e
			}
		case "rebalance":
			if c.Rebalance == nil || c.Rebalance.ID != c.ID || !c.Rebalance.At.Equal(c.At) {
				return nil, errors.New("invalid rebalance command")
			}
			if e := checkIncubationRebalance(s, c); e != nil {
				return nil, e
			}
			if e := observePortfolio(s.Config, r, c.Marks, c.At); e != nil {
				return nil, e
			}
			if e := retainPortfolioRoster(r, *c.Rebalance); e != nil {
				return nil, e
			}
			v, e := allocation.Distribute(*c.Rebalance, sizingSnapshot(r, c.Marks))
			if e != nil {
				return nil, e
			}
			r.distribution = &v
			if r.targets == nil {
				r.targets = map[string]allocation.TargetDecision{}
			}
			for _, t := range c.Rebalance.Targets {
				if !t.Decision.KeepPositions {
					r.targets[t.SleeveID] = t.Decision
				}
			}
		case "reserve", "portfolio_reserve":
			if r.halted && c.Kind == "reserve" || c.Order == nil {
				return nil, errors.New("book halted or missing order")
			}
			o := *c.Order
			q, qe := bookRat(o.Quantity)
			p, pe := bookRat(o.Limit)
			_, fe := bookRat(o.Fee)
			if !bookID.MatchString(o.ID) || !bookSymbol.MatchString(o.Symbol) || qe != nil || pe != nil || fe != nil || q.Sign() <= 0 || p.Sign() <= 0 || (o.Side != "buy" && o.Side != "sell") || !o.At.Equal(c.At) || r.orders[o.ID] != nil {
				return nil, errors.New("invalid book order")
			}
			r.orders[o.ID] = &openOrder{order: o, filled: new(big.Rat), fee: new(big.Rat)}
			if c.Kind == "portfolio_reserve" {
				if e := checkSizedOrder(r, o); e != nil {
					return nil, e
				}
				if e := checkIncubationOrder(s, c); e != nil {
					return nil, e
				}
				if e := observePortfolio(s.Config, r, c.Marks, c.At); e != nil {
					return nil, e
				}
				if e := checkPortfolioRisk(r, o, c.Marks); e != nil {
					return nil, e
				}
			}
			if e := checkReservations(r, c.Marks, c.At); e != nil {
				return nil, e
			}
		case "bind":
			o := r.orders[c.OrderID]
			if o == nil || c.VenueOrderID == "" || o.venue != "" && o.venue != c.VenueOrderID {
				return nil, ErrBookConflict
			}
			for id, v := range r.orders {
				if id != c.OrderID && v.venue == c.VenueOrderID {
					return nil, ErrBookConflict
				}
			}
			o.venue = c.VenueOrderID
		case "fill":
			if c.Fill == nil {
				return nil, errors.New("missing fill")
			}
			f := *c.Fill
			if e := validateIncubationFill(s, c); e != nil {
				return nil, e
			}
			o := r.orders[f.OrderID]
			q, qe := bookRat(f.Quantity)
			p, pe := bookRat(f.Price)
			fee, fe := bookRat(f.Fee)
			if o == nil || o.released || f.ID != c.ID || o.venue == "" || o.venue != f.VenueOrderID || f.Symbol != o.order.Symbol || f.Side != o.order.Side || f.At.Before(o.order.At) || f.At.After(c.At) || qe != nil || pe != nil || fe != nil || q.Sign() <= 0 || p.Sign() <= 0 || !hashText.MatchString(f.SourceSHA256) {
				return nil, errors.New("fill does not match owned order")
			}
			full, _ := bookRat(o.order.Quantity)
			limit, _ := bookRat(o.order.Limit)
			reserve, _ := bookRat(o.order.Fee)
			if new(big.Rat).Add(o.filled, q).Cmp(full) > 0 || new(big.Rat).Add(o.fee, fee).Cmp(reserve) > 0 || o.order.Side == "buy" && p.Cmp(limit) > 0 || o.order.Side == "sell" && p.Cmp(limit) < 0 {
				return nil, errors.New("fill exceeds reserved terms")
			}
			financialEvents = append(financialEvents, c)
			if err := rebuildFinancial(r, s.Config, financialEvents, c.At); err != nil {
				return nil, err
			}
			o.filled.Add(o.filled, q)
			o.fee.Add(o.fee, fee)
		case "reject":
			o := r.orders[c.OrderID]
			if o == nil || o.venue != "" || o.filled.Sign() != 0 {
				return nil, ErrBookConflict
			}
			o.released = true
		case "release":
			o := r.orders[c.OrderID]
			if o == nil || o.venue == "" || o.venue != c.VenueOrderID {
				return nil, ErrBookConflict
			}
			o.released = true
		case "halt":
			r.halted = true
			r.risk.Halted = true
		default:
			return nil, errors.New("unknown capped book event")
		}
	}
	return r, nil
}

var bookSymbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)

var hashText = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ApplyCappedCommand returns a detached state. Exact event retries are idempotent;
// changed retries conflict. A failed command never changes the previous state.
func ApplyCappedCommand(s CappedState, c BookCommand) (CappedState, error) {
	if c.Incubation != nil && c.Kind != "incubation_plan" || c.ModelFill != nil && c.Kind != "fill" {
		return s, errors.New("incubation payload differs from event kind")
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return s, e
	}
	var out CappedState
	if e = json.Unmarshal(raw, &out); e != nil {
		return s, e
	}
	for _, old := range out.Events {
		if old.ID == c.ID {
			a, e := json.Marshal(old)
			if e != nil {
				return s, e
			}
			b, e := json.Marshal(c)
			if e != nil {
				return s, e
			}
			if string(a) != string(b) {
				return s, ErrBookConflict
			}
			return out, nil
		}
	}
	raw, e = json.Marshal(c)
	if e != nil {
		return s, e
	}
	var copied BookCommand
	if e = json.Unmarshal(raw, &copied); e != nil {
		return s, e
	}
	out.Events = append(out.Events, copied)
	out.Version++
	if _, e = replay(out); e != nil {
		return s, e
	}
	return out, nil
}

// SnapshotCapped marks only this book's owned positions and advances settlement.
func SnapshotCapped(s CappedState, prices map[string]string, at time.Time) (CappedSnapshot, error) {
	r, e := replay(s)
	if e != nil {
		return CappedSnapshot{}, e
	}
	if at.Before(s.Config.OpenedAt) || len(s.Events) > 0 && at.Before(s.Events[len(s.Events)-1].At) {
		return CappedSnapshot{}, errors.New("snapshot precedes book history")
	}
	r.funds.Advance(civil(at))
	ps, e := marks(r.positions, prices)
	if e != nil {
		return CappedSnapshot{}, e
	}
	out := CappedSnapshot{Cash: r.funds.Cash().FloatString(9), SettledCash: r.funds.Settled().FloatString(9), Equity: r.funds.Status(ps).Equity.FloatString(9), Positions: map[string]string{}, Pending: []BookOrder{}, Deposits: r.deposits, Halted: r.halted}
	out.Risk, out.Distribution = r.risk, r.distribution
	out.Halted = out.Halted || r.risk.Halted
	for a, q := range r.positions {
		out.Positions[a] = q.FloatString(9)
	}
	ids := []string{}
	for id := range r.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		if !o.released && q.Cmp(o.filled) > 0 {
			out.Pending = append(out.Pending, o.order)
		}
	}
	return out, nil
}

// DueDeposits uses the same Friday 06:00 Chicago schedule as daily replay.
// The application event retains its effective time even when applied late.
func (c CappedConfig) DueDeposits(through time.Time) ([]BookCommand, error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	zone, e := time.LoadLocation("America/Chicago")
	if e != nil {
		return nil, e
	}
	local := c.OpenedAt.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, zone)
	for day.Weekday() != time.Friday || day.Before(c.OpenedAt) {
		day = day.AddDate(0, 0, 1)
	}
	out := []BookCommand{}
	for !day.After(through) {
		out = append(out, BookCommand{ID: "deposit-" + day.Format(time.DateOnly), Kind: "deposit", Amount: c.DepositAmount, EffectiveAt: day.UTC(), At: through})
		day = day.AddDate(0, 0, 7*c.EveryWeeks)
	}
	return out, nil
}

// rebuildFinancial orders economic events independently of receipt/application
// order. A delayed venue fill cannot spend a later deposit; its reservation was
// already checked before POST. The append-only receipt stream remains intact.
func rebuildFinancial(r *bookReplay, cfg CappedConfig, events []BookCommand, through time.Time) error {
	ordered := slices.Clone(events)
	clock := func(c BookCommand) time.Time {
		if c.Kind == "fill" {
			return c.Fill.At
		}
		return c.EffectiveAt
	}
	slices.SortStableFunc(ordered, func(a, b BookCommand) int {
		if clock(a).Before(clock(b)) {
			return -1
		}
		if clock(a).After(clock(b)) {
			return 1
		}
		if a.Kind == "deposit" && b.Kind != "deposit" {
			return -1
		}
		if b.Kind == "deposit" && a.Kind != "deposit" {
			return 1
		}
		return 0
	})
	cash, _ := bookRat(cfg.OpeningCash)
	funds := New(cfg.Type, cash)
	positions := map[string]*big.Rat{}
	for _, event := range ordered {
		if event.Kind == "deposit" {
			amount, _ := bookRat(event.Amount)
			funds.AddCash(amount)
			continue
		}
		f := event.Fill
		q, _ := bookRat(f.Quantity)
		p, _ := bookRat(f.Price)
		fee, _ := bookRat(f.Fee)
		o := BookOrder{Symbol: f.Symbol, Side: f.Side}
		if err := applyTrade(funds, positions, o, q, p, fee, event.Marks, f.At); err != nil {
			return err
		}
	}
	funds.Advance(civil(through))
	r.funds = funds
	r.positions = positions
	return nil
}

// DefaultCappedConfig starts the intended $2,000 book with $500 biweekly flows.
func DefaultCappedConfig(id, broker string, kind Type, opened time.Time) CappedConfig {
	return CappedConfig{ID: id, BrokerAccountID: broker, Type: kind, OpenedAt: opened, OpeningCash: "2000", DepositAmount: "500", EveryWeeks: 2}
}
