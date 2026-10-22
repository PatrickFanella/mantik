package service

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// These are renderer envelope fixtures, not imported owner/qualification proof.
// Actual owner-basis replay and HTTP behavior are exercised by the Memory and
// disposable PostgreSQL contract tests.
func explanationEnvelope(action string) storage.PaperDecisionRecord {
	r := storage.PaperDecisionRecord{Schema: "mantik-owned-paper-decision-v1", Command: storage.PaperDecisionCommand{ID: "decision", ReceiverID: "receiver-abcdefghijklmnop", StrategyID: "baseline", StrategySHA256: strings.Repeat("a", 64), RegistrationID: "collector"}, FinalizedAt: time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC), Status: "finalized", Evaluation: storage.PaperDecisionEvaluation{Action: action, ReasonCode: "collector_unavailable"}, BasisSHA256: strings.Repeat("b", 64), Authority: "service_finalized_research_proposal_only"}
	r.SHA256 = workerplan.Hash(r)
	return r
}
func TestDecisionExplanationUsesOnlyFinalizedFacts(t *testing.T) {
	for _, action := range []string{"abstain", "entry_candidate"} {
		t.Run(action, func(t *testing.T) {
			r := explanationEnvelope(action)
			if action == "entry_candidate" {
				r.Evaluation.ReasonCode = "native_threshold_met_admission_required"
				r.Evaluation.PriceCents = 10000
				r.Evaluation.Signal = true
				r.Evaluation.Coverage = domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{Quality: "usable", Code: "whole_cent_ask_v1"}, Session: domain.MarketSessionCoverage{Status: "open", Code: "regular_session_open"}}
				r.SHA256 = ""
				r.SHA256 = workerplan.Hash(r)
			}
			before := r
			out, err := explainPaperDecision(r)
			if err != nil {
				t.Fatal(err)
			}
			again, err := explainPaperDecision(r)
			if err != nil || !reflect.DeepEqual(out, again) || !reflect.DeepEqual(r, before) {
				t.Fatal("renderer changed facts", out, again, err)
			}
			if out.DecisionSHA256 != r.SHA256 || out.BasisSHA256 != r.BasisSHA256 || out.Action != action || out.ReasonCode != r.Evaluation.ReasonCode || out.ReferencePriceCents != r.Evaluation.PriceCents || !reflect.DeepEqual(out.Coverage, r.Evaluation.Coverage) || out.FinalizedAt != r.FinalizedAt {
				t.Fatal(out)
			}
			if out.ModelUsed || out.AdmissionAvailable || out.ExecutionAvailable || out.QualificationCredit {
				t.Fatal("prose granted authority", out)
			}
			text := strings.Join(out.Statements, " ")
			if action == "abstain" && (strings.Contains(text, "reference price was") || strings.Contains(text, "Retained quote coverage:")) {
				t.Fatal("invented missing facts", text)
			}
			seal := out.SHA256
			out.SHA256 = ""
			if workerplan.Hash(out) != seal {
				t.Fatal("unbound explanation")
			}
		})
	}
}
func TestDecisionExplanationRejectsUnfinalizedOrAlteredEnvelope(t *testing.T) {
	for _, mutate := range []func(*storage.PaperDecisionRecord){func(r *storage.PaperDecisionRecord) { r.Status = "draft" }, func(r *storage.PaperDecisionRecord) { r.Evaluation.ReasonCode = "" }, func(r *storage.PaperDecisionRecord) { r.AdmissionAvailable = true }, func(r *storage.PaperDecisionRecord) { r.ExecutionAvailable = true }, func(r *storage.PaperDecisionRecord) { r.QualificationCredit = true }, func(r *storage.PaperDecisionRecord) { r.Evaluation.Action = "execute" }, func(r *storage.PaperDecisionRecord) { r.FinalizedAt = time.Time{} }} {
		r := explanationEnvelope("abstain")
		mutate(&r)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(r)
		if _, err := explainPaperDecision(r); err == nil {
			t.Fatal("invalid finalized envelope accepted", r)
		}
	}
	r := explanationEnvelope("abstain")
	r.BasisSHA256 = strings.Repeat("c", 64)
	if _, err := explainPaperDecision(r); err == nil {
		t.Fatal("changed basis accepted")
	}
}
