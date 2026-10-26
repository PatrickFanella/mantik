package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Portable readback shapes only; these do not attest source custody.
func TestDecisionSessionReadbackShape(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "terminal-token")
	d := decisionViewFixture("one")
	d.Evaluation.Action = "entry_candidate"
	d.Evaluation.Coverage.Quote.InputID = "quote"
	d.SHA256 = ""
	d.SHA256 = workerplan.Hash(d)
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(*service.PaperDecisionMarketEvidence)
	}{
		{"v2", true, func(*service.PaperDecisionMarketEvidence) {}},
		{"v1", true, func(v *service.PaperDecisionMarketEvidence) {
			v.Schema = "mantik-owned-decision-market-evidence-v1"
			v.SessionEvidence = nil
		}},
		{"legacy_injection", false, func(v *service.PaperDecisionMarketEvidence) { v.Schema = "mantik-owned-decision-market-evidence-v1" }},
		{"missing", false, func(v *service.PaperDecisionMarketEvidence) { v.SessionEvidence = nil }},
		{"clock", false, func(v *service.PaperDecisionMarketEvidence) {
			v.SessionEvidence.Evidence.At = d.FinalizedAt.Add(time.Second)
		}},
		{"method", false, func(v *service.PaperDecisionMarketEvidence) { v.SessionEvidence.Method = "caller_asserted" }},
		{"scope", false, func(v *service.PaperDecisionMarketEvidence) { v.SessionEvidence.Evidence.Scope = "caller_asserted" }},
		{"quote", false, func(v *service.PaperDecisionMarketEvidence) { v.SessionEvidence.Evidence.Quote.InputID = "other" }},
		{"qualification", false, func(v *service.PaperDecisionMarketEvidence) { v.SessionEvidence.Evidence.QualificationCredit = true }},
		{"liveness", false, func(v *service.PaperDecisionMarketEvidence) {
			v.SessionEvidence.Evidence.CollectorLivenessVerified = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := service.PaperDecisionMarketEvidence{Schema: "mantik-owned-decision-market-evidence-v2", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, RegistrationID: d.Command.RegistrationID, FinalizedAt: d.FinalizedAt, Authority: "service_owned_recorded_market_dimensions_only", Quote: &alpaca.QuoteDimensions{InputID: "quote"}, SessionEvidence: &service.PaperOutcomeSessionEvidence{Method: "owned-decision-quote-session-at-finalization-v1", Evidence: domain.MarketCoverageReport{Scope: "service_owned_decision_quote_session_at_finalization", At: d.FinalizedAt, Quote: domain.MarketQuoteCoverage{InputID: "quote"}}}}
			tc.change(&v)
			v.SHA256 = workerplan.Hash(v)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = emit(w, v) }))
			defer srv.Close()
			got, err := fetchDecisionInspection(context.Background(), srv.URL, d, "m")
			if tc.valid && (err != nil || got == nil) || !tc.valid && (err == nil || got != nil) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestDecisionSessionDocumentedShareReadback(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "terminal-token")
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(*service.PaperDecisionMarketEvidence)
	}{
		{"v3", true, func(*service.PaperDecisionMarketEvidence) {}},
		{"scaled_shares", false, func(v *service.PaperDecisionMarketEvidence) { v.Quote.AskShares *= 100 }},
		{"substituted_contract", false, func(v *service.PaperDecisionMarketEvidence) {
			v.Quote.ShareSizeContract.SourceSHA256 = strings.Repeat("0", 64)
		}},
		{"foreign_feed", false, func(v *service.PaperDecisionMarketEvidence) { v.Feed = "sip" }},
		{"missing_contract", false, func(v *service.PaperDecisionMarketEvidence) { v.Quote.ShareSizeContract = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decisionViewFixture("one")
			raw := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99,"as":5,"bs":7}}}`, d.FinalizedAt.Add(-time.Second).Format(time.RFC3339Nano))
			rec := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "quote", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: d.FinalizedAt, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "artifact:quote", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
			norm, err := alpaca.Normalize(rec)
			if err != nil {
				t.Fatal(err)
			}
			dims, err := alpaca.InspectQuoteShareDimensions(rec, norm.Inputs[0].ID)
			if err != nil || !dims.ShareQuantitiesAvailable {
				t.Fatal(dims, err)
			}
			d.Evaluation.Action = "entry_candidate"
			d.Evaluation.Coverage.Quote.InputID = dims.InputID
			d.SHA256 = ""
			d.SHA256 = workerplan.Hash(d)
			v := service.PaperDecisionMarketEvidence{Schema: "mantik-owned-decision-market-evidence-v3", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, RegistrationID: d.Command.RegistrationID, FinalizedAt: d.FinalizedAt, Feed: "iex", Authority: "service_owned_recorded_market_dimensions_only", Quote: &dims, SessionEvidence: &service.PaperOutcomeSessionEvidence{Method: "owned-decision-quote-session-at-finalization-v1", Evidence: domain.MarketCoverageReport{Scope: "service_owned_decision_quote_session_at_finalization", At: d.FinalizedAt, Quote: domain.MarketQuoteCoverage{InputID: dims.InputID}}}}
			tc.change(&v)
			v.SHA256 = workerplan.Hash(v)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = emit(w, v) }))
			defer srv.Close()
			got, err := fetchDecisionInspection(context.Background(), srv.URL, d, "m")
			if tc.valid && (err != nil || got == nil) || !tc.valid && (err == nil || got != nil) {
				t.Fatal(got, err)
			}
		})
	}
}
