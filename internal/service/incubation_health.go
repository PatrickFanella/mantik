package service

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

// IncubationCalendar pins the retained exchange response and its coverage. It
// is market data, not a health attestation. Raw bytes stay in private custody.
type IncubationCalendar struct {
	From, Through, SourceSHA256 string
	ObservedAt                  time.Time
	Sessions                    []alpaca.Session
}

func healthCheck(ready bool, code string, at time.Time, refs ...string) workhealth.Check {
	status := "unavailable"
	if ready {
		status = "ready"
	}
	return workhealth.Check{Ready: ready, Status: status, Code: code, At: at, EvidenceRefs: refs}
}

// A reconciliation proof is created only by this runner's completed Sync. It is
// deliberately absent from request JSON and cannot be restored from an old plan.
type incubationReconciliation struct {
	at           time.Time
	err          error
	version      int64
	allocationID string
}

func (r IncubationRunner) admission(ctx context.Context, state account.CappedState, at time.Time, data workhealth.Check, rec *incubationReconciliation, marks map[string]string) workhealth.Report {
	report := workhealth.Report{At: at, Checks: map[string]workhealth.Check{}, Work: map[string]workhealth.Check{}}
	report.Checks["data_freshness"] = data
	report.Checks["reconciliation"] = healthCheck(false, "owned_reconciliation_unavailable", time.Time{}, "book:"+r.BookID)
	if rec != nil {
		c := healthCheck(rec.err == nil && !at.Before(rec.at) && at.Sub(rec.at) <= time.Minute, "owned_reconciliation_current", rec.at, "book:"+r.BookID, fmt.Sprintf("reconciled_version:%d", rec.version))
		c.ExpiresAt = rec.at.Add(time.Minute)
		if rec.err != nil {
			c.Code = "owned_reconciliation_failed"
		} else if !c.Ready {
			c.Code = "owned_reconciliation_expired"
		} else if !reconciliationUnchanged(state, rec, rec.allocationID) {
			c.Ready, c.Status, c.Code = false, "unavailable", "owned_reconciliation_book_changed"
		}
		report.Checks["reconciliation"] = c
	}
	workers := healthCheck(false, "worker_liveness_unavailable", time.Time{})
	snapshot, snapshotErr := r.Store.Snapshot(ctx)
	if snapshotErr == nil {
		readiness := operational.Readiness(snapshot.State, at, true, "storage")
		workers = healthCheck(true, "safety_workers_current", time.Time{})
		for _, name := range []string{"protective_monitoring", "action_delivery"} {
			c := readiness.Capabilities[name]
			if snapshot.State.Operations != nil {
				w := snapshot.State.Operations.Workers[name]
				if workers.At.IsZero() || w.At.Before(workers.At) {
					workers.At = w.At
				}
				workers.EvidenceRefs = append(workers.EvidenceRefs, "worker_reported:"+name+":"+w.At.Format(time.RFC3339Nano))
			}
			workers.EvidenceRefs = append(workers.EvidenceRefs, c.EvidenceRefs...)
			workers.EvidenceRefs = append(workers.EvidenceRefs, "capability:"+name)
			if !c.Ready {
				workers.Ready, workers.Status, workers.Code = false, "unavailable", name+":"+c.Code
				break
			}
			w := snapshot.State.Operations.Workers[name]
			// A report from before this book existed cannot establish its worker era.
			if w.LastSuccessAt.IsZero() || w.LastSuccessAt.Before(state.Config.OpenedAt) {
				workers = healthCheck(false, name+":worker_success_unavailable", w.At, "worker:"+w.WorkerID)
				break
			}
			expiry := w.At.Add(10*time.Second + 3*time.Duration(w.IntervalMillis)*time.Millisecond)
			if workers.ExpiresAt.IsZero() || expiry.Before(workers.ExpiresAt) {
				workers.ExpiresAt = expiry
			}
			if w.At.Before(workers.At) {
				workers.At = w.At
			}
		}
	}
	// Global demo workflow reports cannot establish responsibility for this
	// book. Only the running service worker can supply that association and its
	// independently executed reconciliation/protection evidence.
	coverage := r.Safety.coverageWithClock(ctx, r.Store, state, func() time.Time { return at }, marks)
	if !coverage.Ready {
		workers = coverage
	} else if workers.Ready {
		workers.Code = "book_safety_workers_current"
		workers.EvidenceRefs = append(workers.EvidenceRefs, coverage.EvidenceRefs...)
		if workers.ExpiresAt.IsZero() || coverage.ExpiresAt.Before(workers.ExpiresAt) {
			workers.ExpiresAt = coverage.ExpiresAt
		}
		if coverage.At.Before(workers.At) {
			workers.At = coverage.At
		}
	}
	report.Checks["worker_liveness"] = workers
	brake := healthCheck(false, "risk_brake_unavailable", time.Time{}, "book:"+r.BookID)
	snap, err := account.SnapshotCapped(state, marks, at)
	if err == nil && !snap.Risk.At.IsZero() && !at.Before(snap.Risk.At) {
		// Observe only owned local equity and configured deposits. Never broker cash.
		capital, valid := new(big.Rat).SetString(state.Config.DepositAmount)
		if !valid {
			capital = new(big.Rat)
		}
		capital.Mul(capital, big.NewRat(int64(snap.Deposits), 1))
		observed, e := risk.Observe(snap.Risk, at, snap.Equity, capital.FloatString(18))
		if e == nil {
			brake = healthCheck(true, "risk_brake_current", at, "book:"+r.BookID, fmt.Sprintf("book_version:%d", state.Version))
			switch {
			case snap.Halted || observed.Halted:
				brake.Ready, brake.Code = false, "risk_hard_halt"
			case observed.Kill:
				brake.Ready, brake.Code = false, "risk_kill_switch"
			case observed.ReduceOnly:
				brake.Ready, brake.Code = false, "risk_reduce_only"
			case observed.Paused:
				brake.Ready, brake.Code = false, observed.PauseReason
			}
			if !brake.Ready {
				brake.Status = "blocked"
			}
		}
	}
	report.Checks["risk_brake"] = brake
	entry := healthCheck(true, "health_admitted", at)
	for _, name := range []string{"data_freshness", "reconciliation", "worker_liveness", "risk_brake"} {
		c := report.Checks[name]
		if !c.Ready {
			entry.Ready, entry.Status = false, "blocked"
			entry.EvidenceRefs = append(entry.EvidenceRefs, name+":"+c.Code)
		}
	}
	if !entry.Ready {
		entry.Code = "new_risk_not_admitted"
	}
	report.Work["entry"] = entry
	// These classes remain permitted by health. Their own ledger, quote and
	// reduce-only checks still run; readiness is permission, not execution proof.
	for _, name := range []string{"reduce", "exit", "reconcile", "settle"} {
		report.Work[name] = healthCheck(true, "health_independent_safety_work", at, "book:"+r.BookID)
	}
	if ctx.Err() != nil {
		report.Work["entry"] = healthCheck(false, "health_observation_cancelled", at)
	}
	return report
}

func dailyAdmission(calendar *IncubationCalendar, views []strategy.View, req IncubationRequest, at time.Time) workhealth.Check {
	bad := func(code string, refs ...string) workhealth.Check {
		return healthCheck(false, code, at, append(refs, "input:"+req.InputSHA256)...)
	}
	if calendar == nil {
		return bad("daily_calendar_unavailable")
	}
	hash := calendar.SourceSHA256
	if len(hash) != 64 || slices.ContainsFunc([]byte(hash), func(c byte) bool { return c < '0' || c > '9' && c < 'a' || c > 'f' }) || calendar.ObservedAt.IsZero() || calendar.ObservedAt.After(req.DecidedAt) || at.Before(calendar.ObservedAt) || at.Sub(calendar.ObservedAt) > 24*time.Hour {
		return bad("daily_calendar_clock_invalid", "calendar:"+hash)
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return bad("daily_calendar_unavailable")
	}
	today := at.In(zone).Format(time.DateOnly)
	_, fromErr := time.Parse(time.DateOnly, calendar.From)
	_, throughErr := time.Parse(time.DateOnly, calendar.Through)
	if fromErr != nil || throughErr != nil || calendar.From > calendar.Through || calendar.Through < today {
		return bad("daily_calendar_coverage_missing")
	}
	eligible := []alpaca.Session{}
	prior := ""
	for _, session := range calendar.Sessions {
		if session.Date < calendar.From || session.Date > calendar.Through || session.Date <= prior || session.OpenAt.IsZero() || !session.CloseAt.After(session.OpenAt) || session.OpenAt.In(zone).Format(time.DateOnly) != session.Date || session.CloseAt.In(zone).Format(time.DateOnly) != session.Date {
			return bad("daily_calendar_invalid")
		}
		prior = session.Date
		// Match daily collection's publication embargo; today's unfinished daily
		// bar is never treated as a required one-minute-old intraday observation.
		if !session.CloseAt.Add(alpaca.RecentSIPDelay).After(at) {
			eligible = append(eligible, session)
		}
	}
	if len(eligible) == 0 || len(views) != len(req.Strategies) {
		return bad("daily_bars_unavailable")
	}
	refs := []string{"calendar:" + hash, "calendar_observed:" + calendar.ObservedAt.Format(time.RFC3339Nano), "input:" + req.InputSHA256}
	for i, s := range req.Strategies {
		if s.Strategy == nil {
			return bad("strategy_unavailable")
		}
		spec := s.Strategy.Spec()
		if !slices.Contains(spec.Data, strategy.KindDailyBars) {
			continue
		}
		need := max(1, spec.Warmup)
		if len(eligible) < need || len(s.Assets) == 0 {
			return bad("daily_warmup_unavailable")
		}
		for _, asset := range s.Assets {
			bars := views[i].Bars(asset)
			byDate := map[string]storage.DailyBar{}
			for _, b := range bars {
				raw := b.Raw
				if b.Date > eligible[len(eligible)-1].Date {
					return bad("daily_bar_future_session", "bar:"+asset+":"+b.Date)
				}
				if raw.AssetID != asset || raw.Symbol != asset || raw.Date != b.Date || raw.ObservedAt.IsZero() || raw.PublishedAt.IsZero() || raw.AvailableAt.IsZero() || raw.AvailableAt.Before(raw.ObservedAt) || raw.AvailableAt.Before(raw.PublishedAt) || raw.AvailableAt.After(req.DecidedAt) {
					return bad("daily_bar_availability_invalid", "bar:"+asset+":"+b.Date)
				}
				byDate[b.Date] = raw
			}
			for _, session := range eligible[len(eligible)-need:] {
				if byDate[session.Date].ObservedAt.IsZero() {
					return bad("daily_bar_missing_or_stale", "bar:"+asset+":"+session.Date)
				}
				// The series must have existed after that session closed, not a backdated
				// observation of a bar that had not happened yet.
				b := byDate[session.Date]
				if b.ObservedAt.Before(session.CloseAt.Add(alpaca.RecentSIPDelay)) || b.PublishedAt.Before(session.CloseAt) {
					return bad("daily_bar_availability_invalid", "bar:"+asset+":"+b.Date)
				}
				refs = append(refs, "bar:"+asset+":"+session.Date, "bar_observed:"+b.ObservedAt.Format(time.RFC3339Nano), "bar_published:"+b.PublishedAt.Format(time.RFC3339Nano), "bar_available:"+b.AvailableAt.Format(time.RFC3339Nano))
			}
		}
	}
	symbols := make([]string, 0, len(req.Quotes))
	for symbol := range req.Quotes {
		symbols = append(symbols, symbol)
	}
	slices.Sort(symbols)
	for _, symbol := range symbols {
		q := req.Quotes[symbol]
		if symbol != q.Symbol || account.ValidateIncubationQuote(q, req.DecidedAt, at) != nil {
			return bad("execution_quote_invalid", "quote:"+symbol+":"+q.SourceSHA256)
		}
		refs = append(refs, "quote:"+symbol+":"+q.SourceSHA256, "quote_occurred:"+q.OccurredAt.Format(time.RFC3339Nano), "quote_received:"+q.ReceivedAt.Format(time.RFC3339Nano))
	}
	// Includes assets that did not become targets and all held trading assets.
	for _, s := range req.Strategies {
		for _, a := range s.Assets {
			if _, ok := req.Quotes[a]; !ok {
				return bad("execution_quote_missing", "quote:"+a)
			}
		}
	}
	return healthCheck(true, "daily_sessions_and_quotes_current", at, refs...)
}

func incubationHealthRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(IncubationStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/incubation/{id}/readiness", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		r := IncubationRunner{Store: backend, BookID: req.PathValue("id")}
		state, err := backend.CappedBook(ctx, r.BookID)
		if err != nil {
			failed(w, err)
			return
		}
		at := clock.Now()
		// Durable reports are inspection evidence only. Neither a restart nor a GET
		// renews reconciliation, market observations or entry permission.
		data := healthCheck(false, "current_cycle_data_unavailable", time.Time{})
		report := r.admission(ctx, state, at, data, nil, nil)
		var retained *workhealth.Report
		for _, event := range state.Events {
			if event.Incubation != nil {
				retained = event.Incubation.Health
			}
		}
		write(w, 200, struct {
			Admission workhealth.Report  `json:"admission"`
			Retained  *workhealth.Report `json:"retained_cycle_evidence,omitempty"`
		}{report, retained})
	})
}

func (r IncubationRunner) clockNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now().UTC()
}

func (r IncubationRunner) checkHealthSubmission(ctx context.Context, o alpaca.StockOrder, plan account.IncubationPlan, req IncubationRequest, views []strategy.View, rec *incubationReconciliation, observed *workhealth.Report) error {
	state, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		return err
	}
	var order *account.BookOrder
	for _, event := range state.Events {
		if event.Kind == "portfolio_reserve" && event.Order != nil && event.Order.ID == o.ClientOrderID {
			order = event.Order
		}
	}
	if order == nil {
		return fmt.Errorf("health submission has no retained reservation")
	}
	marks := incubationMarks(plan.Quotes)
	var report workhealth.Report
	if !order.ReduceOnly {
		at := r.clockNow()
		report = r.admission(ctx, state, at, dailyAdmission(req.Calendar, views, req, at), rec, marks)
		*observed = report
		if !report.Work["entry"].Ready {
			return fmt.Errorf("new_risk_not_admitted: %v", report.Work["entry"].EvidenceRefs)
		}
	}
	var forwardClock func(time.Time) bool
	if !order.ReduceOnly && account.Qualification(state, order.SleeveID) == nil && plan.ForwardDeclarations[order.SleeveID] != "" {
		fs, ok := r.Store.(forward.Store)
		if !ok {
			return fmt.Errorf("forward declaration storage unavailable")
		}
		id := plan.ForwardDeclarations[order.SleeveID]
		record, e := fs.ForwardDeclaration(ctx, id)
		if e != nil {
			return e
		}
		eligibility, e := fs.ForwardEligibility(ctx, id, r.clockNow())
		if e != nil {
			return e
		}
		opened, e := account.ForwardHoldingOpened(state, order.SleeveID, r.clockNow())
		if e != nil {
			return e
		}
		forwardClock = func(at time.Time) bool { return record.Clocks(at, eligibility.SearchTotal, opened).EntryEligible }
	}
	// Repeat owner controls and all hard caps after adapter preparation and after
	// health's potentially slow reads. Automated recovery never clears controls.
	state, err = r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		return err
	}
	if err = account.CheckPortfolioSubmission(state, *order, marks); err != nil {
		return err
	}

	if !order.ReduceOnly {
		live := r.Safety.coverageWithClock(ctx, r.Store, state, r.clockNow, marks)
		if !live.Ready {
			return fmt.Errorf("new_risk_not_admitted: %s", live.Code)
		}
		workers := report.Checks["worker_liveness"]
		if live.ExpiresAt.Before(workers.ExpiresAt) {
			workers.ExpiresAt = live.ExpiresAt
		}
		workers.EvidenceRefs = append(workers.EvidenceRefs, live.EvidenceRefs...)
		report.Checks["worker_liveness"] = workers
		*observed = report
	}
	// One storage fence reads both current owner controls and admission. No
	// further database read may follow this snapshot before the final checks.
	state, err = r.Store.CheckCappedSubmission(ctx, r.BookID, *order, marks)
	if err != nil {
		return err
	}

	if !order.ReduceOnly {
		// The final storage fence can block too. Inspect the worker without I/O before
		// admitting the POST, retaining the earlier forward-deadline bound.
		live := r.Safety.coverageSnapshot(r.Store, state, r.clockNow(), marks)
		if !live.Ready {
			return fmt.Errorf("new_risk_not_admitted: %s", live.Code)
		}
	}
	at := r.clockNow()
	if at.Before(plan.Rebalance.At) || at.Sub(plan.Rebalance.At) > time.Minute {
		return fmt.Errorf("incubation quote expired before submission")
	}
	if err = account.ValidateIncubationQuote(plan.Quotes[o.Symbol], plan.DecidedAt, at); err != nil {
		return err
	}
	if !order.ReduceOnly {
		if forwardClock != nil && !forwardClock(at) {
			return fmt.Errorf("forward eligibility expired before submission")
		}
		workers := report.Checks["worker_liveness"]
		if workers.ExpiresAt.IsZero() || !at.Before(workers.ExpiresAt) || at.Before(workers.At) {
			return fmt.Errorf("new_risk_not_admitted: worker evidence expired")
		}
		if !reconciliationUnchanged(state, rec, plan.Rebalance.ID) {
			return fmt.Errorf("new_risk_not_admitted: reconciliation book changed")
		}
		// Any slow health read must not extend reconciliation or daily-session clocks.
		if rec == nil || rec.err != nil || at.Before(rec.at) || at.Sub(rec.at) > time.Minute || !dailyAdmission(req.Calendar, views, req, at).Ready {
			return fmt.Errorf("new_risk_not_admitted: health clock expired")
		}
	}
	return ctx.Err()
}

func (r IncubationRunner) protectivePlan(state account.CappedState, req IncubationRequest) (account.IncubationPlan, error) {
	p := account.IncubationPlan{InputSHA256: req.InputSHA256, DecidedAt: req.DecidedAt, Quotes: req.Quotes, ForwardDeclarations: account.ForwardBindings(state)}
	p.Rebalance.ID, p.Rebalance.At, p.Rebalance.Limits, p.Rebalance.Fees = "inc-"+req.ID, req.ExecuteAt, req.Limits, req.Fees
	roster, err := account.PortfolioRoster(state)
	if err != nil {
		return p, err
	}
	p.Costs = map[string]executioncost.Model{}
	ids := []string{}
	for id := range roster {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		sleeve := roster[id]
		book, e := incubationStrategyBook(state, id, sleeve.StrategyID, req.DecidedAt)
		if e != nil {
			return p, e
		}
		for _, event := range state.Events {
			if event.Incubation != nil {
				if cost, ok := event.Incubation.Costs[id]; ok {
					p.Costs[id] = cost
				}
			}
		}
		if _, ok := p.Costs[id]; !ok {
			return p, fmt.Errorf("protective cost provenance unavailable")
		}
		p.Rebalance.Sleeves = append(p.Rebalance.Sleeves, sleeve)
		p.Rebalance.Targets = append(p.Rebalance.Targets, allocation.SleeveTarget{SleeveID: id, Decision: allocation.TargetDecision{}})
		p.Decisions = append(p.Decisions, account.IncubationDecision{SleeveID: id, StrategyID: sleeve.StrategyID, State: book.State})
	}
	if len(ids) == 0 {
		return p, fmt.Errorf("protective work requires retained sleeve owners")
	}
	return p, nil
}

// Local receipts after reconciliation may retain this plan, its orders and due
// scheduled funding. Another economic observation invalidates entry admission.
func reconciliationUnchanged(state account.CappedState, rec *incubationReconciliation, allocationID string) bool {
	if rec == nil || rec.version < 0 || rec.version > state.Version {
		return false
	}
	version := int64(0)
	for _, event := range state.Events {
		version += account.CappedCommandCount(event)
		if version <= rec.version {
			continue
		}
		switch event.Kind {
		case "risk_checkpoint", "risk_observe", "risk_control", "deposit", "review_declare", "strategy_review":
		case "incubation_plan", "qualification_plan":
			if event.Incubation == nil || event.Incubation.Rebalance.ID != allocationID {
				return false
			}
		case "rebalance":
			if event.ID != allocationID {
				return false
			}
		case "portfolio_reserve":
			if event.Order == nil || event.Order.AllocationID != allocationID {
				return false
			}
		case "bind", "reject":
			owned := false
			for _, v := range state.Events {
				if v.Order != nil && v.Order.ID == event.OrderID && v.Order.AllocationID == allocationID {
					owned = true
				}
			}
			if !owned {
				return false
			}
		default:
			return false
		}
	}
	return version == state.Version
}
