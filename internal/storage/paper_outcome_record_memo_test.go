package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func TestOutcomeRecordMemoMatchesNativeConstruction(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	at := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	definition := strategyDefinition("record-memo")
	definition.InputEvidenceClass = "prospective_market_unqualified"
	s, err := m.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: m.receiverID, Definition: definition}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "memo-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: definition.InputEvidenceClass, JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := m.FeedCollectorCommand(ctx, m.receiverID, c, func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
	}
	d, err := m.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "decision", ReceiverID: m.receiverID, StrategyID: definition.ID, StrategySHA256: s.Record.SHA256, RegistrationID: reg.ID}, func() time.Time { return at }, func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "record_memo_fixture_only"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	basis := PaperOutcomeBasis{d.Record, clone(m.paperDecisions[d.Record.Command.ID].Basis)}
	command := PaperOutcomeCommand{ID: "outcome", ReceiverID: m.receiverID, DecisionID: d.Record.Command.ID, DecisionSHA256: d.Record.SHA256}
	evaluate := func(b PaperOutcomeBasis, _ time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: b.Observation.Feed.ReceiverID}, nil
	}
	calls := 0
	replay := memoPaperOutcomeRecords(memoPaperOutcomeEvaluator(func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		calls++
		return evaluate(b, at)
	}))
	compare := func(schema string, c PaperOutcomeCommand, b PaperOutcomeBasis, at time.Time) {
		t.Helper()
		want, wantErr := paperOutcomeForSchema(schema, c, b, at, evaluate)
		got, err := replay(schema, c, b, at)
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatal("record memo changed native construction", schema, c, err, wantErr)
		}
	}
	for _, schema := range []string{"mantik-owned-paper-outcome-v1", "mantik-owned-paper-outcome-v2", paperOutcomeSchemaV3, paperOutcomeSchemaV4} {
		if _, err := paperOutcomeForSchema(schema, command, basis, at, evaluate); err != nil {
			t.Fatal("fixture must exercise valid native schemas", schema, err)
		}
		for n := range 16 {
			command.ID = fmt.Sprintf("outcome-%d", n)
			compare(schema, command, basis, at)
		}
	}
	if calls != 1 {
		t.Fatal("equal native evaluation was not reused across envelopes", calls)
	}
	for _, change := range []func(*PaperOutcomeCommand){
		func(c *PaperOutcomeCommand) { c.ID = "invalid.id" },
		func(c *PaperOutcomeCommand) { c.DecisionID = "other" },
		func(c *PaperOutcomeCommand) { c.ReceiverID = "receiver-xxxxxxxxxxxxxxxx" },
		func(c *PaperOutcomeCommand) { c.DecisionSHA256 = strings.Repeat("f", 64) },
	} {
		bad := command
		change(&bad)
		compare(paperOutcomeSchemaV3, bad, basis, at)
	}
	compare("unsupported-schema", command, basis, at)
	compare(paperOutcomeSchemaV3, command, basis, at.Add(-time.Nanosecond))
	compare(paperOutcomeSchemaV3, command, basis, at.Add(time.Second))
	compare(paperOutcomeSchemaV3, command, basis, at.In(time.FixedZone("named-zero", 0)))
	changed := clone(basis)
	changed.Observation.Feed.ReceiverID = "changed"
	compare(paperOutcomeSchemaV3, command, changed, at)
	changed.Observation.Feed.Collectors.LastClock = at.In(time.FixedZone("named-zero", 0))
	compare(paperOutcomeSchemaV3, command, changed, at)
	// A returned value and the caller's nested maps cannot mutate a cache key.
	got, err := replay(paperOutcomeSchemaV3, command, basis, at)
	if err != nil {
		t.Fatal(err)
	}
	got.Evaluation.ReasonCode = "changed_return"
	changed.Observation.Feed.Collectors.Entries[reg.ID] = feedcollector.Entry{}
	compare(paperOutcomeSchemaV3, command, basis, at)
	before := clone(basis)
	mutating := memoPaperOutcomeRecords(func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		b.Observation.Feed.Collectors.Entries[reg.ID] = feedcollector.Entry{}
		return evaluate(b, at)
	})
	for range 2 {
		if _, err := mutating(paperOutcomeSchemaV3, command, basis, at); err != nil || !reflect.DeepEqual(before, basis) {
			t.Fatal("callback mutated caller or cache inputs", err)
		}
	}
	failures := 0
	wantErr := errors.New("evaluation failure fixture")
	failing := memoPaperOutcomeRecords(func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		failures++
		return PaperOutcomeEvaluation{}, wantErr
	})
	for range 2 {
		if _, err := failing(paperOutcomeSchemaV3, command, basis, at); !errors.Is(err, wantErr) {
			t.Fatal(err)
		}
	}
	if failures != 2 {
		t.Fatal("record constructor cached an error", failures)
	}
	if _, err := memoPaperOutcomeRecords(nil)(paperOutcomeSchemaV3, command, basis, at); !errors.Is(err, ErrPaperOutcomeInvalid) {
		t.Fatal("nil callback changed native validation", err)
	}
	large := clone(basis)
	large.Observation.Feed.Records["oversized"] = FeedEvidenceRecord{PublishedAt: at, Recording: json.RawMessage(`{"fixture":"` + strings.Repeat("x", MaxPaperOutcomeBytes) + `"}`)}
	oversizedCalls := 0
	bounded := memoPaperOutcomeRecords(func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		oversizedCalls++
		return evaluate(b, at)
	})
	for range 2 {
		if _, err := bounded(paperOutcomeSchemaV3, command, large, at); err != nil {
			t.Fatal(err)
		}
	}
	if oversizedCalls != 2 {
		t.Fatal("oversized record input escaped the cache budget", oversizedCalls)
	}
}
