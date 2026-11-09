package storage

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// Intended acquisition scope only. The operator cannot assert a result, source
// hash, coverage, economic entitlement or admission in a declaration.
type PaperEvaluationCorporateSourcePlan struct {
	Symbol           string `json:"symbol"`
	JournalID        string `json:"journal_id"`
	RootCollectionID string `json:"root_collection_id"`
	AccessRef        string `json:"access_ref"`
	Start            string `json:"start"`
	End              string `json:"end"`
}

var corporatePlanIdentity = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)
var corporatePlanSymbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)

func validateCorporateSourcePlans(plans []PaperEvaluationCorporateSourcePlan) error {
	if plans == nil {
		return nil
	}
	if len(plans) == 0 || len(plans) > 10 {
		return ErrPaperEvaluationDeclarationInvalid
	}
	seen := map[string]bool{}
	for i, p := range plans {
		_, _, err := alpacarecord.CalendarRange(p.Start, p.End)
		key := p.JournalID + ":" + p.RootCollectionID
		if err != nil || !corporatePlanSymbol.MatchString(p.Symbol) || !corporatePlanIdentity.MatchString(p.JournalID) || !corporatePlanIdentity.MatchString(p.RootCollectionID) || len(p.AccessRef) == 0 || len(p.AccessRef) > 256 || !utf8.ValidString(p.AccessRef) || strings.ContainsFunc(p.AccessRef, unicode.IsControl) || seen[key] || i > 0 && plans[i-1].Symbol >= p.Symbol {
			return ErrPaperEvaluationDeclarationInvalid
		}
		seen[key] = true
	}
	return nil
}

func corporateSourcePlanSymbols(plans []PaperEvaluationCorporateSourcePlan, variants []paperstrategy.Record) error {
	if plans == nil {
		return nil
	}
	want := map[string]bool{}
	for _, s := range variants {
		want[s.Command.Definition.Symbol] = true
	}
	if len(want) != len(plans) {
		return ErrPaperEvaluationDeclarationConflict
	}
	for _, p := range plans {
		if !want[p.Symbol] {
			return ErrPaperEvaluationDeclarationConflict
		}
	}
	return nil
}

type PaperEvaluationCorporateSelectionRow struct {
	Plan                     PaperEvaluationCorporateSourcePlan `json:"plan"`
	PlanSHA256               string                             `json:"plan_sha256"`
	Status                   string                             `json:"status"`
	PredeclaredChainResolved bool                               `json:"predeclared_chain_resolved"`
	Chain                    *PaperEvaluationCorporateChain     `json:"chain,omitempty"`
}
type PaperEvaluationCorporateSelection struct {
	Schema                   string                                 `json:"schema"`
	AssessmentID             string                                 `json:"assessment_id"`
	AssessmentSHA256         string                                 `json:"assessment_sha256"`
	DeclarationSHA256        string                                 `json:"declaration_sha256"`
	SourceCutSHA256          string                                 `json:"source_cut_sha256,omitempty"`
	Rows                     []PaperEvaluationCorporateSelectionRow `json:"rows"`
	Status                   string                                 `json:"status"`
	Authority                string                                 `json:"authority"`
	CoverageComplete         bool                                   `json:"coverage_complete"`
	EconomicApplicationReady bool                                   `json:"economic_application_ready"`
	AdmissionAvailable       bool                                   `json:"admission_available"`
	ExecutionAvailable       bool                                   `json:"execution_available"`
	QualificationCredit      bool                                   `json:"qualification_credit"`
	SHA256                   string                                 `json:"sha256"`
}
type PaperEvaluationCorporateSelectionStore interface {
	PaperEvaluationCorporateSelection(context.Context, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationCorporateSelection, error)
}

func selectAssessmentCorporateSources(ctx context.Context, id string, cat PaperEvaluationAssessmentCatalog, records []CorporateActionSourceRecord) (PaperEvaluationCorporateSelection, error) {
	inspection, err := inspectAssessmentCorporateSources(ctx, id, cat, records)
	if err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	var d PaperEvaluationDeclaration
	for _, r := range cat.Records {
		if r.Command.ID == id {
			d = r.Evidence.Declaration
			break
		}
	}
	out := PaperEvaluationCorporateSelection{Schema: "mantik-owned-predeclared-corporate-selection-v1", AssessmentID: id, AssessmentSHA256: inspection.AssessmentSHA256, DeclarationSHA256: inspection.DeclarationSHA256, SourceCutSHA256: inspection.SourceCutSHA256, Rows: []PaperEvaluationCorporateSelectionRow{}, Status: "corporate_source_plan_not_predeclared", Authority: "service_owned_predeclared_acquisition_selection_only"}
	if d.Command.Plan.CorporateSources != nil {
		out.Status = "predeclared_acquisition_selection_only"
		for _, plan := range d.Command.Plan.CorporateSources {
			row := PaperEvaluationCorporateSelectionRow{Plan: plan, PlanSHA256: workerplan.Hash(plan), Status: "predeclared_root_unavailable_at_cut"}
			roots := []CorporateActionSourceRecord{}
			for _, p := range inspection.Pages {
				r := records[p.Pin.Sequence-1]
				if r.Acquisition != nil && r.Acquisition.JournalID == plan.JournalID && r.Acquisition.CollectionID == plan.RootCollectionID {
					roots = append(roots, r)
				}
			}
			if len(roots) > 1 {
				row.Status = "predeclared_root_ambiguous"
			} else if len(roots) == 1 {
				r := roots[0]
				p, err := alpacarecord.NormalizeCorporate(r.Command.Recording)
				if err != nil {
					return PaperEvaluationCorporateSelection{}, err
				}
				if p.PageToken != "" || p.Start != plan.Start || p.End != plan.End || p.AccessRef != plan.AccessRef || !slices.Equal(p.Symbols, []string{plan.Symbol}) {
					row.Status = "predeclared_query_scope_mismatch"
				} else {
					chain, err := inspectAssessmentCorporateChain(ctx, id, r.Command.ID, cat, records)
					if err != nil {
						return PaperEvaluationCorporateSelection{}, err
					}
					row.Chain = &chain
					row.Status = chain.Status
					for _, p := range chain.Pages {
						if p.RequestedAt == nil || p.AvailableAt == nil || !p.RequestedAt.After(d.DeclaredAt) || p.AvailableAt.After(d.Command.Plan.OutcomeDeadline) {
							row.Status = "acquisition_outside_predeclared_clock_bounds"
						}
					}
					row.PredeclaredChainResolved = row.Status == "page_chain_terminated"
				}
			}
			out.Rows = append(out.Rows, row)
		}
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > MaxPaperEvaluationCorporateChainBytes {
		return PaperEvaluationCorporateSelection{}, ErrPaperEvaluationAssessmentCapacity
	}
	return out, nil
}

func (m *Memory) PaperEvaluationCorporateSelection(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateSelection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	cat, records, err := m.assessmentInspectionSourcesLocked(ctx, de, oe)
	if err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	return selectAssessmentCorporateSources(ctx, id, cat, records)
}
func (p *Postgres) PaperEvaluationCorporateSelection(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateSelection, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	cat, records, err := readAssessmentInspectionSources(ctx, tx, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	out, err := selectAssessmentCorporateSources(ctx, id, cat, records)
	if err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperEvaluationCorporateSelection{}, err
	}
	return out, nil
}
