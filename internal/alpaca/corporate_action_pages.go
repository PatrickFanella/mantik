package alpaca

import (
	"errors"
	"slices"
	"time"
)

const MaxCorporateActionChainPages = 32

// CorporateActionPageChain links observed cursors, not a provider-atomic dataset.
// No page count or terminal token establishes action-free or economic coverage.
type CorporateActionPageChain struct {
	Method                   string                    `json:"method"`
	AsOf                     time.Time                 `json:"as_of"`
	RootCaptureID            string                    `json:"root_capture_id"`
	RootRevision             int                       `json:"root_revision"`
	Pages                    []CorporateActionSnapshot `json:"pages"`
	Status                   string                    `json:"status"`
	PaginationTerminated     bool                      `json:"pagination_terminated"`
	RecordsObserved          int                       `json:"records_observed"`
	CandidateCaptures        []CorporateActionPageRef  `json:"candidate_captures"`
	CoverageComplete         bool                      `json:"coverage_complete"`
	ProviderAtomicityProven  bool                      `json:"provider_atomicity_proven"`
	EconomicApplicationReady bool                      `json:"economic_application_ready"`
	Authority                string                    `json:"authority"`
}
type CorporateActionPageRef struct {
	CaptureID    string `json:"capture_id"`
	Revision     int    `json:"revision"`
	SourceSHA256 string `json:"source_sha256"`
}

func sameCorporateActionScope(a, b CorporateActionSnapshot) bool {
	return a.Contract == b.Contract && a.QueryDateBasis == b.QueryDateBasis && a.DataQuality == b.DataQuality && a.Region == b.Region && slices.Equal(a.Symbols, b.Symbols) && a.Start == b.Start && a.End == b.End && a.EvidenceClass == b.EvidenceClass && a.AccessRef == b.AccessRef
}

// CorporateActionPageChain inspects a bounded prefix from one explicit first
// page and historical cut. All links require completed journal acquisitions,
// unchanged query scope and a request after the previous page became available.
// Multiple matching acquisitions are ambiguous; no latest-page choice is made.
func (j *Journal) CorporateActionPageChain(captureID string, revision int, asOf time.Time) (CorporateActionPageChain, error) {
	out := CorporateActionPageChain{Method: "retained-corporate-action-cursor-chain-v1", AsOf: asOf.UTC(), RootCaptureID: captureID, RootRevision: revision, Pages: []CorporateActionSnapshot{}, CandidateCaptures: []CorporateActionPageRef{}, Authority: "journal_owned_source_page_links_only"}
	if !capturePattern.MatchString(captureID) || revision < 1 || revision > 1000000 {
		return out, errors.New("invalid corporate-action root identity")
	}
	snapshots, err := j.CorporateActionSnapshots(asOf)
	if err != nil {
		return out, err
	}
	var root *CorporateActionSnapshot
	for _, s := range snapshots {
		if s.CaptureID == captureID && s.Revision == revision {
			copy := s
			root = &copy
		}
	}
	if root == nil {
		out.Status = "root_unavailable_at_cut"
		return out, nil
	}
	if root.PageToken != "" {
		return out, errors.New("corporate-action chain requires a first-page root")
	}
	current := *root
	cursors := map[string]bool{"": true}
	records := map[string]bool{}
	for {
		out.Pages = append(out.Pages, current)
		out.RecordsObserved += len(current.Records)
		overlap := false
		for _, r := range current.Records {
			if records[r.SourceID] {
				overlap = true
			}
			records[r.SourceID] = true
		}
		if current.RequestedAt == nil {
			out.Status = "acquisition_custody_unavailable"
			return out, nil
		}
		if current.RequestedAt.After(current.ReceivedAt) {
			out.Status = "acquisition_clock_invalid"
			return out, nil
		}
		if overlap {
			out.Status = "overlapping_source_ids"
			return out, nil
		}
		if current.NextPageToken == nil || *current.NextPageToken == "" {
			out.Status = "page_chain_terminated"
			out.PaginationTerminated = true
			return out, nil
		}
		cursor := *current.NextPageToken
		if cursors[cursor] {
			out.Status = "cursor_cycle"
			return out, nil
		}
		if len(out.Pages) == MaxCorporateActionChainPages {
			out.Status = "page_bound_reached"
			return out, nil
		}
		candidates := []CorporateActionSnapshot{}
		noncausal := false
		for _, s := range snapshots {
			if s.PageToken != cursor || !sameCorporateActionScope(*root, s) {
				continue
			}
			if s.RequestedAt == nil || s.RequestedAt.After(s.ReceivedAt) || !s.RequestedAt.After(current.AvailableAt) {
				noncausal = true
				continue
			}
			candidates = append(candidates, s)
		}
		if len(candidates) == 0 {
			out.Status = "next_page_unavailable_at_cut"
			if noncausal {
				out.Status = "next_page_acquisition_not_causal"
			}
			return out, nil
		}
		if len(candidates) > 1 {
			out.Status = "ambiguous_next_page"
			for _, s := range candidates {
				out.CandidateCaptures = append(out.CandidateCaptures, CorporateActionPageRef{s.CaptureID, s.Revision, s.SourceSHA256})
			}
			return out, nil
		}
		cursors[cursor] = true
		current = candidates[0]
	}
}
