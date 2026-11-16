package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func fixture() Declaration {
	asOf := time.Date(2026, 5, 12, 23, 0, 0, 0, time.UTC)
	m := Metadata{Provider: model.Codex, Model: "gpt-6.1-sol", Cutoff: "2026-04-30", Source: "https://developers.openai.com/api/docs/models/gpt-6.1-sol", ReviewedBy: "fixture-owner", ReviewedAt: asOf.Add(-time.Minute)}
	receipt := TriggerReceipt{Kind: "owner_request", ID: "request-86", At: asOf.Add(-time.Minute), Actor: "fixture-owner", Action: "research"}
	d := Declaration{ID: "desk-test", Family: "desk", Identity: Identity{Provider: m.Provider, Model: m.Model, Cutoff: m.Cutoff, Source: m.Source, Metadata: marshal(m), MetadataSHA256: digest(marshal(m))}, Trigger: Trigger{Kind: receipt.Kind, ID: receipt.ID, At: receipt.At, Receipt: marshal(receipt), SourceSHA256: digest(marshal(receipt))}, AsOf: asOf, MaxCalls: 8, DailyBudget: 10, TimeoutSeconds: 60, Goal: "Synthetic contract test", Data: Manifest{Source: "synthetic fixture", SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", Actions: map[string][]marketdata.CorporateAction{}}}
	for _, day := range []string{"2026-05-04", "2026-05-05", "2026-05-06", "2026-05-07", "2026-05-08", "2026-05-11"} {
		at, _ := time.Parse(time.RFC3339, day+"T20:15:00Z")
		d.Data.Bars = append(d.Data.Bars, storage.DailyBar{AssetID: "SPY", Symbol: "SPY", Date: day, Open: "100", High: "102", Low: "99", Close: "101", VWAP: "100", Volume: 100, PublishedAt: at, ObservedAt: at, AvailableAt: at, Revision: 1})
	}
	return d
}

type memoryJournal struct {
	mu     sync.Mutex
	events map[string][]Event
}

func (j *memoryJournal) Lock(context.Context, string) (func(), error) {
	if !j.mu.TryLock() {
		return nil, ErrConflict
	}
	return j.mu.Unlock, nil
}
func (j *memoryJournal) Events(_ context.Context, id string) ([]Event, error) {
	return append([]Event(nil), j.events[id]...), nil
}
func (j *memoryJournal) Append(_ context.Context, id string, seq int, e Event) error {
	if j.events == nil {
		j.events = map[string][]Event{}
	}
	if seq != len(j.events[id]) {
		return ErrConflict
	}
	j.events[id] = append(j.events[id], e)
	return nil
}

type scripted struct {
	replies []string
	calls   int
	fail    error
	t       *testing.T
}

func (s *scripted) Call(_ context.Context, c model.Call, budget int) (model.Record, error) {
	s.t.Helper()
	if c.Model != "gpt-6.1-sol" || !c.RestrictedTools || budget != 10 {
		s.t.Fatal("unbounded model call", c.Model, c.RestrictedTools, budget)
	}
	s.calls++
	if s.fail != nil {
		return model.Record{}, s.fail
	}
	if s.calls > len(s.replies) {
		s.t.Fatal("unexpected model call")
	}
	raw := s.replies[s.calls-1]
	return model.Record{Provider: c.Provider, Model: c.Model, Response: raw, ResponseSHA256: digest([]byte(raw))}, nil
}

const validSpec = `{"name":"desk_trend","score":{"op":"return","window":1,"args":[{"op":"field","field":"close"}]},"gross":0.7,"long_only":true}`

func TestDeskEverySpecAndNoMoneyAuthority(t *testing.T) {
	replies := []string{
		`{"tool":"compose_spec","spec":{"name":"bad","gross":1,"score":{"op":"trade"}}}`,
		`{"tool":"activate","spec":` + validSpec + `}`,
		`{"tool":"run_backtest","spec":` + validSpec + `,"quantity":900}`,
		`{"tool":"compose_spec","spec":` + validSpec + `}`,
		`{"tool":"run_backtest","spec":` + validSpec + `,"parents":["desk-test-trial-4"]}`,
		`{"tool":"trade","id":"SPY"}`,
		`{"tool":"read_journal"}`,
		`{"tool":"done"}`,
	}
	store := storage.NewMemory()
	j := &memoryJournal{}
	caller := &scripted{replies: replies, t: t}
	d := fixture()
	result, e := Run(context.Background(), j, store, nil, caller, d)
	if e != nil || result.Status != "completed" || len(result.TrialIDs) != 5 {
		t.Fatal(result, e)
	}
	records, e := store.StrategyTrials(context.Background(), d.Family)
	if e != nil || len(records) != 5 {
		t.Fatal(records, e)
	}
	for i, r := range records {
		if r.Outcome == nil || r.Start.Config == nil {
			t.Fatal("missing attempt", r)
		}
		if i < 3 && r.Outcome.Status != "failed" {
			t.Fatal("invalid attempted spec escaped", r)
		}
	}
	last := records[4]
	if len(last.Returns) != 6 || last.Outcome.Status != "completed" || last.Start.Parents[0] != "desk-test-trial-4" {
		t.Fatal(last)
	}
	n := caller.calls
	again, e := Run(context.Background(), j, store, nil, caller, d)
	if e != nil || again.EventsSHA256 != result.EventsSHA256 || caller.calls != n {
		t.Fatal("retry relaunched", again, e)
	}
	d.Goal = "changed"
	if _, e = Run(context.Background(), j, store, nil, caller, d); !errors.Is(e, ErrConflict) {
		t.Fatal("changed retry accepted", e)
	}
}

func TestDeskRejectsCutoffTriggerAndDataClock(t *testing.T) {
	cases := map[string]func(*Declaration){
		"unknown cutoff":          func(d *Declaration) { d.Identity.Cutoff = "" },
		"metadata pin":            func(d *Declaration) { d.Identity.MetadataSHA256 = strings.Repeat("0", 64) },
		"invalid model id":        func(d *Declaration) { d.Identity.Model = "--unsafe" },
		"model mismatch":          func(d *Declaration) { d.Identity.Model = "another" },
		"untrusted cutoff origin": func(d *Declaration) { d.Identity.Source = "https://example.org/model" },
		"pre cutoff warmup":       func(d *Declaration) { d.Data.Bars[0].Date = "2026-04-30" },
		"future publication":      func(d *Declaration) { d.Data.Bars[0].PublishedAt = d.AsOf.Add(time.Second) },
		"future observation":      func(d *Declaration) { d.Data.Bars[0].ObservedAt = d.AsOf.Add(time.Second) },
		"future availability":     func(d *Declaration) { d.Data.Bars[0].AvailableAt = d.AsOf.Add(time.Second) },
		"zero availability":       func(d *Declaration) { d.Data.Bars[0].AvailableAt = time.Time{} },
		"early availability":      func(d *Declaration) { d.Data.Bars[0].AvailableAt = d.Data.Bars[0].PublishedAt.Add(-time.Second) },
		"duplicate revision":      func(d *Declaration) { d.Data.Bars = append(d.Data.Bars, d.Data.Bars[0]) },
		"pre cutoff adjustment": func(d *Declaration) {
			d.Data.Actions["SPY"] = []marketdata.CorporateAction{{SourceID: "old", Kind: "split", ExDate: "2026-04-30", From: "1", To: "2", KnownAt: d.AsOf}}
		},
		"unknown trigger": func(d *Declaration) { d.Trigger.Kind = "model" },
		"changed receipt": func(d *Declaration) { d.Trigger.ID = "other" },
		"future trigger":  func(d *Declaration) { d.Trigger.At = d.AsOf.Add(time.Second) },
		"future asof":     func(d *Declaration) { d.AsOf = time.Now().Add(time.Hour) },
		"unbounded tools": func(d *Declaration) { d.MaxCalls = 13 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := fixture()
			change(&d)
			if d.Validate(time.Now()) == nil {
				t.Fatal("admitted invalid declaration")
			}
		})
	}
	for _, kind := range []string{"owner_request", "schedule", "overseer_finding"} {
		t.Run(kind, func(t *testing.T) {
			d := fixture()
			var r TriggerReceipt
			_ = json.Unmarshal(d.Trigger.Receipt, &r)
			r.Kind = kind
			r.Finalized = true
			if kind == "schedule" {
				r.ScheduledFor = &r.At
			}
			d.Trigger.Kind = kind
			d.Trigger.Receipt = marshal(r)
			d.Trigger.SourceSHA256 = digest(d.Trigger.Receipt)
			if e := d.Validate(time.Now()); e != nil {
				t.Fatal(e)
			}
			if kind != "owner_request" {
				r.Finalized = false
				d.Trigger.Receipt = marshal(r)
				d.Trigger.SourceSHA256 = digest(d.Trigger.Receipt)
				if d.Validate(time.Now()) == nil {
					t.Fatal("unfinalized trigger accepted")
				}
			}
		})
	}
}

func TestDeskBoundedFailureAndInterruptedReplyRecovery(t *testing.T) {
	for _, mode := range []string{"model_failure", "call_limit", "uncertain_call", "pending_reply", "uncertain_trial"} {
		t.Run(mode, func(t *testing.T) {
			d := fixture()
			d.MaxCalls = 1
			j := &memoryJournal{}
			store := storage.NewMemory()
			caller := &scripted{t: t, replies: []string{`{"tool":"read_library"}`}}
			ctx := context.Background()
			switch mode {
			case "model_failure":
				caller.fail = errors.New("private provider diagnostic")
			case "uncertain_call", "pending_reply", "uncertain_trial":
				at := d.AsOf
				_ = j.Append(ctx, d.ID, 0, Event{Kind: "start", At: at, Body: marshal(d)})
				_ = j.Append(ctx, d.ID, 1, Event{Kind: "call", At: at, Body: marshal("intent")})
				if mode != "uncertain_call" {
					_ = j.Append(ctx, d.ID, 2, Event{Kind: "reply", At: at, Body: marshal(`{"tool":"run_backtest","spec":` + validSpec + `}`)})
				}
				if mode == "uncertain_trial" {
					// Simulate retained start followed by process loss, by retaining the
					// declaration generated by dispatch and removing its result/outcome.
					tap := &unfinishedTrials{Trials: store}
					_, _, _ = dispatch(ctx, tap, nil, d, j.events[d.ID], `{"tool":"run_backtest","spec":`+validSpec+`}`, at, 1)
				}
			}
			result, e := Run(ctx, j, store, nil, caller, d)
			if mode == "uncertain_call" {
				if !errors.Is(e, ErrInterrupted) || caller.calls != 0 {
					t.Fatal(result, e)
				}
				return
			}
			if mode == "model_failure" {
				if e == nil || result.Status != "model_unavailable" {
					t.Fatal(result, e)
				}
				return
			}
			if e != nil || result.Status != "call_limit" {
				t.Fatal(result, e)
			}
			if mode == "pending_reply" || mode == "uncertain_trial" {
				if caller.calls != 0 || len(result.TrialIDs) != 1 {
					t.Fatal(result)
				}
				r, _ := store.StrategyTrial(ctx, result.TrialIDs[0])
				if mode == "uncertain_trial" && r.Outcome.Status != "crashed" {
					t.Fatal("uncertain execution reran", r)
				}
			}
		})
	}
}

type unfinishedTrials struct{ Trials }

func (u *unfinishedTrials) AppendStrategyTrialReturn(context.Context, string, strategytrial.DailyReturn) error {
	return errors.New("storage unavailable")
}
func (u *unfinishedTrials) FinishStrategyTrial(context.Context, string, strategytrial.Outcome) error {
	return errors.New("storage unavailable")
}

func TestDeskSourceDoesNotLeakFutureBars(t *testing.T) {
	d := fixture()
	at := d.Data.Bars[2].AvailableAt
	got, e := d.Data.AdjustedBars(context.Background(), "SPY", at)
	if e != nil || len(got.Bars) != 3 {
		t.Fatal(got, e)
	}
	for _, b := range got.Bars {
		if b.Raw.AvailableAt.After(at) || b.Date <= d.Identity.Cutoff {
			t.Fatal("clock escaped")
		}
	}
}

func TestDeskGateReadCannotWriteOrReadOtherData(t *testing.T) {
	// Concrete stores have money and gate-write methods, but dispatch only gets
	// the read interface; an unsupported command can at most create a failed trial.
	d := fixture()
	s := storage.NewMemory()
	d.GateIDs = []string{"missing"}
	for _, tool := range []string{"activate", "size", "trade", "record_gate", "approve_source", "risk_control", "set_budget"} {
		r, done, e := dispatch(context.Background(), s, s, d, nil, fmt.Sprintf(`{"tool":%q}`, tool), d.AsOf, 1)
		if e != nil || done || r.Code != "tool_rejected" {
			t.Fatal(tool, r, e)
		}
	}
}

// captureJournal compares live event bytes to their PostgreSQL readback.
type captureJournal struct {
	Journal
	written []Event
}

func (j *captureJournal) Append(ctx context.Context, id string, seq int, e Event) error {
	if err := j.Journal.Append(ctx, id, seq, e); err != nil {
		return err
	}
	j.written = append(j.written, e)
	return nil
}

type gateReader struct {
	record strategygate.Record
	calls  int
}

func (g *gateReader) StrategyGate(context.Context, string) (strategygate.Record, error) {
	g.calls++
	return g.record, nil
}
func TestDeskGateReadsAreBoundToPostCutoffManifest(t *testing.T) {
	for _, mode := range []string{"matching", "other_manifest", "precutoff_calendar", "precutoff_benchmark", "unselected"} {
		t.Run(mode, func(t *testing.T) {
			d := fixture()
			d.GateIDs = []string{"gate-86"}
			reader := &gateReader{record: strategygate.Record{Declaration: strategygate.Declaration{DataSHA256: digest(marshal(d.Data)), Calendar: []string{"2026-05-04"}}, Decision: &strategygate.Decision{Status: "failed"}}}
			switch mode {
			case "other_manifest":
				reader.record.Declaration.DataSHA256 = strings.Repeat("b", 64)
			case "precutoff_calendar":
				reader.record.Declaration.Calendar = []string{"2026-04-30"}
			case "precutoff_benchmark":
				reader.record.Declaration.SPY.Returns = []strategygate.ReferenceReturn{{Date: "2026-01-01", Return: "0.1"}}
			case "unselected":
				d.GateIDs = nil
			}
			r, done, e := dispatch(context.Background(), storage.NewMemory(), reader, d, nil, `{"tool":"read_gate","id":"gate-86"}`, d.AsOf, 1)
			want := "tool_rejected"
			if mode == "matching" {
				want = "gate_report"
			}
			if e != nil || done || r.Code != want {
				t.Fatal(r, e)
			}
			if mode == "unselected" && reader.calls != 0 {
				t.Fatal("unscoped gate read")
			}
		})
	}
}

func TestDeskBacktestRetainsActualAfterCostEngineReturns(t *testing.T) {
	d := fixture()
	d.MaxCalls = 2
	raw := `{"tool":"run_backtest","spec":{"name":"constant_long","score":{"op":"const","value":1},"gross":0.7,"long_only":true}}`
	j := &memoryJournal{}
	s := storage.NewMemory()
	caller := &scripted{t: t, replies: []string{raw, `{"tool":"done"}`}}
	summary, e := Run(context.Background(), j, s, nil, caller, d)
	if e != nil {
		t.Fatal(e)
	}
	trial, e := s.StrategyTrial(context.Background(), summary.TrialIDs[0])
	if e != nil {
		t.Fatal(e)
	}
	if len(trial.Returns) != 6 || trial.Returns[1].Return == "0" || trial.Outcome.Status != "completed" {
		t.Fatal("engine returns missing", trial)
	}
	var fills int
	for _, ev := range j.events[d.ID] {
		if ev.Kind == "result" {
			var r struct {
				Value struct {
					Fills int `json:"fills"`
				} `json:"value"`
			}
			_ = json.Unmarshal(ev.Body, &r)
			fills += r.Value.Fills
		}
	}
	if fills == 0 {
		t.Fatal("synthetic recipe never exercised engine fills")
	}
}

func TestDeskOversizedAttemptStillCounts(t *testing.T) {
	d := fixture()
	d.MaxCalls = 1
	raw := `{"tool":"compose_spec","spec":{"name":"` + strings.Repeat("x", 30000) + `"}}`
	caller := &scripted{t: t, replies: []string{raw}}
	s := storage.NewMemory()
	j := &memoryJournal{}
	result, e := Run(context.Background(), j, s, nil, caller, d)
	if e != nil || len(result.TrialIDs) != 1 {
		t.Fatal(result, e)
	}
	trial, e := s.StrategyTrial(context.Background(), result.TrialIDs[0])
	if e != nil || trial.Outcome.Status != "failed" {
		t.Fatal(trial, e)
	}
	var original string
	_ = json.Unmarshal(j.events[d.ID][2].Body, &original)
	if original != raw {
		t.Fatal("original oversized reply was lost")
	}
}

func TestDeskInterruptedTrialIsDiscoverableBeforeResult(t *testing.T) {
	d := fixture()
	events := []Event{{Kind: "call", Body: marshal("intent")}, {Kind: "reply", Body: marshal(`{"tool":"run_backtest","spec":` + validSpec + `}`)}}
	ids := AttemptIDs(d.ID, events)
	if len(ids) != 1 || ids[0] != d.ID+"-trial-1" {
		t.Fatal("unfinished trial omitted", ids)
	}
}

func TestDeskDuplicateMembersRejectAndRetainEverySpec(t *testing.T) {
	first := strings.Replace(validSpec, "desk_trend", "discarded_spec", 1)
	for name, raw := range map[string]string{
		"spec":          `{"tool":"run_backtest","spec":` + first + `,"spec":` + validSpec + `}`,
		"case alias":    `{"tool":"run_backtest","Spec":` + first + `,"spec":` + validSpec + `}`,
		"nested member": `{"tool":"run_backtest","spec":` + strings.Replace(validSpec, `"gross":0.7`, `"gross":0.7,"gross":0.8`, 1) + `}`,
		"tool":          `{"tool":"done","tool":"run_backtest","spec":` + validSpec + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := fixture()
			d.MaxCalls = 1
			j := &memoryJournal{}
			s := storage.NewMemory()
			c := &scripted{t: t, replies: []string{raw}}
			got, e := Run(context.Background(), j, s, nil, c, d)
			want := 1
			if name == "spec" || name == "case alias" {
				want = 2
			}
			if e != nil || got.Status != "call_limit" || len(got.TrialIDs) != want {
				t.Fatal(got, e)
			}
			records, e := s.StrategyTrials(context.Background(), d.Family)
			if e != nil || len(records) != want {
				t.Fatal(records, e)
			}
			for _, r := range records {
				if r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 || !strings.Contains(string(r.Start.Spec), digest([]byte(raw))) {
					t.Fatal("duplicate member reached execution or lost evidence", r)
				}
			}
			ids := AttemptIDs(d.ID, j.events[d.ID])
			if fmt.Sprint(ids) != fmt.Sprint(got.TrialIDs) {
				t.Fatal("attempt discovery differs", ids, got.TrialIDs)
			}
			retry, e := Run(context.Background(), j, s, nil, c, d)
			if e != nil || retry.EventsSHA256 != got.EventsSHA256 || c.calls != 1 {
				t.Fatal("duplicate retry changed evidence", retry, e)
			}
			pending := &memoryJournal{events: map[string][]Event{d.ID: append([]Event(nil), j.events[d.ID][:3]...)}}
			noCalls := &scripted{t: t}
			recovered, e := Run(context.Background(), pending, s, nil, noCalls, d)
			if e != nil || len(recovered.TrialIDs) != want || noCalls.calls != 0 {
				t.Fatal("pending duplicate reply recovery", recovered, e)
			}
		})
	}
}

func TestDeskRecoversTerminalResultWithoutAnotherCall(t *testing.T) {
	d := fixture()
	j := &memoryJournal{}
	at := time.Now().UTC().Truncate(time.Microsecond)
	events := []Event{{Kind: "start", At: at, Body: marshal(d)}, {Kind: "call", At: at, Body: marshal("intent")}, {Kind: "reply", At: at, Body: marshal(`{"tool":"done"}`)}, {Kind: "result", At: at, Body: marshal(toolResult{Code: "done"})}}
	for i, ev := range events {
		if e := j.Append(context.Background(), d.ID, i, ev); e != nil {
			t.Fatal(e)
		}
	}
	c := &scripted{t: t}
	got, e := Run(context.Background(), j, storage.NewMemory(), nil, c, d)
	if e != nil || c.calls != 0 || got.Status != "completed" || got.Calls != 1 || len(j.events[d.ID]) != 5 {
		t.Fatal(got, e)
	}
	retry, e := Run(context.Background(), j, storage.NewMemory(), nil, c, d)
	if e != nil || retry.EventsSHA256 != got.EventsSHA256 || c.calls != 0 {
		t.Fatal(retry, e)
	}
}

func TestDeskRejectsFutureParentWithoutLosingAttempt(t *testing.T) {
	d := fixture()
	s := storage.NewMemory()
	at := time.Now().UTC().Truncate(time.Microsecond)
	// Capture the declaration without finishing it; future clocks are allowed
	// in retained trial history but cannot parent an earlier model reply.
	_, _, _ = dispatch(context.Background(), &unfinishedTrials{Trials: s}, nil, d, nil, `{"tool":"compose_spec","spec":`+validSpec+`}`, at.Add(time.Hour), 9)
	parent := d.ID + "-trial-9"
	out, _, e := dispatch(context.Background(), s, nil, d, nil, `{"tool":"run_backtest","spec":`+validSpec+`,"parents":["`+parent+`"]}`, at, 1)
	if e != nil || out.TrialID == "" || out.Code != "spec_rejected" {
		t.Fatal(out, e)
	}
	r, e := s.StrategyTrial(context.Background(), out.TrialID)
	if e != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Start.Parents) != 0 || len(r.Returns) != 0 || !strings.Contains(string(r.Start.Spec), parent) {
		t.Fatal("invalid chronology suppressed or executed attempt", r, e)
	}
	if n, e := s.StrategyTrialSearchTotal(context.Background(), d.Family); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}

type microsecondJournal struct{ memoryJournal }

func (j *microsecondJournal) Append(ctx context.Context, id string, seq int, e Event) error {
	e.At = e.At.UTC().Truncate(time.Microsecond)
	return j.memoryJournal.Append(ctx, id, seq, e)
}

func TestDeskFailedCallHashMatchesMicrosecondRoundtrip(t *testing.T) {
	d := fixture()
	j := &microsecondJournal{}
	s := storage.NewMemory()
	c := &scripted{t: t, fail: errors.New("private fixture diagnostic")}
	first, e := Run(context.Background(), j, s, nil, c, d)
	if e == nil || first.Status != "model_unavailable" {
		t.Fatal(first, e)
	}
	retry, e := Run(context.Background(), j, s, nil, c, d)
	if e != nil || first.EventsSHA256 != retry.EventsSHA256 || c.calls != 1 {
		t.Fatal("failure hash changed after timestamp roundtrip", first, retry, e)
	}
}

func TestDeskTransportRejectedOutputRetainsFailedAttempt(t *testing.T) {
	for _, mode := range []string{"oversize", "valid_tool_with_failed_exit"} {
		t.Run(mode, func(t *testing.T) { testDeskRejectedOutput(t, mode) })
	}
}

func testDeskRejectedOutput(t *testing.T, mode string) {
	t.Helper()
	d := fixture()
	d.MaxCalls = 1
	dir := t.TempDir()
	// The fixture is an isolated CLI replacement; no provider is contacted.
	fake := filepath.Join(dir, "codex")
	script := "#!/usr/bin/python3\nprint('{\"tool\":\"run_backtest\",\"spec\":{\"name\":\"'+'x'*70000+'\"}}')\n"
	wantLength := 65536
	if mode == "valid_tool_with_failed_exit" {
		raw := `{"tool":"run_backtest","spec":` + validSpec + `}`
		script = "#!/bin/sh\nprintf '%s' '" + raw + "'\nexit 1\n"
		wantLength = len(raw)
	}
	if e := os.WriteFile(fake, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	c, e := model.OpenJournal(filepath.Join(dir, "journal"))
	if e != nil {
		t.Fatal(e)
	}
	j := &memoryJournal{}
	s := storage.NewMemory()
	got, e := Run(context.Background(), j, s, nil, c, d)
	if e == nil || got.Status != "model_unavailable" || len(got.TrialIDs) != 1 || got.Calls != 1 {
		t.Fatal(got, e)
	}
	var rejected rejectedReply
	if e = json.Unmarshal(j.events[d.ID][2].Body, &rejected); e != nil || len(rejected.Response) != wantLength || digest([]byte(rejected.Response)) != rejected.ResponseSHA256 || !rejected.Incomplete {
		t.Fatal("bounded rejected stdout missing", e)
	}
	r, e := s.StrategyTrial(context.Background(), got.TrialIDs[0])
	if e != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 {
		t.Fatal(r, e)
	}
	if n, e := s.StrategyTrialSearchTotal(context.Background(), d.Family); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	// Recover each post-reply crash window against the retained trial. Rejected
	// stdout must never reach the dispatcher, and no model call may be repeated.
	for _, count := range []int{3, 4, 5} {
		recovery := &memoryJournal{events: map[string][]Event{d.ID: append([]Event(nil), j.events[d.ID][:count]...)}}
		noCalls := &scripted{t: t}
		retry, e := Run(context.Background(), recovery, s, nil, noCalls, d)
		if e != nil || retry.Status != "model_unavailable" || len(retry.TrialIDs) != 1 || noCalls.calls != 0 {
			t.Fatal("rejected-output recovery", count, retry, e)
		}
	}
	// A crash immediately after retaining the rejected reply must also create
	// the failed trial when no prior trial write reached storage.
	recovery := &memoryJournal{events: map[string][]Event{d.ID: append([]Event(nil), j.events[d.ID][:3]...)}}
	fresh := storage.NewMemory()
	noCalls := &scripted{t: t}
	retry, e := Run(context.Background(), recovery, fresh, nil, noCalls, d)
	if e != nil || retry.Status != "model_unavailable" || len(retry.TrialIDs) != 1 || noCalls.calls != 0 {
		t.Fatal("rejected output before trial start", retry, e)
	}
	r, e = fresh.StrategyTrial(context.Background(), retry.TrialIDs[0])
	if e != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 {
		t.Fatal("recovered rejected output reached engine", r, e)
	}
}
