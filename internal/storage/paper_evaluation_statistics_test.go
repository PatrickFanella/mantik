package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSignTestKnownValues(t *testing.T) {
	// P(X >= 15 | n=20) = 21700 / 2^20.
	if got := signTestTail(20, 15).Int64(); got != 21700 {
		t.Fatal(got)
	}
	if got := signTestTail(5, 0).Int64(); got != 32 {
		t.Fatal("full tail", got)
	}
	for _, tc := range []struct {
		n, k  int
		alpha int64
		m     int
		pass  bool
	}{
		{5, 5, 500, 1, true},    // 1/32 = 0.03125 <= 0.05
		{5, 5, 500, 2, false},   // 0.03125 > 0.025
		{5, 4, 500, 1, false},   // 6/32 = 0.1875
		{20, 15, 500, 1, true},  // 0.0207 <= 0.05
		{20, 15, 200, 1, false}, // 0.0207 > 0.02
		{20, 15, 500, 2, true},  // 0.0207 <= 0.025
		{20, 15, 500, 3, false}, // 0.0207 > 0.0167
		{1, 1, 1000, 1, false},  // 0.5
		{0, 0, 1000, 1, false},
		{5, 6, 1000, 1, false},
	} {
		if got := signTestPasses(tc.n, tc.k, tc.alpha, tc.m); got != tc.pass {
			t.Fatal(tc, got)
		}
	}
}

func statisticalCommand() PaperEvaluationDeclarationCommand {
	c := sensitivityCommand()
	c.Plan.SearchMultiplicity = &PaperEvaluationSearchPlan{Method: PaperSearchHistoryMethod, MaximumSearchedStrategies: 3}
	c.Plan.StatisticalTests = &PaperEvaluationStatisticalPlan{Method: PaperStatisticalTestMethod, MinimumHoldoutNetCents: 0, MinimumTradingDays: 20, AlphaBasisPoints: 500}
	return c
}

func TestStatisticalPlanShape(t *testing.T) {
	if !validStatisticalPlan(statisticalCommand()) {
		t.Fatal("valid statistical plan refused")
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"method":         func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.Method = "other" },
		"no sensitivity": func(c *PaperEvaluationDeclarationCommand) { c.Plan.Sensitivity = nil },
		"no search":      func(c *PaperEvaluationDeclarationCommand) { c.Plan.SearchMultiplicity = nil },
		"negative floor": func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.MinimumHoldoutNetCents = -1 },
		"zero alpha":     func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.AlphaBasisPoints = 0 },
		"large alpha":    func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.AlphaBasisPoints = 1001 },
		"zero days":      func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.MinimumTradingDays = 0 },
		"too many days":  func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.MinimumTradingDays = 33 },
		// 2^4 = 16 < 10000*3/500 = 60: no outcome could pass.
		"unreachable pass": func(c *PaperEvaluationDeclarationCommand) { c.Plan.StatisticalTests.MinimumTradingDays = 4 },
	} {
		bad := statisticalCommand()
		change(&bad)
		if validStatisticalPlan(bad) {
			t.Fatal(name, "invalid statistical plan accepted")
		}
	}
}

func decisionPinFor(id string) string { return strings.Repeat(fmt.Sprintf("%x", len(id)%16), 64) }

func observedDecision(id, base, stress string) PaperEvaluationDecisionReturn {
	return PaperEvaluationDecisionReturn{DecisionID: id, DecisionSHA256: decisionPinFor(id), Status: "observed_total_return", BaseNetCents: &base, StressNetCents: &stress, BaseSHA256: strings.Repeat("a", 64), StressSHA256: strings.Repeat("b", 64)}
}

// statisticalAssessment has one variant "a" with two training decisions and
// two holdout decisions, plus one selected disjoint window per listed day.
func statisticalAssessment(holdout [2][2]string, days []string) PaperEvaluationAssessment {
	plan := statisticalCommand().Plan
	plan.Variants = []PaperEvaluationVariant{{ID: "a"}}
	plan.SearchMultiplicity.MaximumSearchedStrategies = 1
	r := PaperEvaluationAssessment{Status: "incomplete", Evidence: PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"}, Holdout: &PaperEvaluationHoldout{Variants: []PaperEvaluationHoldoutVariant{{StrategyID: "a", Status: "unavailable"}}}, SampleDesign: &PaperEvaluationSampleDesign{Variants: []PaperEvaluationSampleVariant{{StrategyID: "a"}}}, SearchHistory: &PaperEvaluationSearchHistory{}, Portfolio: &PaperEvaluationPortfolio{Variants: []PaperEvaluationPortfolioVariant{{StrategyID: "a"}}}}
	r.Evidence.Declaration.Command.Plan = plan
	add := func(id string) {
		r.Evidence.Rows = append(r.Evidence.Rows, PaperEvaluationEvidenceRow{Status: "selected_research_evidence", Decision: PaperDecisionRecord{Command: PaperDecisionCommand{ID: id, StrategyID: "a"}}})
	}
	for i, n := range holdout {
		id := fmt.Sprintf("holdout-%d", i)
		add(id)
		r.Holdout.Rows = append(r.Holdout.Rows, PaperEvaluationHoldoutRow{DecisionID: id, Partition: "holdout"})
		if n[0] != "" {
			r.Portfolio.Variants[0].Decisions = append(r.Portfolio.Variants[0].Decisions, observedDecision(id, n[0], n[1]))
		}
	}
	for i, day := range days {
		id := fmt.Sprintf("window-%d", i)
		add(id)
		r.Holdout.Rows = append(r.Holdout.Rows, PaperEvaluationHoldoutRow{DecisionID: id, Partition: "training"})
		start, _ := time.ParseInLocation(time.DateOnly, day, newYork)
		start = start.Add(10 * time.Hour)
		r.SampleDesign.Variants[0].Windows = append(r.SampleDesign.Variants[0].Windows, PaperEvaluationSampleWindow{DecisionID: id, Start: start, End: start.Add(time.Minute), Status: "selected_disjoint_window"})
		net := "125"
		if strings.HasSuffix(day, "-31") {
			net = "-1"
		}
		r.Portfolio.Variants[0].Decisions = append(r.Portfolio.Variants[0].Decisions, observedDecision(id, net, net))
	}
	for _, name := range []string{"out_of_sample", "sample_sufficiency", "search_multiplicity"} {
		r.Checks = append(r.Checks, PaperEvaluationCheck{StrategyID: "a", Name: name, Status: "unavailable"})
	}
	return r
}

func tradingDays(n int) []string {
	out := []string{}
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); len(out) < n; d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			out = append(out, d.Format(time.DateOnly))
		}
	}
	return out
}

func TestStatisticalTestsDecision(t *testing.T) {
	gain := [2][2]string{{"10", "5"}, {"-4", "-5"}}
	p := *statisticalCommand().Plan.StatisticalTests
	for name, tc := range map[string]struct {
		holdout    [2][2]string
		days       []string
		phase      string
		oos, ss    string
		sampleWhy  string
		searchWant string
	}{
		"all pass":           {gain, tradingDays(20), "outcome_deadline_elapsed", "passed", "passed", "day_clustered_sign_test_significant_at_corrected_alpha", "passed"},
		"holdout stress":     {[2][2]string{{"10", "4"}, {"-4", "-5"}}, tradingDays(20), "outcome_deadline_elapsed", "failed", "passed", "day_clustered_sign_test_significant_at_corrected_alpha", "passed"},
		"holdout missing":    {[2][2]string{{"10", "5"}, {"", ""}}, tradingDays(20), "outcome_deadline_elapsed", "unavailable", "passed", "day_clustered_sign_test_significant_at_corrected_alpha", "passed"},
		"too few days":       {gain, tradingDays(19), "outcome_deadline_elapsed", "passed", "failed", "trading_day_windows_below_predeclared_minimum", "passed"},
		"same day collapses": {gain, append(tradingDays(19), tradingDays(1)...), "outcome_deadline_elapsed", "passed", "failed", "trading_day_windows_below_predeclared_minimum", "passed"},
		// 2026-08-31 is a loss: 20 of 21 positive gives p = 22/2^21, still significant.
		"one loss":    {gain, append([]string{"2026-08-31"}, tradingDays(20)...), "outcome_deadline_elapsed", "passed", "passed", "day_clustered_sign_test_significant_at_corrected_alpha", "passed"},
		"window open": {gain, tradingDays(20), "awaiting_outcome_deadline", "pending", "pending", "predeclared_outcome_window_not_closed", "pending"},
	} {
		r := statisticalAssessment(tc.holdout, tc.days)
		r.Evidence.Phase = tc.phase
		if err := applyStatisticalTests(&r, &p); err != nil {
			t.Fatal(name, err)
		}
		completeApplicableEvaluation(&r)
		oos, ss, search := r.Checks[0], r.Checks[1], r.Checks[2]
		if oos.Status != tc.oos || ss.Status != tc.ss || ss.ReasonCode != tc.sampleWhy || search.Status != tc.searchWant {
			t.Fatal(name, oos, ss, search)
		}
		want := "incomplete"
		switch {
		case tc.oos == "failed" || tc.ss == "failed":
			want = "blocked"
		case tc.oos == "passed" && tc.ss == "passed" && tc.searchWant == "passed":
			want = "complete"
		}
		if r.Status != want || r.EvaluationComplete != (want == "complete") || (want == "complete") != (len(r.CompleteStrategyIDs) == 1) {
			t.Fatal(name, "status", r.Status, r.CompleteStrategyIDs)
		}
		st := r.StatisticalTests
		if st == nil || st.CorrectedAlpha != "1/20" || st.IndependenceEstablished || !st.ExchangeabilityAssumed || st.EvaluationComplete || st.AdmissionAvailable {
			t.Fatal(name, st)
		}
	}

	// Eleven of twenty positive is not significant.
	days := tradingDays(20)
	r := statisticalAssessment(gain, days)
	for i := range 9 {
		loss := "-3"
		r.Portfolio.Variants[0].Decisions[2+i].StressNetCents = &loss
	}
	if err := applyStatisticalTests(&r, &p); err != nil {
		t.Fatal(err)
	}
	if ss := r.Checks[1]; ss.Status != "failed" || ss.ReasonCode != "sign_test_not_significant_at_corrected_alpha" || r.StatisticalTests.Variants[0].PositiveDays != 11 {
		t.Fatal("insignificant test passed", ss, r.StatisticalTests.Variants[0])
	}

	// Earlier failures are kept.
	r = statisticalAssessment(gain, days)
	r.Checks[0].Status, r.Checks[0].ReasonCode = "failed", "purged_partition_occurrence_upper_bound_below_predeclared_minimum"
	r.Checks[1].Status, r.Checks[1].ReasonCode = "failed", "observed_occurrence_upper_bound_below_predeclared_minimum"
	r.Checks[2].Status, r.Checks[2].ReasonCode = "failed", "registered_search_exceeds_predeclared_bound"
	if err := applyStatisticalTests(&r, &p); err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].ReasonCode != "purged_partition_occurrence_upper_bound_below_predeclared_minimum" || r.Checks[1].ReasonCode != "observed_occurrence_upper_bound_below_predeclared_minimum" || r.Checks[2].ReasonCode != "registered_search_exceeds_predeclared_bound" {
		t.Fatal("earlier failure overwritten", r.Checks)
	}

	// An unavailable unit leaves the sample check unavailable.
	r = statisticalAssessment(gain, days)
	r.Portfolio.Variants[0].Decisions[5] = PaperEvaluationDecisionReturn{DecisionID: "window-3", DecisionSHA256: decisionPinFor("window-3"), Status: "terminal_mark_unavailable", BaseSHA256: strings.Repeat("a", 64), StressSHA256: strings.Repeat("b", 64)}
	if err := applyStatisticalTests(&r, &p); err != nil {
		t.Fatal(err)
	}
	if ss := r.Checks[1]; ss.Status != "unavailable" || ss.ReasonCode != "clustered_window_total_return_unavailable" || r.StatisticalTests.Variants[0].PValue != nil {
		t.Fatal("unavailable unit decided", ss)
	}

	for name, change := range map[string]func(*PaperEvaluationAssessment){
		"inexact net": func(r *PaperEvaluationAssessment) {
			bad := "1e3"
			r.Portfolio.Variants[0].Decisions[0].BaseNetCents = &bad
		},
		"net without observation": func(r *PaperEvaluationAssessment) {
			r.Portfolio.Variants[0].Decisions[0].Status = "decision_not_funded"
		},
		"duplicate decision": func(r *PaperEvaluationAssessment) {
			r.Portfolio.Variants[0].Decisions = append(r.Portfolio.Variants[0].Decisions, r.Portfolio.Variants[0].Decisions[0])
		},
		"foreign decision": func(r *PaperEvaluationAssessment) {
			r.Portfolio.Variants[0].Decisions = append(r.Portfolio.Variants[0].Decisions, observedDecision("elsewhere", "1", "1"))
		},
		"no sample design": func(r *PaperEvaluationAssessment) { r.SampleDesign = nil },
	} {
		r := statisticalAssessment(gain, days)
		change(&r)
		if err := applyStatisticalTests(&r, &p); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
}

func TestCompletionNeedsEveryCheckOfAVariant(t *testing.T) {
	r := PaperEvaluationAssessment{Status: "incomplete", Evidence: PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"}}
	r.Evidence.Declaration.Command.Plan.Variants = []PaperEvaluationVariant{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	r.Checks = []PaperEvaluationCheck{{StrategyID: "a", Status: "passed"}, {StrategyID: "a", Status: "not_applicable"}, {StrategyID: "b", Status: "passed"}, {StrategyID: "b", Status: "unavailable"}}
	completeApplicableEvaluation(&r)
	if r.Status != "complete" || !r.EvaluationComplete || len(r.CompleteStrategyIDs) != 1 || r.CompleteStrategyIDs[0] != "a" || r.AdmissionAvailable {
		t.Fatal("variant without checks or with an open check completed", r.Status, r.CompleteStrategyIDs)
	}
	for _, change := range []func(*PaperEvaluationAssessment){
		func(r *PaperEvaluationAssessment) { r.Status = "blocked" },
		func(r *PaperEvaluationAssessment) { r.Evidence.Phase = "awaiting_outcome_deadline" },
		func(r *PaperEvaluationAssessment) { r.Checks[1].Status = "pending" },
	} {
		r := PaperEvaluationAssessment{Status: "incomplete", Evidence: PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"}}
		r.Evidence.Declaration.Command.Plan.Variants = []PaperEvaluationVariant{{ID: "a"}}
		r.Checks = []PaperEvaluationCheck{{StrategyID: "a", Status: "passed"}, {StrategyID: "a", Status: "passed"}}
		change(&r)
		completeApplicableEvaluation(&r)
		if r.Status == "complete" || r.EvaluationComplete || r.CompleteStrategyIDs != nil {
			t.Fatal("assessment completed", r.Status)
		}
	}
}

func TestDecisionReturnShape(t *testing.T) {
	if !validDecisionReturn(observedDecision("d", "1", "1/3")) {
		t.Fatal("valid decision return refused")
	}
	unfunded := PaperEvaluationDecisionReturn{DecisionID: "d", DecisionSHA256: decisionPinFor("d"), Status: "decision_not_funded"}
	if !validDecisionReturn(unfunded) {
		t.Fatal("unfunded decision refused")
	}
	unfunded.BaseSHA256 = strings.Repeat("a", 64)
	if validDecisionReturn(unfunded) {
		t.Fatal("unfunded decision with a total-return hash accepted")
	}
	bad := observedDecision("d", "1", "1")
	bad.StressNetCents = nil
	if validDecisionReturn(bad) {
		t.Fatal("observed decision without stress net accepted")
	}
	bad = observedDecision("d", "1", "1")
	bad.Status = "unknown"
	if validDecisionReturn(bad) {
		t.Fatal("unknown status accepted")
	}
}
