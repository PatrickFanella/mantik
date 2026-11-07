package storage

import (
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperSearchHistoryEvaluationRecipe = "native-threshold-search-history-plan-v6"
const PaperSearchHistoryAssessmentRecipe = "native-predeclared-search-history-v11"
const PaperSearchHistoryMethod = "native-receiver-strategy-registry-history-v1"

// The bound is fixed before observations. It is an upper limit on the actual
// registered search, not a corrected significance threshold or a sample count.
type PaperEvaluationSearchPlan struct {
	Method                    string `json:"method"`
	MaximumSearchedStrategies int    `json:"maximum_searched_strategies"`
}

type PaperEvaluationSearchedStrategy struct {
	StrategyID     string    `json:"strategy_id"`
	StrategySHA256 string    `json:"strategy_sha256"`
	RegisteredAt   time.Time `json:"registered_at"`
	Declared       bool      `json:"declared"`
}

type PaperEvaluationSearchHistory struct {
	Method               string                            `json:"method"`
	DeclarationSHA256    string                            `json:"declaration_sha256"`
	Plan                 PaperEvaluationSearchPlan         `json:"plan"`
	AsOf                 time.Time                         `json:"as_of"`
	Strategies           []PaperEvaluationSearchedStrategy `json:"strategies"`
	SearchedStrategies   int                               `json:"searched_strategies"`
	Status               string                            `json:"status"`
	ReasonCode           string                            `json:"reason_code"`
	Authority            string                            `json:"authority"`
	MultiplicityAdjusted bool                              `json:"multiplicity_adjusted"`
	EvaluationComplete   bool                              `json:"evaluation_complete"`
	AdmissionAvailable   bool                              `json:"admission_available"`
	ExecutionAvailable   bool                              `json:"execution_available"`
	QualificationCredit  bool                              `json:"qualification_credit"`
	SHA256               string                            `json:"sha256"`
}

func validSearchPlan(c PaperEvaluationDeclarationCommand) bool {
	p := c.Plan.SearchMultiplicity
	return p != nil && p.Method == PaperSearchHistoryMethod && p.MaximumSearchedStrategies >= len(c.Plan.Variants) && p.MaximumSearchedStrategies <= paperstrategy.MaxDefinitions && c.Plan.SampleDesign != nil
}

// Every strategy the receiver registered by the assessment clock is part of the
// search, whether or not a declaration selected it. Strategy writers must be
// strictly later than retained assessments, so historical replay keeps this cut.
func searchHistoryFrom(d PaperEvaluationDeclaration, searched []PaperEvaluationSearchedStrategy, at time.Time) (PaperEvaluationSearchHistory, error) {
	p := d.Command.Plan.SearchMultiplicity
	if !validSearchPlan(d.Command) || !declarationTime(at) || len(searched) > paperstrategy.MaxDefinitions {
		return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
	}
	declared := map[string]string{}
	for _, v := range d.Command.Plan.Variants {
		declared[v.ID] = v.SHA256
	}
	out := PaperEvaluationSearchHistory{Method: p.Method, DeclarationSHA256: d.SHA256, Plan: *p, AsOf: at.UTC(), Strategies: clone(searched), Authority: "owner_derived_registered_search_history_only"}
	if out.Strategies == nil {
		out.Strategies = []PaperEvaluationSearchedStrategy{}
	}
	slices.SortFunc(out.Strategies, func(a, b PaperEvaluationSearchedStrategy) int { return strings.Compare(a.StrategyID, b.StrategyID) })
	found := 0
	for i, s := range out.Strategies {
		if !paperaccount.ID.MatchString(s.StrategyID) || !decisionPin.MatchString(s.StrategySHA256) || !declarationTime(s.RegisteredAt) || s.RegisteredAt.After(at) || (i > 0 && out.Strategies[i-1].StrategyID == s.StrategyID) {
			return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
		}
		pin, ok := declared[s.StrategyID]
		if s.Declared != ok || (ok && pin != s.StrategySHA256) {
			return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
		}
		if ok {
			found++
		}
	}
	if found != len(declared) {
		return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
	}
	out.SearchedStrategies = len(out.Strategies)
	out.Status, out.ReasonCode = "unavailable", "search_count_without_multiplicity_adjusted_statistic"
	if out.SearchedStrategies > p.MaximumSearchedStrategies {
		out.Status, out.ReasonCode = "failed", "registered_search_exceeds_predeclared_bound"
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

// Actual replay selects the receiver's real registry at the assessment clock.
func ownedSearchHistory(e PaperEvaluationEvidence, strategies paperstrategy.Catalog, at time.Time) (PaperEvaluationSearchHistory, error) {
	if strategies.Validate() != nil || strategies.ReceiverID != e.ReceiverID {
		return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
	}
	declared := map[string]bool{}
	for _, v := range e.Declaration.Command.Plan.Variants {
		declared[v.ID] = true
	}
	searched := []PaperEvaluationSearchedStrategy{}
	for _, s := range strategies.Records {
		if s.RegisteredAt.After(at) {
			continue
		}
		searched = append(searched, PaperEvaluationSearchedStrategy{StrategyID: s.Command.Definition.ID, StrategySHA256: s.SHA256, RegisteredAt: s.RegisteredAt, Declared: declared[s.Command.Definition.ID]})
	}
	return searchHistoryFrom(e.Declaration, searched, at)
}

// Envelope validation rederives status and hashes from the retained inventory.
// It is not actual registry replay; catalogue reads compare against the owner.
func retainedSearchHistory(e PaperEvaluationEvidence, h *PaperEvaluationSearchHistory) (PaperEvaluationSearchHistory, error) {
	if h == nil {
		return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
	}
	want, err := searchHistoryFrom(e.Declaration, h.Strategies, e.AsOf)
	if err != nil || !reflect.DeepEqual(want, *h) {
		return PaperEvaluationSearchHistory{}, ErrPaperEvaluationAssessmentInvalid
	}
	return want, nil
}

func applySearchHistory(r *PaperEvaluationAssessment, h PaperEvaluationSearchHistory) {
	r.SearchHistory = &h
	for i := range r.Checks {
		c := &r.Checks[i]
		if c.Name != "search_multiplicity" {
			continue
		}
		c.Status, c.ReasonCode = h.Status, h.ReasonCode
		if h.Status == "failed" {
			r.Status = "blocked"
		}
	}
}

// Strategy writers use validated retained envelopes as a clock floor only for
// search-history assessments. Older recipes never selected the registry cut.
func paperStrategySearchClock(owner string, all map[string]PaperEvaluationAssessment, at time.Time) error {
	if !ValidReceiverID(owner) || !declarationTime(at) || len(all) > MaxPaperEvaluationAssessments {
		return ErrPaperEvaluationAssessmentInvalid
	}
	for id, r := range all {
		if !slices.Contains([]string{"mantik-owned-paper-evaluation-assessment-v11", "mantik-owned-paper-evaluation-assessment-v12", "mantik-owned-paper-evaluation-assessment-v13", "mantik-owned-paper-evaluation-assessment-v14", "mantik-owned-paper-evaluation-assessment-v15", "mantik-owned-paper-evaluation-assessment-v16"}, r.Schema) {
			continue
		}
		want, err := paperAssessmentWithBenchmarks(r.Command, r.Evidence, r.WarmUp, r.CorporateSourceCut, r.Portfolio, r.Holdout, r.SearchHistory, r.CorporateCoverage, r.Schema)
		if err != nil || id != r.Command.ID || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) {
			return ErrPaperEvaluationAssessmentInvalid
		}
		if !at.After(r.AssessedAt) {
			return ErrPaperEvaluationAssessmentConflict
		}
	}
	return nil
}
