package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperEvaluationCorporateInspectionBytes = 512 << 10

// Metadata resolves an assessment's immutable custody pins, never an economic
// dataset. Original response bodies remain available through targeted source GET.
type PaperEvaluationCorporatePage struct {
	AcquisitionSHA256  string                            `json:"acquisition_sha256,omitempty"`
	Pin                PaperEvaluationCorporateSourcePin `json:"pin"`
	CaptureID          string                            `json:"capture_id"`
	Revision           int                               `json:"revision"`
	Contract           string                            `json:"contract"`
	QueryDateBasis     string                            `json:"query_date_basis"`
	DataQuality        string                            `json:"data_quality"`
	Region             string                            `json:"region"`
	Symbols            []string                          `json:"symbols"`
	Start              string                            `json:"start"`
	End                string                            `json:"end"`
	EvidenceClass      string                            `json:"evidence_class"`
	ReceivedAt         time.Time                         `json:"received_at"`
	PageToken          string                            `json:"page_token"`
	NextPageToken      *string                           `json:"next_page_token"`
	PageState          string                            `json:"page_state"`
	RecordsObserved    int                               `json:"records_observed"`
	AcquisitionCustody string                            `json:"acquisition_custody"`
}
type PaperEvaluationCorporateInspection struct {
	Schema                   string                         `json:"schema"`
	ReceiverID               string                         `json:"receiver_id"`
	AssessmentID             string                         `json:"assessment_id"`
	AssessmentSHA256         string                         `json:"assessment_sha256"`
	DeclarationID            string                         `json:"declaration_id"`
	DeclarationSHA256        string                         `json:"declaration_sha256"`
	AsOf                     time.Time                      `json:"as_of"`
	SourceCutSHA256          string                         `json:"source_cut_sha256,omitempty"`
	Pages                    []PaperEvaluationCorporatePage `json:"pages"`
	Status                   string                         `json:"status"`
	Authority                string                         `json:"authority"`
	ProviderAttested         bool                           `json:"provider_attested"`
	CoverageComplete         bool                           `json:"coverage_complete"`
	EconomicApplicationReady bool                           `json:"economic_application_ready"`
	AdmissionAvailable       bool                           `json:"admission_available"`
	ExecutionAvailable       bool                           `json:"execution_available"`
	QualificationCredit      bool                           `json:"qualification_credit"`
	SHA256                   string                         `json:"sha256"`
}
type PaperEvaluationCorporateInspectionStore interface {
	PaperEvaluationCorporateInspection(context.Context, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationCorporateInspection, error)
}

// corporateCutPage resolves one pinned source of a replayed cut.
func corporateCutPage(pin PaperEvaluationCorporateSourcePin, r CorporateActionSourceRecord) (assessmentCorporatePage, error) {
	page, err := alpacarecord.NormalizeCorporate(r.Command.Recording)
	if err != nil {
		return assessmentCorporatePage{}, err
	}
	m := PaperEvaluationCorporatePage{Pin: pin, CaptureID: page.CaptureID, Revision: page.Revision, Contract: page.Contract, QueryDateBasis: page.QueryDateBasis, DataQuality: page.DataQuality, Region: page.Region, Symbols: clone(page.Symbols), Start: page.Start, End: page.End, EvidenceClass: page.EvidenceClass, ReceivedAt: page.ReceivedAt, PageToken: page.PageToken, NextPageToken: clone(page.NextPageToken), PageState: page.PageState, RecordsObserved: len(page.Records), AcquisitionCustody: "caller_reported_response_only_request_and_completion_unavailable"}
	if r.Acquisition != nil {
		m.AcquisitionSHA256 = r.Acquisition.SHA256
		m.AcquisitionCustody = "service_retained_configured_journal_acquisition"
	}
	return assessmentCorporatePage{m, *page, r.Acquisition}, nil
}

// Both catalogues are replayed from the same owner snapshot. Never join public
// readbacks, choose a latest revision or populate legacy assessments retroactively.
func inspectAssessmentCorporateSources(ctx context.Context, id string, cat PaperEvaluationAssessmentCatalog, records []CorporateActionSourceRecord) (PaperEvaluationCorporateInspection, error) {
	if !paperaccount.ID.MatchString(id) {
		return PaperEvaluationCorporateInspection{}, ErrPaperEvaluationAssessmentInvalid
	}
	owner := cat.ReceiverID
	var assessment *PaperEvaluationAssessment
	for _, v := range cat.Records {
		if v.Command.ID == id {
			copy := v
			assessment = &copy
			break
		}
	}
	if assessment == nil {
		return PaperEvaluationCorporateInspection{}, ErrPaperEvaluationAssessmentNotFound
	}
	a := *assessment
	out := PaperEvaluationCorporateInspection{Schema: "mantik-owned-assessment-corporate-sources-v1", ReceiverID: owner, AssessmentID: id, AssessmentSHA256: a.SHA256, DeclarationID: a.Command.DeclarationID, DeclarationSHA256: a.Command.DeclarationSHA256, AsOf: a.AssessedAt, Pages: []PaperEvaluationCorporatePage{}, Status: "source_cut_unavailable_for_legacy_assessment", Authority: "service_owned_assessment_source_inspection_only"}
	if a.CorporateSourceCut != nil {
		cut, err := ownedCorporateEvaluationCut(ctx, a.Evidence, records)
		if err != nil || !reflect.DeepEqual(cut, *a.CorporateSourceCut) {
			return PaperEvaluationCorporateInspection{}, ErrPaperEvaluationAssessmentInvalid
		}
		out.SourceCutSHA256 = a.CorporateSourceCut.SHA256
		out.Status = "retained_source_inventory_only"
		for _, pin := range a.CorporateSourceCut.Sources {
			if err := ctx.Err(); err != nil {
				return PaperEvaluationCorporateInspection{}, err
			}
			// Catalogue replay already proved the exact prefix and owner hashes.
			page, err := corporateCutPage(pin, records[pin.Sequence-1])
			if err != nil {
				return PaperEvaluationCorporateInspection{}, err
			}
			out.Pages = append(out.Pages, page.metadata)
		}
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > MaxPaperEvaluationCorporateInspectionBytes {
		return PaperEvaluationCorporateInspection{}, ErrPaperEvaluationAssessmentCapacity
	}
	return out, nil
}
func (m *Memory) PaperEvaluationCorporateInspection(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateInspection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	cat, records, err := m.assessmentInspectionSourcesLocked(ctx, de, oe)
	if err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	return inspectAssessmentCorporateSources(ctx, id, cat, records)
}
func (p *Postgres) PaperEvaluationCorporateInspection(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateInspection, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	cat, records, err := readAssessmentInspectionSources(ctx, tx, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	out, err := inspectAssessmentCorporateSources(ctx, id, cat, records)
	if err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperEvaluationCorporateInspection{}, err
	}
	return out, nil
}

func (m *Memory) assessmentInspectionSourcesLocked(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, []CorporateActionSourceRecord, error) {
	cat, err := m.paperAssessmentOwnersLocked(ctx, de, oe)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, nil, err
	}
	sources, err := corporateActionSourceCatalog(ctx, m.receiverID, m.corporateActionSources)
	return cat, sources.Records, err
}

func readAssessmentInspectionSources(ctx context.Context, tx pgx.Tx, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (PaperEvaluationAssessmentCatalog, []CorporateActionSourceRecord, error) {
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return PaperEvaluationAssessmentCatalog{}, nil, err
	}
	all, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, nil, err
	}
	cat, err := readPaperAssessmentOwners(ctx, tx, owner, all, de, oe, portfolio)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, nil, err
	}
	sources, err := readCorporateActionSources(ctx, tx, false)
	return cat, sources.Records, err
}
