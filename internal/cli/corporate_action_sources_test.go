package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// The caller is the existing actual Memory/PostgreSQL finalized-owner HTTP
// harness. Original page bytes are manufactured; custody never authenticates it.
func corporateActionSourceHTTPContracts(t *testing.T, base string, d storage.PaperDecisionRecord) {
	t.Helper()
	ctx := context.Background()
	raw := `{"corporate_actions":{"cash_dividends":[{"id":"manufactured","rate":0.123456789}]},"next_page_token":null}`
	c := storage.CorporateActionSourceCommand{ID: "corporate-http", ReceiverID: d.Command.ReceiverID, Recording: alpacarecord.Recording{Schema: alpacarecord.RecordingSchema, Currency: "USD", CaptureID: "corporate-http", Revision: 1, Capability: "corporate_actions", Feed: alpacarecord.CorporateActionFeed, Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", ReceivedAt: d.FinalizedAt.Add(-time.Nanosecond), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured", RawRef: "journal:http-fixture:capture-corporate-http-r1.json", Original: raw, SHA256: alpacarecord.Digest(raw), MaxQuoteAgeSeconds: 60}}
	var receipt storage.CorporateActionSourceReceipt
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources", c, &receipt, nil, storage.MaxCorporateActionSourceBytes+4096, true); err != nil || receipt.Duplicate || receipt.Record.Command.Recording.Original != raw || receipt.Record.ProviderAttested || receipt.Record.CoverageComplete || receipt.Record.EconomicApplicationReady || receipt.Record.AdmissionAvailable || receipt.Record.ExecutionAvailable || receipt.Record.QualificationCredit {
		t.Fatal("source custody failed or acquired authority", receipt, err)
	}
	var retry storage.CorporateActionSourceReceipt
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources", c, &retry, nil, storage.MaxCorporateActionSourceBytes+4096, true); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, receipt.Record) {
		t.Fatal("source retry changed", err)
	}
	var retained storage.CorporateActionSourceRecord
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources/"+c.ID, nil, &retained, nil, storage.MaxCorporateActionSourceBytes+4096, true); err != nil || !reflect.DeepEqual(retained, receipt.Record) {
		t.Fatal("original source readback lost", err)
	}
	var catalog struct {
		ReceiverID          string            `json:"receiver_id"`
		Records             []json.RawMessage `json:"records"`
		Authority           string            `json:"authority"`
		QualificationCredit bool              `json:"qualification_credit"`
	}
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources", nil, &catalog, nil, 32768, true); err != nil || len(catalog.Records) != 1 || catalog.ReceiverID != c.ReceiverID || catalog.QualificationCredit {
		t.Fatal("source discovery failed", catalog, err)
	}
	// Existing assessments predate this append. Their inspection must resolve
	// the retained cut rather than use the current corporate-source catalogue.
	var assessments storage.PaperEvaluationAssessmentCatalog
	if err := requestBounded(ctx, base, "/v1/paper-evaluation-assessments", nil, &assessments, nil, storage.MaxPaperEvaluationAssessmentCatalogBytes, true); err != nil || len(assessments.Records) == 0 {
		t.Fatal("assessment fixture unavailable", err)
	}
	for _, a := range assessments.Records {
		var inspection storage.PaperEvaluationCorporateInspection
		path := "/v1/paper-evaluation-assessments/" + a.Command.ID + "/corporate-sources"
		if err := requestBounded(ctx, base, path, nil, &inspection, nil, storage.MaxPaperEvaluationCorporateInspectionBytes, true); err != nil || inspection.AssessmentSHA256 != a.SHA256 || len(inspection.Pages) != 0 || inspection.ProviderAttested || inspection.CoverageComplete || inspection.EconomicApplicationReady || inspection.AdmissionAvailable || inspection.ExecutionAvailable || inspection.QualificationCredit {
			t.Fatal("assessment HTTP inspection used later source or granted authority", inspection, err)
		}
		if err := requestBounded(ctx, base, path+"?at=future", nil, &inspection, nil, 4096, true); err == nil {
			t.Fatal("inspection accepted a caller-selected cut")
		}
		var chain storage.PaperEvaluationCorporateChain
		chainPath := "/v1/paper-evaluation-assessments/" + a.Command.ID + "/corporate-page-chains/" + c.ID
		if err := requestBounded(ctx, base, chainPath, nil, &chain, nil, storage.MaxPaperEvaluationCorporateChainBytes, true); err != nil || chain.Status != "root_unavailable_at_cut" || len(chain.Pages) != 0 || chain.RootSourceSHA256 != "" || chain.AssessmentSHA256 != a.SHA256 || chain.PaginationTerminated || chain.CoverageComplete || chain.EconomicApplicationReady || chain.AdmissionAvailable || chain.ExecutionAvailable || chain.QualificationCredit {
			t.Fatal("chain used source appended after assessment", chain, err)
		}
		if err := requestBounded(ctx, base, chainPath+"?at=future", nil, &chain, nil, 4096, true); err == nil {
			t.Fatal("chain accepted a caller clock")
		}
	}
	var absent storage.CorporateActionSourceRecord
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources/missing", nil, &absent, nil, 4096, true); err == nil {
		t.Fatal("missing original became an owner record")
	}
	old := c.ReceiverID
	c.ReceiverID = "receiver-abcdefghijklmnop"
	if err := requestBounded(ctx, base, "/v1/corporate-action-sources", c, &retry, nil, 4096, true); err == nil {
		t.Fatal("wrong receiver accepted")
	}
	c.ReceiverID = old
	// This separate journal must not widen the quote/calendar publication owner.
	var ignored any
	if err := requestBounded(ctx, base, "/v1/feed-evidence", map[string]any{"receiver_id": c.ReceiverID, "recording": c.Recording}, &ignored, nil, 4096, true); err == nil {
		t.Fatal("source custody became quote publication")
	}
}
