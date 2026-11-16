// Package cappedbook connects the local capped ledger to the existing journaled
// Alpaca paper adapter. It never consults the broker's balance or buying power.
package cappedbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Broker is deliberately paper-only, bound to an expected account. Order is a
// lookup and never submits; recovery cannot turn reconciliation into a new POST.
type Broker interface {
	PaperAccountID() string
	AbortUnsubmitted(context.Context, string) error
	Submit(context.Context, alpaca.StockOrder) (alpaca.PaperOrder, error)
	Order(context.Context, string) (alpaca.PaperOrder, error)
	Fills(context.Context, time.Time) ([]json.RawMessage, error)
}

// Client applies the durable reservation before delegating any paper submission.
// Independent clients are serialized by the Store's book version check.
type Client struct {
	Store  storage.CappedBookStore
	Broker Broker
	BookID string
}

func (c Client) load(ctx context.Context) (account.CappedState, error) {
	if c.Store == nil || c.Broker == nil {
		return account.CappedState{}, errors.New("capped client requires store and paper broker")
	}
	s, e := c.Store.CappedBook(ctx, c.BookID)
	if e == nil && s.Config.BrokerAccountID != c.Broker.PaperAccountID() {
		return s, errors.New("paper account does not match capped book")
	}
	return s, e
}
func (c Client) append(ctx context.Context, cmd account.BookCommand) (account.CappedState, error) {
	for i := 0; i < 8; i++ {
		s, e := c.load(ctx)
		if e != nil {
			return s, e
		}
		s, e = c.Store.ApplyCappedBook(ctx, c.BookID, s.Version, cmd)
		if !errors.Is(e, account.ErrBookConflict) {
			return s, e
		}
	}
	return account.CappedState{}, account.ErrBookConflict
}

// Fund applies only configured due deposits, with scheduled IDs. Repeating a
// later invocation skips existing flows without changing their original clocks.
func (c Client) Fund(ctx context.Context, at time.Time) (account.CappedState, error) {
	s, e := c.load(ctx)
	if e != nil {
		return s, e
	}
	due, e := s.Config.DueDeposits(at)
	if e != nil {
		return s, e
	}
	for _, d := range due {
		exists := false
		for _, v := range s.Events {
			if v.ID == d.ID {
				exists = true
			}
		}
		if exists {
			continue
		}
		s, e = c.append(ctx, d)
		if e != nil {
			return s, e
		}
	}
	return s, nil
}

// Submit supports explicit day limit orders. Worst-case price and total fee
// reserve are checked against the book and all outstanding reservations. An
// ambiguous broker outcome retains the reservation and original client ID.
func (c Client) Submit(ctx context.Context, o account.BookOrder, marks map[string]string) (alpaca.PaperOrder, error) {
	order := alpaca.StockOrder{ClientOrderID: o.ID, Symbol: o.Symbol, Qty: o.Quantity, Side: o.Side, Type: "limit", TimeInForce: "day", LimitPrice: o.Limit}
	if err := order.Validate(); err != nil {
		return alpaca.PaperOrder{}, err
	}
	if _, err := c.Fund(ctx, o.At); err != nil {
		return alpaca.PaperOrder{}, err
	}
	if _, err := c.append(ctx, account.BookCommand{ID: "reserve-" + o.ID, Kind: "reserve", At: o.At, Order: &o, Marks: marks}); err != nil {
		return alpaca.PaperOrder{}, fmt.Errorf("capped book refusal: %w", err)
	}
	response, err := c.Broker.Submit(ctx, order)
	if err != nil {
		if abortErr := c.Broker.AbortUnsubmitted(ctx, o.ID); abortErr == nil {
			_, releaseErr := c.append(ctx, account.BookCommand{ID: "reject-" + o.ID, Kind: "reject", At: o.At, OrderID: o.ID})
			return response, errors.Join(err, releaseErr)
		}
		return response, err
	}
	if !matchesOrder(response, o) {
		_, haltErr := c.append(ctx, account.BookCommand{ID: "halt-submission", Kind: "halt", At: o.At})
		return response, errors.Join(errors.New("broker response differs from reserved order"), haltErr)
	}
	_, err = c.append(ctx, account.BookCommand{ID: "bind-" + o.ID, Kind: "bind", At: o.At, OrderID: o.ID, VenueOrderID: response.ID})
	return response, err
}
func matchesOrder(v alpaca.PaperOrder, o account.BookOrder) bool {
	return v.ID != "" && v.ClientOrderID == o.ID && v.Symbol == o.Symbol && v.Side == o.Side && v.Type == "limit" && v.TimeInForce == "day" && sameDecimal(v.Qty, o.Quantity) && sameDecimal(v.LimitPrice, o.Limit)
}

func sameDecimal(a, b string) bool {
	x, xok := new(big.Rat).SetString(a)
	y, yok := new(big.Rat).SetString(b)
	return xok && yok && x.Cmp(y) == 0
}

// Sync imports only activities for venue orders bound to this book. It retains
// each activity hash and refuses changed IDs, overfills, fees/price breaches or
// incomplete terminal reconciliation. Any such discrepancy halts new admission;
// raw venue responses remain in the broker journal for inspection.
func (c Client) Sync(ctx context.Context, at time.Time, marks map[string]string) (account.CappedSnapshot, error) {
	s, e := c.load(ctx)
	if e != nil {
		return account.CappedSnapshot{}, e
	}
	if _, err := account.SnapshotCapped(s, marks, at); err != nil {
		return account.CappedSnapshot{}, err
	}
	fail := func(err error) (account.CappedSnapshot, error) {
		_, haltErr := c.append(ctx, account.BookCommand{ID: "halt-reconciliation", Kind: "halt", At: at})
		return account.CappedSnapshot{}, errors.Join(err, haltErr)
	}
	orders := map[string]account.BookOrder{}
	rejected := map[string]bool{}
	bound := map[string]string{}
	for _, v := range s.Events {
		if v.Kind == "reject" {
			rejected[v.OrderID] = true
		}
		if v.Kind == "reserve" {
			orders[v.Order.ID] = *v.Order
		}
		if v.Kind == "bind" {
			bound[v.VenueOrderID] = v.OrderID
		}
	}
	statuses := map[string]alpaca.PaperOrder{}
	ids := []string{}
	for id := range orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if rejected[id] {
			continue
		}
		v, err := c.Broker.Order(ctx, id)
		if errors.Is(err, alpaca.ErrPaperNotSubmitted) {
			if err = c.Broker.AbortUnsubmitted(ctx, id); err != nil {
				return account.CappedSnapshot{}, err
			}
			s, err = c.append(ctx, account.BookCommand{ID: "reject-" + id, Kind: "reject", At: at, OrderID: id})
			if err != nil {
				return account.CappedSnapshot{}, err
			}
			continue
		}
		if err != nil {
			return account.CappedSnapshot{}, err
		}
		if !matchesOrder(v, orders[id]) {
			return fail(errors.New("paper order differs from reservation"))
		}
		if v.ID == "" || v.ClientOrderID != id {
			return fail(errors.New("paper order identity changed"))
		}
		statuses[id] = v
		if old, ok := bound[v.ID]; ok && old != id {
			return fail(errors.New("venue order is bound twice"))
		}
		bound[v.ID] = id
		exists := false
		for _, event := range s.Events {
			if event.Kind == "bind" && event.OrderID == id {
				if event.VenueOrderID != v.ID {
					return fail(errors.New("venue order binding changed"))
				}
				exists = true
			}
		}
		if !exists {
			s, e = c.append(ctx, account.BookCommand{ID: "bind-" + id, Kind: "bind", At: at, OrderID: id, VenueOrderID: v.ID})
			if e != nil {
				return fail(e)
			}
		}
	}
	raws, e := c.Broker.Fills(ctx, s.Config.OpenedAt.Add(-time.Nanosecond))
	if e != nil {
		return account.CappedSnapshot{}, e
	}
	fills := []account.BookFill{}
	for _, raw := range raws {
		var v struct {
			ID      string    `json:"id"`
			OrderID string    `json:"order_id"`
			Symbol  string    `json:"symbol"`
			Qty     string    `json:"qty"`
			Price   string    `json:"price"`
			Side    string    `json:"side"`
			Fee     string    `json:"commission"`
			At      time.Time `json:"transaction_time"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return fail(errors.New("invalid activity JSON"))
		}
		id, owned := bound[v.OrderID]
		if !owned {
			continue
		}
		if v.Fee == "" {
			v.Fee = "0"
		}
		sum := sha256.Sum256(raw)
		fills = append(fills, account.BookFill{ID: v.ID, OrderID: id, VenueOrderID: v.OrderID, Symbol: v.Symbol, Quantity: v.Qty, Price: v.Price, Fee: v.Fee, Side: v.Side, SourceSHA256: hex.EncodeToString(sum[:]), At: v.At})
	}
	slices.SortFunc(fills, func(a, b account.BookFill) int {
		if a.At.Before(b.At) {
			return -1
		}
		if a.At.After(b.At) {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, f := range fills {
		var old *account.BookFill
		for _, v := range s.Events {
			if v.Kind == "fill" && v.Fill.ID == f.ID {
				old = v.Fill
			}
		}
		if old != nil {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(f)
			if string(a) != string(b) {
				return fail(errors.New("retained venue fill changed"))
			}
			continue
		}
		s, e = c.append(ctx, account.BookCommand{ID: f.ID, Kind: "fill", At: at, Fill: &f, Marks: marks})
		if e != nil {
			return fail(e)
		}
	}
	for _, id := range ids {
		v, known := statuses[id]
		if !known {
			continue
		}
		if !slices.Contains([]string{"filled", "canceled", "expired", "rejected"}, v.Status) {
			continue
		}
		total := new(big.Rat)
		released := false
		for _, event := range s.Events {
			if event.Kind == "fill" && event.Fill.OrderID == id {
				x, ok := new(big.Rat).SetString(event.Fill.Quantity)
				if !ok {
					return fail(errors.New("invalid retained fill quantity"))
				}
				total.Add(total, x)
			}
			if event.Kind == "release" && event.OrderID == id {
				released = true
			}
		}
		if !sameDecimal(total.FloatString(9), v.FilledQty) {
			return fail(errors.New("terminal order does not reconcile with owned fill activities"))
		}
		if !released {
			s, e = c.append(ctx, account.BookCommand{ID: "release-" + id, Kind: "release", At: at, OrderID: id, VenueOrderID: v.ID})
			if e != nil {
				return fail(e)
			}
		}
	}
	return account.SnapshotCapped(s, marks, at)
}
