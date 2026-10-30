package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type portfolioHTTPBackend interface {
	ownedDecisionHTTPBackend
	storage.PaperEvaluationDeclarationStore
	storage.PaperEvaluationAssessmentStore
}

func TestPortfolioOwnerRejectsMissingCompiler(t *testing.T) {
	if m, err := storage.NewMemoryWithPortfolioEvaluator(nil); err == nil || m != nil {
		t.Fatal("missing compiler accepted", err)
	}
	if p, err := storage.OpenPostgresWithPortfolioEvaluator(context.Background(), "invalid-without-compiler", nil); err == nil || p != nil {
		t.Fatal("missing compiler reached database construction", err)
	}
}

func TestOwnedPortfolioAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m)
}

// These are manufactured captures submitted through the actual owners and
// public handler. Their prospective labels do not give them qualification credit.
func ownedPortfolioAssessmentHTTP(t *testing.T, s portfolioHTTPBackend, useHoldout ...bool) storage.PaperEvaluationAssessment {
	causal := len(useHoldout) > 3 && useHoldout[3]
	holdout := len(useHoldout) > 0 && useHoldout[0]
	benchmarks := len(useHoldout) > 2 && useHoldout[2]
	delayed := len(useHoldout) > 1 && useHoldout[1]
	var referencePublishedAt time.Time
	t.Helper()
	ctx := context.Background()
	clock := &ownedDecisionClock{at: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(service.HandlerWithClock(s, "portfolio-fixture-token", clock))
	defer server.Close()
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		if text, ok := body.(string); ok {
			raw = []byte(text)
		}
		r, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer portfolio-fixture-token")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(response.Body)
		if response.StatusCode != want {
			t.Fatal(method, path, response.StatusCode, want, out.String())
		}
		return out.Bytes()
	}
	n, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := n.ReceiverID
	d := strategyHTTPDefinition()
	d.Safety.MaxHoldingSeconds = 10
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.CreatePaperAccount(ctx, owner, paperaccount.Config{ID: "portfolio-account", Currency: "USD", OpeningCashCents: 200000, CapitalLimitCents: 200000}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	policy, err := s.RegisterAllocationPolicy(ctx, allocation.PolicyCommand{ReceiverID: owner, AccountID: account.Account.Config.ID, ExecutionModel: &model, Policy: allocation.Policy{ID: "portfolio-policy", AccountSHA256: account.Account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: d.ID, ArtifactSHA256: strategy.Record.DefinitionSHA256, LimitCents: 200000}}, Symbols: []allocation.Symbol{{ID: "SPY", LimitCents: 200000, Groups: []string{}}}, Groups: []allocation.Group{}}}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	start := clock.at.Add(10 * time.Second)
	stress := model
	stress.FeeBPS = 1
	plan := storage.PaperEvaluationDeclarationCommand{ID: "portfolio-plan", ReceiverID: owner, Recipe: storage.PaperEvaluationRecipe, PolicyID: policy.Record.Command.Policy.ID, PolicySHA256: policy.Record.SHA256, Plan: storage.PaperEvaluationPlan{FamilyID: "portfolio-family", Variants: []storage.PaperEvaluationVariant{{ID: d.ID, SHA256: strategy.Record.SHA256}}, WindowStart: start, WindowEnd: start.Add(40 * time.Second), OutcomeDeadline: start.Add(110 * time.Second), MinIndependentDecisions: 2, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 60, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress}}
	if holdout {
		plan.Recipe = storage.PaperHoldoutEvaluationRecipe
		plan.Plan.Holdout = &storage.PaperEvaluationHoldoutPlan{Method: storage.PaperHoldoutMethod, SplitAt: start.Add(20 * time.Second), EmbargoSeconds: 2, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	}
	if benchmarks {
		plan.Recipe = storage.PaperBenchmarkEvaluationRecipe
		plan.Plan.BenchmarkMethod = papereconomics.BenchmarkMethod
	}
	if causal {
		plan.Recipe = storage.PaperCausalEvaluationRecipe
		plan.Plan.FeatureCausalityMethod = storage.PaperFeatureCausalityMethod
	}
	var declaration storage.PaperEvaluationDeclarationReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", plan, 201), &declaration); err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "portfolio-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-portfolio", AccessRef: "manufactured-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	publish := func(id string, bid, ask int64, calendar bool) {
		t.Helper()
		r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: id, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.at, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, RawRef: "journal:journal-portfolio:capture-" + id + "-r1.json", MaxQuoteAgeSeconds: 60}
		r.Original = fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%d,"bp":%d,"as":10,"bs":10}}}`, clock.at.Format(time.RFC3339Nano), ask, bid)
		if calendar {
			r.Capability, r.Feed, r.Start, r.End = "market_calendar", "regular_session", "2026-10-21", "2026-10-21"
			r.Original = `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`
		}
		r.SHA256 = alpaca.Digest(r.Original)
		inputs, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range inputs.Inputs {
			if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock.Now); err != nil {
				t.Fatal(err)
			}
		}
		if delayed {
			clock.at = clock.at.Add(100 * time.Millisecond)
		}
		request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": owner, "fence": fence, "recording": r}, 200)
		if id == "reference" {
			referencePublishedAt = clock.at
		}
	}
	if causal {
		clock.at = clock.at.Add(time.Second)
		var empty storage.PaperEvaluationAssessmentReceipt
		command := storage.PaperEvaluationAssessmentCommand{ID: "causal-empty", ReceiverID: owner, DeclarationID: declaration.Record.Command.ID, DeclarationSHA256: declaration.Record.SHA256}
		if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", command, 201), &empty); err != nil {
			t.Fatal(err)
		}
		for _, check := range empty.Record.Checks {
			if check.Name == "leakage" && check.Status != "unavailable" {
				t.Fatal("empty source cut passed causality", check)
			}
		}
	}
	clock.at = start
	publish("calendar", 99, 100, true)
	publish("reference", 99, 100, false)
	clock.at = start.Add(time.Second)
	var decision storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", storage.PaperDecisionCommand{ID: "portfolio-decision", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, 201), &decision); err != nil || decision.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal(decision, err)
	}
	for _, q := range []struct{ seconds, bid, ask int64 }{{2, 99, 100}, {4, 94, 95}, {5, 93, 94}, {12, 95, 96}} {
		clock.at = start.Add(time.Duration(q.seconds) * time.Second)
		publish(fmt.Sprintf("quote-%d", q.seconds), q.bid, q.ask, false)
	}
	request("POST", "/v1/paper-outcomes", storage.PaperOutcomeCommand{ID: "portfolio-outcome", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, 201)
	clock.at = declaration.Record.Command.Plan.OutcomeDeadline.Add(time.Second)
	c := storage.PaperEvaluationAssessmentCommand{ID: "portfolio-assessment", ReceiverID: owner, DeclarationID: declaration.Record.Command.ID, DeclarationSHA256: declaration.Record.SHA256}
	raw, _ := json.Marshal(c)
	request("POST", "/v1/paper-evaluation-assessments", strings.Replace(string(raw), `"id":`, `"portfolio":{},"id":`, 1), 422)
	var got storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 201), &got); err != nil {
		t.Fatal(err)
	}
	r := got.Record
	wantSchema, wantRecipe := "mantik-owned-paper-evaluation-assessment-v6", storage.PaperPortfolioAssessmentRecipe
	if holdout {
		wantSchema, wantRecipe = "mantik-owned-paper-evaluation-assessment-v7", storage.PaperHoldoutAssessmentRecipe
	}
	if benchmarks {
		wantSchema, wantRecipe = "mantik-owned-paper-evaluation-assessment-v8", storage.PaperBenchmarkAssessmentRecipe
	}
	if causal {
		wantSchema, wantRecipe = "mantik-owned-paper-evaluation-assessment-v9", storage.PaperCausalAssessmentRecipe
	}
	if r.Schema != wantSchema || r.Recipe != wantRecipe || r.Portfolio == nil || len(r.Portfolio.Variants) != 1 || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
	if holdout {
		expectedDeclaration := "mantik-owned-paper-evaluation-declaration-v4"
		if benchmarks {
			expectedDeclaration = "mantik-owned-paper-evaluation-declaration-v5"
		}
		if causal {
			expectedDeclaration = "mantik-owned-paper-evaluation-declaration-v6"
		}
		if declaration.Record.Schema != expectedDeclaration || r.Holdout == nil || len(r.Holdout.Variants) != 1 || r.Holdout.Variants[0].TrainingOccurrenceUpperBound != 1 || r.Holdout.Variants[0].HoldoutOccurrenceUpperBound != 0 || r.Holdout.Variants[0].Status != "failed" || r.Holdout.IndependenceEstablished || r.Holdout.OutOfSamplePerformanceAvailable {
			t.Fatal("owner-derived holdout partition", r.Holdout)
		}
	}
	if delayed && !r.Holdout.Rows[0].FeatureAvailableAt.Equal(referencePublishedAt) {
		t.Fatalf("holdout availability predates owner publication: retained=%s published=%s", r.Holdout.Rows[0].FeatureAvailableAt, referencePublishedAt)
	}
	if causal {
		found := false
		for _, check := range r.Checks {
			if check.Name == "leakage" {
				found = true
				if check.Status != "passed" || check.ReasonCode != "owner_replayed_stateless_native_features_available_at_finalization" {
					t.Fatal("causal native feature did not pass its declared check", check)
				}
			}
		}
		if !found || r.Status != "blocked" {
			t.Fatal("causality erased insufficient holdout evidence", r)
		}
	}
	v := r.Portfolio.Variants[0]
	if benchmarks {
		if r.Portfolio.Method != storage.PaperBenchmarkPortfolioMethod || v.BaseBenchmarks == nil || v.StressBenchmarks == nil || v.BaseBenchmarks.CashEndingCents != 200000 || v.BaseBenchmarks.TotalReturnComparisonAvailable || v.BaseBenchmarks.CorporateCoverageReconciled || v.BaseBenchmarks.AdmissionAvailable || v.BaseBenchmarks.ExecutionAvailable || v.BaseBenchmarks.QualificationCredit {
			t.Fatal("owner benchmark envelope", v)
		}
		found := false
		for _, check := range r.Checks {
			if check.Name == "benchmark_comparison" {
				found = true
				if check.Status != "unavailable" || check.ReasonCode != "observed_price_benchmarks_do_not_establish_total_return_or_corporate_coverage" {
					t.Fatal("observed prices promoted to total-return acceptance", check)
				}
			}
		}
		if !found {
			t.Fatal("mandatory benchmark check missing")
		}

	}

	if v.Base == nil || v.Stress == nil || v.Base.EndingCashCents != 193000 || v.Base.RealizedNetCents != -7000 || v.Base.ClosedNetCents == nil || *v.Base.ClosedNetCents != -7000 || v.Base.ObservedMaximumDrawdownCents != 7000 || v.Base.TotalReturnAvailable || v.Stress.EndingCashCents >= v.Base.EndingCashCents {
		t.Fatal("owner-pinned known economics", v)
	}
	clock.at = clock.at.Add(time.Second)
	publish("later-source", 90, 91, false)
	clock.panicRead = true
	var again storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 200), &again); err != nil || !again.Duplicate || !reflect.DeepEqual(again.Record, r) {
		t.Fatal("later source rewrote immutable economics", again, err)
	}
	var read storage.PaperEvaluationAssessment
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments/"+c.ID, nil, 200), &read); err != nil || !reflect.DeepEqual(read, r) {
		t.Fatal("read sampled live clock or changed source cut", err)
	}
	var retries sync.WaitGroup
	for range 4 {
		retries.Go(func() {
			var duplicate storage.PaperEvaluationAssessmentReceipt
			if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 200), &duplicate); err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, r) {
				t.Error("concurrent retry changed retained accounting", err)
			}
		})
	}
	retries.Wait()
	if holdout {
		changed := plan
		profile := *plan.Plan.Holdout
		profile.MinimumHoldoutOccurrences++
		changed.Plan.Holdout = &profile
		request("POST", "/v1/paper-evaluation-declarations", changed, 409)
	}
	clock.panicRead = false
	clock.at = clock.at.Add(time.Second)
	if err := service.RunPaperEvaluationCycle(ctx, s, clock); err != nil {
		t.Fatal(err)
	}
	var all storage.PaperEvaluationAssessmentCatalog
	expectedAssessments := 2
	if causal {
		expectedAssessments++
	}
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments", nil, 200), &all); err != nil || len(all.Records) != expectedAssessments {
		t.Fatal(all, err)
	}
	for _, a := range all.Records {
		if a.Command.ID != c.ID && a.Command.ID != "causal-empty" && (a.Recipe != wantRecipe || a.Command.ID == service.AutomaticPaperEvaluationID(declaration.Record) || !strings.HasPrefix(a.Command.ID, service.AutomaticPaperEvaluationPrefix)) {
			t.Fatal("worker reused legacy recipe identity", a)
		}
	}
	clock.at = clock.at.Add(time.Second)
	var comparison storage.OwnedAllocationReceipt
	if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", storage.OwnedAllocationCommand{ID: "portfolio-comparison", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}, 201), &comparison); err != nil || len(comparison.Record.AdmissionChecks) != 1 || comparison.Record.AdmissionChecks[0].AdmissionAvailable || comparison.Record.ExecutionAvailable || comparison.Record.QualificationCredit || comparison.Record.Entry.Plan == nil || comparison.Record.Entry.Plan.Decisions[0].Status != "reject" {
		t.Fatal("portfolio math changed allocation authority", comparison, err)
	}
	request("GET", "/v1/paper-attribution", nil, 200)
	return r
}

func TestPostgresContractsOwnedPortfolioAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, false)
}
func TestPostgresContractsOwnedHoldoutAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, true)
}
func TestOwnedHoldoutAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m, true)
}
func postgresPortfolioAssessmentHTTP(t *testing.T, holdout bool, benchmark ...bool) {
	benchmarks := len(benchmark) > 0 && benchmark[0]
	causal := len(benchmark) > 1 && benchmark[1]
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	db := "mantik_test_portfolio_http_" + strings.ToLower(rand.Text())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{db}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + db
	s, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	expected := ownedPortfolioAssessmentHTTP(t, s, holdout, holdout, benchmarks, causal)
	s.Close()
	legacy, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	legacyServer := httptest.NewServer(service.HandlerWithClock(legacy, "legacy-token", &ownedDecisionClock{panicRead: true}))
	legacyRequest, _ := http.NewRequestWithContext(ctx, "GET", legacyServer.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	legacyRequest.Header.Set("Authorization", "Bearer legacy-token")
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	var legacyError map[string]string
	legacyDecode := json.NewDecoder(legacyResponse.Body).Decode(&legacyError)
	legacyResponse.Body.Close()
	legacyServer.Close()
	legacy.Close()
	if legacyResponse.StatusCode != http.StatusUnprocessableEntity || legacyDecode != nil || legacyError["error"] != "invalid owned paper evaluation assessment" {
		t.Fatal("legacy owner did not reject unconfigured v6 replay", legacyResponse.StatusCode, legacyError, legacyDecode)
	}
	reopened, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(service.HandlerWithClock(reopened, "reopen-token", &ownedDecisionClock{panicRead: true}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	req.Header.Set("Authorization", "Bearer reopen-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var actual storage.PaperEvaluationAssessment
	if err = json.NewDecoder(response.Body).Decode(&actual); err != nil || response.StatusCode != 200 || !reflect.DeepEqual(actual, expected) {
		t.Fatal("restart lost original economics", actual, err)
	}
	uRecord := actual
	uRecord.SHA256 = ""
	if workerplan.Hash(uRecord) != expected.SHA256 {
		t.Fatal("restart record hash changed")
	}

	// A coherently rehashed arithmetic edit is still not an owner-derived
	// result. Catalogue replay must rerun the compiler against actual sources.
	data, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close(ctx)
	var original []byte
	if err := data.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_assessments WHERE id=$1", expected.Command.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var changed storage.PaperEvaluationAssessment
	if err := json.Unmarshal(original, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Portfolio.Variants[0].Base.EndingCashCents++
	changed.Portfolio.Variants[0].Base.SHA256 = ""
	changed.Portfolio.Variants[0].Base.SHA256 = workerplan.Hash(*changed.Portfolio.Variants[0].Base)
	changed.Portfolio.SHA256 = ""
	changed.Portfolio.SHA256 = workerplan.Hash(*changed.Portfolio)
	changed.SHA256 = ""
	changed.SHA256 = workerplan.Hash(changed)
	corrupt, _ := json.Marshal(changed)
	if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", corrupt, expected.Command.ID); err != nil {
		t.Fatal(err)
	}
	denied, err := http.DefaultClient.Do(req.Clone(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var deniedError map[string]string
	deniedDecode := json.NewDecoder(denied.Body).Decode(&deniedError)
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnprocessableEntity || deniedDecode != nil || deniedError["error"] != "invalid owned paper evaluation assessment" {
		t.Fatal("fabricated accounting did not fail owner replay", denied.StatusCode, deniedError, deniedDecode)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", original, expected.Command.ID); err != nil {
		t.Fatal(err)
	}
	if holdout {
		for _, tc := range []struct {
			name   string
			change func(*storage.PaperEvaluationAssessment)
		}{
			{"changed-feature-publication", func(r *storage.PaperEvaluationAssessment) {
				r.Holdout.Rows[0].FeatureAvailableAt = r.Holdout.Rows[0].FeatureAvailableAt.Add(-50 * time.Millisecond)
			}},
			{"changed-leakage-check", func(r *storage.PaperEvaluationAssessment) {
				for i := range r.Checks {
					if r.Checks[i].Name == "leakage" {
						r.Checks[i].Status = "passed"
						r.Checks[i].ReasonCode = "fabricated"
					}
				}
			}},
			{"causality-schema-downgrade", func(r *storage.PaperEvaluationAssessment) {
				if causal {
					r.Schema = "mantik-owned-paper-evaluation-assessment-v8"
					r.Recipe = storage.PaperBenchmarkAssessmentRecipe
				} else {
					r.Holdout.Rows[0].HorizonAt = r.Holdout.Rows[0].HorizonAt.Add(time.Second)
				}
			}},
			{"changed-horizon", func(r *storage.PaperEvaluationAssessment) {
				r.Holdout.Rows[0].HorizonAt = r.Holdout.Rows[0].HorizonAt.Add(time.Second)
			}},
			{"legacy-downgrade", func(r *storage.PaperEvaluationAssessment) {
				r.Schema = "mantik-owned-paper-evaluation-assessment-v6"
				r.Recipe = storage.PaperPortfolioAssessmentRecipe
				r.Holdout = nil
			}},
			{"omit-portfolio", func(r *storage.PaperEvaluationAssessment) { r.Portfolio = nil }},
			{"benchmark-substitution", func(r *storage.PaperEvaluationAssessment) {
				if benchmarks {
					b := r.Portfolio.Variants[0].BaseBenchmarks
					b.CashEndingCents++
					b.SHA256 = ""
					b.SHA256 = workerplan.Hash(*b)
					r.Portfolio.SHA256 = ""
					r.Portfolio.SHA256 = workerplan.Hash(*r.Portfolio)
				} else {
					r.Holdout.Rows[0].HorizonAt = r.Holdout.Rows[0].HorizonAt.Add(time.Second)
				}
			}},
			{"benchmark-schema-downgrade", func(r *storage.PaperEvaluationAssessment) {
				if benchmarks {
					r.Schema = "mantik-owned-paper-evaluation-assessment-v7"
					r.Recipe = storage.PaperHoldoutAssessmentRecipe
				} else {
					r.Holdout.Rows[0].HorizonAt = r.Holdout.Rows[0].HorizonAt.Add(time.Second)
				}
			}},
		} {
			var forged storage.PaperEvaluationAssessment
			if err := json.Unmarshal(original, &forged); err != nil {
				t.Fatal(err)
			}
			tc.change(&forged)
			if forged.Holdout != nil {
				forged.Holdout.SHA256 = ""
				forged.Holdout.SHA256 = workerplan.Hash(*forged.Holdout)
			}
			forged.SHA256 = ""
			forged.SHA256 = workerplan.Hash(forged)
			fabricated, _ := json.Marshal(forged)
			if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", fabricated, expected.Command.ID); err != nil {
				t.Fatal(err)
			}
			rejected, err := http.DefaultClient.Do(req.Clone(ctx))
			if err != nil {
				t.Fatal(err)
			}
			rejected.Body.Close()
			if rejected.StatusCode != 422 {
				t.Fatal(tc.name, "self-rehashed alteration accepted", rejected.StatusCode)
			}
			if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", original, expected.Command.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	migration := 75
	if holdout {
		migration = 76
	}
	if benchmarks {
		migration = 77
	}
	if causal {
		migration = 80
	}
	var hash string
	if err := data.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=$1", migration).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=$1", migration); err != nil {
		t.Fatal(err)
	}
	if broken, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio); err == nil || broken != nil {
		t.Fatal("damaged migration accepted", err)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=$2", hash, migration); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Exec(ctx, fmt.Sprintf(`DELETE FROM mantik_schema WHERE version>=%d;
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'pre-portfolio-migration';
CREATE FUNCTION reject_portfolio_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=%d THEN RAISE EXCEPTION 'manufactured migration failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_portfolio_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_portfolio_migration()`, migration, migration)); err != nil {
		t.Fatal(err)
	}
	if failed, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio); err == nil || failed != nil {
		t.Fatal("failed migration acknowledged", err)
	}
	var comment string
	var retained []byte
	if err := data.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_assessments'::regclass)").Scan(&comment); err != nil || comment != "pre-portfolio-migration" {
		t.Fatal("migration rollback changed prior metadata", comment, err)
	}
	if err := data.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_assessments WHERE id=$1", expected.Command.ID).Scan(&retained); err != nil || !bytes.Equal(retained, original) {
		t.Fatal("migration rewrote retained economics", err)
	}
	if _, err := data.Exec(ctx, "DROP TRIGGER reject_portfolio_migration ON mantik_schema; DROP FUNCTION reject_portfolio_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()

	// Restore a closed test-owned database into another isolated database. This
	// exercises actual v6 source/assessment custody after a database copy, without
	// changing the original records or claiming funded-account recovery.
	server.Close()
	reopened.Close()
	_ = data.Close(ctx)
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{db}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
		t.Fatal(err)
	}
	restoredDB := db + "_restore"
	if len(restoredDB) > 63 {
		t.Fatal("fixture database name exceeds PostgreSQL identifier bound")
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" TEMPLATE "+pgx.Identifier{db}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + restoredDB
	restored, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredServer := httptest.NewServer(service.HandlerWithClock(restored, "restored-token", &ownedDecisionClock{panicRead: true}))
	defer restoredServer.Close()
	restoredRequest, _ := http.NewRequestWithContext(ctx, "GET", restoredServer.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	restoredRequest.Header.Set("Authorization", "Bearer restored-token")
	restoredResponse, err := http.DefaultClient.Do(restoredRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredResponse.Body.Close()
	var restoredRecord storage.PaperEvaluationAssessment
	if err := json.NewDecoder(restoredResponse.Body).Decode(&restoredRecord); err != nil || restoredResponse.StatusCode != 200 || !reflect.DeepEqual(restoredRecord, expected) {
		t.Fatal("restored database lost source-bound accounting", restoredRecord, err)
	}
}

func TestHoldoutDelayedPublicationAvailability(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m, true, true)
}

func TestOwnedBenchmarkAssessmentHTTP(t *testing.T) {
	s, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, s, true, true, true)
}
func TestPostgresContractsOwnedBenchmarkAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, true, true)
}

func TestOwnedCausalFeatureAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m, true, true, true, true)
}
func TestPostgresContractsOwnedCausalFeatureAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, true, true, true)
}
