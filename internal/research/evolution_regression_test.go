package research

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
)

func TestEvolutionWrongTypeThenDuplicateArray(t *testing.T) {
	for name, value := range map[string]string{"boolean": "false", "null": "null", "object": `{"ignored":{"variants":[1,2]}}`, "number": "1", "string": `"invalid"`} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			d := fixture()
			s := storage.NewMemory()
			ps := seedParents(t, s, d)
			v := mutation(ps[0], &spec.Expr{Op: "const", Value: 1})
			raw := `{"tool":"evolve_specs","variants":` + value + `,"variants":[` + string(v) + `,` + string(v) + `]}`
			out, _, err := dispatch(ctx, s, nil, d, nil, raw, time.Now().UTC().Truncate(time.Microsecond), 3)
			if err != nil {
				t.Fatal(err)
			}
			n, err := s.StrategyTrialSearchTotal(ctx, d.Family)
			if err != nil || n != 5 || len(out.TrialIDs) != 3 {
				t.Fatalf("malformed value hid later proposals: N=%d result=%+v error=%v", n, out, err)
			}
			for _, id := range out.TrialIDs {
				r, err := s.StrategyTrial(ctx, id)
				if err != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 {
					t.Fatal("rejected envelope executed a candidate", r, err)
				}
			}
		})
	}
}
func TestEvolutionRejectedSiblingExactReplay(t *testing.T) {
	ctx := context.Background()
	d := fixture()
	s := storage.NewMemory()
	ps := seedParents(t, s, d)
	raw := evolutionReply(mutation(ps[0], &spec.Expr{Op: "const", Value: 1}), mutation(attemptID(d.ID, 3, 0), &spec.Expr{Op: "const", Value: 2}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	out, _, err := dispatch(ctx, s, nil, d, nil, raw, at, 3)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.StrategyTrial(ctx, attemptID(d.ID, 3, 1))
	if len(r.Start.Parents) != 0 || r.Outcome == nil || r.Outcome.Status != "failed" {
		t.Fatal("rejected sibling acquired lineage", out, r)
	}
	_, _, err = dispatch(ctx, s, nil, d, nil, raw, at, 3)
	if err != nil {
		t.Errorf("exact retained-reply replay must succeed: %v", err)
	}
	again, err := s.StrategyTrial(ctx, attemptID(d.ID, 3, 1))
	if err != nil || !reflect.DeepEqual(again, r) {
		t.Fatal("exact replay changed rejected lineage", again, err)
	}
}
func TestEvolutionRejectedSiblingReplyRecovery(t *testing.T) {
	ctx := context.Background()
	d := fixture()
	d.MaxCalls = 3
	s := storage.NewMemory()
	ps := seedParents(t, s, d)
	raw := evolutionReply(mutation(ps[0], &spec.Expr{Op: "const", Value: 1}), mutation(attemptID(d.ID, 3, 0), &spec.Expr{Op: "const", Value: 2}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	events := []Event{{Kind: "start", At: at, Body: marshal(d)}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(`{"tool":"read_library"}`)}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(`{"tool":"read_library"}`)}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "library"})}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(raw)}}
	if _, _, err := dispatch(ctx, s, nil, d, events, raw, at, 3); err != nil {
		t.Fatal(err)
	}
	j := &memoryJournal{events: map[string][]Event{d.ID: events}}
	c := &scripted{t: t}
	got, err := Run(ctx, j, s, nil, c, d)
	if err != nil || got.Status != "call_limit" || c.calls != 0 {
		t.Errorf("resuming durable reply must complete without inference: %+v calls=%d error=%v", got, c.calls, err)
	}
}
