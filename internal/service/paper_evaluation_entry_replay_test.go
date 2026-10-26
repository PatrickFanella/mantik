package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func entryReplayFixture(t *testing.T) ([]storage.PaperEvaluationEntrySource, []PaperOutcomeMarketEvidence) {
	t.Helper()
	source := outcomeMarketFixture(t)
	at := source.Basis.Decision.FinalizedAt
	outcomeCalendarFixture(t, &source, "entry-calendar", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, at.Add(-time.Second), at.Add(-time.Second), at.Add(-time.Second))
	market, err := inspectPaperOutcomeMarket(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}

	strategy := paperstrategy.Record{Command: paperstrategy.Command{Definition: paperstrategy.Definition{ID: "variant", Symbol: "SPY", Feed: "iex", InputEvidenceClass: "synthetic_unqualified", Safety: domain.Safety{MaxQuoteAgeSeconds: 60}}}}
	rows := []storage.PaperEvaluationEntrySource{}
	for _, id := range []string{"entry-a", "entry-b"} {
		d := source.Basis.Decision
		d.Command.ID = id
		cost := storage.OwnedExecutionCost{Quantity: 8, LimitCents: 10000, MaxDebitCents: 80000, ValidUntil: at.Add(10 * time.Second)}
		rows = append(rows, storage.PaperEvaluationEntrySource{Decision: d, Strategy: strategy, Status: "entry_sources_selected", Outcome: &source, BaseCost: &cost, StressCost: &cost})
	}
	return rows, []PaperOutcomeMarketEvidence{market, market}
}
func TestEvaluationEntryReplaySharesOccurrenceAndPinsModels(t *testing.T) {
	rows, markets := entryReplayFixture(t)
	base := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	stress := base
	stress.LiquidityBPS = 5000
	before := workerplan.Hash(struct {
		Rows    []storage.PaperEvaluationEntrySource
		Markets []PaperOutcomeMarketEvidence
	}{rows, markets})
	b, s, excluded, err := replayEvaluationEntryVariant(context.Background(), rows, markets, base, stress)
	if err != nil || b == nil || s == nil || b.Book.Method != paperfill.OccurrenceBookMethod || b.Book.Model != base || s.Book.Model != stress {
		t.Fatal(b, s, err)
	}
	if b.Projection.Orders[0].Projection.FilledQuantity != 5 || b.Projection.Orders[1].Projection.FilledQuantity != 0 || s.Projection.Orders[0].Projection.FilledQuantity != 2 || s.Projection.Orders[1].Projection.FilledQuantity != 0 {
		t.Fatal("replenished shared occurrence capacity", b.Projection, s.Projection)
	}
	if len(excluded) == 0 || b.Projection.ExecutionAttested || b.Projection.QualificationCredit {
		t.Fatal("missing exclusions or invented authority", b, excluded)
	}
	if !b.Book.Orders[0].PlacedAt.After(rows[0].Decision.FinalizedAt) {
		t.Fatal("reference occurrence activated entry")
	}
	replay, err := paperfill.ReplayBook(context.Background(), b.Book)
	if err != nil || !reflect.DeepEqual(replay, b.Projection) {
		t.Fatal("book replay", err)
	}
	bb, ss, ee, err := replayEvaluationEntryVariant(context.Background(), rows, markets, base, stress)
	if err != nil || !reflect.DeepEqual(b, bb) || !reflect.DeepEqual(s, ss) || !reflect.DeepEqual(excluded, ee) {
		t.Fatal("unstable retry", err)
	}
	if workerplan.Hash(struct {
		Rows    []storage.PaperEvaluationEntrySource
		Markets []PaperOutcomeMarketEvidence
	}{rows, markets}) != before {
		t.Fatal("mutated retained inputs")
	}
}
func TestEvaluationEntryReplayRefusesUnsupportedOrUnavailableQuotes(t *testing.T) {
	for _, kind := range []string{"session", "quantity", "delayed_publication"} {
		t.Run(kind, func(t *testing.T) {
			rows, markets := entryReplayFixture(t)
			markets = markets[:1]
			for i := range markets[0].Rows {
				r := &markets[0].Rows[i]
				if r.Quote == nil {
					continue
				}
				switch kind {
				case "session":
					r.SessionEvidence = nil
				case "quantity":
					r.Quote.AskShares = paperfill.MaxQuantity + 1
				case "delayed_publication":
					r.AvailableAt = rows[0].BaseCost.ValidUntil.Add(time.Second)
				}
			}
			model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
			b, _, excluded, err := replayEvaluationEntryVariant(context.Background(), rows, markets, model, model)
			if err != nil || b == nil || len(excluded) == 0 || b.Projection.Orders[0].Projection.FilledQuantity != 0 {
				t.Fatal(b, excluded, err)
			}
		})
	}
}

type entryReplayBodyStore struct {
	*storage.Memory
	called bool
}

func (s *entryReplayBodyStore) PaperEvaluationEntrySources(context.Context, string, func() time.Time, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperEvaluationEntrySources, error) {
	s.called = true
	return storage.PaperEvaluationEntrySources{}, storage.ErrPaperEvaluationEvidenceInvalid
}
func TestEvaluationEntryReplayHTTPRejectsAssertions(t *testing.T) {
	for _, query := range []string{"?model=caller", "?"} {
		s := &entryReplayBodyStore{Memory: storage.NewMemory()}
		mux := http.NewServeMux()
		paperEvaluationEntryReplayRoutes(mux, s, WallClock{})
		req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/entry-replay"+query, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 422 || s.called {
			t.Fatal(w.Code, s.called)
		}
	}
	s := &entryReplayBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperEvaluationEntryReplayRoutes(mux, s, WallClock{})
	req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/entry-replay", strings.NewReader(`{"balances":1}`))
	req.ContentLength = -1
	req.TransferEncoding = nil
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 422 || s.called {
		t.Fatal(w.Code, s.called)
	}
	req = httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/entry-replay", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 503 || !s.called {
		t.Fatal(w.Code, s.called)
	}
}

func TestEvaluationEntryReplayEnvelopeRemainsNongrantAndPending(t *testing.T) {
	rows, _ := entryReplayFixture(t)
	base := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	source := storage.PaperEvaluationEntrySources{Rows: rows[:1]}
	source.Policy.Command.ExecutionModel = &base
	source.Evidence.Phase = "outcome_deadline_elapsed"
	source.Evidence.Declaration.Command.Plan.Variants = []storage.PaperEvaluationVariant{{ID: "variant"}}
	source.Evidence.Declaration.Command.Plan.StressModel = base
	out, err := inspectEvaluationEntryReplay(context.Background(), source)
	if err != nil || len(out.Variants) != 1 || out.Variants[0].Base == nil || out.PortfolioEconomicsAvailable || out.AdmissionAvailable || out.ExecutionAvailable || out.QualificationCredit || out.Authority != "service_owned_entry_counterfactual_only" {
		t.Fatal(out, err)
	}
	pin := out.SHA256
	out.SHA256 = ""
	if workerplan.Hash(out) != pin {
		t.Fatal("replay envelope hash")
	}
	source.Evidence.Phase = "awaiting_outcome_deadline"
	pending, err := inspectEvaluationEntryReplay(context.Background(), source)
	if err != nil || pending.Variants[0].Base != nil || pending.Variants[0].Stress != nil || pending.Variants[0].Status != "pending_outcome_deadline" {
		t.Fatal("premature replay", pending, err)
	}
}
