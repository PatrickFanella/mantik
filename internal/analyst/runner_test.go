package analyst

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	compiled "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

type memoryJournal struct {
	mu       sync.Mutex
	events   map[string][]research.Event
	failKind string
	ackLost  bool
}

func (j *memoryJournal) Lock(context.Context, string) (func(), error) {
	if !j.mu.TryLock() {
		return nil, errors.New("busy")
	}
	return j.mu.Unlock, nil
}
func (j *memoryJournal) Events(_ context.Context, id string) ([]research.Event, error) {
	return append([]research.Event(nil), j.events[id]...), nil
}
func (j *memoryJournal) Append(_ context.Context, id string, seq int, v research.Event) error {
	if seq != len(j.events[id]) {
		return errors.New("sequence")
	}
	if j.failKind == v.Kind && !j.ackLost {
		return errors.New("fixture append failure")
	}
	if j.events == nil {
		j.events = map[string][]research.Event{}
	}
	j.events[id] = append(j.events[id], v)
	if j.failKind == v.Kind {
		return errors.New("fixture lost acknowledgement")
	}
	return nil
}

type caller struct {
	t         *testing.T
	q         Request
	calls     []model.Call
	replies   []string
	failAt    int
	fail      error
	after     func(int)
	transport func(*model.Record)
}

func (c *caller) Call(_ context.Context, call model.Call, limit int) (model.Record, error) {
	c.t.Helper()
	if limit != c.q.Config.DailyLimit || call.Model != c.q.Config.Model || call.Provider != model.Codex || !call.RestrictedTools || call.Timeout != time.Minute {
		c.t.Fatal("call lost frozen bounds", call, limit)
	}
	c.calls = append(c.calls, call)
	n := len(c.calls)
	if n > 4 {
		c.t.Fatal("workflow exceeded fixed call budget")
	}
	if c.after != nil {
		c.after(n)
	}
	raw := `{"action":"long","asset":"SPY"}`
	if n <= len(c.replies) {
		raw = c.replies[n-1]
	}
	r := model.Record{Provider: call.Provider, Model: call.Model, Response: raw, Status: "completed", StartedAt: c.q.AsOf, FinishedAt: c.q.AsOf, PromptSHA256: hash([]byte(call.Prompt)), ResponseSHA256: hash([]byte(raw))}
	if c.transport != nil {
		c.transport(&r)
	}
	if c.failAt == n {
		return r, c.fail
	}
	return r, nil
}

func fixture(t *testing.T, minimum string) (Runner, Request, *storage.Memory, *memoryJournal, *caller) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC().Add(time.Minute)
	c := compiled.Config{Provider: "codex", Model: "fixture-model", Cutoff: "2026-04-30", Source: "https://developers.openai.com/api/docs/models/fixture-model", ReviewedBy: "synthetic-test-owner", ReviewedAt: at.Add(-time.Hour), Assets: []string{"SPY"}, DailyLimit: 10, TimeoutSeconds: 60}
	c.MetadataSHA256 = c.MetadataHash()
	q := Request{ID: "fixture-cycle", ForwardDeclarationID: "fixture-forward", Config: c, AsOf: at, Data: research.Manifest{Source: "in-process synthetic fixture", SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"}}
	day := at.AddDate(0, 0, -1)
	clock := time.Date(day.Year(), day.Month(), day.Day(), 20, 15, 0, 0, time.UTC)
	q.Data.Bars = []storage.DailyBar{{AssetID: "SPY", Symbol: "SPY", Date: day.Format(time.DateOnly), Open: "100", High: "102", Low: "99", Close: "101", VWAP: "100", Volume: 100, PublishedAt: clock, ObservedAt: clock, AvailableAt: clock, Revision: 1}}
	spec, e := c.Spec()
	if e != nil {
		t.Fatal(e)
	}
	sid, _ := spec.ID()
	s := storage.NewMemory()
	if _, e = s.CreateCappedBook(ctx, account.DefaultCappedConfig("fixture-book", "fixture-paper", account.Cash, at.Add(-time.Hour))); e != nil {
		t.Fatal(e)
	}
	g, e := s.DeclareStrategyGate(ctx, strategygate.Declaration{ID: "fixture-gate", Family: "analyst-fixture", StrategyID: sid, Spec: marshal(spec), Policy: "backtest-gate-v1", Benchmark: "cash", Minimum: minimum, Account: "cash", Balance: "2000", DataSHA256: strings.Repeat("b", 64), Assets: c.Assets})
	if e != nil {
		t.Fatal(e)
	}
	h, _ := strategygate.Hash(g.Declaration)
	if _, e = s.RecordStrategyGate(ctx, strategygate.Decision{ID: g.Declaration.ID, StrategyID: sid, DeclarationSHA256: h, FamilySHA256: strings.Repeat("c", 64), Policy: "backtest-gate-v1", Status: "forward_only", Stage: "incubation", EvidenceClass: "forward_only", Minimum: minimum, At: at.Add(-45 * time.Second), Checks: []strategygate.Check{{Code: "forward_only", Skipped: true, Reason: "runtime_model"}}}); e != nil {
		t.Fatal(e)
	}
	bench := strategy.Spec{Name: "cash", Version: "1", Data: []string{strategy.KindDailyBars}}
	bid, _ := bench.ID()
	d := forward.Declaration{ID: q.ForwardDeclarationID, Family: g.Declaration.Family, GateID: g.Declaration.ID, BookID: "fixture-book", SleeveID: "model", StrategyID: sid, Spec: spec, Benchmark: forward.Benchmark{StrategyID: bid, Spec: bench}, WindowStart: at.Add(-10 * time.Second), WindowEnd: at.Add(14 * 24 * time.Hour), OutcomeDeadline: at.Add(42 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 5, MinIndependentDecisions: 5, MinimumNetExcess: "0", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	if _, e = s.DeclareForward(ctx, d, func() time.Time { return at.Add(-30 * time.Second) }); e != nil {
		t.Fatal(e)
	}
	j := &memoryJournal{}
	call := &caller{t: t, q: q}
	return Runner{Store: s, Journal: j, Caller: call, now: func() time.Time { return at }}, q, s, j, call
}

func TestWorkflowAndReplay(t *testing.T) {
	r, q, _, j, c := fixture(t, "1")
	ctx := context.Background()
	out, e := r.Run(ctx, q)
	if e != nil || out.Status != "target" || out.Calls != 4 || out.Target.HoldExposure || out.QualificationCredit || !reflect.DeepEqual(out.Target.Targets, []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}) {
		t.Fatal(out, e)
	}
	for i, call := range c.calls {
		if !strings.Contains(call.Prompt, "Role "+roles[i]) {
			t.Fatal("wrong role")
		}
		if i < 2 && !strings.HasSuffix(call.Prompt, "Prior typed views: null") {
			t.Fatal("independent analyst saw peer output")
		}
		if i >= 2 && !strings.Contains(call.Prompt, `Prior typed views: [{"action":"long","asset":"SPY"}`) {
			t.Fatal("debate/trader missing analyst evidence")
		}
	}
	if len(j.events["analyst-"+q.ID]) != 14 {
		t.Fatal("missing durable evidence", len(j.events["analyst-"+q.ID]))
	}
	again, e := r.Run(ctx, q)
	if e != nil || !reflect.DeepEqual(again, out) || len(c.calls) != 4 {
		t.Fatal("retry relaunched", again, e)
	}
	q.Data.Source = "changed source"
	if _, e = r.Run(ctx, q); e == nil || len(c.calls) != 4 {
		t.Fatal("changed retry admitted")
	}
}

func TestFailuresAreExposureHoldsAndFlatIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		fail      error
		reason    string
	}{
		{"outage", `{"action":"long","asset":"SPY"}`, errors.New("synthetic provider outage"), "model_unavailable"},
		{"daily_limit", `{"action":"long","asset":"SPY"}`, model.ErrBudgetExhausted, "model_unavailable"},
		{"backoff", "", model.ErrBackoff, "model_unavailable"},
		{"malformed", `not json`, nil, "invalid_output"},
		{"quantity", `{"action":"long","asset":"SPY","quantity":"1000"}`, nil, "invalid_output"},
		{"activation", `{"action":"long","asset":"SPY","activate":true}`, nil, "invalid_output"},
		{"research_proposal", `{"tool":"compose_spec","spec":{"name":"new"}}`, nil, "invalid_output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, q, _, _, c := fixture(t, "1")
			c.replies = []string{tc.raw}
			if tc.fail != nil {
				c.failAt = 1
				c.fail = tc.fail
			}
			out, e := r.Run(context.Background(), q)
			if e != nil || out.Status != "no_target" || out.Reason != tc.reason || !out.Target.HoldExposure || allocation.FlatTarget(out.Target) || len(out.Target.Targets) != 0 || len(c.calls) != 1 {
				t.Fatal(out, e)
			}
			again, e := r.Run(context.Background(), q)
			if e != nil || !again.Target.HoldExposure || len(c.calls) != 1 {
				t.Fatal("failure retried", again, e)
			}
		})
	}
	for _, action := range []string{"hold", "flat"} {
		t.Run(action, func(t *testing.T) {
			r, q, _, _, c := fixture(t, "1")
			c.replies = []string{`{"action":"hold","asset":""}`, `{"action":"hold","asset":""}`, `{"action":"hold","asset":""}`, fmt.Sprintf(`{"action":%q,"asset":""}`, action)}
			out, e := r.Run(context.Background(), q)
			if e != nil || out.Target.HoldExposure != (action == "hold") || allocation.FlatTarget(out.Target) != (action == "flat") {
				t.Fatal(out, e)
			}
		})
	}
}

func TestCurrentAdmissionBeforeEachCall(t *testing.T) {
	for _, mode := range []string{"minimum", "pending_family", "new_family_start", "window_expiry", "changed_identity", "budget_revoked_after_call"} {
		t.Run(mode, func(t *testing.T) {
			minimum := "1"
			if mode == "minimum" {
				minimum = "21"
			}
			r, q, s, _, c := fixture(t, minimum)
			ctx := context.Background()
			switch mode {
			case "pending_family":
				if e := s.BeginStrategyTrialBatch(ctx, "pending", "analyst-fixture"); e != nil {
					t.Fatal(e)
				}
			case "new_family_start":
				spec, _ := q.Config.Spec()
				sid, _ := spec.ID()
				if _, e := s.StartStrategyTrial(ctx, strategytrial.Start{ID: "actual-start", Family: "analyst-fixture", StrategyID: sid, Spec: marshal(spec), Config: json.RawMessage(`{"fixture":true}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: q.AsOf}); e != nil {
					t.Fatal(e)
				}
			case "window_expiry":
				expired := q.AsOf.Add(15 * 24 * time.Hour)
				r.now = func() time.Time { return expired }
				q.AsOf = expired
				c.q = q
			case "changed_identity":
				q.Config.Model = "different-model"
				q.Config.MetadataSHA256 = q.Config.MetadataHash()
			case "budget_revoked_after_call":
				c.after = func(n int) {
					if n == 1 {
						if e := s.BeginStrategyTrialBatch(ctx, "pending", "analyst-fixture"); e != nil {
							t.Fatal(e)
						}
					}
				}
			}
			out, e := r.Run(ctx, q)
			if e != nil || out.Status != "no_target" || out.Reason != "admission_unavailable" || !out.Target.HoldExposure {
				t.Fatal(out, e)
			}
			want := 0
			if mode == "budget_revoked_after_call" {
				want = 1
			}
			if len(c.calls) != want {
				t.Fatal("model ran without current admission", len(c.calls))
			}
		})
	}
}

func TestInterruptedCallsNeverRelaunch(t *testing.T) {
	for _, kind := range []string{"call", "reply", "result", "done"} {
		for _, lost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ack-lost-%v", kind, lost), func(t *testing.T) {
				r, q, _, j, c := fixture(t, "1")
				j.failKind, j.ackLost = kind, lost
				if _, e := r.Run(context.Background(), q); e == nil {
					t.Fatal("fixture did not fail")
				}
				calls := len(c.calls)
				j.failKind = ""
				out, e := r.Run(context.Background(), q)
				if e != nil || len(c.calls) != calls {
					t.Fatal("relaunch after ambiguous evidence", out, e, len(c.calls))
				}
				if kind != "done" || !lost {
					if out.Reason != "interrupted_no_relaunch" || !out.Target.HoldExposure {
						t.Fatal(out)
					}
				}
			})
		}
	}
}

func TestCutoffAndEvidenceValidation(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"historical_research":    func(q *Request) { q.Data.EvidenceClass = "historical_market_unqualified" },
		"unknown_cutoff":         func(q *Request) { q.Config.Cutoff = "unknown" },
		"unreviewed_metadata":    func(q *Request) { q.Config.MetadataSHA256 = strings.Repeat("a", 64) },
		"raw_before_cutoff":      func(q *Request) { q.Data.Bars[0].Date = "2026-04-30" },
		"future_revision":        func(q *Request) { q.Data.Bars[0].AvailableAt = q.AsOf.Add(time.Second) },
		"backdated_availability": func(q *Request) { q.Data.Bars[0].AvailableAt = q.Data.Bars[0].ObservedAt.Add(-time.Second) },
		"duplicate_bar":          func(q *Request) { q.Data.Bars = append(q.Data.Bars, q.Data.Bars[0]) },
		"missing_asset":          func(q *Request) { q.Data.Bars = nil },
		"missing_asset_bars":     func(q *Request) { q.Config.Assets = append(q.Config.Assets, "QQQ") },
	} {
		t.Run(name, func(t *testing.T) {
			r, q, _, _, c := fixture(t, "1")
			change(&q)
			if _, e := r.Run(context.Background(), q); e == nil || len(c.calls) != 0 {
				t.Fatal("invalid evidence invoked model", e)
			}
		})
	}
}

func TestTransportFailuresDoNotUseValidLookingStdout(t *testing.T) {
	for name, mutate := range map[string]func(*model.Record){
		"wrong_model":   func(r *model.Record) { r.Model = "other" },
		"failed_status": func(r *model.Record) { r.Status = "failed" },
		"response_hash": func(r *model.Record) { r.ResponseSHA256 = "bad" },
		"prompt_hash":   func(r *model.Record) { r.PromptSHA256 = "bad" },
		"future_finish": func(r *model.Record) { r.FinishedAt = r.StartedAt.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			r, q, _, _, c := fixture(t, "1")
			c.transport = mutate
			out, e := r.Run(context.Background(), q)
			if e != nil || out.Reason != "invalid_transport" || !out.Target.HoldExposure {
				t.Fatal(out, e)
			}
		})
	}
}

func TestFixedSizingAndOutageHoldAfterDeposit(t *testing.T) {
	r, q, _, _, _ := fixture(t, "1")
	out, e := r.Run(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	sleeve := allocation.Sleeve{ID: "model", StrategyID: out.StrategyID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}
	req := allocation.RebalanceRequest{ID: "fixture-sizing", At: q.AsOf, Sleeves: []allocation.Sleeve{sleeve}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: out.Target}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	snap := allocation.SizingSnapshot{Equity: "2000", Marks: map[string]string{"SPY": "100"}}
	dist, e := allocation.Distribute(req, snap)
	if e != nil || dist.Sleeves[0].Budget != "20.000000000" || len(dist.Orders) != 1 || dist.Orders[0].Quantity != "0.200000000" {
		t.Fatal(dist, e)
	}
	// The same model version can never ask for more capital after an outage.
	snap.Equity = "2500"
	snap.Positions = []allocation.SizingPosition{{SleeveID: "model", Symbol: "SPY", Quantity: "0.2"}}
	snap.PreviousTargets = map[string]allocation.TargetDecision{"model": out.Target}
	req.Targets[0].Decision = allocation.TargetDecision{HoldExposure: true}
	dist, e = allocation.Distribute(req, snap)
	if e != nil || len(dist.Orders) != 0 {
		t.Fatal("outage resized or liquidated", dist, e)
	}
	req.Targets[0].Decision = allocation.TargetDecision{}
	dist, e = allocation.Distribute(req, snap)
	if e != nil || len(dist.Orders) != 1 || dist.Orders[0].Side != "sell" || dist.Orders[0].Quantity != "0.200000000" {
		t.Fatal("explicit flat did not exit", dist, e)
	}
}

func TestExistingSubscriptionDailyLimitWithoutLaunchingCLI(t *testing.T) {
	r, q, _, _, _ := fixture(t, "1")
	root := filepath.Join(t.TempDir(), "subscription")
	j, e := model.OpenJournal(root)
	if e != nil {
		t.Fatal(e)
	}
	// This is the adapter's real budget guard. Exhaustion occurs before any
	// process dispatch; no model CLI or provider is contacted by this test.
	raw := marshal(map[string]any{"date": time.Now().UTC().Format(time.DateOnly), "calls": q.Config.DailyLimit})
	if e = os.WriteFile(filepath.Join(root, "budget.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	r.Caller = j
	out, e := r.Run(context.Background(), q)
	if e != nil || out.Reason != "model_unavailable" || !out.Target.HoldExposure || out.Calls != 1 {
		t.Fatal(out, e)
	}
	if _, e = os.Stat(filepath.Join(root, "calls.jsonl")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("exhausted budget reached dispatch", e)
	}
}

func TestCompletionClockAndConcurrentExactRetries(t *testing.T) {
	r, q, _, _, c := fixture(t, "1")
	now := q.AsOf
	r.now = func() time.Time { return now }
	c.after = func(int) { now = now.Add(time.Second) }
	c.transport = func(record *model.Record) { record.FinishedAt = now }
	out, e := r.Run(context.Background(), q)
	if e != nil || !out.Receipt.InputAt.Equal(q.AsOf) || !out.Receipt.At.Equal(q.AsOf.Add(4*time.Second)) || out.Status != "target" {
		t.Fatal(out, e)
	}
	s, e := compiled.New(q.Config, out.Receipt)
	if e != nil {
		t.Fatal(e)
	}
	early, e := view(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = strategy.Decide(s, early); !errors.Is(e, compiled.ErrNoTarget) {
		t.Fatal("model output backdated to input clock", e)
	}
	// A separate runner races only exact retries, protected by the same journal
	// lock. The original four calls cannot be repeated by another caller.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			again, e := r.Run(context.Background(), q)
			if e == nil && !reflect.DeepEqual(again, out) {
				t.Error("retry changed result", again)
			}
		})
	}
	wg.Wait()
	if len(c.calls) != 4 {
		t.Fatal("concurrent retry repeated workflow", len(c.calls))
	}
}

func TestPoolBudgetUsesExistingDistributor(t *testing.T) {
	r, q, _, _, _ := fixture(t, "1")
	out, e := r.Run(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	req := allocation.RebalanceRequest{ID: "fixture-pool", At: q.AsOf}
	for i := range 6 {
		id := fmt.Sprintf("model-%d", i)
		req.Sleeves = append(req.Sleeves, allocation.Sleeve{ID: id, StrategyID: "strategy-" + hash([]byte(id)), Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"})
		req.Targets = append(req.Targets, allocation.SleeveTarget{SleeveID: id, Decision: out.Target})
	}
	req.Limits = map[string]string{"SPY": "100"}
	req.Fees = map[string]string{"SPY": "0"}
	dist, e := allocation.Distribute(req, allocation.SizingSnapshot{Equity: "2000", Marks: map[string]string{"SPY": "100"}})
	if e != nil || len(dist.Sleeves) != 6 || len(dist.Orders) != 6 {
		t.Fatal(dist, e)
	}
	for _, a := range dist.Sleeves {
		if a.Budget != "16.666666667" {
			t.Fatal("5% pool was not shared", a)
		}
	}
}

func TestOwnerRiskControlsPreventModelCalls(t *testing.T) {
	for _, action := range []string{"halt", "kill", "reduce_only", "retire_strategy", "pause_strategy"} {
		t.Run(action, func(t *testing.T) {
			r, q, s, _, c := fixture(t, "1")
			spec, _ := q.Config.Spec()
			sid, _ := spec.ID()
			command := account.BookCommand{ID: "owner-control", Kind: "risk_control", At: q.AsOf, RiskControl: &risk.Control{Action: action, StrategyID: sid}}
			if action == "halt" {
				command.Kind = "halt"
				command.RiskControl = nil
			}
			if _, e := s.ApplyCappedBook(context.Background(), "fixture-book", 0, command); e != nil {
				t.Fatal(e)
			}
			out, e := r.Run(context.Background(), q)
			if e != nil || !out.Target.HoldExposure || out.Reason != "admission_unavailable" || len(c.calls) != 0 {
				t.Fatal(out, e, len(c.calls))
			}
		})
	}
}
