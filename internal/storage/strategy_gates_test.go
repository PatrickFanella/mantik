package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/migrations"
	"time"
)

func gateStorageContracts(t *testing.T, s strategygate.Store) {
	t.Helper()
	ctx := context.Background()
	raw := json.RawMessage(`{"name":"model","version":"1","params":{},"data":["daily_bars_adjusted"],"warmup":0,"model_driven":true}`)
	h := sha256.Sum256(raw)
	d := strategygate.Declaration{Account: "cash", ID: "retained-gate", Family: "retained", StrategyID: "strategy-" + hex.EncodeToString(h[:]), Spec: raw, Balance: "2000", Minimum: "1", Benchmark: "cash", Policy: "backtest-gate-v1", DataSHA256: strings.Repeat("a", 64)}
	r, e := s.DeclareStrategyGate(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.DeclareStrategyGate(ctx, d)
	if e != nil || !again.DeclaredAt.Equal(r.DeclaredAt) {
		t.Fatal("declaration retry changed clock", again, e)
	}
	changed := d
	changed.Balance = "100000"
	if _, e = s.DeclareStrategyGate(ctx, changed); !errors.Is(e, strategygate.ErrConflict) {
		t.Fatal("declaration changed", e)
	}
	dh, e := strategygate.Hash(d)
	if e != nil {
		t.Fatal(e)
	}
	decision := strategygate.Decision{ID: d.ID, StrategyID: d.StrategyID, DeclarationSHA256: dh, FamilySHA256: strings.Repeat("b", 64), Policy: d.Policy, EvidenceClass: "forward_only", Status: "forward_only", Stage: "incubation", Minimum: "1", At: r.DeclaredAt, Checks: []strategygate.Check{{Code: "model", Skipped: true, Reason: "forward_only_fixture"}}}
	r, e = s.RecordStrategyGate(ctx, decision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordStrategyGate(ctx, decision); e != nil {
		t.Fatal("decision retry conflicted", e)
	}
	changedDecision := decision
	changedDecision.Minimum = "2"
	if _, e = s.RecordStrategyGate(ctx, changedDecision); !errors.Is(e, strategygate.ErrConflict) {
		t.Fatal("decision changed", e)
	}
	r.Decision.Checks[0].Reason = "mutated"
	r.Declaration.Spec[0] = '['
	r, e = s.StrategyGate(ctx, d.ID)
	if e != nil || r.Decision.Checks[0].Reason == "mutated" || r.Declaration.Spec[0] != '{' {
		t.Fatal("reader mutated history", r, e)
	}
	expectedHash, e := strategygate.Hash(r)
	if e != nil {
		t.Fatal(e)
	}
	invalidDecision := decision
	invalidDecision.DeclarationSHA256 = strings.Repeat("c", 64)
	syntheticDecision := decision
	syntheticDecision.EvidenceClass = "synthetic_fixture"
	legacyDecision := decision
	legacyDecision.EvidenceClass = ""
	for _, tc := range []struct {
		name string
		call func() (strategygate.Record, error)
		want error
	}{
		{"declaration conflict", func() (strategygate.Record, error) { return s.DeclareStrategyGate(ctx, changed) }, strategygate.ErrConflict},
		{"decision conflict", func() (strategygate.Record, error) { return s.RecordStrategyGate(ctx, changedDecision) }, strategygate.ErrConflict},
		{"invalid decision", func() (strategygate.Record, error) { return s.RecordStrategyGate(ctx, invalidDecision) }, strategygate.ErrInvalid},
		{"synthetic admission", func() (strategygate.Record, error) { return s.RecordStrategyGate(ctx, syntheticDecision) }, strategygate.ErrInvalid},
		{"unclassified admission", func() (strategygate.Record, error) { return s.RecordStrategyGate(ctx, legacyDecision) }, strategygate.ErrInvalid},
	} {
		t.Run(tc.name+" returns detached history", func(t *testing.T) {
			got, err := tc.call()
			if !errors.Is(err, tc.want) || got.Decision == nil {
				t.Fatal("missing retained error result", got, err)
			}
			originalSpec := got.Declaration.Spec[0]
			originalStatus, originalReason := got.Decision.Status, got.Decision.Checks[0].Reason
			defer func() {
				got.Declaration.Spec[0] = originalSpec
				got.Decision.Status = originalStatus
				got.Decision.Checks[0].Reason = originalReason
			}()
			got.Declaration.Spec[0] = '['
			got.Decision.Status = "passed"
			got.Decision.Checks[0].Reason = "mutated through error result"
			retained, err := s.StrategyGate(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			actualHash, err := strategygate.Hash(retained)
			if err != nil || actualHash != expectedHash {
				t.Fatal("error result mutated retained gate", retained, err)
			}
		})
	}
}

func TestMemoryStrategyGates(t *testing.T) { gateStorageContracts(t, NewMemory()) }
func TestPostgresContractsStrategyGates(t *testing.T) {
	p := timerPostgres(t, "strategy_gates")
	gateStorageContracts(t, p)
	portfolioJournalContracts(t, p)
	portfolioFamilyFenceContracts(t, p)
	ctx := context.Background()
	if _, e := p.pool.Exec(ctx, migrations.StrategyGates); e != nil {
		t.Fatal("migration replay", e)
	}
	for _, table := range []string{"mantik_strategy_gates", "mantik_strategy_gate_decisions"} {
		for _, sql := range []string{"DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			if _, e := p.pool.Exec(ctx, sql); e == nil {
				t.Fatal("gate history mutable", sql)
			}
		}
	}
	q, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	r, e := q.StrategyGate(ctx, "retained-gate")
	if e != nil || r.Decision == nil || r.Decision.Status != "forward_only" {
		t.Fatal("restart lost decision", r, e)
	}
}

func portfolioFamilyFenceContracts(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	g, e := p.StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.StartStrategyTrial(ctx, strategytrial.Start{ID: "new-family-attempt", Family: g.Declaration.Family, StrategyID: g.Declaration.StrategyID, Spec: g.Declaration.Spec, Config: json.RawMessage(`{"additional":true}`), DataSHA256: g.Declaration.DataSHA256, EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
	if e != nil {
		t.Fatal(e)
	}
	state, e := p.CappedBook(ctx, "risk-book")
	if e != nil {
		t.Fatal(e)
	}
	at := state.Config.OpenedAt
	marks := map[string]string{"SPY": "100"}
	req := *state.Events[0].Rebalance
	req.ID = "stale-rebalance"
	if _, e = p.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks}); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") {
		t.Fatal("SQL rebalance accepted stale search", e)
	}
	control := risk.Control{Action: "unkill"}
	state, e = p.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "unkill", Kind: "risk_control", At: at, RiskControl: &control, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, marks, at)
	if e != nil {
		t.Fatal(e)
	}
	v := snap.Distribution.Orders[0]
	o := account.BookOrder{ReduceOnly: v.ReduceOnly, ID: v.ID, AllocationID: v.AllocationID, SleeveID: v.SleeveID, StrategyID: v.StrategyID, Symbol: v.Symbol, Quantity: v.Quantity, Limit: v.Limit, Side: v.Side, Fee: v.Fee, At: v.At}
	if _, e = p.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "reserve-" + o.ID, Kind: "portfolio_reserve", At: at, Order: &o, Marks: marks}); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") {
		t.Fatal("SQL reservation accepted stale search", e)
	}
}

func portfolioJournalContracts(t *testing.T, s CappedBookStore) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, e := s.CreateCappedBook(ctx, account.DefaultCappedConfig("risk-book", "risk-account", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	gate, e := s.(strategygate.Store).StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	sid := gate.Declaration.StrategyID
	req := allocation.RebalanceRequest{ID: "rebalance", At: at, Sleeves: []allocation.Sleeve{{ID: "trend", StrategyID: sid, Stage: "forward_only", ModelDriven: true, GateID: "retained-gate", Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	marks := map[string]string{"SPY": "100"}
	state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	control := risk.Control{Action: "kill"}
	state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "kill", Kind: "risk_control", At: at, RiskControl: &control, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	retained, e := s.CappedBook(ctx, state.Config.ID)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(retained, marks, at)
	if e != nil || !snap.Risk.Kill || snap.Distribution == nil {
		t.Fatal("risk/distribution journal lost", snap, e)
	}
	v := snap.Distribution.Orders[0]
	o := account.BookOrder{ReduceOnly: v.ReduceOnly, ID: v.ID, AllocationID: v.AllocationID, SleeveID: v.SleeveID, StrategyID: v.StrategyID, Symbol: v.Symbol, Quantity: v.Quantity, Limit: v.Limit, Side: v.Side, Fee: v.Fee, At: v.At}
	for _, class := range []string{"", "synthetic_fixture"} {
		for _, cmd := range []account.BookCommand{
			{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks},
			{ID: "reserve-" + o.ID, Kind: "portfolio_reserve", At: at, Order: &o, Marks: marks},
		} {
			read := func(string) (strategygate.Record, error) {
				g, err := s.(strategygate.Store).StrategyGate(ctx, "retained-gate")
				if err == nil {
					g.Decision.EvidenceClass = class
				}
				return g, err
			}
			if err := checkPortfolioAdmissions(state, cmd, read, func(string) (int64, error) { return 0, nil }); err == nil || !strings.Contains(err.Error(), "gate_evidence_ineligible") {
				t.Fatal("transactional admission accepted ineligible evidence", class, cmd.Kind, err)
			}
		}
	}
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "reserve-" + o.ID, Kind: "portfolio_reserve", At: at, Order: &o, Marks: marks}); e == nil || !strings.Contains(e.Error(), "risk_kill_switch") {
		t.Fatal("retained kill bypassed", e)
	}
	control.Action = "resume"
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "kill", Kind: "risk_control", At: at, RiskControl: &control, Marks: marks}); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed owner command accepted", e)
	}
}
