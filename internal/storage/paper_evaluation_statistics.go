package storage

import (
	"math/big"
	"slices"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperStatisticalEvaluationRecipe = "native-threshold-holdout-sign-test-plan-v10"
const PaperStatisticalAssessmentRecipe = "native-predeclared-holdout-sign-test-v15"
const PaperStatisticalTestMethod = "native-holdout-return-day-clustered-sign-test-v1"
const PaperDecisionReturnPortfolioMethod = "owner-selected-finite-capital-decision-total-return-v4"

var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

// A variant's book holds at most this many decisions, so no more units exist.
const maxSignTestTradingDays = paperfill.MaxBookOrders

// PaperEvaluationStatisticalPlan predeclares the out-of-sample floor and the
// day-clustered sign test. Alpha is in basis points and is divided by the
// plan's search bound (Bonferroni) before the test is decided.
type PaperEvaluationStatisticalPlan struct {
	Method                 string `json:"method"`
	MinimumHoldoutNetCents int64  `json:"minimum_holdout_net_cents"`
	MinimumTradingDays     int    `json:"minimum_trading_days"`
	AlphaBasisPoints       int64  `json:"alpha_basis_points"`
}

// PaperEvaluationDecisionReturn is one selected decision evaluated alone: a
// book with the full opening capital that trades only that decision, with the
// covered instrument's reconciled actions applied. The finite-capital book of
// a whole variant stops funding once its budget is used, so later decisions
// may never trade there.
type PaperEvaluationDecisionReturn struct {
	DecisionID     string  `json:"decision_id"`
	DecisionSHA256 string  `json:"decision_sha256"`
	Status         string  `json:"status"`
	BaseNetCents   *string `json:"base_net_cents,omitempty"`
	StressNetCents *string `json:"stress_net_cents,omitempty"`
	BaseSHA256     string  `json:"base_sha256,omitempty"`
	StressSHA256   string  `json:"stress_sha256,omitempty"`
}

// Statuses that end before total-return accounting runs.
var decisionReturnPrestatuses = []string{"decision_not_funded", "capital_replay_capacity_exceeded", "instrument_not_covered"}

func validDecisionReturn(d PaperEvaluationDecisionReturn) bool {
	if !paperaccount.ID.MatchString(d.DecisionID) || !decisionPin.MatchString(d.DecisionSHA256) {
		return false
	}
	observed := d.Status == "observed_total_return"
	if observed != (d.BaseNetCents != nil) || observed != (d.StressNetCents != nil) {
		return false
	}
	if observed {
		if _, ok := exactCents(d.BaseNetCents); !ok {
			return false
		}
		if _, ok := exactCents(d.StressNetCents); !ok {
			return false
		}
	}
	switch {
	case slices.Contains(totalReturnStatuses, d.Status):
		return decisionPin.MatchString(d.BaseSHA256) && decisionPin.MatchString(d.StressSHA256)
	case slices.Contains(decisionReturnPrestatuses, d.Status):
		return d.BaseSHA256 == "" && d.StressSHA256 == ""
	}
	return false
}

func validDecisionReturns(ds []PaperEvaluationDecisionReturn) bool {
	seen := map[string]bool{}
	for _, d := range ds {
		if seen[d.DecisionID] || !validDecisionReturn(d) {
			return false
		}
		seen[d.DecisionID] = true
	}
	return true
}

func validStatisticalPlan(c PaperEvaluationDeclarationCommand) bool {
	p := c.Plan.StatisticalTests
	if p == nil || p.Method != PaperStatisticalTestMethod || c.Plan.Sensitivity == nil || c.Plan.SearchMultiplicity == nil || p.MinimumHoldoutNetCents < 0 || p.MinimumHoldoutNetCents > paperaccount.MaxOpeningCashCents || p.AlphaBasisPoints < 1 || p.AlphaBasisPoints > 1000 || p.MinimumTradingDays < 1 || p.MinimumTradingDays > maxSignTestTradingDays {
		return false
	}
	// A pass must be reachable: n positive days of n give p = 2^-n.
	return signTestPasses(p.MinimumTradingDays, p.MinimumTradingDays, p.AlphaBasisPoints, c.Plan.SearchMultiplicity.MaximumSearchedStrategies)
}

// signTestTail returns the one-sided exact binomial tail P(X >= k) for
// X ~ Binomial(n, 1/2) as numerator over 2^n.
func signTestTail(n, k int) *big.Int {
	tail, c := new(big.Int), big.NewInt(1)
	for i := 0; i <= n; i++ {
		if i >= k {
			tail.Add(tail, c)
		}
		c.Mul(c, big.NewInt(int64(n-i)))
		c.Quo(c, big.NewInt(int64(i+1)))
	}
	return tail
}

// signTestPasses decides P(X >= k) <= alpha/(10000*m) in exact integers.
func signTestPasses(n, k int, alphaBasisPoints int64, m int) bool {
	if n < 1 || k < 0 || k > n || m < 1 {
		return false
	}
	lhs := new(big.Int).Mul(signTestTail(n, k), big.NewInt(10000*int64(m)))
	return lhs.Cmp(new(big.Int).Lsh(big.NewInt(alphaBasisPoints), uint(n))) <= 0
}

type PaperEvaluationSignTestUnit struct {
	DecisionID     string  `json:"decision_id"`
	TradingDay     string  `json:"trading_day"`
	StressNetCents *string `json:"stress_net_cents,omitempty"`
	Status         string  `json:"status"`
}

type PaperEvaluationStatisticalVariant struct {
	StrategyID            string                        `json:"strategy_id"`
	HoldoutDecisions      int                           `json:"holdout_decisions"`
	HoldoutBaseNetCents   *string                       `json:"holdout_base_net_cents,omitempty"`
	HoldoutStressNetCents *string                       `json:"holdout_stress_net_cents,omitempty"`
	OutOfSampleStatus     string                        `json:"out_of_sample_status"`
	OutOfSampleReasonCode string                        `json:"out_of_sample_reason_code"`
	Units                 []PaperEvaluationSignTestUnit `json:"units"`
	TradingDays           int                           `json:"trading_days"`
	PositiveDays          int                           `json:"positive_days"`
	PValue                *string                       `json:"p_value,omitempty"`
	SampleStatus          string                        `json:"sample_status"`
	SampleReasonCode      string                        `json:"sample_reason_code"`
}

// PaperEvaluationStatisticalTests is derived from the retained portfolio,
// holdout partition and sample design; it has no sources of its own. The
// sign test treats day-clustered disjoint windows as exchangeable. That is a
// stated assumption, not established independence.
type PaperEvaluationStatisticalTests struct {
	Method                  string                              `json:"method"`
	Plan                    PaperEvaluationStatisticalPlan      `json:"plan"`
	SearchBound             int                                 `json:"search_bound"`
	CorrectedAlpha          string                              `json:"corrected_alpha"`
	Variants                []PaperEvaluationStatisticalVariant `json:"variants"`
	Authority               string                              `json:"authority"`
	ExchangeabilityAssumed  bool                                `json:"exchangeability_assumed"`
	IndependenceEstablished bool                                `json:"independence_established"`
	EvaluationComplete      bool                                `json:"evaluation_complete"`
	AdmissionAvailable      bool                                `json:"admission_available"`
	ExecutionAvailable      bool                                `json:"execution_available"`
	QualificationCredit     bool                                `json:"qualification_credit"`
	SHA256                  string                              `json:"sha256"`
}

func sumNets(values []*string) (*big.Rat, bool) {
	sum := new(big.Rat)
	for _, v := range values {
		x, ok := exactCents(v)
		if !ok {
			return nil, false
		}
		sum.Add(sum, x)
	}
	return sum, true
}

// applyStatisticalTests decides out_of_sample, sample_sufficiency and
// search_multiplicity. Earlier failures stay failed.
func applyStatisticalTests(r *PaperEvaluationAssessment, p *PaperEvaluationStatisticalPlan) error {
	search := r.Evidence.Declaration.Command.Plan.SearchMultiplicity
	if r.Portfolio == nil || r.Holdout == nil || r.SampleDesign == nil || r.SearchHistory == nil || p == nil || search == nil {
		return ErrPaperEvaluationAssessmentInvalid
	}
	m := search.MaximumSearchedStrategies
	returns := map[string]map[string]PaperEvaluationDecisionReturn{}
	for _, v := range r.Portfolio.Variants {
		if !validDecisionReturns(v.Decisions) {
			return ErrPaperEvaluationAssessmentInvalid
		}
		byID := map[string]PaperEvaluationDecisionReturn{}
		for _, d := range v.Decisions {
			byID[d.DecisionID] = d
		}
		returns[v.StrategyID] = byID
	}
	strategyOf := map[string]string{}
	for _, row := range r.Evidence.Rows {
		if row.Status == "selected_research_evidence" {
			strategyOf[row.Decision.Command.ID] = row.Decision.Command.StrategyID
		}
	}
	for id, byID := range returns {
		for decision := range byID {
			if strategyOf[decision] != id {
				return ErrPaperEvaluationAssessmentInvalid
			}
		}
	}
	closed := r.Evidence.Phase == "outcome_deadline_elapsed"
	corrected := new(big.Rat).SetFrac(big.NewInt(p.AlphaBasisPoints), big.NewInt(10000*int64(m)))
	out := PaperEvaluationStatisticalTests{Method: p.Method, Plan: *p, SearchBound: m, CorrectedAlpha: corrected.RatString(), Variants: []PaperEvaluationStatisticalVariant{}, Authority: "owner_derived_predeclared_statistical_tests_only", ExchangeabilityAssumed: true}
	// Failures decided earlier keep their own reason codes.
	holdoutFailed, sampleFailed := map[string]string{}, map[string]string{}
	for _, c := range r.Checks {
		if c.Status != "failed" {
			continue
		}
		switch c.Name {
		case "out_of_sample":
			holdoutFailed[c.StrategyID] = c.ReasonCode
		case "sample_sufficiency":
			sampleFailed[c.StrategyID] = c.ReasonCode
		}
	}
	designs := map[string]PaperEvaluationSampleVariant{}
	for _, v := range r.SampleDesign.Variants {
		designs[v.StrategyID] = v
	}
	floor := new(big.Rat).SetInt64(p.MinimumHoldoutNetCents)
	for _, declared := range r.Evidence.Declaration.Command.Plan.Variants {
		id, byID := declared.ID, returns[declared.ID]
		v := PaperEvaluationStatisticalVariant{StrategyID: id, Units: []PaperEvaluationSignTestUnit{}}

		base, stress, available := []*string{}, []*string{}, true
		for _, h := range r.Holdout.Rows {
			if h.Partition != "holdout" || strategyOf[h.DecisionID] != id {
				continue
			}
			v.HoldoutDecisions++
			d, ok := byID[h.DecisionID]
			if !ok || d.Status != "observed_total_return" {
				available = false
				continue
			}
			base, stress = append(base, d.BaseNetCents), append(stress, d.StressNetCents)
		}
		var baseSum, stressSum *big.Rat
		if available {
			var ok1, ok2 bool
			baseSum, ok1 = sumNets(base)
			stressSum, ok2 = sumNets(stress)
			if !ok1 || !ok2 {
				return ErrPaperEvaluationAssessmentInvalid
			}
			b, s := baseSum.RatString(), stressSum.RatString()
			v.HoldoutBaseNetCents, v.HoldoutStressNetCents = &b, &s
		}
		switch {
		case holdoutFailed[id] != "":
			v.OutOfSampleStatus, v.OutOfSampleReasonCode = "failed", holdoutFailed[id]
		case !closed:
			v.OutOfSampleStatus, v.OutOfSampleReasonCode = "pending", "predeclared_outcome_window_not_closed"
		case !available:
			v.OutOfSampleStatus, v.OutOfSampleReasonCode = "unavailable", "holdout_decision_total_return_unavailable"
		case baseSum.Cmp(floor) < 0 || stressSum.Cmp(floor) < 0:
			v.OutOfSampleStatus, v.OutOfSampleReasonCode = "failed", "holdout_total_return_below_predeclared_floor"
		default:
			v.OutOfSampleStatus, v.OutOfSampleReasonCode = "passed", "holdout_total_return_meets_predeclared_floor_in_base_and_stress"
		}

		// One unit per New York trading day: the earliest-ending selected
		// disjoint window that starts on that date.
		days := map[string]bool{}
		unitsAvailable := true
		for _, w := range designs[id].Windows {
			if w.Status != "selected_disjoint_window" {
				continue
			}
			day := w.Start.In(newYork).Format(time.DateOnly)
			if days[day] {
				continue
			}
			days[day] = true
			u := PaperEvaluationSignTestUnit{DecisionID: w.DecisionID, TradingDay: day, Status: "total_return_unavailable"}
			if d, ok := byID[w.DecisionID]; ok && d.Status == "observed_total_return" {
				net, ok := exactCents(d.StressNetCents)
				if !ok {
					return ErrPaperEvaluationAssessmentInvalid
				}
				u.StressNetCents, u.Status = d.StressNetCents, "negative_or_flat"
				if net.Sign() > 0 {
					u.Status = "positive"
					v.PositiveDays++
				}
			} else {
				unitsAvailable = false
			}
			v.Units = append(v.Units, u)
		}
		v.TradingDays = len(v.Units)
		if unitsAvailable && v.TradingDays > 0 {
			pv := new(big.Rat).SetFrac(signTestTail(v.TradingDays, v.PositiveDays), new(big.Int).Lsh(big.NewInt(1), uint(v.TradingDays))).RatString()
			v.PValue = &pv
		}
		switch {
		case sampleFailed[id] != "":
			v.SampleStatus, v.SampleReasonCode = "failed", sampleFailed[id]
		case !closed:
			v.SampleStatus, v.SampleReasonCode = "pending", "predeclared_outcome_window_not_closed"
		case !unitsAvailable:
			v.SampleStatus, v.SampleReasonCode = "unavailable", "clustered_window_total_return_unavailable"
		case v.TradingDays < p.MinimumTradingDays:
			v.SampleStatus, v.SampleReasonCode = "failed", "trading_day_windows_below_predeclared_minimum"
		case !signTestPasses(v.TradingDays, v.PositiveDays, p.AlphaBasisPoints, m):
			v.SampleStatus, v.SampleReasonCode = "failed", "sign_test_not_significant_at_corrected_alpha"
		default:
			v.SampleStatus, v.SampleReasonCode = "passed", "day_clustered_sign_test_significant_at_corrected_alpha"
		}
		out.Variants = append(out.Variants, v)
	}
	out.SHA256 = workerplan.Hash(out)
	r.StatisticalTests = &out

	byStrategy := map[string]PaperEvaluationStatisticalVariant{}
	for _, v := range out.Variants {
		byStrategy[v.StrategyID] = v
	}
	for i := range r.Checks {
		c := &r.Checks[i]
		v, ok := byStrategy[c.StrategyID]
		if !ok {
			return ErrPaperEvaluationAssessmentInvalid
		}
		switch c.Name {
		case "out_of_sample":
			c.Status, c.ReasonCode = v.OutOfSampleStatus, v.OutOfSampleReasonCode
		case "sample_sufficiency":
			c.Status, c.ReasonCode = v.SampleStatus, v.SampleReasonCode
		case "search_multiplicity":
			// The sign test divides alpha by the declared bound, which the
			// search history has already checked against the registry.
			if c.Status == "failed" {
				continue
			}
			c.Status, c.ReasonCode = "passed", "sign_test_bonferroni_adjusted_for_predeclared_search_bound"
			if !closed {
				c.Status, c.ReasonCode = "pending", "predeclared_outcome_window_not_closed"
			}
		default:
			continue
		}
		if c.Status == "failed" {
			r.Status = "blocked"
		}
	}
	return nil
}

// completeApplicableEvaluation marks the assessment complete when the window
// has closed, nothing failed, and at least one variant has every planned check
// passed or not applicable. Other variants, such as sensitivity neighbors that
// are not centers, stay incomplete. Completion grants no admission.
func completeApplicableEvaluation(r *PaperEvaluationAssessment) {
	if r.Evidence.Phase != "outcome_deadline_elapsed" || r.Status != "incomplete" {
		return
	}
	open := map[string]bool{}
	for _, c := range r.Checks {
		if c.Status != "passed" && c.Status != "not_applicable" {
			open[c.StrategyID] = true
		}
	}
	ids := []string{}
	for _, v := range r.Evidence.Declaration.Command.Plan.Variants {
		if !open[v.ID] && slices.ContainsFunc(r.Checks, func(c PaperEvaluationCheck) bool { return c.StrategyID == v.ID }) {
			ids = append(ids, v.ID)
		}
	}
	if len(ids) > 0 {
		r.Status, r.EvaluationComplete, r.CompleteStrategyIDs = "complete", true, ids
	}
}
