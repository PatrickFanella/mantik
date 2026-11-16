package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	compiled "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

type analystBridgeJournal struct{ events map[string][]research.Event }

func (j *analystBridgeJournal) Lock(context.Context, string) (func(), error) { return func() {}, nil }
func (j *analystBridgeJournal) Events(_ context.Context, id string) ([]research.Event, error) {
	return j.events[id], nil
}
func (j *analystBridgeJournal) Append(_ context.Context, id string, seq int, e research.Event) error {
	if seq != len(j.events[id]) {
		return errors.New("sequence")
	}
	j.events[id] = append(j.events[id], e)
	return nil
}

type analystBridgeCaller struct {
	action string
	calls  int
	fail   bool
}

func (c *analystBridgeCaller) Call(_ context.Context, call model.Call, _ int) (model.Record, error) {
	c.calls++
	if c.fail {
		return model.Record{}, errors.New("fixture outage")
	}
	asset := ""
	if c.action == "long" {
		asset = "SPY"
	}
	response := fmt.Sprintf(`{"action":%q,"asset":%q}`, c.action, asset)
	return model.Record{Status: "completed", Provider: call.Provider, Model: call.Model, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), PromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(call.Prompt))), ResponseSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(response))), Response: response}, nil
}

func analystBridgeFixture(t *testing.T) (IncubationRunner, analyst.Runner, analyst.Request, *analystBridgeJournal, *analystBridgeCaller, *incubationTestBroker) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	c := compiled.Config{Provider: "codex", Model: "fixture-model", Cutoff: "2026-04-30", Source: "https://developers.openai.com/api/docs/models/fixture-model", ReviewedBy: "synthetic-fixture", ReviewedAt: at.Add(-time.Hour), Assets: []string{"SPY"}, DailyLimit: 10, TimeoutSeconds: 60}
	c.MetadataSHA256 = c.MetadataHash()
	spec, _ := c.Spec()
	sid, _ := spec.ID()
	raw, _ := json.Marshal(spec)
	s := storage.NewMemory()
	_, err := s.CreateCappedBook(ctx, account.DefaultCappedConfig("analyst-book", "paper-fixture", account.Cash, at.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.DeclareStrategyGate(ctx, strategygate.Declaration{ID: "analyst-gate", Family: "analyst-bridge", StrategyID: sid, Spec: raw, Policy: "backtest-gate-v1", Benchmark: "cash", Minimum: "1", Account: "cash", Balance: "2000", DataSHA256: strings.Repeat("a", 64), Assets: c.Assets})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := strategygate.Hash(g.Declaration)
	_, err = s.RecordStrategyGate(ctx, strategygate.Decision{ID: g.Declaration.ID, StrategyID: sid, DeclarationSHA256: h, FamilySHA256: strings.Repeat("b", 64), Policy: g.Declaration.Policy, Status: "forward_only", Stage: "incubation", EvidenceClass: "forward_only", Minimum: "1", At: g.DeclaredAt, Checks: []strategygate.Check{{Code: "fixture", Skipped: true, Reason: "runtime_model"}}})
	if err != nil {
		t.Fatal(err)
	}
	bench := strategy.Spec{Name: "cash", Version: "1", Data: []string{strategy.KindDailyBars}}
	bid, _ := bench.ID()
	declared := time.Now().UTC()
	fd := forward.Declaration{ID: "analyst-forward", Family: g.Declaration.Family, GateID: g.Declaration.ID, BookID: "analyst-book", SleeveID: "model", StrategyID: sid, Spec: spec, Benchmark: forward.Benchmark{StrategyID: bid, Spec: bench}, WindowStart: declared.Add(time.Nanosecond), WindowEnd: declared.Add(14 * 24 * time.Hour), OutcomeDeadline: declared.Add(42 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 5, MinIndependentDecisions: 5, MinimumNetExcess: "0", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	if _, err = s.DeclareForward(ctx, fd, func() time.Time { return declared }); err != nil {
		t.Fatal(err)
	}
	day := at.AddDate(0, 0, -1)
	clock := time.Date(day.Year(), day.Month(), day.Day(), 21, 15, 0, 0, time.UTC)
	q := analyst.Request{ID: "bridge-cycle", ForwardDeclarationID: fd.ID, Config: c, AsOf: time.Now().UTC(), Data: research.Manifest{Source: "in-process prospective contract fixture", SourceSHA256: strings.Repeat("c", 64), EvidenceClass: "prospective_market_unqualified", Bars: []storage.DailyBar{{AssetID: "SPY", Symbol: "SPY", Date: day.Format(time.DateOnly), Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 100, PublishedAt: clock, ObservedAt: clock, AvailableAt: clock, Revision: 1}}}}
	j := &analystBridgeJournal{events: map[string][]research.Event{}}
	caller := &analystBridgeCaller{action: "long"}
	b := &incubationTestBroker{orders: map[string]alpaca.PaperOrder{}}
	return IncubationRunner{Store: s, Broker: b, BookID: fd.BookID}, analyst.Runner{Store: s, Journal: j, Caller: caller}, q, j, caller, b
}

func analystBridgeRequest(t *testing.T, q analyst.Request, result analyst.Result, at time.Time) IncubationRequest {
	t.Helper()
	s, err := compiled.New(q.Config, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	req := nextForwardCycle(IncubationRequest{}, q.ID, at)
	req.Strategies = []IncubatingStrategy{{SleeveID: "model", GateID: "analyst-gate", ForwardDeclarationID: q.ForwardDeclarationID, Risk: "1", Strategy: s, Source: q.Data, Assets: q.Config.Assets}}
	req.Limits = map[string]string{"SPY": "101"}
	req.Fees = map[string]string{"SPY": "1"}
	return req
}

func TestIncubationAnalystRetainedBridge(t *testing.T) {
	for _, mode := range []string{"long", "hold", "outage", "flat", "expired", "synthetic", "unclassified", "binding", "early", "no_worker"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, ar, q, j, c, b := analystBridgeFixture(t)
			if _, err := ar.Run(ctx, q); err != nil {
				t.Fatal(err)
			}
			original, result, err := analyst.Retained(ctx, j, q.ID)
			if err != nil {
				t.Fatal(err)
			}
			followRequest, followResult := original, result
			if mode == "hold" || mode == "outage" || mode == "flat" {
				followRequest.ID = "follow-cycle"
				c.action = mode
				c.fail = mode == "outage"
				if _, err = ar.Run(ctx, followRequest); err != nil {
					t.Fatal(err)
				}
				followRequest, followResult, err = analyst.Retained(ctx, j, followRequest.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := analystBridgeRequest(t, original, result, result.Receipt.At.Add(time.Second))
			r.now = func() time.Time { return req.ExecuteAt }
			r.Safety = startIncubationTestSafety(t, r, req.DecidedAt)
			if mode == "synthetic" || mode == "unclassified" || mode == "binding" || mode == "early" || mode == "no_worker" {
				receipt := result.Receipt
				switch mode {
				case "synthetic":
					receipt.EvidenceClass = "synthetic_fixture"
				case "unclassified":
					receipt.EvidenceClass = ""
				case "binding":
					req.Strategies[0].ForwardDeclarationID = "other-forward"
				case "early":
					req = analystBridgeRequest(t, original, result, result.Receipt.At.Add(-time.Nanosecond))
				case "no_worker":
					r.Safety = nil
				}
				if mode == "synthetic" || mode == "unclassified" {
					adapter, e := compiled.New(q.Config, receipt)
					if e != nil {
						return
					}
					req.Strategies[0].Strategy = adapter
				}
				if _, err = r.Run(ctx, req); err == nil || b.posts != 0 {
					t.Fatal("invalid bridge admitted", err, b.posts)
				}
				return
			}
			entry, err := r.Run(ctx, req)
			if err != nil || b.posts != 1 || len(entry.Orders) != 1 {
				t.Fatal("entry", err, b.posts, entry.Admission)
			}
			var provenance compiled.Receipt
			if json.Unmarshal(entry.Plan.Decisions[0].State, &provenance) != nil || !reflect.DeepEqual(provenance, result.Receipt) || entry.Plan.ForwardDeclarations["model"] != q.ForwardDeclarationID || entry.QualificationCredit {
				t.Fatal("provenance lost", entry.Plan)
			}
			calls := c.calls
			if _, err = r.Run(ctx, req); err != nil || c.calls != calls || b.posts != 1 {
				t.Fatal("retry relaunched", err, c.calls, b.posts)
			}
			if mode == "long" {
				return
			}
			order := entry.Distribution.Orders[0]
			filled := req.ExecuteAt.Add(time.Second)
			b.fill(t, order.ID, "bridge-fill", order.Quantity, "100", "0", filled, true)
			if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
				t.Fatal(err)
			}
			state, _ := r.Store.CappedBook(ctx, r.BookID)
			nextAt := filled.Add(3 * time.Second)
			if mode != "flat" {
				due, _ := state.Config.DueDeposits(q.AsOf.Add(8 * 24 * time.Hour))
				if len(due) == 0 {
					t.Fatal("fixture deposit missing")
				}
				depositAt := due[0].EffectiveAt
				commands, _ := state.Config.DueDeposits(depositAt)
				for _, cmd := range commands {
					state, err = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, cmd)
					if err != nil {
						t.Fatal(err)
					}
				}
				nextAt = depositAt.Add(time.Second)
				snap, e := account.SnapshotCapped(state, map[string]string{"SPY": "100"}, nextAt)
				if e != nil || snap.Deposits != 1 || snap.Positions["SPY"] != order.Quantity {
					t.Fatal("deposit setup", snap, e)
				}
			}
			next := analystBridgeRequest(t, followRequest, followResult, nextAt)
			if mode == "flat" {
				next.Limits["SPY"] = "99"
			}
			next.ID = "follow-execution"
			next.InputSHA256 = incubationDigest(next.ID)
			r.now = func() time.Time { return next.ExecuteAt }
			out, err := r.Run(ctx, next)
			if err != nil {
				t.Fatal("post-deposit", err)
			}
			if mode == "flat" {
				if len(out.Distribution.Orders) != 1 || out.Distribution.Orders[0].Side != "sell" || !out.Distribution.Orders[0].ReduceOnly || out.Distribution.Orders[0].Quantity != order.Quantity {
					t.Fatal("flat lost actual quantity", out)
				}
			} else if len(out.Distribution.Orders) != 0 || !out.Plan.Rebalance.Targets[0].Decision.HoldExposure || b.posts != 1 {
				t.Fatal("hold changed exposure", out, b.posts)
			}
			var retainedReceipt compiled.Receipt
			if json.Unmarshal(out.Plan.Decisions[0].State, &retainedReceipt) != nil || !reflect.DeepEqual(retainedReceipt, followResult.Receipt) {
				t.Fatal("continuation rewrote provenance")
			}
			if mode != "flat" {
				current, err := r.Store.CappedBook(ctx, r.BookID)
				if err != nil {
					t.Fatal(err)
				}
				snap, err := account.SnapshotCapped(current, map[string]string{"SPY": "100"}, next.ExecuteAt)
				if err != nil || snap.Positions["SPY"] != order.Quantity || snap.Deposits != 1 {
					t.Fatal("hold changed retained quantities", snap, err)
				}
			}
			if c.calls != calls {
				t.Fatal("reader/bridge called model")
			}
		})
	}
}

func TestIncubationAnalystBudgetPreviewAppliesCurrentReviews(t *testing.T) {
	ctx := context.Background()
	r, _, base := reviewFixture(t, false)
	in := reviewObservation(base.At.Add(24*time.Hour), "93.5", base.At)
	review, err := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "budget-cut", in)
	if err != nil || review.Scale != "0.5" {
		t.Fatal(review, err)
	}
	state, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(state)
	roster, err := account.PortfolioRoster(state)
	if err != nil {
		t.Fatal(err)
	}
	req := allocation.RebalanceRequest{ID: "current-budget", At: in.At}
	for _, owner := range roster {
		req.Sleeves = append(req.Sleeves, owner)
	}
	budgets, err := account.PreviewPortfolioBudgets(state, req, in.Mark.Prices, in.At)
	if err != nil || len(budgets) != 1 {
		t.Fatal(budgets, err)
	}
	budget, _ := new(big.Rat).SetString(budgets[0].Budget)
	ceiling, _ := new(big.Rat).SetString(review.BudgetCeiling)
	if budget.Sign() <= 0 || budget.Cmp(ceiling) > 0 || budget.Cmp(big.NewRat(50, 1)) >= 0 {
		t.Fatal("same-clock review ignored", budgets, review, err)
	}
	after, _ := json.Marshal(state)
	if string(before) != string(after) {
		t.Fatal("preview changed history")
	}
}
