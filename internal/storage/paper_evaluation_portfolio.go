package storage

import (
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperPortfolioMethod = "owner-selected-finite-capital-observed-equity-v1"
const PaperPortfolioAssessmentRecipe = "native-predeclared-observed-portfolio-v6"

// The callback is an immutable constructor dependency supplied by the compiled
// service. Neither HTTP commands nor declaration payloads contain it or results.
type PaperPortfolioEvaluator func(context.Context, PaperEvaluationEntrySources) (PaperEvaluationPortfolio, error)

type PaperEvaluationPortfolioVariant struct {
	StrategyID string                 `json:"strategy_id"`
	Status     string                 `json:"status"`
	Base       *papereconomics.Result `json:"base,omitempty"`
	Stress     *papereconomics.Result `json:"stress,omitempty"`
}

type PaperEvaluationPortfolio struct {
	Method              string                            `json:"method"`
	EvidenceSHA256      string                            `json:"evidence_sha256"`
	SourceSHA256        string                            `json:"source_sha256"`
	PolicySHA256        string                            `json:"policy_sha256"`
	Variants            []PaperEvaluationPortfolioVariant `json:"variants"`
	Authority           string                            `json:"authority"`
	EvaluationComplete  bool                              `json:"evaluation_complete"`
	AdmissionAvailable  bool                              `json:"admission_available"`
	ExecutionAvailable  bool                              `json:"execution_available"`
	QualificationCredit bool                              `json:"qualification_credit"`
	SHA256              string                            `json:"sha256"`
}

func NewMemoryWithPortfolioEvaluator(eval PaperPortfolioEvaluator) (*Memory, error) {
	if eval == nil {
		return nil, ErrPaperEvaluationAssessmentInvalid
	}
	m := NewMemory()
	m.portfolioEvaluator = eval
	return m, nil
}

func OpenPostgresWithPortfolioEvaluator(ctx context.Context, url string, eval PaperPortfolioEvaluator) (*Postgres, error) {
	if eval == nil {
		return nil, ErrPaperEvaluationAssessmentInvalid
	}
	p, err := OpenPostgres(ctx, url)
	if err == nil {
		p.portfolioEvaluator = eval
	}
	return p, err
}

func (m *Memory) PaperEvaluationAssessmentRecipe() string {
	if m.portfolioEvaluator != nil {
		return PaperPortfolioAssessmentRecipe
	}
	return "native-predeclared-necessary-checks-v5"
}
func (p *Postgres) PaperEvaluationAssessmentRecipe() string {
	if p.portfolioEvaluator != nil {
		return PaperPortfolioAssessmentRecipe
	}
	return "native-predeclared-necessary-checks-v5"
}

// This validates an envelope for clock fencing. Catalogue replay independently
// selects actual pinned source owners and reexecutes the compiled evaluator.
func validatePortfolioEnvelope(e PaperEvaluationEvidence, p PaperEvaluationPortfolio) error {
	if p.Method != PaperPortfolioMethod || p.Authority != "service_owned_observed_portfolio_accounting_only" || p.EvidenceSHA256 != e.SHA256 || p.PolicySHA256 != e.Declaration.Command.PolicySHA256 || !decisionPin.MatchString(p.SourceSHA256) || len(p.Variants) != len(e.Declaration.Command.Plan.Variants) || p.EvaluationComplete || p.AdmissionAvailable || p.ExecutionAvailable || p.QualificationCredit {
		return ErrPaperEvaluationAssessmentInvalid
	}
	for i, v := range p.Variants {
		if v.StrategyID != e.Declaration.Command.Plan.Variants[i].ID || v.Status == "" || (v.Base == nil) != (v.Stress == nil) {
			return ErrPaperEvaluationAssessmentInvalid
		}
		for _, value := range []*papereconomics.Result{v.Base, v.Stress} {
			if value == nil {
				continue
			}
			u := clone(*value)
			u.SHA256 = ""
			if value.Method != papereconomics.Method || value.Authority != "owner_selected_counterfactual_book_accounting_only" || workerplan.Hash(u) != value.SHA256 || value.AdmissionAvailable || value.ExecutionAvailable || value.QualificationCredit || value.TotalReturnAvailable || value.CorporateActionsApplied || value.ContinuousCoverageAvailable {
				return ErrPaperEvaluationAssessmentInvalid
			}
		}
	}
	u := clone(p)
	u.SHA256 = ""
	if workerplan.Hash(u) != p.SHA256 {
		return ErrPaperEvaluationAssessmentInvalid
	}
	return nil
}

func ownedPortfolioEvaluation(ctx context.Context, e PaperEvaluationEvidence, s paperEvaluationSources) (*PaperEvaluationPortfolio, error) {
	if s.portfolioEvaluator == nil {
		return nil, ErrPaperEvaluationAssessmentInvalid
	}
	inputs, err := evaluationEntrySourcesForEvidence(ctx, e, s.strategies, s.policies, s.retainedOutcomes)
	if err != nil {
		return nil, err
	}
	value, err := s.portfolioEvaluator(ctx, clone(inputs))
	if err != nil {
		return nil, err
	}
	if value.SourceSHA256 != workerplan.Hash(inputs) || validatePortfolioEnvelope(e, value) != nil {
		return nil, ErrPaperEvaluationAssessmentInvalid
	}
	return &value, nil
}

func paperAssessmentWithPortfolio(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, w *PaperEvaluationWarmUp, cut *PaperEvaluationCorporateSourceCut, portfolio *PaperEvaluationPortfolio, schema string) (PaperEvaluationAssessment, error) {
	if schema != "mantik-owned-paper-evaluation-assessment-v6" {
		if portfolio != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return paperEvaluationAssessmentWithSources(c, e, w, cut, schema)
	}
	r, err := paperEvaluationAssessmentWithSources(c, e, w, cut, "mantik-owned-paper-evaluation-assessment-v5")
	if err != nil || portfolio == nil || validatePortfolioEnvelope(e, *portfolio) != nil {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	r.Schema, r.Recipe = schema, PaperPortfolioAssessmentRecipe
	value := clone(*portfolio)
	r.Portfolio = &value
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
	}
	return r, nil
}
