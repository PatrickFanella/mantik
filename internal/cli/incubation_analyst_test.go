package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	compiled "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

type analystLookupStore interface {
	analyst.Store
	forward.Store
	storage.CappedBookStore
	DeclareStrategyGate(context.Context, strategygate.Declaration) (strategygate.Record, error)
	RecordStrategyGate(context.Context, strategygate.Decision) (strategygate.Record, error)
}
type analystLookupJournal struct{ events map[string][]research.Event }

func (j *analystLookupJournal) Lock(context.Context, string) (func(), error) { return func() {}, nil }
func (j *analystLookupJournal) Events(_ context.Context, id string) ([]research.Event, error) {
	return j.events[id], nil
}
func (j *analystLookupJournal) Append(_ context.Context, id string, seq int, e research.Event) error {
	if seq != len(j.events[id]) {
		return errors.New("sequence")
	}
	j.events[id] = append(j.events[id], e)
	return nil
}

type analystLookupCaller struct {
	action string
	calls  int
	fail   bool
}

func (c *analystLookupCaller) Call(_ context.Context, call model.Call, _ int) (model.Record, error) {
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

func analystLookupFixture(t *testing.T, s analystLookupStore, j research.Journal) (analyst.Runner, analyst.Request, *analystLookupCaller) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	suffix := strconv.FormatInt(at.UnixNano(), 36)
	bookID, gateID, forwardID, cycleID, family := "analyst-book-"+suffix, "analyst-gate-"+suffix, "analyst-forward-"+suffix, "bridge-cycle-"+suffix, "analyst-bridge-"+suffix
	c := compiled.Config{Provider: "codex", Model: "fixture-model", Cutoff: "2026-04-30", Source: "https://developers.openai.com/api/docs/models/fixture-model", ReviewedBy: "synthetic-fixture", ReviewedAt: at.Add(-time.Hour), Assets: []string{"SPY"}, DailyLimit: 10, TimeoutSeconds: 60}
	c.MetadataSHA256 = c.MetadataHash()
	spec, _ := c.Spec()
	sid, _ := spec.ID()
	raw, _ := json.Marshal(spec)
	_, err := s.CreateCappedBook(ctx, account.DefaultCappedConfig(bookID, "paper-fixture-"+suffix, account.Cash, at.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.DeclareStrategyGate(ctx, strategygate.Declaration{ID: gateID, Family: family, StrategyID: sid, Spec: raw, Policy: "backtest-gate-v1", Benchmark: "cash", Minimum: "1", Account: "cash", Balance: "2000", DataSHA256: strings.Repeat("a", 64), Assets: c.Assets})
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
	fd := forward.Declaration{ID: forwardID, Family: g.Declaration.Family, GateID: g.Declaration.ID, BookID: bookID, SleeveID: "model", StrategyID: sid, Spec: spec, Benchmark: forward.Benchmark{StrategyID: bid, Spec: bench}, WindowStart: declared.Add(time.Nanosecond), WindowEnd: declared.Add(14 * 24 * time.Hour), OutcomeDeadline: declared.Add(42 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 5, MinIndependentDecisions: 5, MinimumNetExcess: "0", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	if _, err = s.DeclareForward(ctx, fd, func() time.Time { return declared }); err != nil {
		t.Fatal(err)
	}
	day := at.AddDate(0, 0, -1)
	clock := time.Date(day.Year(), day.Month(), day.Day(), 20, 15, 0, 0, time.UTC)
	q := analyst.Request{ID: cycleID, ForwardDeclarationID: fd.ID, Config: c, AsOf: time.Now().UTC(), Data: research.Manifest{Source: "in-process prospective contract fixture", SourceSHA256: strings.Repeat("c", 64), EvidenceClass: "prospective_market_unqualified", Bars: []storage.DailyBar{{AssetID: "SPY", Symbol: "SPY", Date: day.Format(time.DateOnly), Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 100, PublishedAt: clock, ObservedAt: clock, AvailableAt: clock, Revision: 1}}}}
	caller := &analystLookupCaller{action: "long"}
	return analyst.Runner{Store: s, Journal: j, Caller: caller}, q, caller
}

func analystLookupInput(q analyst.Request, result analyst.Result) incubationInput {
	return incubationInput{ID: "execution", DecidedAt: result.Receipt.At.Add(time.Second), ExecuteAt: result.Receipt.At.Add(2 * time.Second), Strategies: []incubationStrategyInput{{AnalystCycleID: q.ID, SleeveID: "model"}}}
}
func TestIncubationAnalystLookup(t *testing.T) {
	for _, mode := range []string{"long", "hold", "flat", "outage", "synthetic", "unclassified", "early", "assets", "gate", "forward", "sleeve", "source", "reforged"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := storage.NewMemory()
			j := &analystLookupJournal{events: map[string][]research.Event{}}
			ar, q, c := analystLookupFixture(t, s, j)
			if mode == "hold" || mode == "flat" {
				c.action = mode
			}
			c.fail = mode == "outage"
			if mode == "synthetic" {
				q.Data.EvidenceClass = "synthetic_fixture"
			}
			result, err := ar.Run(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			_, retainedForClock, readErr := analyst.Retained(ctx, j, q.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			in := analystLookupInput(q, retainedForClock)
			switch mode {
			case "unclassified":
				q.Data.EvidenceClass = ""
				j.events["analyst-"+q.ID][0].Body, _ = json.Marshal(q)
			case "early":
				in.DecidedAt = result.Receipt.At.Add(-time.Nanosecond)
			case "assets":
				in.Strategies[0].Assets = []string{"QQQ"}
			case "gate":
				in.Strategies[0].GateID = "other-gate"
			case "forward":
				in.Strategies[0].ForwardDeclarationID = "other-forward"
			case "sleeve":
				in.Strategies[0].SleeveID = "other-sleeve"
			case "source":
				in.Strategies[0].Bars = map[string][]storage.DailyBar{"SPY": q.Data.Bars}
			case "reforged":
				// A self-consistent result/target cannot replace the retained trader reply.
				result.Receipt.Selection = compiled.Selection{Action: "flat"}
				result.Target.Targets = nil
				events := j.events["analyst-"+q.ID]
				events[len(events)-1].Body, _ = json.Marshal(result)
			}
			before, _ := json.Marshal(j.events)
			calls := c.calls
			req, err := incubationRequestFromJournal(ctx, in, j, s)
			valid := mode == "long" || mode == "hold" || mode == "flat" || mode == "outage"
			if !valid {
				if err == nil {
					t.Fatal("invalid source admitted", mode)
				}
				return
			}
			if err != nil || len(req.Strategies) != 1 {
				t.Fatal(req, err)
			}
			entry := req.Strategies[0]
			record, err := s.ForwardDeclaration(ctx, q.ForwardDeclarationID)
			if err != nil {
				t.Fatal(err)
			}
			if entry.GateID != record.Declaration.GateID || entry.ForwardDeclarationID != q.ForwardDeclarationID || !reflect.DeepEqual(entry.Source, q.Data) || !reflect.DeepEqual(entry.Assets, q.Config.Assets) {
				t.Fatal("lookup lost source", entry)
			}
			v, err := strategy.Build(ctx, entry.Strategy.Spec(), entry.Source, in.DecidedAt, entry.Assets, strategy.Book{})
			if err != nil {
				t.Fatal(err)
			}
			decision, hold, err := entry.Strategy.(*compiled.Strategy).IncubationDecision(v)
			if err != nil || hold != (mode == "outage" || mode == "hold") || len(decision.Targets) != map[bool]int{true: 1, false: 0}[mode == "long"] {
				t.Fatal(decision, hold, err)
			}
			retry, err := incubationRequestFromJournal(ctx, in, j, s)
			after, _ := json.Marshal(j.events)
			if err != nil || retry.InputSHA256 != req.InputSHA256 || string(before) != string(after) || calls != c.calls {
				t.Fatal("reader mutated or relaunched", err)
			}
		})
	}
}

func TestPostgresContractsIncubationAnalystLookup(t *testing.T) {
	dsn := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("new disposable database required")
	}
	ctx := context.Background()
	s, err := storage.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j, err := research.OpenPostgresJournal(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ar, q, c := analystLookupFixture(t, s, j)
	result, err := ar.Run(ctx, q)
	if err != nil || result.Status != "target" {
		t.Fatal(result, err)
	}
	in := analystLookupInput(q, result)
	req, err := incubationRequestFromJournal(ctx, in, j, s)
	if err != nil || len(req.Strategies) != 1 {
		t.Fatal(req, err)
	}
	original, retained, err := analyst.Retained(ctx, j, q.ID)
	if err != nil || retained.Receipt.Selection.Action != "long" || !reflect.DeepEqual(original, q) {
		t.Fatal("Postgres JSON/clock roundtrip", retained, err)
	}
	again, err := ar.Run(ctx, q)
	if err != nil || again.Status != "target" || c.calls != 4 {
		t.Fatal("retry relaunched", again, err, c.calls)
	}
}
