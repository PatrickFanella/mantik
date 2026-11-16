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
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
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

// Fund applies automatic schedule slots only to synthetic books. Owner-receipt
// books require confirmed receipts and never acquire cash from elapsed time.
// Repeating a legacy invocation retains the original scheduled-flow clocks.
func (c Client) Fund(ctx context.Context, at time.Time) (account.CappedState, error) {
	s, e := c.load(ctx)
	if e != nil {
		return s, e
	}
	due, e := s.Config.DueDeposits(at)
	if e != nil {
		return s, e
	}
	if s.Config.FundingMode == account.OwnerReceipts {
		// Validate retained receipts without requiring live valuation marks. This
		// also refuses malformed or mixed journals instead of returning them as
		// spendable funding. No broker/model request is involved.
		_, e = account.PortfolioRoster(s)
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
	if o.AllocationID == "" || o.SleeveID == "" {
		return alpaca.PaperOrder{}, errors.New("distributor_order_required")
	}
	if _, err := c.Fund(ctx, o.At); err != nil {
		return alpaca.PaperOrder{}, err
	}
	// Persist loss-triggered brakes even when the following order is refused.
	if _, err := c.append(ctx, account.BookCommand{ID: "observe-" + o.ID, Kind: "risk_observe", At: o.At, Marks: marks}); err != nil {
		return alpaca.PaperOrder{}, err
	}
	return c.submit(ctx, o, marks, "portfolio_reserve")
}

// submit is shared with the low-level ledger contract tests. The only exported
// submission entry always requires a retained distributor order and risk check.
func (c Client) submit(ctx context.Context, o account.BookOrder, marks map[string]string, kind string) (alpaca.PaperOrder, error) {
	order := alpaca.StockOrder{ClientOrderID: o.ID, Symbol: o.Symbol, Qty: o.Quantity, Side: o.Side, Type: "limit", TimeInForce: "day", LimitPrice: o.Limit}
	if err := order.Validate(); err != nil {
		return alpaca.PaperOrder{}, err
	}
	if _, err := c.Fund(ctx, o.At); err != nil {
		return alpaca.PaperOrder{}, err
	}
	if _, err := c.append(ctx, account.BookCommand{ID: "reserve-" + o.ID, Kind: kind, At: o.At, Order: &o, Marks: marks}); err != nil {
		return alpaca.PaperOrder{}, fmt.Errorf("capped book refusal: %w", err)
	}
	if kind == "portfolio_reserve" {
		s, err := c.load(ctx)
		if err != nil {
			return alpaca.PaperOrder{}, err
		}
		if err = account.CheckPortfolioSubmission(s, o, marks); err != nil {
			// A fill can land after reservation but before POST. Persist the fresh
			// loss observation even when that final check refuses the submission.
			at := o.At
			if len(s.Events) > 0 && s.Events[len(s.Events)-1].At.After(at) {
				at = s.Events[len(s.Events)-1].At
			}
			h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", o.ID, s.Version)))
			_, observeErr := c.append(ctx, account.BookCommand{ID: "observe-refusal-" + hex.EncodeToString(h[:16]), Kind: "risk_observe", At: at, Marks: marks})
			return alpaca.PaperOrder{}, errors.Join(err, observeErr)
		}
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
		_, haltErr := c.halt(ctx, "submission", o.At, marks)
		return response, errors.Join(errors.New("broker response differs from reserved order"), haltErr)
	}
	_, err = c.append(ctx, account.BookCommand{ID: "bind-" + o.ID, Kind: "bind", At: o.At, OrderID: o.ID, VenueOrderID: response.ID})
	return response, err
}

// Rebalance retains sizing on current local equity after synthetic-only funding.
// Lifecycle admissions come from stored gates; strategies provide weights only.
// Qualified sleeves require the later forward-qualification owner (#92).
func (c Client) Rebalance(ctx context.Context, req allocation.RebalanceRequest, marks map[string]string) (allocation.Distribution, error) {
	req.Sleeves = slices.Clone(req.Sleeves)
	book, bookErr := c.load(ctx)
	if bookErr != nil {
		return allocation.Distribution{}, bookErr
	}
	if out, retained, e := retainedRebalance(book, req, marks); retained {
		return out, e
	}
	roster, rosterErr := account.PortfolioRoster(book)
	if rosterErr != nil {
		return allocation.Distribution{}, rosterErr
	}
	flat := map[string]bool{}
	for _, t := range req.Targets {
		flat[t.SleeveID] = allocation.FlatTarget(t.Decision)
	}
	reader, ok := c.Store.(interface {
		StrategyGate(context.Context, string) (strategygate.Record, error)
	})
	if !ok {
		return allocation.Distribution{}, errors.New("gate_registry_required")
	}
	for i := range req.Sleeves {
		s := &req.Sleeves[i]
		if old, exists := roster[s.ID]; exists && flat[s.ID] && old.StrategyID == s.StrategyID {
			*s = old
			continue
		}
		if s.Stage == "qualified" {
			q := account.Qualification(book, s.ID)
			if q == nil || q.StrategyID != s.StrategyID {
				return allocation.Distribution{}, errors.New("forward_qualification_required")
			}
			continue
		}
		g, e := reader.StrategyGate(ctx, s.GateID)
		if e != nil {
			return allocation.Distribution{}, e
		}
		if g.Declaration.Account != string(book.Config.Type) {
			return allocation.Distribution{}, errors.New("gate_account_differs_from_book")
		}
		if g.Decision == nil || g.Decision.Stage != "incubation" || g.Decision.StrategyID != s.StrategyID {
			return allocation.Distribution{}, errors.New("incubation_gate_required")
		}
		if !g.Decision.AdmitsIncubation() {
			return allocation.Distribution{}, errors.New("gate_evidence_ineligible")
		}
		// The store checks freshness under the same fence as new family starts.
		s.Minimum = g.Decision.Minimum
		s.Stage = "incubation"
		s.ModelDriven = false
		if g.Decision.Status == "forward_only" {
			s.Stage = "forward_only"
			s.ModelDriven = true
		}
	}
	if _, e := c.Fund(ctx, req.At); e != nil {
		return allocation.Distribution{}, e
	}
	s, e := c.append(ctx, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: marks})
	if e != nil {
		return allocation.Distribution{}, e
	}
	return rebalanceDistribution(s, req.ID)
}

// retainedRebalance recovers a response without asking research to admit it
// again. Only authoritative admission fields are restored; the journal still
// compares all effective request fields, marks and clocks for changed retries.
func retainedRebalance(book account.CappedState, req allocation.RebalanceRequest, marks map[string]string) (allocation.Distribution, bool, error) {
	for i, event := range book.Events {
		if event.ID != req.ID {
			continue
		}
		if event.Kind != "rebalance" || event.Rebalance == nil || len(req.Sleeves) != len(event.Rebalance.Sleeves) {
			return allocation.Distribution{}, true, account.ErrBookConflict
		}
		prefix := account.CappedPrefix(book, i)
		roster, e := account.PortfolioRoster(prefix)
		if e != nil {
			return allocation.Distribution{}, true, e
		}
		flat := map[string]bool{}
		for _, t := range req.Targets {
			flat[t.SleeveID] = allocation.FlatTarget(t.Decision)
		}
		for j := range req.Sleeves {
			s := &req.Sleeves[j]
			if prior, exists := roster[s.ID]; exists && flat[s.ID] && prior.StrategyID == s.StrategyID {
				*s = prior
				continue
			}
			old := event.Rebalance.Sleeves[j]
			if s.ID != old.ID || s.StrategyID != old.StrategyID || s.GateID != old.GateID || s.Stage == "qualified" && old.Stage != "qualified" {
				return allocation.Distribution{}, true, account.ErrBookConflict
			}
			s.Minimum, s.Stage, s.ModelDriven = old.Minimum, old.Stage, old.ModelDriven
		}
		cmd := account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: marks}
		if _, e = account.ApplyCappedCommand(book, cmd); e != nil {
			return allocation.Distribution{}, true, e
		}
		out, e := rebalanceDistribution(book, req.ID)
		return out, true, e
	}
	return allocation.Distribution{}, false, nil
}

func rebalanceDistribution(s account.CappedState, id string) (allocation.Distribution, error) {
	for i, event := range s.Events {
		if event.ID == id && event.Kind == "rebalance" {
			// Exact retries return the original sizing even after later commands.
			s = account.CappedPrefix(s, i+1)
			snap, e := account.SnapshotCapped(s, event.Marks, event.At)
			if e != nil {
				return allocation.Distribution{}, e
			}
			if snap.Distribution != nil {
				return *snap.Distribution, nil
			}
		}
	}
	return allocation.Distribution{}, errors.New("distribution_missing")
}

// Control records an explicit operator action. It is separate from all strategy
// inputs; callers must authenticate owner commands before invoking this method.
func (c Client) Control(ctx context.Context, id string, at time.Time, control risk.Control) (account.CappedState, error) {
	return c.append(ctx, account.BookCommand{ID: id, Kind: "risk_control", At: at, RiskControl: &control})
}
func matchesOrder(v alpaca.PaperOrder, o account.BookOrder) bool {
	return v.ID != "" && v.ClientOrderID == o.ID && v.Symbol == o.Symbol && v.Side == o.Side && v.Type == "limit" && v.TimeInForce == "day" && sameDecimal(v.Qty, o.Quantity) && sameDecimal(v.LimitPrice, o.Limit)
}

// halt uses the current journal version as its identity, so a later discrepancy
// can halt again after an explicit owner clear. Already-halted retries add none.
func (c Client) halt(ctx context.Context, kind string, at time.Time, marks map[string]string) (account.CappedState, error) {
	s, e := c.load(ctx)
	if e != nil {
		return s, e
	}
	if len(s.Events) > 0 && s.Events[len(s.Events)-1].At.After(at) {
		at = s.Events[len(s.Events)-1].At
	}
	v, e := account.SnapshotCapped(s, marks, at)
	if e != nil {
		return s, e
	}
	if v.Halted {
		return s, nil
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", kind, s.Version)))
	return c.append(ctx, account.BookCommand{ID: "halt-" + kind + "-" + hex.EncodeToString(h[:16]), Kind: "halt", At: at})
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
	s, err := c.SyncState(ctx, at, marks)
	if err != nil {
		return account.CappedSnapshot{}, err
	}
	return account.SnapshotCapped(s, marks, at)
}

// SyncState returns the exact journal prefix reconciled against order and fill
// GETs. Intervening commands cause ErrBookConflict; callers must retry a complete
// reconciliation rather than promote a later reload to a reconciled baseline.
func (c Client) SyncState(ctx context.Context, at time.Time, marks map[string]string) (account.CappedState, error) {
	s, e := c.load(ctx)
	if e != nil {
		return account.CappedState{}, e
	}
	if _, err := account.SnapshotCapped(s, marks, at); err != nil {
		return account.CappedState{}, err
	}
	// Every write is fenced to the precise state whose orders were read. A
	// concurrent allocation must get its own reconciliation, never be absorbed
	// by append's ordinary conflict/reload retry.
	appendReconciled := func(cmd account.BookCommand) (account.CappedState, error) {
		expected, err := account.ApplyCappedCommand(s, cmd)
		if err != nil {
			return account.CappedState{}, err
		}
		next, err := c.Store.ApplyCappedBook(ctx, c.BookID, s.Version, cmd)
		if err != nil {
			return account.CappedState{}, err
		}
		// Store retries intentionally accept an identical command even with a
		// stale expected version. That idempotency contract must not promote
		// unrelated receipts to reconciliation authority either.
		if next.Version != expected.Version {
			return account.CappedState{}, account.ErrBookConflict
		}
		return next, nil
	}
	fail := func(err error) (account.CappedState, error) {
		if errors.Is(err, account.ErrBookConflict) {
			return account.CappedState{}, err
		}
		_, haltErr := c.halt(ctx, "reconciliation", at, marks)
		return account.CappedState{}, errors.Join(err, haltErr)
	}
	orders := map[string]account.BookOrder{}
	rejected := map[string]bool{}
	bound := map[string]string{}
	for _, v := range s.Events {
		if v.Kind == "reject" {
			rejected[v.OrderID] = true
		}
		if v.Kind == "reserve" || v.Kind == "portfolio_reserve" {
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
				return account.CappedState{}, err
			}
			s, err = appendReconciled(account.BookCommand{ID: "reject-" + id, Kind: "reject", At: at, OrderID: id})
			if err != nil {
				return account.CappedState{}, err
			}
			continue
		}
		if err != nil {
			return account.CappedState{}, err
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
			s, e = appendReconciled(account.BookCommand{ID: "bind-" + id, Kind: "bind", At: at, OrderID: id, VenueOrderID: v.ID})
			if e != nil {
				return fail(e)
			}
		}
	}
	raws, e := c.Broker.Fills(ctx, s.Config.OpenedAt.Add(-time.Nanosecond))
	if e != nil {
		return account.CappedState{}, e
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
			if qualificationOrder(s, id) {
				return fail(errors.New("qualification venue activity has no observed commission"))
			}
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
		model, modelErr := account.ModelIncubationFill(s, f)
		if modelErr != nil {
			return fail(modelErr)
		}
		s, e = appendReconciled(account.BookCommand{ID: f.ID, Kind: "fill", At: at, Fill: &f, ModelFill: model, Marks: marks})
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
			s, e = appendReconciled(account.BookCommand{ID: "release-" + id, Kind: "release", At: at, OrderID: id, VenueOrderID: v.ID})
			if e != nil {
				return fail(e)
			}
		}
	}
	if len(s.Events) > 0 && !s.Events[len(s.Events)-1].At.After(at) {
		snap, err := account.SnapshotCapped(s, marks, at)
		if err != nil {
			return account.CappedState{}, err
		}
		if snap.Distribution != nil {
			raw, err := json.Marshal(struct {
				At    time.Time
				Marks map[string]string
			}{at, marks})
			if err != nil {
				return account.CappedState{}, err
			}
			h := sha256.Sum256(raw)
			s, err = appendReconciled(account.BookCommand{ID: "observe-sync-" + hex.EncodeToString(h[:16]), Kind: "risk_observe", At: at, Marks: marks})
			if err != nil {
				return account.CappedState{}, err
			}
		}
	}
	current, err := c.load(ctx)
	if err != nil {
		return account.CappedState{}, err
	}
	if current.Version != s.Version {
		return account.CappedState{}, account.ErrBookConflict
	}
	return s, nil
}

func qualificationOrder(s account.CappedState, id string) bool {
	allocationID := ""
	for _, e := range s.Events {
		if e.Order != nil && e.Order.ID == id {
			allocationID = e.Order.AllocationID
		}
	}
	for _, e := range s.Events {
		if e.Kind == "qualification_plan" && e.Incubation != nil && e.Incubation.Rebalance.ID == allocationID {
			return true
		}
	}
	return false
}
