package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/backtest"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

type batchFaults struct {
	Trials
	start, failStart                                           int
	startAck, failReserve, reserveAck, failResolve, resolveAck bool
	failBegin, beginAck                                        bool
}

func (s *batchFaults) BeginStrategyTrialBatch(ctx context.Context, id, family string) error {
	if s.failBegin {
		return ErrInterrupted
	}
	e := s.Trials.BeginStrategyTrialBatch(ctx, id, family)
	if e == nil && s.beginAck {
		return ErrInterrupted
	}
	return e
}

type observedCaller struct {
	Caller
	before func()
}

func (c observedCaller) Call(ctx context.Context, call model.Call, budget int) (model.Record, error) {
	c.before()
	return c.Caller.Call(ctx, call, budget)
}

func (s *batchFaults) StartStrategyTrial(ctx context.Context, b strategytrial.Start) (bool, error) {
	s.start++
	if s.start == s.failStart && !s.startAck {
		return false, ErrInterrupted
	}
	added, e := s.Trials.StartStrategyTrial(ctx, b)
	if e == nil && s.start == s.failStart {
		return false, ErrInterrupted
	}
	return added, e
}
func (s *batchFaults) ReserveStrategyTrialBatch(ctx context.Context, b strategytrial.Batch) error {
	if s.failReserve {
		return ErrInterrupted
	}
	e := s.Trials.ReserveStrategyTrialBatch(ctx, b)
	if e == nil && s.reserveAck {
		return ErrInterrupted
	}
	return e
}
func (s *batchFaults) ResolveStrategyTrialBatch(ctx context.Context, id string) error {
	if s.failResolve {
		return ErrInterrupted
	}
	e := s.Trials.ResolveStrategyTrialBatch(ctx, id)
	if e == nil && s.resolveAck {
		return ErrInterrupted
	}
	return e
}

type appendFault struct {
	Journal
	kind string
	ack  bool
}

func (j *appendFault) Append(ctx context.Context, id string, seq int, ev Event) error {
	if ev.Kind != j.kind {
		return j.Journal.Append(ctx, id, seq, ev)
	}
	if j.ack {
		if e := j.Journal.Append(ctx, id, seq, ev); e != nil {
			return e
		}
	}
	return ErrInterrupted
}

type batchBackend interface {
	strategygate.Store
	Trials
	strategytrial.FamilyReader
}

func batchRecoveryContracts(t *testing.T, s batchBackend, newJournal func() Journal) {
	t.Helper()
	for _, phase := range []string{"begin_write", "begin_ack", "first_start", "second_start", "third_start", "start_ack", "reply_write", "reply_ack", "reserve_write", "reserve_ack", "resolve_write", "resolve_ack", "result_write"} {
		t.Run(phase, func(t *testing.T) {
			s := s
			ctx := context.Background()
			d := fixture()
			d.ID = fmt.Sprintf("batch-%s-%d", phase, time.Now().UnixNano())
			d.Family = d.ID
			d.MaxCalls = 3
			parents := seedParents(t, s, d)
			// Seed an independent completed target to exercise the actual assessment API.
			parent, e := s.StrategyTrial(ctx, parents[0])
			if e != nil {
				t.Fatal(e)
			}
			target := parent.Start
			target.ID = d.ID + "-target"
			target.Config = json.RawMessage(`{"comparison":"independent"}`)
			target.Parents = nil
			if _, e = s.StartStrategyTrial(ctx, target); e != nil {
				t.Fatal(e)
			}
			for i, b := range d.Data.Bars {
				if e = s.AppendStrategyTrialReturn(ctx, target.ID, strategytrial.DailyReturn{Date: b.Date, Return: []string{"0.01", "-0.002", "0.006", "0.009", "0.003", "0.004"}[i], Equity: "2000"}); e != nil {
					t.Fatal(e)
				}
			}
			if e = s.FinishStrategyTrial(ctx, target.ID, strategytrial.Outcome{Status: "completed", FinishedAt: time.Now().UTC()}); e != nil {
				t.Fatal(e)
			}
			other := target
			other.ID = d.ID + "-other"
			// Use the other reviewed strategy identity, with the same statistical inputs.
			secondParent, e := s.StrategyTrial(ctx, parents[1])
			if e != nil {
				t.Fatal(e)
			}
			other.StrategyID = secondParent.Start.StrategyID
			if _, e = s.StartStrategyTrial(ctx, other); e != nil {
				t.Fatal(e)
			}
			for i, b := range d.Data.Bars {
				if e = s.AppendStrategyTrialReturn(ctx, other.ID, strategytrial.DailyReturn{Date: b.Date, Return: []string{"0.002", "-0.007", "0.003", "0.001", "0.006", "0.002"}[i], Equity: "2000"}); e != nil {
					t.Fatal(e)
				}
			}
			if e = s.FinishStrategyTrial(ctx, other.ID, strategytrial.Outcome{Status: "completed", FinishedAt: time.Now().UTC()}); e != nil {
				t.Fatal(e)
			}

			j := newJournal()
			at := time.Now().UTC().Truncate(time.Microsecond)
			events := []Event{{Kind: "start", At: at, Body: marshal(d)}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(`{"tool":"read_library"}`)}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(`{"tool":"read_library"}`)}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}}
			for i, ev := range events {
				if e = j.Append(ctx, d.ID, i, ev); e != nil {
					t.Fatal(e)
				}
			}
			raw := evolutionReply(mutation(parents[0], &spec.Expr{Op: "const", Value: 1}), mutation(parents[0], &spec.Expr{Op: "const", Value: 2}), mutation(parents[0], &spec.Expr{Op: "const", Value: 3}))
			modelSpec := strategy.Spec{Name: "pending_model", Version: "1", Data: []string{strategy.KindDailyBars}, ModelDriven: true}
			sid, e := modelSpec.ID()
			if e != nil {
				t.Fatal(e)
			}
			gate := strategygate.Declaration{ID: d.ID + "-gate", Family: d.Family, Spec: marshal(modelSpec), StrategyID: sid, Account: "cash", Balance: "2000", Minimum: "1", Benchmark: "cash", Policy: "backtest-gate-v1", DataSHA256: target.DataSHA256}
			if _, e := backtest.DeclareGate(ctx, s, gate); e != nil {
				t.Fatal(e)
			}

			c := &scripted{t: t, replies: []string{raw}}
			fault := &batchFaults{Trials: s}
			var writing Journal = j
			switch phase {
			case "begin_write":
				fault.failBegin = true
			case "begin_ack":
				fault.beginAck = true
			case "first_start":
				fault.failStart = 1
			case "second_start":
				fault.failStart = 2
			case "third_start":
				fault.failStart = 3
			case "start_ack":
				fault.failStart = 3
				fault.startAck = true
			case "reply_write":
				writing = &appendFault{Journal: j, kind: "reply"}
			case "reply_ack":
				writing = &appendFault{Journal: j, kind: "reply", ack: true}
			case "reserve_write":
				fault.failReserve = true
			case "reserve_ack":
				fault.reserveAck = true
			case "resolve_write":
				fault.failResolve = true
			case "resolve_ack":
				fault.resolveAck = true
			case "result_write":
				writing = &appendFault{Journal: j, kind: "result"}
			}
			observed := observedCaller{Caller: c, before: func() {
				if _, e := s.StrategyTrialSearchTotal(ctx, d.Family); !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Fatal("model call preceded family fence", e)
				}
			}}
			if _, e = Run(ctx, writing, fault, nil, observed, d); e == nil {
				t.Fatal("fault did not fire")
			}
			if phase == "begin_write" || phase == "begin_ack" {
				if c.calls != 0 {
					t.Fatal("model called without acknowledged intent")
				}
				n, e := s.StrategyTrialSearchTotal(ctx, d.Family)
				if phase == "begin_write" {
					if e != nil || n != 4 {
						t.Fatal(n, e)
					}
				} else if !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Fatal("uncertain intent lost", n, e)
				}
				if _, e = Run(ctx, j, s, nil, c, d); e != nil || c.calls != 1 {
					t.Fatal("pre-call intent retry", e, c.calls)
				}
				if n, e = s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 7 {
					t.Fatal(n, e)
				}
				return
			}

			// Only a confirmed full registry reconciliation may expose a finite count.
			resolved := phase == "result_write" || phase == "resolve_ack"
			check := func() {
				n, e := s.StrategyTrialSearchTotal(ctx, d.Family)
				if resolved {
					if e != nil || n < 7 {
						t.Errorf("resolved count %d %v", n, e)
					}
				} else if !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Errorf("optimistic count %d %v", n, e)
				}
				g, e := backtest.EvaluateGate(ctx, s, gate.ID)
				if resolved {
					if e != nil || g.Decision.SearchTotal < 7 {
						t.Errorf("gate count %+v %v", g, e)
					}
				} else if !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Errorf("gate escaped fence %+v %v", g, e)
				}

				a, e := backtest.Assess(ctx, s, target.ID, backtest.Benchmark{}, backtest.Benchmark{}, 4)
				if resolved {
					if e != nil || a.SearchTotal < 7 {
						t.Errorf("assessment count %+v %v", a, e)
					}
				} else if !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Errorf("assessment escaped fence %+v %v", a, e)
				}
			}
			check()
			// Reader snapshots and unrelated manual trial starts share the same boundary.
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for range 8 {
					check()
				}
			}()
			go func() {
				defer wg.Done()
				manual := target
				manual.ID = d.ID + "-manual"
				if _, e := s.StartStrategyTrial(ctx, manual); e != nil {
					t.Error(e)
				}
			}()
			wg.Wait()
			// A changed reply cannot weaken or replace the pending fence.
			b, e := s.StrategyTrialBatch(ctx, attemptID(d.ID, 3, 0))
			if phase != "reserve_write" {
				if e != nil {
					t.Fatal(e)
				}
				changed := b
				changed.Reply = marshal(raw + " ")
				if e = s.ReserveStrategyTrialBatch(ctx, changed); !errors.Is(e, strategytrial.ErrConflict) {
					t.Fatal("changed retry", e)
				}
			}
			// Reopen the registry too: neither the intent nor its pending state is a
			// process-local guard. A new owner recovers through the ordinary API.
			if _, ok := s.(*storage.Postgres); ok {
				dsn := os.Getenv("MANTIK_DESK_TEST_DATABASE_URL")
				if dsn == "" {
					dsn = os.Getenv("MANTIK_TEST_DATABASE_URL")
				}
				reopened, e := storage.OpenPostgres(ctx, dsn)
				if e != nil {
					t.Fatal(e)
				}
				defer reopened.Close()
				s = reopened
			}

			got, e := Run(ctx, j, s, nil, c, d)
			if phase == "reserve_write" {
				if !errors.Is(e, ErrInterrupted) || c.calls != 1 {
					t.Fatal("unknown captured reply relaunched", got, e, c.calls)
				}
				check()
				return
			}
			if e != nil || got.Status != "call_limit" || c.calls != 1 {
				t.Fatal("exact recovery", got, e, c.calls)
			}
			n, e := s.StrategyTrialSearchTotal(ctx, d.Family)
			if e != nil || n != 8 {
				t.Fatal(n, e)
			}
			a, e := backtest.Assess(ctx, s, target.ID, backtest.Benchmark{}, backtest.Benchmark{}, 4)
			if e != nil || a.SearchTotal != 8 || a.DSR.Value == nil {
				t.Fatal(a, e)
			}
			before, e := s.StrategyTrial(ctx, attemptID(d.ID, 3, 0))
			if e != nil {
				t.Fatal(e)
			}
			if (phase == "second_start" || phase == "third_start" || phase == "start_ack") && len(before.Returns) != 0 {
				t.Fatal("uncertain start reran", before)
			}
			if _, e = Run(ctx, j, s, nil, c, d); e != nil || c.calls != 1 {
				t.Fatal(e)
			}
			after, _ := s.StrategyTrial(ctx, before.Start.ID)
			if string(marshal(before)) != string(marshal(after)) {
				t.Fatal("immutable trial changed")
			}
		})
	}
	t.Run("rejected_reply_write", func(t *testing.T) {
		ctx := context.Background()
		d := fixture()
		d.ID = fmt.Sprintf("rejected-batch-%d", time.Now().UnixNano())
		d.Family = d.ID
		d.MaxCalls = 1
		proposals := make([]json.RawMessage, 11)
		for i := range proposals {
			proposals[i] = json.RawMessage(`{}`)
		}
		c := &rejectedEvolutionCaller{raw: evolutionReply(proposals...)}
		j := newJournal()
		if _, e := Run(ctx, &appendFault{Journal: j, kind: "reply"}, s, nil, c, d); e == nil {
			t.Fatal("reply fault did not fire")
		}
		if _, e := s.StrategyTrialSearchTotal(ctx, d.Family); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("rejected reply escaped fence", e)
		}
		got, e := Run(ctx, j, s, nil, c, d)
		if e != nil || c.calls != 1 || got.Status != "model_unavailable" {
			t.Fatal(got, e, c.calls)
		}
		if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 11 {
			t.Fatal("lost rejected duplicates/excess", n, e)
		}
		for _, id := range got.TrialIDs {
			r, e := s.StrategyTrial(ctx, id)
			if e != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 {
				t.Fatal(r, e)
			}
		}
	})

}
func TestDeskDurableBatchFences(t *testing.T) {
	batchRecoveryContracts(t, storage.NewMemory(), func() Journal { return &memoryJournal{} })
}
func TestPostgresContractsResearchBatchFences(t *testing.T) {
	dsn := os.Getenv("MANTIK_DESK_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("MANTIK_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("fresh disposable PostgreSQL required")
	}
	s, e := storage.OpenPostgres(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	batchRecoveryContracts(t, s, func() Journal {
		j, e := OpenPostgresJournal(context.Background(), dsn)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(j.Close)
		return j
	})
}
