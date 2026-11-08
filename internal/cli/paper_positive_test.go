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
	// automaticDecisions lets the decision worker finalize and compare the
	// next session's decisions, then returns.
	automaticDecisions bool
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
	if shape.automaticDecisions {
		positiveAutomaticDecisions(t, s, request, owner, clock, d.ID, r.SHA256, func() {
			clock.at = time.Date(2026, 11, 9, 15, 0, 10, 0, time.UTC)
			quote("automatic-next", 98, 99)
			clock.at = clock.at.Add(time.Second)
		})
		return r, storage.OwnedAllocationCatalog{}
	}
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
	if g.Schema != storage.OwnedExecutionSchema || len(g.Entry.Command.Comparison.Admissions) != 1 || g.Entry.Command.Comparison.Admissions[0].EvidenceSHA256 != r.SHA256 || g.Entry.Plan.Decisions[0].Status != "reserve_plan" {
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
	if len(catalog.Capital) != 1 || catalog.Capital[0].Schema != "mantik-owned-allocation-capital-v3" || len(catalog.Capital[0].Reservations) != 1 || catalog.Capital[0].AvailableCashCents != 200000-99000 {
		t.Fatal("reserved capital not read back", catalog.Capital)
	}
	// Reserved with nothing filled yet: the row keeps an empty effect list
	// across JSON, and the command accepts it.
	var reservedRow storage.PaperAttributionRow
	if err := json.Unmarshal(request("GET", "/v1/paper-attribution/grant-decision", nil, 200), &reservedRow); err != nil {
		t.Fatal(err)
	}
	if e := reservedRow.Economics; e.Status != "reserved_unfilled" || e.Effects == nil || len(e.Effects) != 0 || e.ReservedCents == nil || *e.ReservedCents != 99000 || validateAttributionRow(reservedRow, owner) != nil {
		t.Fatal("reserved row", reservedRow.Economics)
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
	// Entry execution. Each later record advanced the grant's book: it filled
	// all 1000 shares at the $99 ask published at 15:00:10.1. After every
	// validity has passed, a step releases the contest winner's unfilled order
	// at its expiry. The filled grant keeps its position.
	winner := results[0].Record.Command.Decisions[0].ID
	if results[1].Record.Entry.Plan.Decisions[0].Status == "reserve_plan" {
		winner = results[1].Record.Command.Decisions[0].ID
	}
	clock.at = time.Date(2026, 11, 9, 15, 2, 0, 0, time.UTC)
	stepCommand := storage.OwnedExecutionCommand{ID: "execution-step", ReceiverID: owner}
	var step storage.OwnedExecutionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-executions", stepCommand, 201), &step); err != nil {
		t.Fatal(err)
	}
	if step.Record == nil || step.Record.Schema != storage.OwnedExecutionStepSchema || len(step.Record.Executions) != 1 || len(step.Record.Executions[0].Entries) != 1 {
		t.Fatal("execution step", step)
	}
	release := step.Record.Executions[0].Entries[0].Command.Effect
	if release == nil || release.Kind != "release" || release.DecisionID != winner || release.AmountCents != 99000 || !strings.HasPrefix(release.ID, "unfilled-release-") {
		t.Fatal("unfilled release", release)
	}
	var retried storage.OwnedExecutionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-executions", stepCommand, 200), &retried); err != nil || !retried.Duplicate || retried.Record.SHA256 != step.Record.SHA256 {
		t.Fatal("execution retry", retried, err)
	}
	// The execution worker's identities are reserved.
	request("POST", "/v1/paper-executions", storage.OwnedExecutionCommand{ID: service.AutomaticPaperExecutionPrefix + "caller", ReceiverID: owner}, 422)
	var idle storage.OwnedExecutionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-executions", storage.OwnedExecutionCommand{ID: "execution-idle", ReceiverID: owner}, 200), &idle); err != nil || idle.Record != nil || idle.Status != "no_effects" {
		t.Fatal("idle execution step", idle, err)
	}
	// The grant's 10-second holding deadline after its fill triggered its
	// exit at 15:00:20.1. No later quote arrives to place the exit, so the
	// step lists it as pending instead of inventing a fill.
	if len(idle.Positions) != 1 || idle.Positions[0].OrderID != "grant-decision" || idle.Positions[0].Status != "exit_pending" || !idle.Positions[0].Since.Equal(time.Date(2026, 11, 9, 15, 0, 20, 100000000, time.UTC)) {
		t.Fatal("idle positions", idle.Positions)
	}
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &catalog); err != nil {
		t.Fatal(err)
	}
	capital := catalog.Capital[0]
	if catalog.Ledgers[0].Schema != allocation.CostLedgerSchema || capital.Schema != "mantik-owned-allocation-capital-v3" || capital.AvailableCashCents != 200000-99000 || len(capital.Positions) != 1 || len(catalog.Executions) != 1 {
		t.Fatal("capital after execution", capital, len(catalog.Executions))
	}
	for _, r := range capital.Reservations {
		want := [3]int64{}
		switch r.Proposal.ID {
		case "grant-decision":
			want = [3]int64{0, 99000, 0}
		case winner:
			want = [3]int64{0, 0, 99000}
		}
		if [3]int64{r.ReservedCents, r.FilledCents, r.ReleasedCents} != want {
			t.Fatal("reservation after execution", r)
		}
	}
	fills := 0
	for _, e := range catalog.Ledgers[0].Entries {
		if f := e.Command.Effect; f != nil && f.Kind == "fill" {
			fills++
			if f.DecisionID != "grant-decision" || f.AmountCents != 99000 || !e.At.Equal(time.Date(2026, 11, 9, 15, 0, 10, 100000000, time.UTC)) {
				t.Fatal("entry fill", e)
			}
		}
	}
	if fills != 1 {
		t.Fatal("entry fills", fills)
	}
	positiveExitLifecycleHTTP(t, s, server.URL, owner, clock, failCommit, quote)
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &catalog); err != nil {
		t.Fatal(err)
	}
	return r, catalog
}

// positiveExitLifecycleHTTP closes the grant's position through the public
// step route. Its holding deadline triggered at 15:00:20.1 with no later
// quote, so the exit is pending. A $100 bid at 15:02:30 places the market
// exit, and the next one fills it.
//
// When failCommit is set, a step whose commit fails changes nothing and
// leaves its identity unused. Four step requests and an execution worker
// cycle then race at one clock: exactly one commits the close, the rest
// find nothing left. Retrying the winning identity, as a client
// whose response was lost would, returns the same step. Cash, positions and
// postings must then balance.
func positiveExitLifecycleHTTP(t testing.TB, s portfolioHTTPBackend, url, owner string, clock *ownedDecisionClock, failCommit func(bool), quote func(string, int64, int64)) {
	t.Helper()
	post := func(body any) (int, storage.OwnedExecutionReceipt) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequestWithContext(context.Background(), "POST", url+"/v1/paper-executions", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer positive-fixture-token")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out storage.OwnedExecutionReceipt
		if response.StatusCode < 300 && json.NewDecoder(response.Body).Decode(&out) != nil {
			t.Fatal("step response")
		}
		return response.StatusCode, out
	}
	catalog := func() storage.OwnedAllocationCatalog {
		t.Helper()
		r, _ := http.NewRequestWithContext(context.Background(), "GET", url+"/v1/allocation-comparisons", nil)
		r.Header.Set("Authorization", "Bearer positive-fixture-token")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out storage.OwnedAllocationCatalog
		if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&out) != nil {
			t.Fatal("catalog read", response.StatusCode)
		}
		return out
	}
	clock.at = time.Date(2026, 11, 9, 15, 2, 30, 0, time.UTC)
	quote("exit-place", 100, 101)
	clock.at = time.Date(2026, 11, 9, 15, 2, 31, 0, time.UTC)
	quote("exit-fill", 100, 101)
	clock.at = time.Date(2026, 11, 9, 15, 3, 0, 0, time.UTC)
	before := catalog()
	if failCommit != nil {
		failCommit(true)
		if code, _ := post(storage.OwnedExecutionCommand{ID: "exit-step-0", ReceiverID: owner}); code != 503 {
			t.Fatal("failed step commit", code)
		}
		failCommit(false)
		if after := catalog(); !reflect.DeepEqual(after, before) {
			t.Fatal("failed step commit changed allocation state")
		}
	}
	worker, ok := s.(service.PaperExecutionWorkerStore)
	if !ok {
		t.Fatal("store cannot run the execution worker")
	}
	codes := make([]int, 4)
	receipts := make([]storage.OwnedExecutionReceipt, 5)
	stepper := &service.PaperExecutionStepper{Worker: "lifecycle-worker"}
	var workerErr error
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() {
			codes[i], receipts[i] = post(storage.OwnedExecutionCommand{ID: fmt.Sprintf("exit-step-%d", i), ReceiverID: owner})
		})
	}
	wg.Go(func() { receipts[4], workerErr = stepper.Cycle(context.Background(), worker, clock) })
	wg.Wait()
	// A request that timed out may or may not have committed. Its client and
	// the worker retry the same identity, which either returns the retained
	// step or finds nothing left.
	for i, code := range codes {
		if code == 503 {
			codes[i], receipts[i] = post(storage.OwnedExecutionCommand{ID: fmt.Sprintf("exit-step-%d", i), ReceiverID: owner})
		}
		if codes[i] != 200 && codes[i] != 201 {
			t.Fatal("concurrent step", i, codes[i])
		}
	}
	if workerErr != nil {
		if receipts[4], workerErr = stepper.Cycle(context.Background(), worker, clock); workerErr != nil {
			t.Fatal("worker retry", workerErr)
		}
	}
	winner := -1
	for i, r := range receipts {
		if r.Record == nil {
			if r.Status != "no_effects" || len(r.Positions) != 0 {
				t.Fatal("losing step", i, r)
			}
			continue
		}
		if winner >= 0 {
			t.Fatal("more than one step committed the exit", winner, i)
		}
		winner = i
	}
	if winner < 0 {
		t.Fatal("no step committed the exit")
	}
	step := receipts[winner].Record
	effects := step.Executions[0].Entries
	// The grant spent its whole 99000-cent notional, so the close releases
	// nothing.
	if len(step.Executions) != 1 || len(effects) != 1 || effects[0].Command.Effect.Kind != "close" || effects[0].Command.Effect.DecisionID != "grant-decision" || effects[0].Command.Effect.ProceedsCents != 100000 || !effects[0].At.After(time.Date(2026, 11, 9, 15, 2, 31, 0, time.UTC)) {
		t.Fatal("exit effects", effects)
	}
	// Every client retries its identity as if its response was lost: the
	// winner gets the same step back, the others still find nothing to commit.
	for i := range codes {
		code, retry := post(storage.OwnedExecutionCommand{ID: fmt.Sprintf("exit-step-%d", i), ReceiverID: owner})
		if i == winner && (code != 200 || !retry.Duplicate || retry.Record.SHA256 != step.SHA256) || i != winner && (code != 200 || retry.Record != nil) {
			t.Fatal("retry after a lost response", i, code, retry)
		}
	}
	after := catalog()
	if len(after.Executions) != len(before.Executions)+1 {
		t.Fatal("execution steps", len(after.Executions), len(before.Executions))
	}
	c := after.Capital[0]
	if len(c.Positions) != 0 || c.RealizedPnLCents != 1000 || c.AvailableCashCents != 200000+c.RealizedPnLCents {
		t.Fatal("capital after exit", c)
	}
	var filled, closed, proceeds int64
	for _, e := range after.Ledgers[0].Entries {
		if f := e.Command.Effect; f != nil && f.DecisionID == "grant-decision" {
			switch f.Kind {
			case "fill":
				filled += f.AmountCents
			case "close":
				closed += f.AmountCents
				proceeds += f.ProceedsCents
			}
		}
	}
	for _, r := range c.Reservations {
		if r.ReservedCents != 0 || r.FilledCents+r.ReleasedCents != r.Proposal.NotionalCents {
			t.Fatal("reservation after exit", r)
		}
		if r.Proposal.ID == "grant-decision" && (r.FilledCents != filled || r.ClosedCostCents != closed || r.ProceedsCents != proceeds || closed != filled || proceeds-closed != c.RealizedPnLCents) {
			t.Fatal("grant postings", r, filled, closed, proceeds)
		}
	}
	positiveExecutionAttribution(t, url, owner, filled, proceeds)
}

// positiveExecutionAttribution reads the closed grant back through the
// attribution route and the paper-attribution command: the row joins the
// reservation to its fill and close effects and the realized result, and the
// command validates the whole catalogue before rendering it.
func positiveExecutionAttribution(t testing.TB, url, owner string, filled, proceeds int64) {
	t.Helper()
	r, _ := http.NewRequestWithContext(context.Background(), "GET", url+"/v1/paper-attribution/grant-decision", nil)
	r.Header.Set("Authorization", "Bearer positive-fixture-token")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var row storage.PaperAttributionRow
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&row) != nil {
		t.Fatal("attribution row", response.StatusCode)
	}
	e := row.Economics
	if e.Status != "position_closed" || e.ReservedCents == nil || *e.ReservedCents != 99000 || *e.FilledCostCents != filled || *e.ClosedCostCents != filled || *e.NetProceedsCents != proceeds || *e.ReleasedCents != 0 || e.RealizedPnLCents == nil || *e.RealizedPnLCents != proceeds-filled || len(e.Effects) != 2 || e.Effects[0].Kind != "fill" || e.Effects[1].Kind != "close" || e.ExecutedQuantity != nil || e.FeesCents != nil {
		t.Fatal("execution attribution", e)
	}
	if validateAttributionRow(row, owner) != nil {
		t.Fatal("command rejects the closed grant's row")
	}
	t.Setenv("MANTIK_API_TOKEN", "positive-fixture-token")
	var out, diag bytes.Buffer
	if err := Run(context.Background(), []string{"paper-attribution", "--api-url", url, "--receiver-id", owner}, &out, &diag); err != nil {
		t.Fatal("paper-attribution", err, diag.String())
	}
	text := out.String()
	for _, want := range []string{"EXECUTION paper", `ECONOMICS "position_closed"`, "realized_pnl_cents=1000", `EFFECT "close"`, `status="reserve_plan"`} {
		if !strings.Contains(text, want) {
			t.Fatal("paper-attribution output lacks", want, text)
		}
	}
}

func TestOwnedPositiveEvaluationAndEligibilityHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err == nil {
		err = m.EnablePaperExecution(service.SelectPaperExecutionQuotes)
	}
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
	if err == nil {
		err = s.EnablePaperExecution(service.SelectPaperExecutionQuotes)
	}
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
	if err == nil {
		err = reopened.EnablePaperExecution(service.SelectPaperExecutionQuotes)
	}
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

// positiveAutomaticDecisions runs the decision worker's cycle where the
// fixture otherwise decides and compares by hand. All three declared
// strategies see the $99 ask; the complete assessment admits only the center
// one, so only its entry candidate reserves. A second cycle with no newer
// quote retains nothing, and a newer quote yields one more decision per
// strategy. Callers cannot use the worker's identities.
func positiveAutomaticDecisions(t testing.TB, s portfolioHTTPBackend, request func(string, string, any, int) []byte, owner string, clock *ownedDecisionClock, center, assessment string, nextQuote func()) {
	t.Helper()
	ctx := context.Background()
	worker, ok := s.(service.PaperDecisionWorkerStore)
	if !ok {
		t.Fatal("store cannot run the decision worker")
	}
	var decisions storage.PaperDecisionCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-decisions", nil, 200), &decisions); err != nil {
		t.Fatal(err)
	}
	manual := len(decisions.Records)
	if err := service.RunPaperDecisionCycle(ctx, worker, clock); err != nil {
		t.Fatal("decision cycle", err)
	}
	read := func() (storage.PaperDecisionCatalog, storage.OwnedAllocationCatalog) {
		t.Helper()
		var d storage.PaperDecisionCatalog
		var a storage.OwnedAllocationCatalog
		if err := json.Unmarshal(request("GET", "/v1/paper-decisions", nil, 200), &d); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &a); err != nil {
			t.Fatal(err)
		}
		return d, a
	}
	decisions, comparisons := read()
	automatic := []storage.PaperDecisionRecord{}
	for _, d := range decisions.Records {
		if strings.HasPrefix(d.Command.ID, service.AutomaticPaperDecisionPrefix) {
			automatic = append(automatic, d)
		}
	}
	if len(automatic) != 3 || len(decisions.Records) != manual+3 {
		t.Fatal("automatic decisions", len(automatic), len(decisions.Records), manual)
	}
	reserved := 0
	for _, d := range automatic {
		if d.Evaluation.Action != "entry_candidate" {
			continue
		}
		var found *storage.OwnedAllocationRecord
		for i, r := range comparisons.Records {
			if r.Command.ID == service.AutomaticPaperComparisonID(d) {
				found = &comparisons.Records[i]
			}
		}
		if found == nil || len(found.Command.Decisions) != 1 || found.Command.Decisions[0].SHA256 != d.SHA256 {
			t.Fatal("entry candidate without its comparison", d.Command.ID)
		}
		status := found.Entry.Plan.Decisions[0].Status
		if d.Command.StrategyID == center {
			if status != "reserve_plan" || len(found.Entry.Command.Comparison.Admissions) != 1 || found.Entry.Command.Comparison.Admissions[0].EvidenceSHA256 != assessment {
				t.Fatal("center strategy not admitted and reserved", status, found.Entry.Command.Comparison.Admissions)
			}
			reserved++
		} else if status == "reserve_plan" {
			t.Fatal("unadmitted strategy reserved", d.Command.StrategyID)
		}
	}
	if reserved != 1 {
		t.Fatal("reserved comparisons", reserved)
	}
	// No newer quote: the same identities, nothing retained.
	if err := service.RunPaperDecisionCycle(ctx, worker, clock); err != nil {
		t.Fatal("idle decision cycle", err)
	}
	if again, againComparisons := read(); len(again.Records) != len(decisions.Records) || len(againComparisons.Records) != len(comparisons.Records) {
		t.Fatal("idle cycle retained records", len(again.Records), len(againComparisons.Records))
	}
	nextQuote()
	// A caller decides and compares the center strategy on the newer quote
	// first. The worker's own center decision evaluates the same quote, so it
	// gets no second comparison: one signal reserves at most once.
	var centerPin storage.PaperDecisionRecord
	for _, d := range automatic {
		if d.Command.StrategyID == center {
			centerPin = d
		}
	}
	var manual2 storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", storage.PaperDecisionCommand{ID: "manual-next", ReceiverID: owner, StrategyID: center, StrategySHA256: centerPin.Command.StrategySHA256, RegistrationID: centerPin.Command.RegistrationID}, 201), &manual2); err != nil || manual2.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal("manual decision", manual2.Record.Evaluation, err)
	}
	request("POST", "/v1/allocation-comparisons", storage.OwnedAllocationCommand{ID: "manual-next-comparison", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: manual2.Record.Command.ID, SHA256: manual2.Record.SHA256}}}, 201)
	if err := service.RunPaperDecisionCycle(ctx, worker, clock); err != nil {
		t.Fatal("next decision cycle", err)
	}
	next, nextComparisons := read()
	if len(next.Records) != len(decisions.Records)+4 {
		t.Fatal("newer quote decisions", len(next.Records), len(decisions.Records))
	}
	for _, d := range next.Records {
		if !strings.HasPrefix(d.Command.ID, service.AutomaticPaperDecisionPrefix) || d.Command.StrategyID != center || d.Evaluation.Coverage.Quote.InputID != manual2.Record.Evaluation.Coverage.Quote.InputID {
			continue
		}
		for _, r := range nextComparisons.Records {
			if r.Command.ID == service.AutomaticPaperComparisonID(d) {
				t.Fatal("second comparison for one evaluated quote", d.Command.ID)
			}
		}
	}
	// The worker's identities are reserved.
	request("POST", "/v1/paper-decisions", storage.PaperDecisionCommand{ID: service.AutomaticPaperDecisionPrefix + "caller", ReceiverID: owner, StrategyID: automatic[0].Command.StrategyID, StrategySHA256: automatic[0].Command.StrategySHA256, RegistrationID: automatic[0].Command.RegistrationID}, 422)
	request("POST", "/v1/allocation-comparisons", storage.OwnedAllocationCommand{ID: service.AutomaticPaperComparisonPrefix + "caller", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: automatic[0].Command.ID, SHA256: automatic[0].SHA256}}}, 422)
}

func TestOwnedPositiveAutomaticDecisionsHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err == nil {
		err = m.EnablePaperExecution(service.SelectPaperExecutionQuotes)
	}
	if err != nil {
		t.Fatal(err)
	}
	positiveEvaluationHTTP(t, m, nil, positiveShape{automaticDecisions: true})
}
