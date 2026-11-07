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
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/corporatecoverage"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/referencetest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// The positive fixture's sessions: Monday to Friday, 2026-11-02 to 11-06.
// Eastern time is UTC-5 after 2026-11-01, so 15:00 UTC is 10:00 in New York.
var positiveDays = []string{"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06"}

func positiveCoveragePlan() *storage.PaperEvaluationCoveragePlan {
	return &storage.PaperEvaluationCoveragePlan{Method: corporatecoverage.Method, ProcessLagDays: 10, Instruments: []storage.PaperEvaluationCoverageInstrument{{
		Symbol: "SPY", AlpacaJournalID: "journal-positive", AlpacaRootCollectionIDs: []string{"positive-alpaca"},
		PolygonJournalID: "polygon-journal-positive", PolygonDividendsCollectionID: "positive-dividends", PolygonSplitsCollectionID: "positive-splits",
		IssuerJournalID: "issuer-journal-positive", IssuerCollectionID: "positive-workbook",
	}}}
}

// retainPositiveCoverage retains manufactured sources that report no action
// between 2026-11-02 and 11-06. The issuer workbook lists a later ex-date, so
// the empty window is documented rather than inferred.
func retainPositiveCoverage(t testing.TB, store any, owner string, clock *ownedDecisionClock) {
	t.Helper()
	s, ok := store.(coverageSourceBackend)
	if !ok {
		t.Fatal("backend lacks journal source custody")
	}
	ctx := context.Background()
	closed := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	received := closed.Add(time.Hour)
	raw := `{"corporate_actions":{"cash_dividends":[]},"next_page_token":null}`
	r := alpacarecord.Recording{Schema: alpacarecord.RecordingSchema, Currency: "USD", CaptureID: "positive-alpaca", Revision: 1, Capability: "corporate_actions", Feed: alpacarecord.CorporateActionFeed, Symbols: []string{"SPY"}, Start: "2026-11-02", End: "2026-11-16", ReceivedAt: received, EvidenceClass: "prospective_market_unqualified", AccessRef: "manufactured-access", RawRef: "journal:journal-positive:capture-positive-alpaca-r1.json", Original: raw, SHA256: alpacarecord.Digest(raw), MaxQuoteAgeSeconds: 60}
	a := alpacarecord.CorporateActionAcquisition{Schema: "mantik-journal-corporate-acquisition-v1", Method: "retained-intent-attempts-capture-completion-v1", JournalID: "journal-positive", CollectionID: r.CaptureID, IntentSHA256: strings.Repeat("a", 64), CompletionSHA256: strings.Repeat("b", 64), CaptureRef: r.RawRef, CaptureSHA256: strings.Repeat("c", 64), RecordingSHA256: workerplan.Hash(r), SourceSHA256: r.SHA256, RequestedAt: received.Add(-time.Second), ResponseAt: received, CompletedAt: received.Add(time.Second), AvailableAt: received.Add(time.Second), Attempts: []alpacarecord.CorporateActionAcquisitionAttempt{{Number: 1, StartSHA256: strings.Repeat("d", 64), ResponseSHA256: strings.Repeat("e", 64), StartedAt: received.Add(-time.Nanosecond), EndedAt: received, State: "response", HTTPStatus: 200}}, Authority: "journal_owned_recorded_acquisition_only"}
	a.SHA256 = workerplan.Hash(a)
	clock.at = received.Add(2 * time.Second)
	if _, err := s.RetainJournalCorporateActionSource(ctx, storage.CorporateActionSourceCommand{ID: "positive-alpaca", ReceiverID: owner, Recording: r}, a, clock.Now); err != nil {
		t.Fatal(err)
	}
	requested := closed.Add(2 * time.Hour)
	p, err := polygonref.OpenJournal(filepath.Join(t.TempDir(), "polygon"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.ID = "polygon-journal-positive"
	dividends := polygonref.Spec{ID: "positive-dividends", Method: polygonref.MethodDividends, Symbol: "SPY", Start: "2026-11-01", End: "2026-11-30", AccessRef: "manufactured"}
	splits := dividends
	splits.ID, splits.Method = "positive-splits", polygonref.MethodSplits
	referencetest.PolygonCollection(t, p, dividends, requested, []byte(`{"results":[],"status":"OK"}`))
	referencetest.PolygonCollection(t, p, splits, requested, []byte(`{"results":[],"status":"OK"}`))
	i, err := issuerdoc.OpenJournal(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	i.ID = "issuer-journal-positive"
	referencetest.IssuerCollection(t, i, issuerdoc.Spec{ID: "positive-workbook", Method: issuerdoc.MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured"}, requested, referencetest.SPDRWorkbook(t,
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: "10/21/2026", RecordDate: "10/21/2026", PayableDate: "10/31/2026", Dividend: "1.903516"},
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: "12/18/2026", RecordDate: "12/18/2026", PayableDate: "01/29/2027", Dividend: "1.888834"},
	))
	clock.at = requested.Add(time.Minute)
	for _, id := range []string{"positive-dividends", "positive-splits", "positive-workbook"} {
		c := storage.CorporateReferenceSourceCommand{ID: "reference-" + id, ReceiverID: owner, Kind: storage.CorporateReferencePolygon}
		if id == "positive-workbook" {
			_, a, raw, err := i.Retained(id)
			if err != nil {
				t.Fatal(err)
			}
			c.Kind, c.Issuer, c.Originals = storage.CorporateReferenceIssuer, &a, [][]byte{raw}
		} else {
			_, a, bodies, err := p.Retained(id)
			if err != nil {
				t.Fatal(err)
			}
			c.Polygon, c.Originals = &a, bodies
		}
		clock.at = clock.at.Add(time.Second)
		if _, err := s.RetainCorporateReferenceSource(ctx, c, clock.Now); err != nil {
			t.Fatal(id, err)
		}
	}
}

// positiveEvaluationHTTP drives a known positive result through the public
// handler and actual owners: five sessions where a threshold strategy and two
// bracketing neighbors fill at a $99 ask and exit after ten seconds at a $100 bid,
// while a late $95 bid leaves the passive benchmark behind. The assessment
// must complete for the center strategy. A fresh decision on 2026-11-09 then
// receives owner-issued eligibility and a capital reservation. These are
// manufactured captures with no qualification credit.
// failCommit, when set, makes the store's next commits fail (true) or succeed
// again (false); the Postgres variant uses a deferred constraint trigger.
// positiveShape scales the fixture for BenchmarkOwnedPositiveEvaluation. The
// zero value is the known positive result.
type positiveShape struct {
	sessions    int  // leading positiveDays to use; 0 uses all
	extraQuotes int  // quotes per session after the exit, adding records only
	assessOnly  bool // return after the assessment request
}

func positiveEvaluationHTTP(t testing.TB, s portfolioHTTPBackend, failCommit func(bool), shape positiveShape) (storage.PaperEvaluationAssessment, storage.OwnedAllocationCatalog) {
	t.Helper()
	days := positiveDays
	if shape.sessions > 0 {
		days = positiveDays[:shape.sessions]
	}
	ctx := context.Background()
	clock := &ownedDecisionClock{at: time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.HandlerWithClock(s, "positive-fixture-token", clock)))
	defer server.Close()
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer positive-fixture-token")
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
	d.Safety.MaxNotionalCents += 1000
	declared := []storage.PaperEvaluationVariant{}
	rules := []allocation.Strategy{}
	for _, v := range []struct {
		id    string
		entry int64
	}{{d.ID, d.EntryAtOrBelowCents}, {d.ID + "-above", d.EntryAtOrBelowCents + 100}, {d.ID + "-below", d.EntryAtOrBelowCents - 100}} {
		def := d
		def.ID, def.EntryAtOrBelowCents = v.id, v.entry
		r, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		declared = append(declared, storage.PaperEvaluationVariant{ID: v.id, SHA256: r.Record.SHA256})
		rules = append(rules, allocation.Strategy{ID: v.id, ArtifactSHA256: r.Record.DefinitionSHA256, LimitCents: 200000})
	}
	account, err := s.CreatePaperAccount(ctx, owner, paperaccount.Config{ID: "positive-account", Currency: "USD", OpeningCashCents: 200000, CapitalLimitCents: 200000}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	policy, err := s.RegisterAllocationPolicy(ctx, allocation.PolicyCommand{ReceiverID: owner, AccountID: account.Account.Config.ID, ExecutionModel: &model, Policy: allocation.Policy{ID: "positive-policy", AccountSHA256: account.Account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: rules, Symbols: []allocation.Symbol{{ID: "SPY", LimitCents: 200000, Groups: []string{}}}, Groups: []allocation.Group{}}}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	stress := model
	stress.FeeBPS = 1
	windowStart := time.Date(2026, 11, 2, 14, 30, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 11, 6, 21, 0, 0, 0, time.UTC)
	plan := storage.PaperEvaluationDeclarationCommand{ID: "positive-plan", ReceiverID: owner, Recipe: storage.PaperDrawdownEvaluationRecipe, PolicyID: policy.Record.Command.Policy.ID, PolicySHA256: policy.Record.SHA256, Plan: storage.PaperEvaluationPlan{
		FamilyID: "positive-family", Variants: declared, WindowStart: windowStart, WindowEnd: windowEnd, OutcomeDeadline: windowEnd.Add(time.Hour), MinIndependentDecisions: 2, MaximumDrawdownCents: 2000, AdmissionTTLSeconds: 86400, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress,
		Holdout:                &storage.PaperEvaluationHoldoutPlan{Method: storage.PaperHoldoutMethod, SplitAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), EmbargoSeconds: 2, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2},
		BenchmarkMethod:        papereconomics.BenchmarkMethod,
		FeatureCausalityMethod: storage.PaperFeatureCausalityMethod,
		SampleDesign:           &storage.PaperEvaluationSampleDesignPlan{Method: storage.PaperDisjointSampleMethod, MinimumDisjointOccurrences: 2},
		SearchMultiplicity:     &storage.PaperEvaluationSearchPlan{Method: storage.PaperSearchHistoryMethod, MaximumSearchedStrategies: len(declared)},
		CorporateCoverage:      positiveCoveragePlan(),
		TotalReturnMethod:      papereconomics.TotalReturnMethod,
		Sensitivity:            &storage.PaperEvaluationSensitivityPlan{Method: storage.PaperThresholdSensitivityMethod, Neighborhoods: []storage.PaperEvaluationNeighborhood{{StrategyID: d.ID, NeighborIDs: []string{declared[1].ID, declared[2].ID}}}},
		StatisticalTests:       &storage.PaperEvaluationStatisticalPlan{Method: storage.PaperStatisticalTestMethod, MinimumTradingDays: 5, AlphaBasisPoints: 1000},
		DrawdownMethod:         storage.PaperDrawdownMethod,
	}}
	var declaration storage.PaperEvaluationDeclarationReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", plan, 201), &declaration); err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "positive-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-positive-quotes", AccessRef: "manufactured-only", LeaseSeconds: 300}
	if _, err := s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, clock.Now); err != nil {
		t.Fatal(err)
	}
	// Leases last at most 300 s, so each session claims a new generation.
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker"}
	claim := func() {
		t.Helper()
		fence.Generation++
		if _, err := s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: fmt.Sprintf("claim-%d", fence.Generation), Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(id string, r alpaca.Recording) {
		t.Helper()
		r.Schema, r.Currency, r.CaptureID, r.Revision, r.ReceivedAt, r.EvidenceClass, r.AccessRef, r.MaxQuoteAgeSeconds = alpaca.RecordingSchema, "USD", id, 1, clock.at, reg.EvidenceClass, reg.AccessRef, 60
		r.RawRef = "journal:journal-positive-quotes:capture-" + id + "-r1.json"
		r.SHA256 = alpaca.Digest(r.Original)
		inputs, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(id, err)
		}
		for _, input := range inputs.Inputs {
			if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock.Now); err != nil {
				t.Fatal(id, err)
			}
		}
		// Publication follows receipt, as the causality method requires.
		clock.at = clock.at.Add(100 * time.Millisecond)
		request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": owner, "fence": fence, "recording": r}, 200)
	}
	calendar := func(id string, days ...string) {
		rows := []string{}
		for _, day := range days {
			rows = append(rows, fmt.Sprintf(`{"date":%q,"open":"09:30","close":"16:00"}`, day))
		}
		publish(id, alpaca.Recording{Capability: "market_calendar", Feed: "regular_session", Symbols: []string{"SPY"}, Start: days[0], End: days[len(days)-1], Original: "[" + strings.Join(rows, ",") + "]"})
	}
	quote := func(id string, bid, ask int64) {
		publish(id, alpaca.Recording{Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Original: fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%d,"bp":%d,"as":10,"bs":10}}}`, clock.at.Format(time.RFC3339Nano), ask, bid)})
	}
	decide := func(id, strategy string) storage.PaperDecisionReceipt {
		t.Helper()
		var sha string
		for _, v := range declared {
			if v.ID == strategy {
				sha = v.SHA256
			}
		}
		var out storage.PaperDecisionReceipt
		if err := json.Unmarshal(request("POST", "/v1/paper-decisions", storage.PaperDecisionCommand{ID: id, ReceiverID: owner, StrategyID: strategy, StrategySHA256: sha, RegistrationID: reg.ID}, 201), &out); err != nil || out.Record.Evaluation.Action != "entry_candidate" {
			t.Fatal(id, out.Record.Evaluation, err)
		}
		return out
	}

	for i, day := range days {
		session, _ := time.Parse(time.DateOnly, day)
		at := session.Add(15 * time.Hour)
		// Calendar captures go stale after a day, so each session has its own.
		clock.at = at.Add(-2 * time.Minute)
		claim()
		calendar(fmt.Sprintf("calendar-%d", i), day)
		clock.at = at
		quote(fmt.Sprintf("entry-%d", i), 98, 99)
		clock.at = at.Add(time.Second)
		decisions := []storage.PaperDecisionReceipt{}
		for _, v := range declared {
			decisions = append(decisions, decide(fmt.Sprintf("day%d-%s", i, v.ID), v.ID))
		}
		// Orders fill on a later ask at or below every threshold, then the
		// ten-second holding limit exits on the first quote after it.
		for _, q := range []struct{ offset, bid, ask int64 }{{2, 98, 99}, {5, 99, 100}, {13, 100, 101}} {
			clock.at = at.Add(time.Duration(q.offset) * time.Second)
			quote(fmt.Sprintf("quote-%d-%d", i, q.offset), q.bid, q.ask)
		}
		for j := range shape.extraQuotes {
			clock.at = at.Add(14*time.Second + time.Duration(j)*200*time.Millisecond)
			quote(fmt.Sprintf("extra-%d-%d", i, j), 100, 101)
		}
		if i == len(days)-1 {
			// The passive book is still open at the cut; mark it lower.
			clock.at = at.Add(20 * time.Second)
			quote("passive-mark", 95, 96)
		}
		clock.at = at.Add(21 * time.Second)
		for _, decision := range decisions {
			request("POST", "/v1/paper-outcomes", storage.PaperOutcomeCommand{ID: decision.Record.Command.ID + "-outcome", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, 201)
		}
	}
	retainPositiveCoverage(t, s, owner, clock)

	// Assess on the next session day so the admission is still valid there.
	clock.at = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	c := storage.PaperEvaluationAssessmentCommand{ID: "positive-assessment", ReceiverID: owner, DeclarationID: declaration.Record.Command.ID, DeclarationSHA256: declaration.Record.SHA256}
	var got storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 201), &got); err != nil {
		t.Fatal(err)
	}
	r := got.Record
	if shape.assessOnly {
		return r, storage.OwnedAllocationCatalog{}
	}
	if r.Status != "complete" || !r.EvaluationComplete || len(r.CompleteStrategyIDs) != 1 || r.CompleteStrategyIDs[0] != d.ID || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		for _, check := range r.Checks {
			t.Log(check.StrategyID, check.Name, check.Status, check.ReasonCode)
		}
		if r.StatisticalTests != nil {
			for _, v := range r.StatisticalTests.Variants {
				t.Log(v.StrategyID, v.TradingDays, v.PositiveDays, deref(v.PValue))
				for _, u := range v.Units {
					t.Log("  unit", u.DecisionID, u.TradingDay, deref(u.StressNetCents), u.Status)
				}
			}
			for _, v := range r.Portfolio.Variants {
				for _, d := range v.Decisions {
					t.Log("  decision", v.StrategyID, d.DecisionID, d.Status, deref(d.BaseNetCents), deref(d.StressNetCents))
				}
			}
		}
		t.Fatal("known positive evaluation did not complete", r.Status, r.CompleteStrategyIDs)
	}
	for _, check := range r.Checks {
		if check.StrategyID == d.ID && check.Status != "passed" && check.Status != "not_applicable" {
			t.Fatal("center check not passed", check)
		}
	}

	// Known results: each decision gains (100 - 99) x 10 shares = 1000 cents;
	// the stress model's 1 bp fee costs 10 cents a side. Finite capital funds
	// the first two decisions of each variant book. The passive book is marked
	// down to the $95 bid at the cut.
	for _, v := range r.Portfolio.Variants {
		if deref(v.BaseTotalReturn.Strategy.NetCents) != "2000" || deref(v.StressTotalReturn.Strategy.NetCents) != "1960" || passiveNet(v.BaseTotalReturn) != "observed_total_return -8000" || len(v.Decisions) != len(positiveDays) {
			t.Fatal("variant book", v.StrategyID, deref(v.BaseTotalReturn.Strategy.NetCents), deref(v.StressTotalReturn.Strategy.NetCents), passiveNet(v.BaseTotalReturn))
		}
		for _, decision := range v.Decisions {
			if decision.Status != "observed_total_return" || deref(decision.BaseNetCents) != "1000" || deref(decision.StressNetCents) != "980" {
				t.Fatal("standalone decision", decision)
			}
		}
	}
	// A fill at $99 marked at the $98 bid draws equity down 1000 cents; the
	// stress fee adds 10. Both stay within the predeclared 2000.
	for _, v := range r.Portfolio.Variants {
		if v.Base.ObservedMaximumDrawdownCents != 1000 || v.Stress.ObservedMaximumDrawdownCents != 1010 || !v.Base.AllEventMarksAvailable || !v.Stress.AllEventMarksAvailable {
			t.Fatal("observed drawdown", v.StrategyID, v.Base.ObservedMaximumDrawdownCents, v.Stress.ObservedMaximumDrawdownCents)
		}
	}
	drawdowns := 0
	for _, check := range r.Checks {
		if check.Name == "maximum_drawdown" {
			drawdowns++
			if check.Status != "passed" || check.ReasonCode != "observed_drawdown_within_predeclared_maximum" {
				t.Fatal("drawdown check", check)
			}
		}
	}
	if drawdowns != len(declared) || r.Recipe != storage.PaperDrawdownAssessmentRecipe {
		t.Fatal("drawdown checks", drawdowns, r.Recipe)
	}
	for _, v := range r.StatisticalTests.Variants {
		if v.HoldoutDecisions != 3 || deref(v.HoldoutBaseNetCents) != "3000" || deref(v.HoldoutStressNetCents) != "2940" || v.TradingDays != 5 || v.PositiveDays != 5 || deref(v.PValue) != "1/32" {
			t.Fatal("statistical tests", v)
		}
	}
	if r.StatisticalTests.CorrectedAlpha != "1/30" {
		t.Fatal("corrected alpha", r.StatisticalTests.CorrectedAlpha)
	}

	// A fresh decision within the admission TTL is admitted and reserved.
	clock.at = time.Date(2026, 11, 9, 14, 58, 0, 0, time.UTC)
	claim()
	calendar("grant-calendar", "2026-11-09")
	clock.at = time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	quote("grant-entry", 98, 99)
	clock.at = clock.at.Add(time.Second)
	fresh := decide("grant-decision", d.ID)
	grant := storage.OwnedAllocationCommand{ID: "grant-comparison", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: fresh.Record.Command.ID, SHA256: fresh.Record.SHA256}}}
	if failCommit != nil {
		// A failed commit must leave no comparison, reservation or ledger
		// entry, and the same command must then reserve exactly once.
		failCommit(true)
		request("POST", "/v1/allocation-comparisons", grant, 503)
		failCommit(false)
		var untouched storage.OwnedAllocationCatalog
		if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &untouched); err != nil {
			t.Fatal(err)
		}
		if len(untouched.Records) != 0 || len(untouched.Ledgers) != 0 || len(untouched.Capital) != 1 || untouched.Capital[0].Schema != "mantik-owned-deferral-capital-v1" || untouched.Capital[0].AvailableCashCents != 200000 {
			t.Fatal("failed commit persisted allocation state", untouched)
		}
	}
	var comparison storage.OwnedAllocationReceipt
	if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", grant, 201), &comparison); err != nil {
		t.Fatal(err)
	}
	g := comparison.Record
	if g.Schema != storage.OwnedAllocationGrantSchema || len(g.Entry.Command.Comparison.Admissions) != 1 || g.Entry.Command.Comparison.Admissions[0].EvidenceSHA256 != r.SHA256 || g.Entry.Plan.Decisions[0].Status != "reserve_plan" {
		t.Fatal("complete evaluation did not admit and reserve", g.Entry.Command.Comparison, g.Entry.Plan)
	}
	readiness := g.AdmissionChecks[0]
	if readiness.Schema != storage.OwnedEligibilityReadinessSchema || !readiness.PaperOperationallyEligible || readiness.Status != "eligible" || readiness.ExecutionAvailable || readiness.QualificationCredit {
		t.Fatal("readiness not eligible", readiness)
	}
	var review storage.OwnedAdmissionReviewReceipt
	if err := json.Unmarshal(request("POST", "/v1/admission-reviews", storage.OwnedAdmissionReviewCommand{ID: "grant-review", ReceiverID: owner, ComparisonID: g.Command.ID, ComparisonSHA256: g.SHA256, DecisionID: readiness.DecisionID, DecisionSHA256: readiness.DecisionSHA256}, 201), &review); err != nil {
		t.Fatal(err)
	}
	if review.Record.Status != "eligible" || !review.Record.AdmissionAvailable || review.Record.ExecutionAvailable || review.Record.QualificationCredit {
		t.Fatal("review not eligible", review.Record)
	}
	var catalog storage.OwnedAllocationCatalog
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Capital) != 1 || catalog.Capital[0].Schema != "mantik-owned-allocation-capital-v2" || len(catalog.Capital[0].Reservations) != 1 || catalog.Capital[0].AvailableCashCents != 200000-99000 {
		t.Fatal("reserved capital not read back", catalog.Capital)
	}

	// Two more admitted proposals compete for the 101000 cents left under the
	// 200000 capital limit. Each needs 99000, so exactly one reserves.
	contenders := []storage.PaperDecisionReceipt{}
	for i, second := range []int{10, 20} {
		clock.at = time.Date(2026, 11, 9, 15, 0, second, 0, time.UTC)
		quote(fmt.Sprintf("contest-entry-%d", i), 98, 99)
		clock.at = clock.at.Add(time.Second)
		contenders = append(contenders, decide(fmt.Sprintf("contest-decision-%d", i), d.ID))
	}
	results := make([]storage.OwnedAllocationReceipt, len(contenders))
	var wg sync.WaitGroup
	for i, c := range contenders {
		wg.Go(func() {
			if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", storage.OwnedAllocationCommand{ID: fmt.Sprintf("contest-%d", i), ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: c.Record.Command.ID, SHA256: c.Record.SHA256}}}, 201), &results[i]); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	reserved, deferred := 0, 0
	for _, res := range results {
		switch status := res.Record.Entry.Plan.Decisions[0].Status; status {
		case "reserve_plan":
			reserved++
		case "defer":
			deferred++
		default:
			t.Fatal("contest outcome", status, res.Record.Entry.Plan.Decisions[0].Reasons)
		}
	}
	if reserved != 1 || deferred != 1 {
		t.Fatal("capacity consumed other than exactly once", reserved, deferred)
	}
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Capital[0].AvailableCashCents != 200000-2*99000 || len(catalog.Capital[0].Reservations) != 2 {
		t.Fatal("capital after contest", catalog.Capital[0])
	}
	// An exact retry returns the retained outcome without a new reservation.
	var again storage.OwnedAllocationReceipt
	if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", results[0].Record.Command, 200), &again); err != nil || !again.Duplicate || again.Record.SHA256 != results[0].Record.SHA256 {
		t.Fatal("exact retry", again, err)
	}
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &catalog); err != nil {
		t.Fatal(err)
	}
	return r, catalog
}

func TestOwnedPositiveEvaluationAndEligibilityHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	positiveEvaluationHTTP(t, m, nil, positiveShape{})
}

// The Postgres variant also fails one reserving commit and then reopens the
// store: replay after restart must rederive the same complete assessment,
// admissions and reservations without reading a clock.
func TestPostgresContractsOwnedPositiveEvaluationAndEligibilityHTTP(t *testing.T) {
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
	db := "mantik_test_positive_http_" + strings.ToLower(rand.Text())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
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
	trigger, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer trigger.Close(ctx)
	failCommit := func(on bool) {
		t.Helper()
		statement := `CREATE FUNCTION reject_allocation_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'allocation commit fixture rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_allocation_commit AFTER INSERT OR UPDATE ON mantik_owned_allocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_allocation_commit()`
		if !on {
			statement = "DROP TRIGGER reject_allocation_commit ON mantik_owned_allocations; DROP FUNCTION reject_allocation_commit()"
		}
		if _, err := trigger.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	assessment, catalog := positiveEvaluationHTTP(t, s, failCommit, positiveShape{})
	s.Close()
	reopened, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.HandlerWithClock(reopened, "restart-token", &ownedDecisionClock{panicRead: true})))
	defer server.Close()
	read := func(path string, into any) {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
		r.Header.Set("Authorization", "Bearer restart-token")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(into) != nil {
			t.Fatal("restart read", path, response.StatusCode)
		}
	}
	var replayed storage.PaperEvaluationAssessment
	read("/v1/paper-evaluation-assessments/"+assessment.Command.ID, &replayed)
	var comparisons storage.OwnedAllocationCatalog
	read("/v1/allocation-comparisons", &comparisons)
	if !reflect.DeepEqual(replayed, assessment) || !reflect.DeepEqual(comparisons, catalog) {
		t.Fatal("restart replay changed the complete evaluation or reservations")
	}
}
