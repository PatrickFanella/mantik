// Package paperfill defines a deterministic, versioned paper execution method.
// Inputs must be selected by execution owners. It performs no I/O, admission,
// account mutation or attestation, and is shared by replay and ongoing paper.
package paperfill

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Method = "usd-equity-displayed-quote-v1"
const MaxEntries = 128
const MaxJournalBytes = 1 << 20
const MaxPriceCents int64 = 100000000
const MaxQuantity int64 = 1000000
const MaxMoneyCents int64 = 1000000000000

var ErrInvalid = errors.New("invalid paper fill method input")
var ErrConflict = errors.New("paper fill command or quote identity conflict")
var ErrCapacity = errors.New("paper fill journal capacity exhausted")
var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var pinPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Parameters are declared assumptions, not estimates of actual venue behavior.
// Quote quantities must already be normalized to whole shares by the owner.
type Model struct {
	Method                 string `json:"method"`
	SlippageBPS            int64  `json:"slippage_bps"`
	FeeBPS                 int64  `json:"fee_bps"`
	CommissionPerFillCents int64  `json:"commission_per_fill_cents"`
	LiquidityBPS           int64  `json:"liquidity_bps"`
	LatencyMillis          int64  `json:"latency_millis"`
	MaxQuoteAgeSeconds     int64  `json:"max_quote_age_seconds"`
}

func (m Model) Validate() error {
	if m.Method != Method || m.SlippageBPS < 0 || m.SlippageBPS > 1000 || m.FeeBPS < 0 || m.FeeBPS > 1000 || m.CommissionPerFillCents < 0 || m.CommissionPerFillCents > 1000000 || m.LiquidityBPS < 1 || m.LiquidityBPS > 10000 || m.LatencyMillis < 0 || m.LatencyMillis > 60000 || m.MaxQuoteAgeSeconds < 1 || m.MaxQuoteAgeSeconds > 3600 {
		return ErrInvalid
	}
	return nil
}

type Order struct {
	ID            string    `json:"id"`
	Symbol        string    `json:"symbol"`
	Currency      string    `json:"currency"`
	Side          string    `json:"side"`
	Kind          string    `json:"kind"`
	Quantity      int64     `json:"quantity"`
	LimitCents    int64     `json:"limit_cents"`
	StopCents     int64     `json:"stop_cents"`
	MaxDebitCents int64     `json:"max_debit_cents"`
	PlacedAt      time.Time `json:"placed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Mode          string    `json:"mode"`
}
type Quote struct {
	ID            string    `json:"id"`
	SourceSHA256  string    `json:"source_sha256"`
	Symbol        string    `json:"symbol"`
	Currency      string    `json:"currency"`
	Quality       string    `json:"quality"`
	EvidenceClass string    `json:"evidence_class"`
	OccurredAt    time.Time `json:"occurred_at"`
	ReceivedAt    time.Time `json:"received_at"`
	BidCents      int64     `json:"bid_cents"`
	AskCents      int64     `json:"ask_cents"`
	BidQuantity   int64     `json:"bid_quantity"`
	AskQuantity   int64     `json:"ask_quantity"`
}
type Command struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Quote  *Quote    `json:"quote,omitempty"`
	Cancel bool      `json:"cancel"`
}
type Fill struct {
	OrderID            string    `json:"order_id"`
	CommandID          string    `json:"command_id"`
	Method             string    `json:"method"`
	ModelSHA256        string    `json:"model_sha256"`
	QuoteID            string    `json:"quote_id"`
	QuoteSHA256        string    `json:"quote_sha256"`
	MarketUpdateSHA256 string    `json:"market_update_sha256"`
	EvidenceClass      string    `json:"evidence_class"`
	Mode               string    `json:"mode"`
	Side               string    `json:"side"`
	Quantity           int64     `json:"quantity"`
	PriceCents         int64     `json:"price_cents"`
	GrossCents         int64     `json:"gross_cents"`
	FeeCents           int64     `json:"fee_cents"`
	DebitCents         int64     `json:"debit_cents"`
	NetProceedsCents   int64     `json:"net_proceeds_cents"`
	At                 time.Time `json:"at"`
	SHA256             string    `json:"sha256"`
}
type Outcome struct {
	Code      string `json:"code"`
	Status    string `json:"status"`
	Triggered bool   `json:"triggered"`
	Fill      *Fill  `json:"fill,omitempty"`
}
type Entry struct {
	Command        Command `json:"command"`
	Outcome        Outcome `json:"outcome"`
	PreviousSHA256 string  `json:"previous_sha256"`
	SHA256         string  `json:"sha256"`
}
type Journal struct {
	Schema  string  `json:"schema"`
	Model   Model   `json:"model"`
	Order   Order   `json:"order"`
	Entries []Entry `json:"entries"`
	SHA256  string  `json:"sha256"`
}
type Projection struct {
	Status                string    `json:"status"`
	Triggered             bool      `json:"triggered"`
	FilledQuantity        int64     `json:"filled_quantity"`
	RemainingQuantity     int64     `json:"remaining_quantity"`
	GrossCents            int64     `json:"gross_cents"`
	FeeCents              int64     `json:"fee_cents"`
	DebitCents            int64     `json:"debit_cents"`
	NetProceedsCents      int64     `json:"net_proceeds_cents"`
	LastClock             time.Time `json:"last_clock"`
	LastQuoteReceivedAt   time.Time `json:"last_quote_received_at"`
	ConsumedMarketUpdates []string  `json:"consumed_market_updates"`
	Authority             string    `json:"authority"`
	ExecutionAttested     bool      `json:"execution_attested"`
	QualificationCredit   bool      `json:"qualification_credit"`
}
type Receipt struct {
	Sequence    int     `json:"sequence"`
	EntrySHA256 string  `json:"entry_sha256"`
	Duplicate   bool    `json:"duplicate"`
	Outcome     Outcome `json:"outcome"`
}

func validTime(t time.Time) bool {
	if t.IsZero() || t.UTC().Year() < 1 || t.UTC().Year() > 9998 {
		return false
	}
	_, err := t.MarshalJSON()
	return err == nil
}
func price(n int64) bool { return n >= 1 && n <= MaxPriceCents }
func validOrder(o Order) bool {
	if !paperaccount.ID.MatchString(o.ID) || !symbolPattern.MatchString(o.Symbol) || o.Currency != "USD" || !slices.Contains([]string{"buy", "sell"}, o.Side) || !slices.Contains([]string{"market", "limit", "stop_market", "stop_limit"}, o.Kind) || o.Quantity < 1 || o.Quantity > MaxQuantity || !validTime(o.PlacedAt) || !validTime(o.ExpiresAt) || !o.ExpiresAt.After(o.PlacedAt) || o.ExpiresAt.Sub(o.PlacedAt) > 24*time.Hour || !slices.Contains([]string{"replay", "ongoing_paper", "fixture"}, o.Mode) {
		return false
	}
	limit := o.Kind == "limit" || o.Kind == "stop_limit"
	stop := o.Kind == "stop_market" || o.Kind == "stop_limit"
	return (limit && price(o.LimitCents) || !limit && o.LimitCents == 0) && (stop && price(o.StopCents) || !stop && o.StopCents == 0) && (o.Side == "buy" && o.MaxDebitCents > 0 && o.MaxDebitCents <= MaxMoneyCents || o.Side == "sell" && o.MaxDebitCents == 0)
}
func validQuote(q Quote) bool {
	if !paperaccount.ID.MatchString(q.ID) || !pinPattern.MatchString(q.SourceSHA256) || !symbolPattern.MatchString(q.Symbol) || q.Currency != "USD" || !validTime(q.ReceivedAt) || !slices.Contains([]string{"usable", "missing", "quarantined"}, q.Quality) || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, q.EvidenceClass) {
		return false
	}
	if q.Quality != "usable" {
		return q.BidCents == 0 && q.AskCents == 0 && q.BidQuantity == 0 && q.AskQuantity == 0 && (q.OccurredAt.IsZero() || validTime(q.OccurredAt) && !q.OccurredAt.After(q.ReceivedAt))
	}
	return validTime(q.OccurredAt) && !q.OccurredAt.After(q.ReceivedAt) && price(q.BidCents) && price(q.AskCents) && q.BidCents <= q.AskCents && q.BidQuantity >= 0 && q.BidQuantity <= MaxQuantity && q.AskQuantity >= 0 && q.AskQuantity <= MaxQuantity
}
func detach[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func normalizeCommand(c Command) Command {
	c.At = c.At.UTC()
	if c.Quote != nil {
		q := *c.Quote
		q.OccurredAt = q.OccurredAt.UTC()
		q.ReceivedAt = q.ReceivedAt.UTC()
		c.Quote = &q
	}
	return detach(c)
}
func New(m Model, o Order) (Journal, error) {
	if m.Validate() != nil || !validOrder(o) {
		return Journal{}, ErrInvalid
	}
	o.PlacedAt = o.PlacedAt.UTC()
	o.ExpiresAt = o.ExpiresAt.UTC()
	j := Journal{Schema: "mantik-paper-fill-journal-v1", Model: m, Order: detach(o), Entries: []Entry{}}
	j.SHA256 = workerplan.Hash(j)
	return j, nil
}
func ceilDiv(n, d int64) int64 { return n/d + boolInt(n%d != 0) }
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func cost(m Model, price, quantity int64) (gross, fee int64, ok bool) {
	if quantity < 1 || quantity > MaxQuantity || !priceValidMoney(price, quantity) {
		return 0, 0, false
	}
	gross = price * quantity
	fee = ceilDiv(gross*m.FeeBPS, 10000) + m.CommissionPerFillCents
	return gross, fee, fee <= MaxMoneyCents
}
func priceValidMoney(p, q int64) bool { return price(p) && q <= MaxMoneyCents/p }
func marketUpdate(q Quote) string {
	return workerplan.Hash(struct {
		Symbol                             string
		OccurredAt                         time.Time
		Bid, Ask, BidQuantity, AskQuantity int64
	}{q.Symbol, q.OccurredAt, q.BidCents, q.AskCents, q.BidQuantity, q.AskQuantity})
}
func step(m Model, o Order, p Projection, c Command, seen map[string]string) (Projection, Outcome, error) {
	return stepBounded(m, o, p, c, seen, nil)
}

// Shared-book replay supplies an additional remaining side allowance without
// changing the original quote or its market-update identity. Legacy order
// replay passes nil and retains its original outcomes and hashes.
func stepBounded(m Model, o Order, p Projection, c Command, seen map[string]string, remainingLiquidity *int64) (Projection, Outcome, error) {
	if !paperaccount.ID.MatchString(c.ID) || !validTime(c.At) || c.At.Before(p.LastClock) || c.Cancel && c.Quote != nil {
		return Projection{}, Outcome{}, ErrInvalid
	}
	if p.Status != "working" {
		return Projection{}, Outcome{}, ErrConflict
	}
	if c.Quote != nil {
		q := *c.Quote
		if !validQuote(q) || q.Symbol != o.Symbol {
			return Projection{}, Outcome{}, ErrInvalid
		}
		hash := workerplan.Hash(q)
		if previous, ok := seen[q.ID]; ok && previous != hash {
			return Projection{}, Outcome{}, ErrConflict
		}
		seen[q.ID] = hash
	}
	p.LastClock = c.At
	out := Outcome{Status: p.Status, Triggered: p.Triggered}
	finish := func(code string) (Projection, Outcome, error) {
		out.Code = code
		out.Status = p.Status
		out.Triggered = p.Triggered
		return p, out, nil
	}
	if !c.At.Before(o.ExpiresAt) {
		p.Status = "expired"
		return finish("order_expired")
	}
	if c.Cancel {
		p.Status = "cancelled"
		return finish("order_cancelled")
	}
	eligible := o.PlacedAt.Add(time.Duration(m.LatencyMillis) * time.Millisecond)
	if c.At.Before(eligible) {
		return finish("latency_pending")
	}
	if c.Quote == nil {
		return finish("missing_quote")
	}
	q := *c.Quote
	if q.ReceivedAt.After(c.At) {
		return finish("future_quote")
	}
	if q.ReceivedAt.Before(p.LastQuoteReceivedAt) {
		return finish("superseded_quote")
	}
	p.LastQuoteReceivedAt = q.ReceivedAt
	if q.Quality != "usable" {
		return finish("quote_" + q.Quality)
	}
	if q.OccurredAt.Before(eligible) {
		return finish("quote_before_eligible")
	}
	if c.At.Sub(q.OccurredAt) > time.Duration(m.MaxQuoteAgeSeconds)*time.Second {
		return finish("stale_quote")
	}
	update := marketUpdate(q)
	if slices.Contains(p.ConsumedMarketUpdates, update) {
		return finish("market_update_already_consumed")
	}
	base, available := q.AskCents, q.AskQuantity
	if o.Side == "sell" {
		base, available = q.BidCents, q.BidQuantity
	}
	if o.Kind == "stop_market" || o.Kind == "stop_limit" {
		if !p.Triggered {
			p.Triggered = o.Side == "buy" && base >= o.StopCents || o.Side == "sell" && base <= o.StopCents
		}
		if !p.Triggered {
			return finish("stop_not_triggered")
		}
	}
	executionPrice := ceilDiv(base*(10000+m.SlippageBPS), 10000)
	if o.Side == "sell" {
		executionPrice = base * (10000 - m.SlippageBPS) / 10000
	}
	if !price(executionPrice) {
		return finish("model_price_out_of_range")
	}
	if o.Kind == "limit" || o.Kind == "stop_limit" {
		if o.Side == "buy" && executionPrice > o.LimitCents || o.Side == "sell" && executionPrice < o.LimitCents {
			return finish("limit_not_marketable")
		}
	}
	quantity := min(p.RemainingQuantity, available*m.LiquidityBPS/10000)
	if remainingLiquidity != nil {
		if *remainingLiquidity < 0 {
			return Projection{}, Outcome{}, ErrInvalid
		}
		quantity = min(quantity, *remainingLiquidity)
	}
	if quantity == 0 {
		return finish("displayed_liquidity_unavailable")
	}
	// Cost arithmetic is bounded before multiplication. Binary search chooses the
	// largest whole-share partial that fits the remaining owner's debit budget.
	quantity = min(quantity, MaxMoneyCents/executionPrice)
	if o.Side == "buy" {
		remaining := o.MaxDebitCents - p.DebitCents
		lo, hi := int64(0), quantity
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			gross, fee, ok := cost(m, executionPrice, mid)
			if ok && gross <= remaining && fee <= remaining-gross {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		quantity = lo
		if quantity == 0 {
			return finish("debit_budget_exhausted")
		}
	}
	if quantity == 0 {
		return finish("model_notional_out_of_range")
	}
	gross, fee, ok := cost(m, executionPrice, quantity)
	if !ok || p.GrossCents > MaxMoneyCents-gross || p.FeeCents > MaxMoneyCents-fee {
		return Projection{}, Outcome{}, ErrInvalid
	}
	fill := Fill{OrderID: o.ID, CommandID: c.ID, Method: Method, ModelSHA256: workerplan.Hash(m), QuoteID: q.ID, QuoteSHA256: q.SourceSHA256, MarketUpdateSHA256: update, EvidenceClass: q.EvidenceClass, Mode: o.Mode, Side: o.Side, Quantity: quantity, PriceCents: executionPrice, GrossCents: gross, FeeCents: fee, At: c.At}
	if o.Side == "buy" {
		fill.DebitCents = gross + fee
		p.DebitCents += fill.DebitCents
	} else {
		fill.NetProceedsCents = gross - fee
		p.NetProceedsCents += fill.NetProceedsCents
	}
	fill.SHA256 = workerplan.Hash(fill)
	p.GrossCents += gross
	p.FeeCents += fee
	p.FilledQuantity += quantity
	p.RemainingQuantity -= quantity
	p.ConsumedMarketUpdates = append(p.ConsumedMarketUpdates, update)
	slices.Sort(p.ConsumedMarketUpdates)
	if p.RemainingQuantity == 0 {
		p.Status = "filled"
	}
	out.Fill = &fill
	return finish("paper_fill")
}
func Replay(ctx context.Context, j Journal) (Projection, error) {
	if err := ctx.Err(); err != nil {
		return Projection{}, err
	}
	if j.Schema != "mantik-paper-fill-journal-v1" || j.Model.Validate() != nil || !validOrder(j.Order) || j.Entries == nil || len(j.Entries) > MaxEntries {
		return Projection{}, ErrInvalid
	}
	raw, err := json.Marshal(j)
	if err != nil || len(raw) > MaxJournalBytes {
		return Projection{}, ErrInvalid
	}
	unsigned := j
	unsigned.SHA256 = ""
	if !pinPattern.MatchString(j.SHA256) || workerplan.Hash(unsigned) != j.SHA256 {
		return Projection{}, ErrInvalid
	}
	root, err := New(j.Model, j.Order)
	if err != nil {
		return Projection{}, err
	}
	previous := root.SHA256
	seen := map[string]string{}
	commands := map[string]bool{}
	p := Projection{Status: "working", RemainingQuantity: j.Order.Quantity, LastClock: j.Order.PlacedAt, ConsumedMarketUpdates: []string{}, Authority: "owner_supplied_paper_fill_method_only"}
	for _, entry := range j.Entries {
		if err := ctx.Err(); err != nil {
			return Projection{}, err
		}
		if commands[entry.Command.ID] {
			return Projection{}, ErrConflict
		}
		commands[entry.Command.ID] = true
		next, out, err := step(j.Model, j.Order, p, entry.Command, seen)
		if err != nil {
			return Projection{}, err
		}
		expected := Entry{Command: entry.Command, Outcome: out, PreviousSHA256: previous}
		expected.SHA256 = workerplan.Hash(expected)
		if !reflect.DeepEqual(expected, entry) {
			return Projection{}, ErrConflict
		}
		previous = entry.SHA256
		p = next
	}
	return detach(p), nil
}
func Apply(ctx context.Context, j Journal, c Command) (Journal, Projection, Receipt, error) {
	p, err := Replay(ctx, j)
	if err != nil {
		return Journal{}, Projection{}, Receipt{}, err
	}
	c = normalizeCommand(c)
	for i, entry := range j.Entries {
		if entry.Command.ID == c.ID {
			if !reflect.DeepEqual(entry.Command, c) {
				return Journal{}, Projection{}, Receipt{}, ErrConflict
			}
			return detach(j), p, Receipt{Sequence: i + 1, EntrySHA256: entry.SHA256, Duplicate: true, Outcome: detach(entry.Outcome)}, nil
		}
	}
	if len(j.Entries) >= MaxEntries {
		return Journal{}, Projection{}, Receipt{}, ErrCapacity
	}
	seen := map[string]string{}
	for _, entry := range j.Entries {
		if entry.Command.Quote != nil {
			seen[entry.Command.Quote.ID] = workerplan.Hash(*entry.Command.Quote)
		}
	}
	if err := ctx.Err(); err != nil {
		return Journal{}, Projection{}, Receipt{}, err
	}
	next, out, err := step(j.Model, j.Order, p, c, seen)
	if err != nil {
		return Journal{}, Projection{}, Receipt{}, err
	}
	previous := j.SHA256
	if len(j.Entries) > 0 {
		previous = j.Entries[len(j.Entries)-1].SHA256
	}
	// Empty-journal hash is the chain root, not the changing envelope hash.
	if len(j.Entries) == 0 {
		root, _ := New(j.Model, j.Order)
		previous = root.SHA256
	}
	entry := Entry{Command: c, Outcome: out, PreviousSHA256: previous}
	entry.SHA256 = workerplan.Hash(entry)
	updated := detach(j)
	updated.Entries = append(updated.Entries, entry)
	updated.SHA256 = ""
	updated.SHA256 = workerplan.Hash(updated)
	raw, err := json.Marshal(updated)
	if err != nil || len(raw) > MaxJournalBytes {
		return Journal{}, Projection{}, Receipt{}, ErrCapacity
	}
	return detach(updated), detach(next), Receipt{Sequence: len(updated.Entries), EntrySHA256: entry.SHA256, Outcome: detach(out)}, nil
}
