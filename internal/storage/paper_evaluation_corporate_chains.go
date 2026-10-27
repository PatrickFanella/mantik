package storage

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperEvaluationCorporateChainBytes = 1 << 20

type PaperEvaluationCorporateChainPage struct {
	Source      PaperEvaluationCorporatePage `json:"source"`
	RequestedAt *time.Time                   `json:"requested_at,omitempty"`
	AvailableAt *time.Time                   `json:"available_at,omitempty"`
}
type PaperEvaluationCorporateChain struct {
	Schema                   string                              `json:"schema"`
	Method                   string                              `json:"method"`
	ReceiverID               string                              `json:"receiver_id"`
	AssessmentID             string                              `json:"assessment_id"`
	AssessmentSHA256         string                              `json:"assessment_sha256"`
	DeclarationID            string                              `json:"declaration_id"`
	DeclarationSHA256        string                              `json:"declaration_sha256"`
	SourceCutSHA256          string                              `json:"source_cut_sha256,omitempty"`
	AsOf                     time.Time                           `json:"as_of"`
	RootSourceID             string                              `json:"root_source_id"`
	RootSourceSHA256         string                              `json:"root_source_sha256,omitempty"`
	Pages                    []PaperEvaluationCorporateChainPage `json:"pages"`
	CandidateSources         []PaperEvaluationCorporateSourcePin `json:"candidate_sources"`
	Status                   string                              `json:"status"`
	RecordsObserved          int                                 `json:"records_observed"`
	PaginationTerminated     bool                                `json:"pagination_terminated"`
	Authority                string                              `json:"authority"`
	ProviderAttested         bool                                `json:"provider_attested"`
	CoverageComplete         bool                                `json:"coverage_complete"`
	EconomicApplicationReady bool                                `json:"economic_application_ready"`
	AdmissionAvailable       bool                                `json:"admission_available"`
	ExecutionAvailable       bool                                `json:"execution_available"`
	QualificationCredit      bool                                `json:"qualification_credit"`
	SHA256                   string                              `json:"sha256"`
}
type PaperEvaluationCorporateChainStore interface {
	PaperEvaluationCorporatePageChain(context.Context, string, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationCorporateChain, error)
}
type assessmentCorporatePage struct {
	metadata    PaperEvaluationCorporatePage
	snapshot    alpacarecord.CorporateActionSnapshot
	acquisition *alpacarecord.CorporateActionAcquisition
}

func sameAssessmentCorporateScope(a, b alpacarecord.CorporateActionSnapshot) bool {
	return a.Contract == b.Contract && a.QueryDateBasis == b.QueryDateBasis && a.DataQuality == b.DataQuality && a.Region == b.Region && slices.Equal(a.Symbols, b.Symbols) && a.Start == b.Start && a.End == b.End && a.EvidenceClass == b.EvidenceClass && a.AccessRef == b.AccessRef
}
func sealAssessmentCorporateChain(out PaperEvaluationCorporateChain) (PaperEvaluationCorporateChain, error) {
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > MaxPaperEvaluationCorporateChainBytes {
		return PaperEvaluationCorporateChain{}, ErrPaperEvaluationAssessmentCapacity
	}
	return out, nil
}

// The explicit root selects an inspection, never a qualified dataset. Every
// candidate is resolved from the replayed assessment cut in one owner snapshot.
func inspectAssessmentCorporateChain(ctx context.Context, owner, id, rootID string, all map[string]PaperEvaluationAssessment, s paperEvaluationSources) (PaperEvaluationCorporateChain, error) {
	if !paperaccount.ID.MatchString(rootID) {
		return PaperEvaluationCorporateChain{}, ErrPaperEvaluationAssessmentInvalid
	}
	inspection, err := inspectAssessmentCorporateSources(ctx, owner, id, all, s)
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	out := PaperEvaluationCorporateChain{Schema: "mantik-owned-assessment-corporate-chain-v1", Method: "assessment-pinned-causal-corporate-page-chain-v1", ReceiverID: owner, AssessmentID: id, AssessmentSHA256: inspection.AssessmentSHA256, DeclarationID: inspection.DeclarationID, DeclarationSHA256: inspection.DeclarationSHA256, SourceCutSHA256: inspection.SourceCutSHA256, AsOf: inspection.AsOf, RootSourceID: rootID, Pages: []PaperEvaluationCorporateChainPage{}, CandidateSources: []PaperEvaluationCorporateSourcePin{}, Status: "root_unavailable_at_cut", Authority: "service_owned_assessment_page_links_only"}
	if inspection.SourceCutSHA256 == "" {
		out.Status = "source_cut_unavailable_for_legacy_assessment"
		return sealAssessmentCorporateChain(out)
	}
	pages := []assessmentCorporatePage{}
	root := -1
	for _, p := range inspection.Pages {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationCorporateChain{}, err
		}
		r := s.corporateSources[p.Pin.Sequence-1]
		snapshot, err := alpacarecord.NormalizeCorporate(r.Command.Recording)
		if err != nil {
			return PaperEvaluationCorporateChain{}, err
		}
		if p.Pin.ID == rootID {
			root = len(pages)
		}
		pages = append(pages, assessmentCorporatePage{p, *snapshot, r.Acquisition})
	}
	if root < 0 {
		return sealAssessmentCorporateChain(out)
	}
	current := root
	out.RootSourceSHA256 = pages[root].metadata.Pin.SHA256
	if pages[root].snapshot.PageToken != "" {
		return PaperEvaluationCorporateChain{}, ErrPaperEvaluationAssessmentInvalid
	}
	cursors := map[string]bool{"": true}
	records := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationCorporateChain{}, err
		}
		p := pages[current]
		entry := PaperEvaluationCorporateChainPage{Source: clone(p.metadata)}
		if p.acquisition != nil {
			requested, available := p.acquisition.RequestedAt, p.acquisition.AvailableAt
			entry.RequestedAt = &requested
			entry.AvailableAt = &available
		}
		out.Pages = append(out.Pages, entry)
		out.RecordsObserved += len(p.snapshot.Records)
		if p.acquisition == nil {
			out.Status = "acquisition_custody_unavailable"
			return sealAssessmentCorporateChain(out)
		}
		overlap := false
		for _, r := range p.snapshot.Records {
			if records[r.SourceID] {
				overlap = true
			}
			records[r.SourceID] = true
		}
		if overlap {
			out.Status = "overlapping_source_ids"
			return sealAssessmentCorporateChain(out)
		}
		if p.snapshot.NextPageToken == nil || *p.snapshot.NextPageToken == "" {
			out.Status = "page_chain_terminated"
			out.PaginationTerminated = true
			return sealAssessmentCorporateChain(out)
		}
		cursor := *p.snapshot.NextPageToken
		if cursors[cursor] {
			out.Status = "cursor_cycle"
			return sealAssessmentCorporateChain(out)
		}
		if len(out.Pages) == MaxCorporateActionSources {
			out.Status = "page_bound_reached"
			return sealAssessmentCorporateChain(out)
		}
		candidates := []int{}
		noncausal := false
		for i, v := range pages {
			if v.snapshot.PageToken != cursor || !sameAssessmentCorporateScope(pages[root].snapshot, v.snapshot) {
				continue
			}
			if v.acquisition == nil {
				noncausal = true
				continue
			}
			if v.acquisition.JournalID != pages[root].acquisition.JournalID {
				continue
			}
			if !v.acquisition.RequestedAt.After(p.acquisition.AvailableAt) {
				noncausal = true
				continue
			}
			candidates = append(candidates, i)
		}
		if len(candidates) == 0 {
			out.Status = "next_page_unavailable_at_cut"
			if noncausal {
				out.Status = "next_page_acquisition_not_causal"
			}
			return sealAssessmentCorporateChain(out)
		}
		if len(candidates) > 1 {
			out.Status = "ambiguous_next_page"
			for _, i := range candidates {
				out.CandidateSources = append(out.CandidateSources, pages[i].metadata.Pin)
			}
			return sealAssessmentCorporateChain(out)
		}
		cursors[cursor] = true
		current = candidates[0]
	}
}
func (m *Memory) PaperEvaluationCorporatePageChain(ctx context.Context, id, rootID string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateChain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	s, err := m.paperEvaluationSourcesLocked(de, oe)
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	return inspectAssessmentCorporateChain(ctx, m.receiverID, id, rootID, m.evaluationAssessments, s)
}
func (p *Postgres) PaperEvaluationCorporatePageChain(ctx context.Context, id, rootID string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationCorporateChain, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	s, err := readPaperEvaluationSources(ctx, tx, false, de, oe)
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	all, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	out, err := inspectAssessmentCorporateChain(ctx, s.declarations.ReceiverID, id, rootID, all, s)
	if err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperEvaluationCorporateChain{}, err
	}
	return out, nil
}
