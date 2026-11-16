package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

var incubationSafetyFee = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,9})?$`)

// IncubationSafetySource supplies independently collected quotes to safety work.
// It must not depend on a model or on an entry request's data/health assertions.
type IncubationSafetySource interface {
	Quotes(context.Context, []string, time.Time) (map[string]account.IncubationQuote, error)
}

// IncubationSafetyPolicy fixes the worker's trading universe and maximum
// continuous holding time. Fees reserve the total cost of each protective order.
// Configure it in the service, independently of strategy/cycle/model input.
type IncubationSafetyPolicy struct {
	Symbols        []string
	MaximumHolding time.Duration
	Fees           map[string]string
}

// IncubationSafetyWorker reconciles one book/account and enforces a deterministic
// protective policy without invoking strategies. Evidence exists only while Run
// is active, after a successful cycle, and expires on missed progress or failure.
// It is deliberately not a serializable health attestation or a durable grant.
type IncubationSafetyWorker struct {
	runner     IncubationRunner
	source     IncubationSafetySource
	policy     IncubationSafetyPolicy
	interval   time.Duration
	policyHash string
	mu         sync.Mutex
	ctx        context.Context
	running    bool
	check      workhealth.Check
	config     account.CappedConfig
}

// NewIncubationSafetyWorker prepares a book-specific worker. Run must be started
// by the service; constructing a worker alone cannot admit new risk.
func NewIncubationSafetyWorker(r IncubationRunner, source IncubationSafetySource, policy IncubationSafetyPolicy, interval time.Duration) (*IncubationSafetyWorker, error) {
	if r.Store == nil || r.Broker == nil || r.BookID == "" || source == nil || interval < 100*time.Millisecond || interval > time.Minute || policy.MaximumHolding <= 0 || len(policy.Symbols) == 0 || len(policy.Symbols) > 64 {
		return nil, errors.New("invalid incubation safety worker configuration")
	}
	policy.Symbols = slices.Clone(policy.Symbols)
	slices.Sort(policy.Symbols)
	fees := map[string]string{}
	for i, symbol := range policy.Symbols {
		if symbol == "" || i > 0 && symbol == policy.Symbols[i-1] || policy.Fees[symbol] == "" {
			return nil, errors.New("distinct safety symbols and protective fee reserves required")
		}
		if !incubationSafetyFee.MatchString(policy.Fees[symbol]) {
			return nil, errors.New("invalid protective fee reserve")
		}
		fees[symbol] = policy.Fees[symbol]
	}
	policy.Fees = fees
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	r.Safety = nil
	return &IncubationSafetyWorker{runner: r, source: source, policy: policy, interval: interval, policyHash: hex.EncodeToString(hash[:])}, nil
}

// Run immediately performs safety work, then polls until cancellation. The
// caller owns the goroutine and must cancel and join it on service shutdown.
// A second Run on the same object is rejected rather than replacing its owner.
func (w *IncubationSafetyWorker) Run(ctx context.Context, clock Clock) error {
	if clock == nil {
		return errors.New("safety worker clock required")
	}
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return errors.New("safety worker already running")
	}
	w.running, w.ctx = true, ctx
	w.mu.Unlock()
	pending := [2]pendingFailure{}
	capabilities := []string{"protective_monitoring", "action_delivery"}
	defer func() {
		w.mu.Lock()
		w.running = false
		w.check = healthCheck(false, "book_safety_worker_stopped", clock.Now())
		w.mu.Unlock()
		for i, capability := range capabilities {
			recordWorkerCycle(context.Background(), w.runner.Store, "incubation-"+w.runner.BookID, capability, clock, w.interval, nil, true, &pending[i])
		}
	}()
	for ctx.Err() == nil {
		// Do not keep a prior success live while this cycle is blocked on I/O.
		w.mu.Lock()
		w.check = healthCheck(false, "book_safety_cycle_in_progress", clock.Now())
		w.mu.Unlock()
		observedAt := clock.Now()
		cycleCtx, cancel := context.WithTimeout(ctx, time.Minute)
		state, err := w.cycle(cycleCtx, clock)
		err = errors.Join(err, cycleCtx.Err())
		cancel()
		at := observedAt
		check := healthCheck(err == nil, "book_safety_workers_current", at, "book:"+w.runner.BookID, "paper_account:"+state.Config.BrokerAccountID, "safety_policy:"+w.policyHash, fmt.Sprintf("covered_version:%d", state.Version))
		check.ExpiresAt = at.Add(10*time.Second + 3*w.interval)
		if err != nil {
			check.Code = "book_safety_cycle_failed"
			slog.Error("incubation safety cycle failed", "book", w.runner.BookID, "error", err)
		}
		for i, capability := range capabilities {
			recordWorkerCycle(ctx, w.runner.Store, "incubation-"+w.runner.BookID, capability, clock, w.interval, err, false, &pending[i])
		}
		w.mu.Lock()
		w.check, w.config = check, state.Config
		w.mu.Unlock()
		if err := clock.Wait(ctx, w.interval); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Readiness inspects this worker's existing book-specific evidence. It does not
// execute a cycle, renew a clock, or grant admission to an entry request.
func (w *IncubationSafetyWorker) Readiness(ctx context.Context, at time.Time, marks map[string]string) workhealth.Check {
	if w == nil {
		return healthCheck(false, "book_safety_coverage_unavailable", at)
	}
	state, err := w.runner.Store.CappedBook(ctx, w.runner.BookID)
	if err != nil {
		return healthCheck(false, "book_safety_state_unavailable", at)
	}
	return w.coverageWithClock(ctx, w.runner.Store, state, func() time.Time { return at }, marks)
}

func (w *IncubationSafetyWorker) coverage(store IncubationStore, state account.CappedState, at time.Time, marks map[string]string) workhealth.Check {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return w.coverageWithClock(ctx, store, state, func() time.Time { return at }, marks)
}

// Fetch retained forward pins before taking the final live worker snapshot.
// A slow read cannot preserve withdrawn coverage or extend a protection clock.
func (w *IncubationSafetyWorker) coverageWithClock(ctx context.Context, store IncubationStore, state account.CappedState, now func() time.Time, marks map[string]string) workhealth.Check {
	at := now()
	first := w.coverageSnapshot(store, state, at, marks)
	if !first.Ready {
		return first
	}
	bindings := account.ForwardBindings(state)
	type pin struct {
		record forward.Record
		opened time.Time
		n      int64
	}
	var pins []pin
	snap, err := account.SnapshotCapped(state, marks, at)
	if err != nil {
		return healthCheck(false, "book_safety_state_unavailable", at)
	}
	for sleeve, id := range bindings {
		opened, err := account.ForwardHoldingOpened(state, sleeve, at)
		if err != nil {
			return healthCheck(false, "book_safety_state_unavailable", at)
		}
		pending := false
		for _, order := range snap.Pending {
			if order.SleeveID == sleeve && !order.ReduceOnly {
				pending = true
			}
		}
		if opened.IsZero() && !pending {
			continue
		}
		fs, ok := store.(forward.Store)
		if !ok {
			return healthCheck(false, "book_safety_forward_unavailable", at)
		}
		record, err := fs.ForwardDeclaration(ctx, id)
		if err != nil {
			return healthCheck(false, "book_safety_forward_unavailable", at)
		}
		n, err := store.StrategyTrialSearchTotal(ctx, record.Declaration.Family)
		if err != nil {
			return healthCheck(false, "book_safety_forward_search_unavailable", at)
		}
		pins = append(pins, pin{record, opened, n})
	}
	at = now()
	live := w.coverageSnapshot(store, state, at, marks)
	if !live.Ready {
		return live
	}
	for _, p := range pins {
		clocks := p.record.Clocks(at, p.n, p.opened)
		if !clocks.OutcomeOpen || clocks.HoldExpired {
			return healthCheck(false, "book_safety_protection_due", at, "forward:"+p.record.Declaration.ID)
		}
		deadline := p.record.Declaration.OutcomeDeadline
		if !p.opened.IsZero() {
			holding := p.opened.Add(time.Duration(p.record.Declaration.MaxHoldingSeconds) * time.Second)
			if holding.Before(deadline) {
				deadline = holding
			}
		}
		if deadline.Before(live.ExpiresAt) {
			live.ExpiresAt = deadline
		}
	}
	if err := ctx.Err(); err != nil {
		return healthCheck(false, "book_safety_observation_cancelled", at)
	}
	return live
}

func (w *IncubationSafetyWorker) coverageSnapshot(store IncubationStore, state account.CappedState, at time.Time, marks map[string]string) workhealth.Check {
	if w == nil {
		return healthCheck(false, "book_safety_coverage_unavailable", time.Time{})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	check := w.check
	check.EvidenceRefs = slices.Clone(check.EvidenceRefs)
	if !w.running || w.ctx.Err() != nil {
		return healthCheck(false, "book_safety_worker_stopped", at)
	}
	// Identical book IDs/configuration in another store do not share a worker.
	// Comparable instance identity also supports service decorators, without
	// accepting a caller-defined string scope or traversing wrapper internals.
	if !reflect.ValueOf(store).Comparable() || !reflect.ValueOf(w.runner.Store).Comparable() || store != w.runner.Store {
		return healthCheck(false, "book_safety_store_mismatch", at)
	}
	if !check.Ready {
		return check
	}
	if state.Config != w.config || state.Config.ID != w.runner.BookID || state.Config.BrokerAccountID != w.runner.Broker.PaperAccountID() {
		return healthCheck(false, "book_safety_association_mismatch", at)
	}
	if at.Before(check.At) || at.After(check.ExpiresAt) {
		return healthCheck(false, "book_safety_evidence_expired", check.At)
	}
	// The universal policy applies to every sleeve, including newly admitted ones,
	// but only within this service-configured universe. Check proposed and retained
	// assets; an entry request cannot extend the safety worker's responsibility.
	for symbol := range marks {
		if !slices.Contains(w.policy.Symbols, symbol) {
			return healthCheck(false, "book_safety_symbol_uncovered", at, "symbol:"+symbol)
		}
	}
	for _, event := range state.Events {
		if event.Order != nil && !slices.Contains(w.policy.Symbols, event.Order.Symbol) {
			return healthCheck(false, "book_safety_symbol_uncovered", at, "symbol:"+event.Order.Symbol)
		}
	}
	snap, err := account.SnapshotCapped(state, marks, at)
	if err != nil {
		return healthCheck(false, "book_safety_state_unavailable", at)
	}
	for _, order := range snap.Pending {
		if !order.ReduceOnly && !at.Before(order.At.Add(w.policy.MaximumHolding)) {
			return healthCheck(false, "book_safety_protection_due", at, "order:"+order.ID)
		}
	}
	roster, err := account.PortfolioRoster(state)
	if err != nil {
		return healthCheck(false, "book_safety_state_unavailable", at)
	}
	for sleeve, owner := range roster {
		opened, err := account.ForwardHoldingOpened(state, sleeve, at)
		if err != nil {
			return healthCheck(false, "book_safety_state_unavailable", at)
		}
		if !opened.IsZero() && !at.Before(opened.Add(w.policy.MaximumHolding)) || snap.Risk.Breakers[owner.StrategyID] || snap.Risk.Retired[owner.StrategyID] || account.ReviewExitRequired(state, sleeve, owner.StrategyID, at) {
			return healthCheck(false, "book_safety_protection_due", at, "sleeve:"+sleeve)
		}
	}
	return check
}

func (w *IncubationSafetyWorker) cycle(ctx context.Context, clock Clock) (account.CappedState, error) {
	quotes, err := w.source.Quotes(ctx, slices.Clone(w.policy.Symbols), clock.Now())
	if err != nil {
		return account.CappedState{}, err
	}
	at := clock.Now()
	for _, symbol := range w.policy.Symbols {
		q, ok := quotes[symbol]
		if !ok || q.Symbol != symbol {
			return account.CappedState{}, errors.New("safety quote missing or misbound")
		}
		if err := account.ValidateIncubationQuote(q, at.Add(-time.Minute-time.Nanosecond), at); err != nil {
			return account.CappedState{}, err
		}
	}
	marks := incubationMarks(quotes)
	c := cappedbook.Client{Store: w.runner.Store, Broker: w.runner.Broker, BookID: w.runner.BookID}
	state, err := c.SyncState(ctx, at, marks)
	if err != nil {
		return state, err
	}
	snap, err := account.SnapshotCapped(state, marks, at)
	if err != nil {
		return state, err
	}
	roster, err := account.PortfolioRoster(state)
	if err != nil {
		return state, err
	}
	flatten := snap.Halted || snap.Risk.Kill || snap.Risk.Paused || snap.Risk.ReduceOnly
	for _, order := range snap.Pending {
		if !order.ReduceOnly && !at.Before(order.At.Add(w.policy.MaximumHolding)) {
			flatten = true
		}
	}
	bindings := account.ForwardBindings(state)
	for sleeve, owner := range roster {
		opened, err := account.ForwardHoldingOpened(state, sleeve, at)
		if err != nil {
			return state, err
		}
		if snap.Risk.Breakers[owner.StrategyID] || snap.Risk.Retired[owner.StrategyID] || account.ReviewExitRequired(state, sleeve, owner.StrategyID, at) || !opened.IsZero() && !at.Before(opened.Add(w.policy.MaximumHolding)) {
			flatten = true
		}
		if id := bindings[sleeve]; id != "" {
			fs, ok := w.runner.Store.(forward.Store)
			if !ok {
				return state, errors.New("safety forward storage unavailable")
			}
			record, err := fs.ForwardDeclaration(ctx, id)
			if err != nil {
				return state, err
			}
			n, err := w.runner.Store.StrategyTrialSearchTotal(ctx, record.Declaration.Family)
			if errors.Is(err, strategytrial.ErrFamilyPending) {
				// Unknown multiplicity denies entry, never protective delivery.
				flatten = true
				continue
			}
			if err != nil {
				return state, err
			}
			clocks := record.Clocks(at, n, opened)
			if !clocks.OutcomeOpen || clocks.HoldExpired {
				flatten = true
			}
		}
	}
	if !flatten {
		return state, nil
	}
	// Pending opening legs cannot be canceled by this broker contract. They remain
	// owned and reconciled; never claim successful protection while they can fill.
	unresolvedOpening := false
	for _, order := range snap.Pending {
		if !order.ReduceOnly {
			unresolvedOpening = true
		}
	}
	exposed := false
	for _, qty := range snap.Positions {
		if qty != "0.000000000" {
			exposed = true
		}
	}
	if !exposed {
		if unresolvedOpening {
			return state, errors.New("safety has unresolved opening order")
		}
		return state, nil
	}
	// The distributor waits for sleeves with pending legs, while independent
	// retained owners may still reduce. Never let an unresolved entry prevent
	// the runner from attempting those independent protective exits.
	// Execution quotes must be collected after the finalized protective
	// decision, just as in a normal incubation cycle.
	// Freeze the protective decision while a file/stream collector supplies a
	// genuinely newer observation. Re-reading its current snapshot is not a fill.
	quoteCtx, cancelQuotes := context.WithTimeout(ctx, 10*time.Second)
	defer cancelQuotes()
	var executionAt time.Time
	for {
		quotes, err = w.source.Quotes(quoteCtx, slices.Clone(w.policy.Symbols), clock.Now())
		if err != nil {
			return state, err
		}
		executionAt = clock.Now()
		postDecision := executionAt.After(at)
		for _, symbol := range w.policy.Symbols {
			q, ok := quotes[symbol]
			if !ok || q.Symbol != symbol {
				return state, errors.New("protective execution quote missing or misbound")
			}
			if !q.OccurredAt.After(at) {
				postDecision = false
				continue
			}
			if err = account.ValidateIncubationQuote(q, at, executionAt); err != nil {
				return state, err
			}
		}
		if postDecision {
			break
		}
		if err = clock.Wait(quoteCtx, 100*time.Millisecond); err != nil {
			return state, err
		}
	}
	limits := map[string]string{}
	raw, err := json.Marshal(struct {
		Book    string
		Version int64
		At      time.Time
	}{w.runner.BookID, state.Version, executionAt})
	if err != nil {
		return state, err
	}
	hash := sha256.Sum256(raw)
	req := IncubationRequest{ID: "safety-" + hex.EncodeToString(hash[:16]), InputSHA256: hex.EncodeToString(hash[:]), Protective: true, DecidedAt: at, ExecuteAt: executionAt, Quotes: quotes, Limits: limits, Fees: w.policy.Fees}
	runner := w.runner
	runner.now = clock.Now
	protective, err := runner.protectivePlan(state, req)
	if err != nil {
		return state, err
	}
	// Use the most adverse retained cost across owners so every sell can pass
	// the same model/limit fence as ordinary incubation. Round down to the
	// ledger's decimal precision; rounding up could cross the modeled price.
	maxSlip := new(big.Rat)
	for _, cost := range protective.Costs {
		_, slip, err := cost.Parse()
		if err != nil {
			return state, err
		}
		if slip.Cmp(maxSlip) > 0 {
			maxSlip.Set(slip)
		}
	}
	for symbol, q := range quotes {
		bid, ok := new(big.Rat).SetString(q.Bid)
		if !ok {
			return state, errors.New("invalid protective quote")
		}
		price := executioncost.Price(bid, maxSlip, false)
		scaled := new(big.Rat).Mul(price, big.NewRat(1000000000, 1))
		units := new(big.Int).Quo(scaled.Num(), scaled.Denom())
		if units.Sign() <= 0 {
			return state, errors.New("protective price below ledger precision")
		}
		limits[symbol] = new(big.Rat).SetFrac(units, big.NewInt(1000000000)).FloatString(9)
	}
	if _, err := runner.Run(ctx, req); err != nil {
		return state, err
	}
	// Sending or queuing an exit is not proof that the protective obligation is
	// fulfilled. Reconcile again on the next cycle; keep new risk unavailable.
	return state, errors.New("safety protective exit awaiting reconciliation")
}
