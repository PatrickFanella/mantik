package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func mutation(parent string, score *spec.Expr) json.RawMessage {
	return marshal(variant{Operator: "mutate_score", Parents: []string{parent}, Replacement: score})
}

type rejectedEvolutionCaller struct {
	calls int
	raw   string
}

func (c *rejectedEvolutionCaller) Call(_ context.Context, call model.Call, _ int) (model.Record, error) {
	c.calls++
	return model.Record{Provider: call.Provider, Model: call.Model, Response: c.raw, ResponseSHA256: digest([]byte(c.raw))}, errors.New("fixture rejected stdout")
}

func TestDeskEvolutionRejectedTransportCountsCapturedVariants(t *testing.T) {
	ctx := context.Background()
	d := fixture()
	d.MaxCalls = 1
	// A truncated reply has two complete proposals and an incomplete third.
	raw := `{"tool":"evolve_specs","variants":[{"operator":"mutate_score"},{"operator":"bad"},{"operator":`
	c := &rejectedEvolutionCaller{raw: raw}
	j := &memoryJournal{}
	s := storage.NewMemory()
	first, e := Run(ctx, j, s, nil, c, d)
	if e == nil || first.Status != "model_unavailable" || len(first.TrialIDs) != 3 {
		t.Fatal(first, e)
	}
	for _, id := range first.TrialIDs {
		r, e := s.StrategyTrial(ctx, id)
		if e != nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 {
			t.Fatal(r, e)
		}
	}
	if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 3 {
		t.Fatal(n, e)
	}
	pending := &memoryJournal{events: map[string][]Event{d.ID: append([]Event(nil), j.events[d.ID][:3]...)}}
	fresh := storage.NewMemory()
	again, e := Run(ctx, pending, fresh, nil, c, d)
	if e != nil || c.calls != 1 || len(again.TrialIDs) != 3 || again.Status != "model_unavailable" {
		t.Fatal(again, e)
	}
	if n, e := fresh.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 3 {
		t.Fatal("rejected batch recovery", n, e)
	}
}
func evolutionReply(proposals ...json.RawMessage) string {
	return string(marshal(Request{Tool: "evolve_specs", Variants: proposals}))
}
func retainedEvolution(t *testing.T, r strategytrial.Record) evolutionEvidence {
	t.Helper()
	var value struct {
		Evolution evolutionEvidence `json:"evolution"`
	}
	if e := json.Unmarshal(r.Start.Spec, &value); e != nil || value.Evolution.Policy != "desk-evolution-v1" {
		t.Fatal(r, e)
	}
	return value.Evolution
}
func seedParents(t *testing.T, s Trials, d Declaration) []string {
	t.Helper()
	for step, raw := range []string{validSpec, strings.Replace(validSpec, `"window":1`, `"window":2`, 1)} {
		out, _, e := dispatch(context.Background(), s, nil, d, nil, `{"tool":"compose_spec","spec":`+raw+`}`, time.Now().UTC().Truncate(time.Microsecond), step+1)
		if e != nil || out.Code != "inactive_idea" {
			t.Fatal(out, e)
		}
	}
	return []string{attemptID(d.ID, 1, 0), attemptID(d.ID, 2, 0)}
}

func TestDeskEvolutionCallableLineageAndAllVariants(t *testing.T) {
	ctx := context.Background()
	d := fixture()
	d.MaxCalls = 4
	parent1, parent2 := attemptID(d.ID, 1, 0), attemptID(d.ID, 2, 0)
	mut := mutation(parent1, &spec.Expr{Op: "const", Value: 1})
	recombine := marshal(variant{Operator: "recombine_add", Parents: []string{parent1, parent2}})
	raw := evolutionReply(mut, recombine, mut, mutation(parent1, &spec.Expr{Op: "trade"}), json.RawMessage(`{"operator":"unknown","parents":["`+parent1+`"]}`))
	c := &scripted{t: t, replies: []string{`{"tool":"compose_spec","spec":` + validSpec + `}`, `{"tool":"compose_spec","spec":` + strings.Replace(validSpec, `"window":1`, `"window":2`, 1) + `}`, raw, `{"tool":"done"}`}}
	j := &memoryJournal{}
	s := storage.NewMemory()
	got, e := Run(ctx, j, s, nil, c, d)
	if e != nil || len(got.TrialIDs) != 7 || got.Status != "completed" {
		t.Fatal(got, e)
	}
	if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 7 {
		t.Fatal(n, e)
	}
	for i := 0; i < 5; i++ {
		r, e := s.StrategyTrial(ctx, attemptID(d.ID, 3, i))
		if e != nil {
			t.Fatal(e)
		}
		ev := retainedEvolution(t, r)
		want := []string{parent1}
		if i == 1 {
			want = []string{parent1, parent2}
		}
		if !slices.Equal(r.Start.Parents, want) {
			t.Fatal("lineage", r.Start.Parents, want)
		}
		if r.Start.Family != d.Family || r.Start.DataSHA256 != digest(marshal(d.Data)) || r.Outcome == nil {
			t.Fatal(r)
		}
		if i == 0 {
			if !ev.Selected || len(r.Returns) != 6 || r.Outcome.Status != "completed" {
				t.Fatal("selected simulation", r, ev)
			}
		} else if len(r.Returns) != 0 || ev.Selected {
			t.Fatal("unselected ran", r, ev)
		}
		if i >= 2 && (r.Outcome.Status != "failed" || ev.Rejection == "") {
			t.Fatal("rejected proposal vanished", r, ev)
		}
		if i == 1 && (r.Outcome.Status != "completed" || ev.Rejection != "") {
			t.Fatal("recombination invalid", r, ev)
		}
		if !strings.Contains(string(r.Start.Config), `"initial_cash":"2000"`) || !strings.Contains(string(r.Start.Config), `"unlevered":true`) {
			t.Fatal("fixed engine policy missing")
		}
	}
	// The model protocol is an actual tool path, not an uncalled library helper.
	if !strings.Contains(makePrompt(d, nil), "evolve_specs") {
		t.Fatal("tool not exposed")
	}
	before := got.EventsSHA256
	again, e := Run(ctx, j, s, nil, c, d)
	if e != nil || again.EventsSHA256 != before || c.calls != 4 {
		t.Fatal("exact retry", again, e)
	}
}

func TestDeskEvolutionPenaltiesDetermineSelection(t *testing.T) {
	for _, mode := range []string{"complexity", "near_library"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			d := fixture()
			s := storage.NewMemory()
			parents := seedParents(t, s, d)
			simple := &spec.Expr{Op: "field", Field: "volume"}
			complex := &spec.Expr{Op: "abs", Args: []*spec.Expr{{Op: "abs", Args: []*spec.Expr{simple}}}}
			first, second := complex, simple
			if mode == "near_library" {
				// The price leaf has fewer complexity units, but matches common library
				// signal structure. The more complex volume expression must win.
				first = &spec.Expr{Op: "field", Field: "close"}
				second = &spec.Expr{Op: "abs", Args: []*spec.Expr{{Op: "field", Field: "volume"}}}
			}
			out, _, e := dispatch(ctx, s, nil, d, nil, evolutionReply(mutation(parents[0], first), mutation(parents[0], second)), time.Now().UTC().Truncate(time.Microsecond), 3)
			if e != nil {
				t.Fatal(e)
			}
			a, _ := s.StrategyTrial(ctx, out.TrialIDs[0])
			b, _ := s.StrategyTrial(ctx, out.TrialIDs[1])
			ae, be := retainedEvolution(t, a), retainedEvolution(t, b)
			if ae.Selected || !be.Selected || len(a.Returns) != 0 || len(b.Returns) != 6 || ae.Penalty <= be.Penalty {
				t.Fatal(ae, be)
			}
			if mode == "complexity" && (ae.Similarity != be.Similarity || ae.Complexity <= be.Complexity) {
				t.Fatal(ae, be)
			}
			if mode == "near_library" && (ae.Complexity >= be.Complexity || ae.Similarity <= be.Similarity) {
				t.Fatal(ae, be)
			}
		})
	}
}

func TestDeskCanonicalLibrarySimilarity(t *testing.T) {
	c := library.Default(library.Trend)
	original := spec.Definition{Name: "first", Library: &c, Gross: c.Gross}
	near := original
	near.Name = "rename_is_not_novelty"
	cfg := c
	cfg.Lookbacks = []int{253 - 1, 64, 21}
	near.Library = &cfg
	_, exact, kind := penalties(original)
	_, close, _ := penalties(near)
	distinct := spec.Definition{Name: "distinct", Score: &spec.Expr{Op: "field", Field: "volume"}, Gross: .7, LongOnly: true}
	_, far, _ := penalties(distinct)
	if exact != 1000 || kind != library.Trend || close < 800 || far >= close {
		t.Fatal(exact, close, far, kind)
	}
	renamed := original
	renamed.Name = "another"
	reversed := c
	reversed.Lookbacks = []int{252, 63, 21}
	renamed.Library = &reversed
	if definitionDigest(renamed) != definitionDigest(original) {
		t.Fatal("noncanonical names/lookback order")
	}
	expression := distinct
	expression.Score = recipeSignal(c)
	expression.LongOnly = false
	_, signalSimilarity, _ := penalties(expression)
	if signalSimilarity <= far {
		t.Fatal("expression signal did not compare against library", signalSimilarity, far)
	}
}

// failStart simulates a process disappearing between immutable batch starts.
// failReturn simulates disappearance after the engine returned some days.
type interruptedBatch struct {
	Trials
	failStart    int
	starts       int
	failReturn   bool
	beforeReturn func()
}

func (s *interruptedBatch) StartStrategyTrial(ctx context.Context, start strategytrial.Start) (bool, error) {
	s.starts++
	if s.starts == s.failStart {
		return false, errors.New("simulated batch process exit")
	}
	return s.Trials.StartStrategyTrial(ctx, start)
}
func (s *interruptedBatch) AppendStrategyTrialReturn(ctx context.Context, id string, day strategytrial.DailyReturn) error {
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	if s.failReturn {
		return errors.New("simulated interrupted return write")
	}
	return s.Trials.AppendStrategyTrialReturn(ctx, id, day)
}
func (s *interruptedBatch) FinishStrategyTrial(ctx context.Context, id string, o strategytrial.Outcome) error {
	if s.failReturn {
		return errors.New("simulated process absent before outcome")
	}
	return s.Trials.FinishStrategyTrial(ctx, id, o)
}

func TestDeskEvolutionPartialBatchRecovery(t *testing.T) {
	for _, phase := range []string{"reserve_half", "before_evaluation", "partial_returns", "before_result"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			d := fixture()
			d.MaxCalls = 3
			s := storage.NewMemory()
			parents := seedParents(t, s, d)
			raw := evolutionReply(mutation(parents[0], &spec.Expr{Op: "const", Value: 1}), marshal(variant{Operator: "recombine_add", Parents: parents}), json.RawMessage(`{"operator":"bad","parents":["`+parents[0]+`"]}`))
			at := time.Now().UTC().Truncate(time.Microsecond)
			// A retained reply is the durable batch intent. Recovery must account for
			// every ordinal even when the process never reserved its later starts.
			events := []Event{{Kind: "start", At: at, Body: marshal(d)}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal("{\"tool\":\"read_library\"}")}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal("{\"tool\":\"read_library\"}")}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(raw)}}
			wrapped := &interruptedBatch{Trials: s}
			switch phase {
			case "reserve_half":
				wrapped.failStart = 2
			case "before_evaluation":
				wrapped.failStart = 4
			case "partial_returns":
				wrapped.failReturn = true
			}
			wrapped.beforeReturn = func() {
				if wrapped.starts != 3 {
					t.Fatal("evaluated before all starts", wrapped.starts)
				}
				if _, e := s.StrategyTrialSearchTotal(ctx, d.Family); !errors.Is(e, strategytrial.ErrFamilyPending) {
					t.Fatal("batch evaluation must remain fenced", e)
				}
			}

			if phase == "before_evaluation" {
				// Reserve via a failed return write, leaving all starts unfinished.
				wrapped.failReturn = true
			}
			_, _, firstErr := dispatch(ctx, wrapped, nil, d, events, raw, at, 3)
			if phase != "before_result" && firstErr == nil {
				t.Fatal("fault did not fire")
			}
			if phase == "partial_returns" {
				if e := s.AppendStrategyTrialReturn(ctx, attemptID(d.ID, 3, 0), strategytrial.DailyReturn{Date: "2026-05-04", Return: "0", Equity: "2000"}); e != nil {
					t.Fatal(e)
				}
			}
			j := &memoryJournal{events: map[string][]Event{d.ID: events}}
			caller := &scripted{t: t}
			got, e := Run(ctx, j, s, nil, caller, d)
			if e != nil || caller.calls != 0 || got.Status != "call_limit" || len(got.TrialIDs) != 3 {
				t.Fatal(got, e)
			}
			if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 5 {
				t.Fatal("lost variants", n, e)
			}
			for _, id := range AttemptIDs(d.ID, events) {
				r, e := s.StrategyTrial(ctx, id)
				if e != nil || r.Outcome == nil {
					t.Fatal(r, e)
				}
			}
			selected, _ := s.StrategyTrial(ctx, attemptID(d.ID, 3, 0))
			if phase == "partial_returns" && (selected.Outcome.Status != "crashed" || len(selected.Returns) != 1) {
				t.Fatal("uncertain engine reran", selected)
			}
			if phase == "before_result" && len(selected.Returns) != 6 {
				t.Fatal("finished trial changed", selected)
			}
			again, e := Run(ctx, j, s, nil, caller, d)
			if e != nil || again.EventsSHA256 != got.EventsSHA256 {
				t.Fatal(again, e)
			}
		})
	}
}

func TestDeskEvolutionMalformedExcessAndDuplicateJSONCount(t *testing.T) {
	for _, mode := range []string{"excess", "duplicate_array", "alias_array", "bad_suffix", "duplicate_operator", "oversized", "stray_spec", "malformed_value", "bad_parent_id", "bad_parent_type", "unknown_field"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			d := fixture()
			s := storage.NewMemory()
			parents := seedParents(t, s, d)
			proposal := mutation(parents[0], &spec.Expr{Op: "const", Value: 1})
			raw := evolutionReply(proposal, proposal)
			want := 2
			switch mode {
			case "excess":
				variants := []json.RawMessage{}
				for i := 0; i < 10; i++ {
					variants = append(variants, mutation(parents[0], &spec.Expr{Op: "const", Value: float64(i + 1)}))
				}
				raw = evolutionReply(variants...)
				want = 10
			case "duplicate_array":
				raw = `{"tool":"evolve_specs","variants":[` + string(proposal) + `],"variants":[` + string(proposal) + `]}`
			case "alias_array":
				raw = `{"tool":"evolve_specs","Variants":[` + string(proposal) + `],"variants":[` + string(proposal) + `]}`
			case "bad_suffix":
				raw = `{"tool":"evolve_specs","variants":[` + string(proposal) + `, {"operator":`
			case "duplicate_operator":
				raw = evolutionReply(json.RawMessage(`{"operator":"mutate_score","operator":"recombine_add","parents":["` + parents[0] + `"]}`))
				want = 1
			case "oversized":
				raw = evolutionReply(json.RawMessage(`{"operator":"` + strings.Repeat("x", 30000) + `","parents":["` + parents[0] + `"]}`))
				want = 1
			case "stray_spec":
				raw = `{"tool":"evolve_specs","spec":` + validSpec + `,"variants":[` + string(proposal) + `]}`
			case "bad_parent_id":
				raw = evolutionReply(mutation("@invalid", &spec.Expr{Op: "const", Value: 1}))
				want = 1
			case "bad_parent_type":
				raw = `{"tool":"evolve_specs","variants":[{"operator":"mutate_score","parents":[7],"replacement":{"op":"const","value":1}}]}`
				want = 1
			case "unknown_field":
				raw = `{"tool":"evolve_specs","variants":[{"operator":"mutate_score","parents":["` + parents[0] + `"],"allocate":true}]}`
				want = 1
			case "malformed_value":
				raw = `{"tool":"evolve_specs","variants":false}`
				want = 1
			}
			at := time.Now().UTC().Truncate(time.Microsecond)
			got, _, e := dispatch(ctx, s, nil, d, nil, raw, at, 3)
			if e != nil || len(got.TrialIDs) != min(want, maxVariants) {
				t.Fatal(got, e)
			}
			events := []Event{{Kind: "call"}, {Kind: "call"}, {Kind: "call"}, {Kind: "reply", Body: marshal(raw)}}
			ids := AttemptIDs(d.ID, events)
			if len(ids) != want || !slices.Equal(ids[:min(len(ids), maxVariants)], got.TrialIDs) {
				t.Fatal("pending discovery lost variants", AttemptIDs(d.ID, events), got.TrialIDs)
			}
			if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != int64(2+want) {
				t.Fatal(n, e)
			}
			for i, id := range ids {
				r, e := s.StrategyTrial(ctx, id)
				if e != nil || r.Outcome == nil {
					t.Fatal(r, e)
				}
				if mode != "excess" && (len(r.Returns) > 0 || r.Outcome.Status != "failed") {
					t.Fatal("malformed envelope executed", mode, r)
				}
				if mode == "excess" && i >= 8 && retainedEvolution(t, r).Rejection != "variant limit" {
					t.Fatal("excess not retained", r)
				}
			}
		})
	}
}

func TestDeskEvolutionRejectsParentBoundaryWithoutHidingAttempts(t *testing.T) {
	for _, mode := range []string{"other_session", "other_family", "future_parent", "future_outcome", "unfinished", "manifest", "missing", "invalid_path", "wrong_type", "policy_mismatch", "native_tool"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			d := fixture()
			s := storage.NewMemory()
			parents := seedParents(t, s, d)
			parent, _ := s.StrategyTrial(ctx, parents[0])
			copy := parent.Start
			copy.ID = "foreign-parent"
			switch mode {
			case "other_session":
				copy.Config = marshal(map[string]string{"session_id": "different"})
			case "other_family":
				copy.Family = "other"
			case "future_parent":
				copy.StartedAt = time.Now().Add(time.Hour)
			case "manifest":
				copy.DataSHA256 = strings.Repeat("b", 64)
			}
			if mode == "missing" {
				copy.ID = "missing"
			} else {
				if _, e := s.StartStrategyTrial(ctx, copy); e != nil {
					t.Fatal(e)
				}
				if mode != "unfinished" {
					end := time.Now().UTC()
					if mode == "future_parent" {
						end = copy.StartedAt.Add(time.Second)
					}
					if mode == "future_outcome" {
						end = end.Add(time.Hour)
					}
					if e := s.FinishStrategyTrial(ctx, copy.ID, strategytrial.Outcome{Status: "completed", FinishedAt: end}); e != nil {
						t.Fatal(e)
					}
				}
			}
			v := variant{Operator: "mutate_score", Parents: []string{copy.ID}, Replacement: &spec.Expr{Op: "const", Value: 1}}
			if mode == "invalid_path" {
				v.Path = []int{99}
			}
			if mode == "wrong_type" {
				v.Replacement = &spec.Expr{Op: "gt", Args: []*spec.Expr{{Op: "const"}, {Op: "const"}}}
			}
			if mode == "native_tool" {
				v.Operator = "activate"
			}
			if mode == "policy_mismatch" {
				other := strings.Replace(validSpec, `"gross":0.7`, `"gross":0.8`, 1)
				if _, _, e := dispatch(ctx, s, nil, d, nil, `{"tool":"compose_spec","spec":`+other+`}`, time.Now().UTC(), 7); e != nil {
					t.Fatal(e)
				}
				v = variant{Operator: "recombine_add", Parents: []string{parents[0], attemptID(d.ID, 7, 0)}}
			}
			out, _, e := dispatch(ctx, s, nil, d, nil, evolutionReply(marshal(v)), time.Now().UTC().Truncate(time.Microsecond), 3)
			if e != nil || len(out.TrialIDs) != 1 {
				t.Fatal(out, e)
			}
			r, e := s.StrategyTrial(ctx, out.TrialIDs[0])
			if e != nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 || retainedEvolution(t, r).Selected {
				t.Fatal("parent boundary escaped", r, e)
			}
		})
	}
}

func TestDeskEvolutionConcurrentSessionsCountIndependently(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	var wg sync.WaitGroup
	const sessions = 4
	for i := 0; i < sessions; i++ {
		wg.Go(func() {
			d := fixture()
			d.ID = fmt.Sprintf("concurrent-%d", i)
			d.MaxCalls = 2
			parent := attemptID(d.ID, 1, 0)
			c := &scripted{t: t, replies: []string{`{"tool":"compose_spec","spec":` + validSpec + `}`, evolutionReply(mutation(parent, &spec.Expr{Op: "const", Value: 1}), mutation(parent, &spec.Expr{Op: "const", Value: 1}))}}
			if got, e := Run(ctx, &memoryJournal{}, s, nil, c, d); e != nil || len(got.TrialIDs) != 3 {
				t.Error(got, e)
			}
		})
	}
	wg.Wait()
	if n, e := s.StrategyTrialSearchTotal(ctx, fixture().Family); e != nil || n != sessions*3 {
		t.Fatal(n, e)
	}
}

func TestDeskEvolutionSubtreeMutationAndStableTie(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	d := fixture()
	parents := seedParents(t, s, d)
	raw := evolutionReply(marshal(variant{Operator: "mutate_score", Parents: []string{parents[0]}, Path: []int{0}, Replacement: &spec.Expr{Op: "field", Field: "volume"}}))
	out, _, e := dispatch(ctx, s, nil, d, nil, raw, time.Now().UTC().Truncate(time.Microsecond), 3)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.StrategyTrial(ctx, out.TrialIDs[0])
	if e != nil {
		t.Fatal(e)
	}
	var retained struct {
		Definition spec.Definition `json:"definition"`
	}
	if e := json.Unmarshal(r.Start.Spec, &retained); e != nil || retained.Definition.Score.Op != "return" || retained.Definition.Score.Window != 1 || retained.Definition.Score.Args[0].Field != "volume" {
		t.Fatal(retained, e)
	}
	parent, e := parentDefinition(ctx, s, d, parents[0], time.Now().UTC())
	if e != nil || parent.Score.Args[0].Field != "close" {
		t.Fatal("parent changed", parent, e)
	}
	raw = evolutionReply(mutation(parents[0], &spec.Expr{Op: "field", Field: "high"}), mutation(parents[0], &spec.Expr{Op: "field", Field: "low"}))
	out, _, e = dispatch(ctx, s, nil, d, nil, raw, time.Now().UTC().Truncate(time.Microsecond), 4)
	if e != nil {
		t.Fatal(e)
	}
	first, _ := s.StrategyTrial(ctx, out.TrialIDs[0])
	second, _ := s.StrategyTrial(ctx, out.TrialIDs[1])
	a, b := retainedEvolution(t, first), retainedEvolution(t, second)
	if a.Penalty != b.Penalty || !a.Selected || b.Selected {
		t.Fatal("unstable ordinal tiebreak", a, b)
	}
}
