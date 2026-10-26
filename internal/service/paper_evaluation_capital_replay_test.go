package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Manufactured study sources; actual owner custody is tested by the shared
// Memory/PostgreSQL HTTP contracts. No synthetic account or fill is qualified.
func capitalFixture(t *testing.T, opening, capital int64) (storage.PaperEvaluationEntrySources, time.Time, paperfill.Model) {
	t.Helper()
	rows, at, model := protectiveFixture(t)
	owner := "receiver-abcdefghijklmnop"
	account, err := paperaccount.Create(owner, paperaccount.Config{ID: "study", Currency: "USD", OpeningCashCents: opening, CapitalLimitCents: capital}, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	command := allocation.PolicyCommand{ReceiverID: owner, AccountID: "study", ExecutionModel: &model, Policy: allocation.Policy{ID: "study-policy", AccountSHA256: account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: "variant", ArtifactSHA256: strings.Repeat("a", 64), LimitCents: opening}}, Symbols: []allocation.Symbol{{ID: "SPY", LimitCents: opening, Groups: []string{"equity"}}}, Groups: []allocation.Group{{ID: "equity", LimitCents: opening}}}}
	policy, err := allocation.NewPolicyRecord(command, account, at.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].Strategy.DefinitionSHA256 = strings.Repeat("a", 64)
		rows[i].Decision.SHA256 = workerplan.Hash(rows[i].Decision.Command.ID)
		c := *rows[i].BaseCost
		c.ModelSHA256 = workerplan.Hash(model)
		rows[i].BaseCost = &c
		stress := c
		rows[i].StressCost = &stress
	}
	source := storage.PaperEvaluationEntrySources{Policy: policy, Rows: rows}
	source.Evidence.Declaration.Command.Plan.StressModel = model
	return source, at, model
}
func TestCapitalReplayRejectsUnfundedBeforeLiquidityAndConserves(t *testing.T) {
	source, at, model := capitalFixture(t, 75000, 75000)
	second := source.Rows[1]
	second.Decision.FinalizedAt = at.Add(time.Second)
	c := *second.BaseCost
	c.MaxDebitCents = 72000
	c.LimitCents = 9000
	second.BaseCost = &c
	second.StressCost = &c
	source.Rows[1] = second
	quotes := []paperfill.BookCommand{protectiveQuote(at, "entry", 2, 8990, 9000, 8), protectiveQuote(at, "close", 13, 9000, 9010, 8)}
	before := workerplan.Hash(source)
	got, err := replayCapitalModel(context.Background(), source, source.Rows, quotes, at.Add(20*time.Second), model, false)
	if err != nil || len(got.Candidates) != 2 || got.Candidates[0].Status != "not_selected_for_counterfactual" || got.Candidates[1].Status != "selected_for_counterfactual_only" || got.CumulativeFundingCents != 72000 || !got.ConservationVerified || got.EndingCashCents != 75000 || got.ClosedNetCents == nil || *got.ClosedNetCents != 0 {
		t.Fatal(got, err)
	}
	if len(got.Replay.EntryBook.Orders) != 1 || got.Replay.EntryBook.Orders[0].ID != "entry-b" || got.Replay.Positions[0].EnteredQuantity != 8 {
		t.Fatal("unfunded candidate consumed capacity", got.Replay)
	}
	if workerplan.Hash(source) != before {
		t.Fatal("mutated source policy/costs")
	}
	reverse := append([]storage.PaperEvaluationEntrySource{}, source.Rows...)
	slices.Reverse(reverse)
	again, err := replayCapitalModel(context.Background(), source, reverse, quotes, at.Add(20*time.Second), model, false)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("input order changed clock/ID funding priority", again, err)
	}
}
func TestCapitalReplayPartialBasisAndFullClosure(t *testing.T) {
	source, at, model := capitalFixture(t, 90000, 90000)
	source.Rows = source.Rows[:1]
	quotes := []paperfill.BookCommand{protectiveQuote(at, "entry", 2, 9990, 10000, 5), protectiveQuote(at, "stop", 3, 9400, 9500, 5), protectiveQuote(at, "partial-close", 4, 9300, 9400, 3)}
	got, err := replayCapitalModel(context.Background(), source, source.Rows, quotes, at.Add(15*time.Second), model, false)
	if err != nil || !got.ConservationVerified || got.EndingCashCents != 67900 || got.RemainingCostBasisCents != 20000 || got.RealizedNetCents != -2100 || got.ClosedNetCents != nil || got.NetExcessOverCashCents != nil || got.PassiveBenchmarkAvailable || got.DrawdownAvailable {
		t.Fatal("fabricated closed economics", got, err)
	}
	quotes = append(quotes, protectiveQuote(at, "final-close", 5, 9200, 9300, 2))
	closed, err := replayCapitalModel(context.Background(), source, source.Rows, quotes, at.Add(15*time.Second), model, false)
	if err != nil || closed.RemainingCostBasisCents != 0 || closed.EndingCashCents != 86300 || closed.ClosedNetCents == nil || *closed.ClosedNetCents != -3700 || *closed.NetExcessOverCashCents != -3700 {
		t.Fatal(closed, err)
	}
}
func TestCapitalReplayEnforcesAccountAndGroupLimitsAndExitFunding(t *testing.T) {
	for _, kind := range []string{"capital", "group", "exit_funding"} {
		t.Run(kind, func(t *testing.T) {
			source, at, model := capitalFixture(t, 95000, 95000)
			source.Rows = source.Rows[:1]
			reason := ""
			switch kind {
			case "capital":
				account, _ := paperaccount.Create(source.Policy.Account.ReceiverID, paperaccount.Config{ID: "study", Currency: "USD", OpeningCashCents: 95000, CapitalLimitCents: 70000}, source.Policy.Account.CreatedAt)
				command := source.Policy.Command
				command.Policy.AccountSHA256 = account.SHA256
				source.Policy, _ = allocation.NewPolicyRecord(command, account, source.Policy.RegisteredAt)
				reason = "account_capital_limit_exceeded"
			case "group":
				command := source.Policy.Command
				command.Policy.Groups[0].LimitCents = 70000
				source.Policy, _ = allocation.NewPolicyRecord(command, source.Policy.Account, source.Policy.RegisteredAt)
				reason = "group_budget_exceeded:equity"
			case "exit_funding":

				model.CommissionPerFillCents = 1000
				command := source.Policy.Command
				command.ExecutionModel = &model
				source.Policy, _ = allocation.NewPolicyRecord(command, source.Policy.Account, source.Policy.RegisteredAt)

				c := *source.Rows[0].BaseCost
				c.MaxDebitCents = 88000
				c.ModelSHA256 = workerplan.Hash(model)
				source.Rows[0].BaseCost = &c
				reason = "opening_cash_budget_exceeded"
			}
			got, err := replayCapitalModel(context.Background(), source, source.Rows, nil, at.Add(20*time.Second), model, false)
			if err != nil || got.CumulativeFundingCents != 0 || got.EndingCashCents != 95000 || got.Replay != nil || !slices.Contains(got.Candidates[0].Reasons, reason) || !got.ConservationVerified {
				t.Fatal(got, err)
			}
			if kind == "exit_funding" && got.Candidates[0].ExitShortfallCeilingCents != 7992 {
				t.Fatal("omitted exit shortfall funding", got.Candidates[0])
			}
		})
	}
}
func TestCapitalReplayRefusesMixedModelAndCallerAssertions(t *testing.T) {
	source, at, model := capitalFixture(t, 90000, 90000)
	bad := model
	bad.FeeBPS++
	got, err := replayCapitalModel(context.Background(), source, source.Rows, nil, at.Add(20*time.Second), bad, false)
	if err == nil || got != nil {
		t.Fatal("mixed owner model", got, err)
	}
	s := &entryReplayBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperEvaluationCapitalReplayRoutes(mux, s, WallClock{})
	for _, suffix := range []string{"?cash=caller", "?"} {
		req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/capital-replay"+suffix, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 422 || s.called {
			t.Fatal(w.Code, s.called)
		}
	}
	req := httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/capital-replay", strings.NewReader(`{"admission":true,"snapshot":{}}`))
	req.ContentLength = -1
	req.TransferEncoding = nil
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 422 || s.called {
		t.Fatal(w.Code, s.called)
	}
	req = httptest.NewRequest("GET", "/v1/paper-evaluation-declarations/plan/capital-replay", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 503 || !s.called {
		t.Fatal(w.Code, s.called)
	}
}

func TestCapitalReplayFundsExitFeesAboveSaleProceeds(t *testing.T) {
	source, at, model := capitalFixture(t, 96000, 96000)
	source.Rows = source.Rows[:1]
	model.CommissionPerFillCents = 1000
	command := source.Policy.Command
	command.ExecutionModel = &model
	var err error
	source.Policy, err = allocation.NewPolicyRecord(command, source.Policy.Account, source.Policy.RegisteredAt)
	if err != nil {
		t.Fatal(err)
	}
	cost := *source.Rows[0].BaseCost
	cost.MaxDebitCents = 88000
	cost.ModelSHA256 = workerplan.Hash(model)
	source.Rows[0].BaseCost = &cost
	quotes := []paperfill.BookCommand{protectiveQuote(at, "entry", 2, 9990, 10000, 8), protectiveQuote(at, "gap-stop", 3, 1, 2, 8), protectiveQuote(at, "gap-close", 4, 1, 2, 8)}
	got, err := replayCapitalModel(context.Background(), source, source.Rows, quotes, at.Add(15*time.Second), model, false)
	if err != nil || got.CumulativeFundingCents != 95992 || got.ExitNetProceedsCents != -992 || got.EndingCashCents != 14008 || got.RealizedNetCents != -81992 || !got.ConservationVerified || got.ClosedNetCents == nil {
		t.Fatal("unfunded negative exit proceeds", got, err)
	}
}
