package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

type healthBarSource struct{ change func(*marketdata.Series) }

func (s healthBarSource) AdjustedBars(ctx context.Context, a string, at time.Time) (marketdata.Series, error) {
	v, e := (incubationTestSource{}).AdjustedBars(ctx, a, at)
	if e == nil {
		s.change(&v)
	}
	return v, e
}

type healthUnavailableStore struct{ IncubationStore }

func (s healthUnavailableStore) Snapshot(context.Context) (domain.Snapshot, error) {
	return domain.Snapshot{}, errors.New("fixture outage")
}

type healthReconciliationFailure struct{ *incubationTestBroker }

func (b healthReconciliationFailure) Fills(context.Context, time.Time) ([]json.RawMessage, error) {
	return nil, errors.New("fixture reconciliation unavailable")
}

func TestIncubationHealthAdmissionFailures(t *testing.T) {
	for _, name := range []string{"healthy", "missing-bars", "stale-bars", "future-bars", "zero-availability", "backdated-availability", "missing-asset-quote", "missing-calendar", "future-calendar", "unknown-workers", "dead-worker", "expired-worker", "future-worker", "failed-worker", "reconciliation-unavailable", "owner-pause"} {
		t.Run(name, func(t *testing.T) {
			r, req, _, b := incubationFixture(t, false)
			switch name {
			case "missing-bars":
				req.Strategies[0].Source = healthBarSource{func(v *marketdata.Series) { v.Bars = nil }}
			case "stale-bars":
				req.Strategies[0].Source = healthBarSource{func(v *marketdata.Series) { v.Bars[0].Date = "2020-01-01"; v.Bars[0].Raw.Date = "2020-01-01" }}
			case "future-bars":
				req.Strategies[0].Source = incubationTestSource{future: true}
			case "zero-availability":
				req.Strategies[0].Source = healthBarSource{func(v *marketdata.Series) { v.Bars[0].Raw.AvailableAt = time.Time{} }}
			case "backdated-availability":
				req.Strategies[0].Source = healthBarSource{func(v *marketdata.Series) { v.Bars[0].Raw.AvailableAt = v.Bars[0].Raw.ObservedAt.Add(-time.Second) }}
			case "missing-asset-quote":
				req.Quotes = map[string]account.IncubationQuote{}
			case "missing-calendar":
				req.Calendar = nil
			case "future-calendar":
				req.Calendar.ObservedAt = req.ExecuteAt
			case "unknown-workers":
				r.Store = healthUnavailableStore{r.Store}
			case "reconciliation-unavailable":
				r.Broker = healthReconciliationFailure{b}
			case "owner-pause":
				c := cappedbook.Client{Store: r.Store, Broker: b, BookID: r.BookID}
				if _, e := c.Control(context.Background(), "owner-pause", req.DecidedAt, risk.Control{Action: "reduce_only"}); e != nil {
					t.Fatal(e)
				}
			case "dead-worker", "expired-worker", "future-worker", "failed-worker":
				w := domain.WorkerHealth{Capability: "protective_monitoring", WorkerID: "fixture-worker", At: req.ExecuteAt, IntervalMillis: 100}
				switch name {
				case "dead-worker":
					w.Stopped = true
				case "expired-worker":
					w.At = req.ExecuteAt.Add(-time.Minute)
				case "future-worker":
					w.At = req.ExecuteAt.Add(time.Second)
				case "failed-worker":
					w.ErrorCode = "cycle_failed"
				}
				// Use a fresh store for the older report, avoiding a backwards report write.
				if name == "expired-worker" {
					r.now = func() time.Time { return req.ExecuteAt.Add(30 * time.Second) }
					w.At = req.ExecuteAt
				}
				if e := r.Store.(storage.OperationalStore).ReportWorker(context.Background(), domain.WorkerReport{WorkerHealth: w}); e != nil {
					t.Fatal(e)
				}
			}
			out, e := r.Run(context.Background(), req)
			if name == "healthy" {
				if e != nil || b.posts != 1 || !out.Admission.Work["entry"].Ready {
					t.Fatal(out, e, b.posts)
				}
				for check, c := range out.Admission.Checks {
					if !c.Ready || c.At.IsZero() {
						t.Fatal(check, c)
					}
				}
				if out.Plan.Health == nil || out.Plan.Health.QualificationCredit {
					t.Fatal("health evidence missing or qualified")
				}
			} else if e == nil || b.posts != 0 {
				t.Fatal("unhealthy entry reached adapter", name, out, e, b.posts)
			}
		})
	}
}

func TestIncubationHealthProtectiveExitAndSettlementIndependent(t *testing.T) {
	ctx := context.Background()
	r, req, s, b := incubationFixture(t, false)
	entry, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	o := entry.Distribution.Orders[0]
	b.fill(t, o.ID, "entry-fill", o.Quantity, "100", "0", req.ExecuteAt.Add(time.Second), true)
	if _, e = r.Sync(ctx, req.ExecuteAt.Add(2*time.Second), req.Quotes); e != nil {
		t.Fatal(e)
	}
	next := nextForwardCycle(req, "protective", req.ExecuteAt.Add(3*time.Second))
	next.Protective = true
	next.Strategies = nil // Neither compilation nor missing bars can strand this exit.
	next.Calendar = nil
	next.Limits = map[string]string{"SPY": "99"}
	r.now = func() time.Time { return next.ExecuteAt }
	c := cappedbook.Client{Store: r.Store, Broker: b, BookID: r.BookID}
	if _, e = c.Control(ctx, "kill", next.DecidedAt, risk.Control{Action: "kill"}); e != nil {
		t.Fatal(e)
	}
	if e = r.Store.(storage.OperationalStore).ReportWorker(ctx, domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: "protective_monitoring", WorkerID: "dead-fixture", At: next.ExecuteAt, IntervalMillis: 100, Stopped: true}}); e != nil {
		t.Fatal(e)
	}
	exit, e := r.Run(ctx, next)
	if e != nil || b.posts != 2 || s.calls != 1 || len(exit.Distribution.Orders) != 1 || !exit.Distribution.Orders[0].ReduceOnly {
		t.Fatal(exit, e, b.posts, s.calls)
	}
	if exit.Admission.Work["entry"].Ready || !exit.Admission.Work["exit"].Ready || !exit.Admission.Work["settle"].Ready {
		t.Fatal(exit.Admission)
	}
	closeOrder := exit.Distribution.Orders[0]
	b.fill(t, closeOrder.ID, "exit-fill", closeOrder.Quantity, "100", "0", next.ExecuteAt.Add(time.Second), true)
	syncAt := next.ExecuteAt.Add(2 * time.Second)
	if _, e = r.Sync(ctx, syncAt, next.Quotes); e != nil {
		t.Fatal(e)
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	before, e := account.SnapshotCapped(state, incubationMarks(next.Quotes), syncAt)
	if e != nil {
		t.Fatal(e)
	}
	settlementAt := account.SettlementDate(syncAt).Add(12 * time.Hour)
	later := map[string]account.IncubationQuote{}
	for symbol, q := range next.Quotes {
		q.OccurredAt, q.ReceivedAt = settlementAt, settlementAt
		later[symbol] = q
	}
	if _, e = r.Sync(ctx, settlementAt, later); e != nil {
		t.Fatal(e)
	}
	state, e = r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	after, e := account.SnapshotCapped(state, incubationMarks(later), settlementAt)
	if e != nil {
		t.Fatal(e)
	}
	if before.SettledCash == after.SettledCash || after.SettledCash != after.Cash || !after.Risk.Kill {
		t.Fatal("settlement or owner state changed incorrectly", before, after)
	}
	for _, qty := range after.Positions {
		if qty != "0.000000000" {
			t.Fatal("exit left exposure", after)
		}
	}
}

func TestIncubationHealthReadinessAuthenticationAndUnavailableEvidence(t *testing.T) {
	r, req, _, _ := incubationFixture(t, false)
	if _, e := r.Run(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	h := HandlerWithClock(r.Store.(*storage.Memory), "health-token", fixedHealthClock{req.ExecuteAt})
	path := "/v1/incubation/" + r.BookID + "/readiness"
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", path, nil))
	if unauth.Code != 401 {
		t.Fatal(unauth.Code)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Authorization", "Bearer health-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	var v struct {
		Admission workhealth.Report
		Retained  *workhealth.Report `json:"retained_cycle_evidence"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &v) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if v.Admission.Work["entry"].Ready || v.Admission.Checks["reconciliation"].Code != "owned_reconciliation_unavailable" || v.Retained == nil || !v.Retained.Work["entry"].Ready {
		t.Fatal(response.Body.String())
	}
	for _, class := range []string{"reduce", "exit", "reconcile", "settle"} {
		if !v.Admission.Work[class].Ready {
			t.Fatal(class, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "qualification_credit\":true") {
		t.Fatal(response.Body.String())
	}
}

type fixedHealthClock struct{ at time.Time }

func (c fixedHealthClock) Now() time.Time { return c.at }

func TestIncubationHealthUnknownReportsAndRestartFailClosed(t *testing.T) {
	r, req, _, _ := incubationFixture(t, false)
	state, e := r.Store.CappedBook(context.Background(), r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	report := r.admission(context.Background(), state, req.ExecuteAt, healthCheck(true, "fixture_data", req.ExecuteAt), nil, incubationMarks(req.Quotes))
	if report.Checks["risk_brake"].Ready || report.Checks["reconciliation"].Ready || report.Work["entry"].Ready {
		t.Fatal(report)
	}
	// A durable successful plan is not a live reconciliation proof on restart.
	if _, e = r.Run(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	state, e = r.Store.CappedBook(context.Background(), r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	plan := state.Events[1].Incubation
	if plan == nil || plan.Health == nil {
		t.Fatal("missing retained report")
	}
	report = r.admission(context.Background(), state, req.ExecuteAt.Add(4*time.Minute), plan.Health.Checks["data_freshness"], nil, incubationMarks(req.Quotes))
	if report.Work["entry"].Ready || report.Checks["worker_liveness"].Ready {
		t.Fatal(report)
	}
	// A new real worker cycle revives liveness only; it cannot renew reconciliation.
	healthyIncubationWorkers(t, r, req.ExecuteAt.Add(4*time.Minute))
	report = r.admission(context.Background(), state, req.ExecuteAt.Add(4*time.Minute), plan.Health.Checks["data_freshness"], nil, incubationMarks(req.Quotes))
	if !report.Checks["worker_liveness"].Ready || report.Work["entry"].Ready {
		t.Fatal(report)
	}
}

var _ strategy.BarSource = healthBarSource{}

func (c fixedHealthClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

type warmupHealthStrategy struct{ *incubationTestStrategy }

func (s warmupHealthStrategy) Spec() strategy.Spec {
	v := s.incubationTestStrategy.Spec()
	v.Warmup = 2
	return v
}

type sessionHealthSource struct{ bars map[string][]storage.DailyBar }

func (s sessionHealthSource) AdjustedBars(_ context.Context, asset string, at time.Time) (marketdata.Series, error) {
	return marketdata.Adjust(asset, s.bars[asset], nil, at)
}

func TestIncubationHealthDailySessionsWarmupAndEveryAsset(t *testing.T) {
	// Thanksgiving, a Friday early close and the weekend must not make daily
	// closes look stale merely because they are several calendar days old.
	for _, name := range []string{"holiday-weekend-healthy", "missing-warmup", "second-asset-missing", "second-quote-missing", "future-quote", "bad-receipt", "unfinished-daily-bar"} {
		t.Run(name, func(t *testing.T) {
			at := time.Date(2026, 11, 30, 15, 0, 0, 0, time.UTC)
			r, req, s, _ := incubationFixture(t, false)
			_ = r
			req.DecidedAt, req.ExecuteAt = at.Add(-time.Second), at
			sessions := []alpaca.Session{}
			for _, d := range []struct{ day, hour int }{{25, 21}, {27, 18}, {30, 21}} {
				sessions = append(sessions, alpaca.Session{Date: time.Date(2026, 11, d.day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), OpenAt: time.Date(2026, 11, d.day, 14, 30, 0, 0, time.UTC), CloseAt: time.Date(2026, 11, d.day, d.hour, 0, 0, 0, time.UTC)})
			}
			req.Calendar = &IncubationCalendar{From: "2026-11-25", Through: "2026-11-30", SourceSHA256: strings.Repeat("a", 64), ObservedAt: at.Add(-time.Minute), Sessions: sessions}
			source := sessionHealthSource{bars: map[string][]storage.DailyBar{}}
			req.Strategies[0].Strategy = warmupHealthStrategy{s}
			req.Strategies[0].Assets = []string{"SPY", "QQQ"}
			for _, asset := range req.Strategies[0].Assets {
				q := req.Quotes["SPY"]
				q.Symbol = asset
				q.OccurredAt, q.ReceivedAt = at, at
				req.Quotes[asset] = q
				for _, session := range sessions[:2] {
					available := session.CloseAt.Add(alpaca.RecentSIPDelay)
					source.bars[asset] = append(source.bars[asset], storage.DailyBar{AssetID: asset, Symbol: asset, Date: session.Date, Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", ObservedAt: available, PublishedAt: available, AvailableAt: available})
				}
			}
			switch name {
			case "missing-warmup":
				source.bars["SPY"] = source.bars["SPY"][1:]
			case "second-asset-missing":
				source.bars["QQQ"] = nil
			case "second-quote-missing":
				delete(req.Quotes, "QQQ")
			case "future-quote":
				q := req.Quotes["QQQ"]
				q.OccurredAt, q.ReceivedAt = at.Add(time.Second), at.Add(time.Second)
				req.Quotes["QQQ"] = q
			case "bad-receipt":
				q := req.Quotes["QQQ"]
				q.ReceivedAt = at.Add(-time.Second)
				req.Quotes["QQQ"] = q
			case "unfinished-daily-bar":
				b := source.bars["QQQ"][1]
				b.Date = "2026-11-30"
				source.bars["QQQ"] = append(source.bars["QQQ"], b)
			}
			view, e := strategy.Build(context.Background(), req.Strategies[0].Strategy.Spec(), source, req.DecidedAt, req.Strategies[0].Assets, strategy.Book{})
			if e != nil {
				t.Fatal(e)
			}
			c := dailyAdmission(req.Calendar, []strategy.View{view}, req, at)
			if c.Ready != (name == "holiday-weekend-healthy") {
				t.Fatal(name, c)
			}
		})
	}
}

type boundaryHealthStore struct {
	IncubationStore
	afterSnapshot func(context.Context)
	armed         bool
}

func (s *boundaryHealthStore) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	snapshot, e := s.IncubationStore.Snapshot(ctx)
	if s.armed {
		s.armed = false
		s.afterSnapshot(ctx)
	}
	return snapshot, e
}

type boundaryHealthBroker struct {
	*incubationTestBroker
	prepare func()
}

func (b boundaryHealthBroker) SubmitChecked(ctx context.Context, o alpaca.StockOrder, check func() error) (alpaca.PaperOrder, error) {
	b.prepare()
	if e := check(); e != nil {
		return alpaca.PaperOrder{}, e
	}
	return b.incubationTestBroker.Submit(ctx, o)
}

func TestIncubationHealthSlowSnapshotCannotExtendLivenessOrHideOwnerPause(t *testing.T) {
	for _, name := range []string{"worker-expiry", "owner-pause"} {
		t.Run(name, func(t *testing.T) {
			r, req, _, b := incubationFixture(t, false)
			at := req.ExecuteAt
			r.now = func() time.Time { return at }
			if e := r.Store.(storage.OperationalStore).ReportWorker(context.Background(), domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: "protective_monitoring", WorkerID: "short-heartbeat", At: at, IntervalMillis: 100}}); e != nil {
				t.Fatal(e)
			}
			store := &boundaryHealthStore{IncubationStore: r.Store}
			store.afterSnapshot = func(ctx context.Context) {
				if name == "worker-expiry" {
					at = at.Add(11 * time.Second)
					return
				}
				state, e := store.CappedBook(ctx, r.BookID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "late-owner-pause", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "reduce_only"}}); e != nil {
					t.Fatal(e)
				}
			}
			r.Store = store
			r.Safety = startIncubationTestSafety(t, r, req.DecidedAt)
			r.Broker = boundaryHealthBroker{incubationTestBroker: b, prepare: func() { store.armed = true }}
			_, e := r.Run(context.Background(), req)
			if e == nil || b.posts != 0 {
				t.Fatal("stale snapshot authorized POST", e, b.posts)
			}
			want := "worker evidence expired"
			if name == "owner-pause" {
				want = "risk_reduce_only"
			}
			if !strings.Contains(e.Error(), want) {
				t.Fatal(e)
			}
		})
	}
}
