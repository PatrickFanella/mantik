package paperfill

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const BookMethod = "usd-equity-shared-displayed-quote-v1"
const OccurrenceBookMethod = "usd-equity-shared-occurrence-quote-v2"
const MaxBookOrders = 32
const MaxBookBytes = 8 << 20

// Orders are an immutable, ID-sorted priority list. This internal simulation
// aggregate owns shared liquidity arithmetic, not capital or operational grants.
type Book struct {
	Schema  string      `json:"schema"`
	Method  string      `json:"method"`
	Model   Model       `json:"model"`
	Orders  []Order     `json:"orders"`
	Entries []BookEntry `json:"entries"`
	SHA256  string      `json:"sha256"`
}
type BookCommand struct {
	ID            string    `json:"id"`
	At            time.Time `json:"at"`
	Quote         *Quote    `json:"quote,omitempty"`
	CancelOrderID string    `json:"cancel_order_id,omitempty"`
}
type BookOrderOutcome struct {
	OrderID string  `json:"order_id"`
	Outcome Outcome `json:"outcome"`
}
type BookEntry struct {
	Command        BookCommand        `json:"command"`
	Outcomes       []BookOrderOutcome `json:"outcomes"`
	PreviousSHA256 string             `json:"previous_sha256"`
	SHA256         string             `json:"sha256"`
}
type BookOrderProjection struct {
	OrderID    string     `json:"order_id"`
	Projection Projection `json:"projection"`
}
type BookCapacity struct {
	Bid int64 `json:"bid"`
	Ask int64 `json:"ask"`
}
type BookLiquidity struct {
	Capacity           *BookCapacity `json:"capacity,omitempty"`
	MarketUpdateSHA256 string        `json:"market_update_sha256"`
	BidUsed            int64         `json:"bid_used"`
	AskUsed            int64         `json:"ask_used"`
}
type BookProjection struct {
	Orders              []BookOrderProjection `json:"orders"`
	Liquidity           []BookLiquidity       `json:"liquidity"`
	LastClock           time.Time             `json:"last_clock"`
	Authority           string                `json:"authority"`
	ExecutionAttested   bool                  `json:"execution_attested"`
	QualificationCredit bool                  `json:"qualification_credit"`
}
type BookReceipt struct {
	Sequence    int                `json:"sequence"`
	EntrySHA256 string             `json:"entry_sha256"`
	Duplicate   bool               `json:"duplicate"`
	Outcomes    []BookOrderOutcome `json:"outcomes"`
}

func NewBook(m Model, orders []Order) (Book, error) {
	return newBook(BookMethod, m, orders)
}

// NewOccurrenceBook shares a conservative capacity across price/size corrections
// at one symbol/time. The owner must provide one provider/feed stream.
func NewOccurrenceBook(m Model, orders []Order) (Book, error) {
	return newBook(OccurrenceBookMethod, m, orders)
}

// RootBook retains the exact book method in financial effect bindings.
func RootBook(b Book) (Book, error) { return newBook(b.Method, b.Model, b.Orders) }

func newBook(method string, m Model, orders []Order) (Book, error) {
	if method != BookMethod && method != OccurrenceBookMethod {
		return Book{}, ErrInvalid
	}
	if m.Validate() != nil || len(orders) == 0 || len(orders) > MaxBookOrders {
		return Book{}, ErrInvalid
	}
	orders = detach(orders)
	for i := range orders {
		if !validOrder(orders[i]) || i > 0 && (orders[i-1].ID >= orders[i].ID || orders[i].Mode != orders[0].Mode) {
			return Book{}, ErrInvalid
		}
		orders[i].PlacedAt = orders[i].PlacedAt.UTC()
		orders[i].ExpiresAt = orders[i].ExpiresAt.UTC()
	}
	b := Book{Schema: "mantik-shared-paper-fill-book-v1", Method: method, Model: m, Orders: orders, Entries: []BookEntry{}}
	b.SHA256 = workerplan.Hash(b)
	return b, nil
}
func bookInitial(b Book) BookProjection {
	p := BookProjection{Orders: []BookOrderProjection{}, Liquidity: []BookLiquidity{}, LastClock: b.Orders[0].PlacedAt, Authority: "owner_supplied_shared_paper_fill_method_only"}
	for _, o := range b.Orders {
		p.LastClock = minTime(p.LastClock, o.PlacedAt)
		p.Orders = append(p.Orders, BookOrderProjection{OrderID: o.ID, Projection: Projection{Status: "working", RemainingQuantity: o.Quantity, LastClock: o.PlacedAt, ConsumedMarketUpdates: []string{}, Authority: "owner_supplied_paper_fill_method_only"}})
	}
	return p
}
func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
func normalizeBookCommand(c BookCommand) BookCommand {
	command := normalizeCommand(Command{ID: c.ID, At: c.At, Quote: c.Quote})
	c.At, c.Quote = command.At, command.Quote
	return c
}
func bookStep(b Book, p BookProjection, c BookCommand, seen map[string]string) (BookProjection, []BookOrderOutcome, error) {
	if !paperaccount.ID.MatchString(c.ID) || !validTime(c.At) || c.At.Before(p.LastClock) || c.CancelOrderID != "" && c.Quote != nil {
		return BookProjection{}, nil, ErrInvalid
	}
	if c.Quote != nil {
		if !validQuote(*c.Quote) {
			return BookProjection{}, nil, ErrInvalid
		}
		hash := workerplan.Hash(*c.Quote)
		if old, ok := seen[c.Quote.ID]; ok && old != hash {
			return BookProjection{}, nil, ErrConflict
		}
		seen[c.Quote.ID] = hash
		found := false
		for _, o := range b.Orders {
			if o.Symbol == c.Quote.Symbol {
				found = true
			}
		}
		if !found {
			return BookProjection{}, nil, ErrConflict
		}
	}
	if c.CancelOrderID != "" {
		found := false
		for i, o := range b.Orders {
			if o.ID == c.CancelOrderID {
				found = true
				if c.At.Before(o.PlacedAt) || p.Orders[i].Projection.Status != "working" {
					return BookProjection{}, nil, ErrConflict
				}
			}
		}
		if !found {
			return BookProjection{}, nil, ErrConflict
		}
	}
	// Record usable, already available occurrence capacity even when no order
	// fills. A later favorable correction cannot enlarge an earlier observation.
	if b.Method == OccurrenceBookMethod && c.Quote != nil && c.Quote.Quality == "usable" && !c.Quote.ReceivedAt.After(c.At) {
		q := *c.Quote
		key := bookLiquidityKey(b, q)
		bid, ask := q.BidQuantity*b.Model.LiquidityBPS/10000, q.AskQuantity*b.Model.LiquidityBPS/10000
		index := -1
		for i, l := range p.Liquidity {
			if l.MarketUpdateSHA256 == key {
				index = i
				break
			}
		}
		if index < 0 {
			p.Liquidity = append(p.Liquidity, BookLiquidity{MarketUpdateSHA256: key, Capacity: &BookCapacity{Bid: bid, Ask: ask}})
		} else {
			cap := p.Liquidity[index].Capacity
			cap.Bid, cap.Ask = min(cap.Bid, bid), min(cap.Ask, ask)
		}
	}
	p.LastClock = c.At
	outcomes := []BookOrderOutcome{}
	for i, o := range b.Orders {
		if p.Orders[i].Projection.Status != "working" || c.At.Before(o.PlacedAt) {
			continue
		}
		if c.CancelOrderID != "" && c.CancelOrderID != o.ID {
			continue
		}
		if c.Quote != nil && c.Quote.Symbol != o.Symbol {
			continue
		}
		command := Command{ID: c.ID, At: c.At, Quote: c.Quote, Cancel: c.CancelOrderID == o.ID}
		var allowance *int64
		var remaining int64
		index := -1
		if c.Quote != nil && c.Quote.Quality == "usable" {
			update := bookLiquidityKey(b, *c.Quote)
			for j, l := range p.Liquidity {
				if l.MarketUpdateSHA256 == update {
					index = j
					break
				}
			}
			used := int64(0)
			if index >= 0 {
				used = p.Liquidity[index].AskUsed
				if o.Side == "sell" {
					used = p.Liquidity[index].BidUsed
				}
			}
			quantity := c.Quote.AskQuantity
			if o.Side == "sell" {
				quantity = c.Quote.BidQuantity
			}
			capacity := quantity * b.Model.LiquidityBPS / 10000
			if index >= 0 && p.Liquidity[index].Capacity != nil {
				capacity = p.Liquidity[index].Capacity.Ask
				if o.Side == "sell" {
					capacity = p.Liquidity[index].Capacity.Bid
				}
			}
			remaining = max(int64(0), capacity-used)
			allowance = &remaining
		}
		next, out, err := stepBounded(b.Model, o, p.Orders[i].Projection, command, seen, allowance)
		if err != nil {
			return BookProjection{}, nil, err
		}
		p.Orders[i].Projection = next
		outcomes = append(outcomes, BookOrderOutcome{OrderID: o.ID, Outcome: out})
		if out.Fill != nil {
			if index < 0 {
				p.Liquidity = append(p.Liquidity, BookLiquidity{MarketUpdateSHA256: bookLiquidityKey(b, *c.Quote)})
				index = len(p.Liquidity) - 1
			}
			if o.Side == "sell" {
				p.Liquidity[index].BidUsed += out.Fill.Quantity
			} else {
				p.Liquidity[index].AskUsed += out.Fill.Quantity
			}
		}
	}
	return p, outcomes, nil
}

func bookLiquidityKey(b Book, q Quote) string {
	if b.Method == OccurrenceBookMethod {
		return workerplan.Hash(struct {
			Symbol     string
			OccurredAt time.Time
		}{q.Symbol, q.OccurredAt.UTC()})
	}
	return marketUpdate(q)
}

// ReplayBook rederives outcomes from unchanged source quotes. Repeated capture
// IDs, receipt times and source pins cannot replenish an identical market update.
func ReplayBook(ctx context.Context, b Book) (BookProjection, error) {
	if err := ctx.Err(); err != nil {
		return BookProjection{}, err
	}
	root, err := RootBook(b)
	if err != nil || b.Schema != root.Schema || b.Method != root.Method || b.Entries == nil || len(b.Entries) > MaxEntries {
		return BookProjection{}, ErrInvalid
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > MaxBookBytes {
		return BookProjection{}, ErrInvalid
	}
	unsigned := b
	unsigned.SHA256 = ""
	if !pinPattern.MatchString(b.SHA256) || workerplan.Hash(unsigned) != b.SHA256 {
		return BookProjection{}, ErrInvalid
	}
	p := bookInitial(root)
	previous := root.SHA256
	seen := map[string]string{}
	commands := map[string]bool{}
	for _, entry := range b.Entries {
		if err := ctx.Err(); err != nil {
			return BookProjection{}, err
		}
		if commands[entry.Command.ID] {
			return BookProjection{}, ErrConflict
		}
		commands[entry.Command.ID] = true
		next, out, err := bookStep(root, p, entry.Command, seen)
		if err != nil {
			return BookProjection{}, err
		}
		expected := BookEntry{Command: entry.Command, Outcomes: out, PreviousSHA256: previous}
		expected.SHA256 = workerplan.Hash(expected)
		if !reflect.DeepEqual(expected, entry) {
			return BookProjection{}, ErrConflict
		}
		previous = entry.SHA256
		p = next
	}
	return detach(p), nil
}
func ApplyBook(ctx context.Context, b Book, c BookCommand) (Book, BookProjection, BookReceipt, error) {
	a, err := NewBookAppender(ctx, b)
	if err != nil {
		return Book{}, BookProjection{}, BookReceipt{}, err
	}
	p, receipt, err := a.Apply(ctx, c)
	if err != nil {
		return Book{}, BookProjection{}, BookReceipt{}, err
	}
	return a.Book(), p, receipt, nil
}

// BookAppender applies a sequence of commands to a book it replays once.
// After each Apply, Book and the returned projection equal what ApplyBook
// returns for the previous book, without replaying every earlier entry
// again. An appender is unusable after Apply returns an error.
type BookAppender struct {
	book       Book
	projection BookProjection
	seen       map[string]string
	commands   map[string]int
	size       int
	signed     bool
	err        error
}

func NewBookAppender(ctx context.Context, b Book) (*BookAppender, error) {
	p, err := ReplayBook(ctx, b)
	if err != nil {
		return nil, err
	}
	a := &BookAppender{book: detach(b), projection: p, seen: map[string]string{}, commands: map[string]int{}, signed: true}
	for i, e := range a.book.Entries {
		a.commands[e.Command.ID] = i
		if e.Command.Quote != nil {
			a.seen[e.Command.Quote.ID] = workerplan.Hash(*e.Command.Quote)
		}
	}
	raw, err := json.Marshal(a.book)
	if err != nil {
		return nil, ErrInvalid
	}
	a.size = len(raw)
	return a, nil
}

func (a *BookAppender) Apply(ctx context.Context, c BookCommand) (BookProjection, BookReceipt, error) {
	if a.err != nil {
		return BookProjection{}, BookReceipt{}, a.err
	}
	c = normalizeBookCommand(c)
	if i, ok := a.commands[c.ID]; ok {
		e := a.book.Entries[i]
		if !reflect.DeepEqual(e.Command, c) {
			a.err = ErrConflict
			return BookProjection{}, BookReceipt{}, a.err
		}
		return copyProjection(a.projection), BookReceipt{Sequence: i + 1, EntrySHA256: e.SHA256, Duplicate: true, Outcomes: detach(e.Outcomes)}, nil
	}
	if len(a.book.Entries) >= MaxEntries {
		a.err = ErrCapacity
		return BookProjection{}, BookReceipt{}, a.err
	}
	if err := ctx.Err(); err != nil {
		a.err = err
		return BookProjection{}, BookReceipt{}, err
	}
	next, out, err := bookStep(a.book, a.projection, c, a.seen)
	if err != nil {
		a.err = err
		return BookProjection{}, BookReceipt{}, err
	}
	previous := a.book.SHA256
	if len(a.book.Entries) > 0 {
		previous = a.book.Entries[len(a.book.Entries)-1].SHA256
	}
	e := BookEntry{Command: c, Outcomes: out, PreviousSHA256: previous}
	e.SHA256 = workerplan.Hash(e)
	// The encoded book grows by this entry and, after the first, a comma.
	raw, err := json.Marshal(e)
	size := a.size + len(raw)
	if len(a.book.Entries) > 0 {
		size++
	}
	if err != nil || size > MaxBookBytes {
		a.err = ErrCapacity
		return BookProjection{}, BookReceipt{}, a.err
	}
	a.commands[c.ID] = len(a.book.Entries)
	a.book.Entries = append(a.book.Entries, e)
	a.projection, a.size, a.signed = next, size, false
	return copyProjection(next), BookReceipt{Sequence: len(a.book.Entries), EntrySHA256: e.SHA256, Outcomes: detach(out)}, nil
}

// copyProjection detaches every slice and pointer in a projection without the
// JSON round trip, whose cost grows with every consumed market update. Keep
// it in step with BookProjection and Projection.
func copyProjection(p BookProjection) BookProjection {
	out := p
	out.Orders = slices.Clone(p.Orders)
	for i := range out.Orders {
		out.Orders[i].Projection.ConsumedMarketUpdates = slices.Clone(p.Orders[i].Projection.ConsumedMarketUpdates)
	}
	out.Liquidity = slices.Clone(p.Liquidity)
	for i, l := range out.Liquidity {
		if l.Capacity != nil {
			c := *l.Capacity
			out.Liquidity[i].Capacity = &c
		}
	}
	return out
}

// Book returns a detached, signed copy of the current book.
func (a *BookAppender) Book() Book {
	if !a.signed {
		a.book.SHA256 = ""
		a.book.SHA256 = workerplan.Hash(a.book)
		a.signed = true
	}
	return detach(a.book)
}
