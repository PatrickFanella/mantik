package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Manufactured method fixtures, not observed market activity or qualification.
func protectiveFixture(t *testing.T) ([]storage.PaperEvaluationEntrySource, time.Time, paperfill.Model) {
	t.Helper()
	rows, _ := entryReplayFixture(t)
	for i := range rows {
		rows[i].Strategy.Command.Definition.Safety.StopCents = 9500
		rows[i].Strategy.Command.Definition.Safety.TargetCents = 11000
		rows[i].Strategy.Command.Definition.Safety.MaxHoldingSeconds = 10
	}
	return rows, rows[0].Decision.FinalizedAt, paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
}
func protectiveQuote(at time.Time, id string, second int, bid, ask, quantity int64) paperfill.BookCommand {
	when := at.Add(time.Duration(second) * time.Second)
	q := paperfill.Quote{ID: id, SourceSHA256: workerplan.Hash(id), Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: when, ReceivedAt: when, BidCents: bid, AskCents: ask, BidQuantity: quantity, AskQuantity: quantity}
	return paperfill.BookCommand{ID: id, At: when, Quote: &q}
}
func TestProtectiveReplaySealsPartialBeforeStopAndSharesExitLiquidity(t *testing.T) {
	rows, at, model := protectiveFixture(t)
	quotes := []paperfill.BookCommand{protectiveQuote(at, "entry", 2, 9990, 10000, 5), protectiveQuote(at, "stop", 3, 9400, 9500, 5), protectiveQuote(at, "close", 4, 9300, 9400, 3)}
	before := workerplan.Hash(struct {
		Rows   []storage.PaperEvaluationEntrySource
		Quotes []paperfill.BookCommand
	}{rows, quotes})
	out, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(15*time.Second), model, false)
	if err != nil || len(out.Positions) != 2 || out.ExitBook == nil {
		t.Fatal(out, err)
	}
	first := out.Positions[0]
	if first.EnteredQuantity != 5 || first.ClosedQuantity != 3 || first.UnclosedQuantity != 2 || first.TriggerReason != "stop_reached" || !first.FirstFillAt.Equal(at.Add(2*time.Second)) || !first.HoldingDeadline.Equal(at.Add(12*time.Second)) || !first.TriggerAt.Equal(at.Add(3*time.Second)) || first.ClosedNetCents != nil {
		t.Fatal("partial/timer binding", first)
	}
	if out.EntryProjection.Orders[0].Projection.Status != "cancelled" || out.EntryProjection.Orders[0].Projection.FilledQuantity != 5 {
		t.Fatal("stop replenished partial entry", out.EntryProjection)
	}
	// The second entry may fill the stop quote, but cannot consume the same
	// three-share close capacity independently when its timeout later arrives.
	if out.Positions[1].ClosedQuantity != 0 || out.Positions[1].TriggerReason != "stop_reached" {
		t.Fatal("second position", out.Positions[1])
	}

	bothEligible := append(append([]paperfill.BookCommand{}, quotes...), protectiveQuote(at, "shared-exit", 5, 9200, 9300, 3))
	shared, e := replayProtectiveModel(context.Background(), rows, bothEligible, at.Add(15*time.Second), model, false)
	if e != nil || shared.Positions[0].ClosedQuantity != 5 || shared.Positions[1].ClosedQuantity != 1 || shared.Positions[1].ClosedNetCents != nil {
		t.Fatal("reused shared exit bid capacity", shared, e)
	}
	entry, err := paperfill.ReplayBook(context.Background(), out.EntryBook)
	if err != nil || !reflect.DeepEqual(entry, out.EntryProjection) {
		t.Fatal("entry book replay", err)
	}
	exit, err := paperfill.ReplayBook(context.Background(), *out.ExitBook)
	if err != nil || !reflect.DeepEqual(exit, *out.ExitProjection) {
		t.Fatal("exit book replay", err)
	}
	again, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(15*time.Second), model, false)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("unstable replay", err)
	}
	if workerplan.Hash(struct {
		Rows   []storage.PaperEvaluationEntrySource
		Quotes []paperfill.BookCommand
	}{rows, quotes}) != before {
		t.Fatal("mutated source")
	}
}
func TestProtectiveReplayTimeoutUsesFirstFillAndPreservesShortCut(t *testing.T) {
	rows, at, model := protectiveFixture(t)
	rows = rows[:1]
	quotes := []paperfill.BookCommand{protectiveQuote(at, "late-entry", 8, 9990, 10000, 8), protectiveQuote(at, "after-timeout", 19, 10000, 10010, 8)}
	short, err := replayProtectiveModel(context.Background(), rows, quotes[:1], at.Add(15*time.Second), model, false)
	if err != nil || len(short.Positions) != 1 || short.ExitBook != nil || !short.Positions[0].TriggerAt.IsZero() || !short.Positions[0].HoldingDeadline.Equal(at.Add(18*time.Second)) || short.Positions[0].UnclosedQuantity != 8 {
		t.Fatal("manufactured early timer/close", short, err)
	}

	exact, err := replayProtectiveModel(context.Background(), rows, quotes[:1], at.Add(18*time.Second), model, false)
	if err != nil || exact.ExitBook != nil || exact.Positions[0].TriggerReason != "max_holding_elapsed" || exact.Positions[0].UnclosedQuantity != 8 {
		t.Fatal("future exit projected beyond exact cut", exact, err)
	}
	closed, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(20*time.Second), model, false)
	if err != nil || closed.Positions[0].TriggerReason != "max_holding_elapsed" || !closed.Positions[0].TriggerAt.Equal(at.Add(18*time.Second)) || closed.Positions[0].ClosedQuantity != 8 || closed.Positions[0].ClosedNetCents == nil {
		t.Fatal(closed, err)
	}
	noFill, err := replayProtectiveModel(context.Background(), rows, nil, at.Add(20*time.Second), model, false)
	if err != nil || len(noFill.Positions) != 0 || noFill.ExitBook != nil {
		t.Fatal("invented position/timer", noFill, err)
	}
}
func TestProtectiveReplayTriggerLatencyFeesAndExactTimeoutPriority(t *testing.T) {
	for _, spec := range []struct {
		name   string
		second int
		bid    int64
		reason string
	}{{"target", 3, 11100, "target_reached"}, {"timeout_priority", 12, 9400, "max_holding_elapsed"}} {
		t.Run(spec.name, func(t *testing.T) {
			rows, at, model := protectiveFixture(t)
			rows = rows[:1]
			model.LatencyMillis = 500
			model.FeeBPS = 10
			model.CommissionPerFillCents = 1
			quotes := []paperfill.BookCommand{protectiveQuote(at, "entry", 2, 9990, 10000, 8), protectiveQuote(at, "trigger", spec.second, spec.bid, spec.bid+10, 8), protectiveQuote(at, "close", spec.second+1, 11000, 11010, 8)}
			out, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(time.Duration(spec.second+2)*time.Second), model, false)
			if err != nil || len(out.Positions) != 1 || out.Positions[0].TriggerReason != spec.reason || out.Positions[0].ClosedQuantity != out.Positions[0].EnteredQuantity || out.Positions[0].ClosedNetCents == nil {
				t.Fatal(out, err)
			}
			for _, e := range out.ExitBook.Entries {
				for _, o := range e.Outcomes {
					if o.Outcome.Fill != nil && o.Outcome.Fill.QuoteID == "trigger" {
						t.Fatal("trigger quote filled latency-bound exit")
					}
				}
			}
			if out.Positions[0].ExitNetProceedsCents >= out.Positions[0].ClosedQuantity*11000 {
				t.Fatal("ignored exit fees", out.Positions[0])
			}
		})
	}
}
func TestProtectiveReplayCapacityFailsWithoutPartialResult(t *testing.T) {
	rows, at, model := protectiveFixture(t)
	rows = rows[:1]
	quotes := []paperfill.BookCommand{}
	for i := range paperfill.MaxEntries {
		quotes = append(quotes, protectiveQuote(at, fmt.Sprintf("quote-%d", i), i+1, 12000, 12010, 1))
	}
	out, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(130*time.Second), model, false)
	if err != paperfill.ErrCapacity || out != nil {
		t.Fatal("truncated success", out, err)
	}
}
func TestProtectiveReplayHTTPRejectsCallerAssertions(t *testing.T) {
	for _, suffix := range []string{"?at=future", "?"} {
		s := &entryReplayBodyStore{Memory: storage.NewMemory()}
		mux := http.NewServeMux()
		paperEvaluationProtectiveReplayRoutes(mux, s, WallClock{})
		req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/protective-replay"+suffix, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 422 || s.called {
			t.Fatal(w.Code, s.called)
		}
	}
	s := &entryReplayBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperEvaluationProtectiveReplayRoutes(mux, s, WallClock{})
	req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/protective-replay", strings.NewReader(`{"effect":{},"quote":{}}`))
	req.ContentLength = -1
	req.TransferEncoding = nil
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 422 || s.called {
		t.Fatal(w.Code, s.called)
	}
	req = httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/protective-replay", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 503 || !s.called {
		t.Fatal(w.Code, s.called)
	}
}

func TestProtectiveReplayStressOwnsItsFillClockAndExitLatency(t *testing.T) {
	rows, at, base := protectiveFixture(t)
	rows = rows[:1]
	stress := base
	stress.LatencyMillis = 3000
	quotes := []paperfill.BookCommand{protectiveQuote(at, "early-entry", 2, 9990, 10000, 8), protectiveQuote(at, "later-entry", 4, 9990, 10000, 8), protectiveQuote(at, "forward-close", 15, 10000, 10010, 8)}
	b, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(20*time.Second), base, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := replayProtectiveModel(context.Background(), rows, quotes, at.Add(20*time.Second), stress, true)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Positions[0].FirstFillAt.Equal(at.Add(2*time.Second)) || !s.Positions[0].FirstFillAt.Equal(at.Add(4*time.Second)) || !s.Positions[0].HoldingDeadline.Equal(at.Add(14*time.Second)) || b.Positions[0].ClosedQuantity != 8 || s.Positions[0].ClosedQuantity != 0 {
		t.Fatal("borrowed base fill clock or bypassed stress exit latency", b, s)
	}
}
func TestProtectiveReplayOldOccurrenceCannotTriggerNewPosition(t *testing.T) {
	rows, at, model := protectiveFixture(t)
	rows = rows[:1]
	entry := protectiveQuote(at, "entry", 2, 9990, 10000, 5)
	correction := protectiveQuote(at, "old-correction", 3, 9400, 9500, 5)
	correction.Quote.OccurredAt = entry.Quote.OccurredAt
	out, err := replayProtectiveModel(context.Background(), rows, []paperfill.BookCommand{entry, correction}, at.Add(11*time.Second), model, false)
	if err != nil || len(out.Positions) != 1 || !out.Positions[0].TriggerAt.IsZero() || out.Positions[0].EnteredQuantity != 5 || out.ExitBook != nil {
		t.Fatal("correction replenished entry or triggered from old occurrence", out, err)
	}
}

func TestProtectiveReplayEnvelopeKeepsAuthorityAndPendingSeparate(t *testing.T) {
	rows, _, model := protectiveFixture(t)
	source := storage.PaperEvaluationEntrySources{Rows: rows[:1]}
	source.Policy.Command.ExecutionModel = &model
	source.Evidence.Phase = "outcome_deadline_elapsed"
	source.Evidence.Declaration.Command.Plan.Variants = []storage.PaperEvaluationVariant{{ID: "variant"}}
	source.Evidence.Declaration.Command.Plan.StressModel = model
	source.Evidence.Declaration.Command.Plan.StressModel.LiquidityBPS = 5000
	out, err := inspectEvaluationProtectiveReplay(context.Background(), source)
	if err != nil || len(out.Variants) != 1 || out.Variants[0].Base == nil || out.Variants[0].Stress == nil || out.PortfolioEconomicsAvailable || out.AdmissionAvailable || out.ExecutionAvailable || out.QualificationCredit || out.Authority != "service_owned_protective_counterfactual_only" {
		t.Fatal(out, err)
	}
	pin := out.SHA256
	out.SHA256 = ""
	if workerplan.Hash(out) != pin {
		t.Fatal("protective envelope digest")
	}
	source.Evidence.Phase = "awaiting_outcome_deadline"
	pending, err := inspectEvaluationProtectiveReplay(context.Background(), source)
	if err != nil || pending.Variants[0].Base != nil || pending.Variants[0].Stress != nil || pending.Variants[0].Status != "pending_outcome_deadline" {
		t.Fatal("premature protective result", pending, err)
	}
}
