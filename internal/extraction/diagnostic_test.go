package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func diagnosticSource() news.Article {
	a := news.FixtureArticle()
	a.Schema = news.DiagnosticArticleSchema
	a.PublishedAt = time.Time{}
	a.EvidenceClass = "recorded_market_unqualified"
	a.Publication = &news.PublicationPrecision{Precision: "date_only", Date: "2025-05-01"}
	return a
}

func TestSwitchyardDiagnosticCaptureReplayEvaluationAndServiceFence(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(localReply(SwitchyardModel, string(quoteJSON(quoteFixture())), "stop"))
	}))
	defer s.Close()
	ctx := context.Background()
	a := diagnosticSource()
	for _, precision := range []news.PublicationPrecision{{Precision: "date_only", Date: "2025-05-01"}, {Precision: "unknown"}} {
		a.Publication = &precision
		var saved []SwitchyardReceipt
		f, err := RunSwitchyard(ctx, a, "event", SwitchyardConfig{Endpoint: s.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r SwitchyardReceipt) error { saved = append(saved, copyJSON(r)); return nil })
		if err != nil || len(saved) != 2 || f.Schema != SwitchyardDiagnosticSchema || f.Result == nil || f.Result.Outcome != "extracted" || f.Result.Authority != "diagnostic_claim_candidates_only" {
			t.Fatal(f, err)
		}
		p := saved[0]
		if bytes.Contains([]byte(f.Result.Article.Original), []byte("0001-01-01")) {
			t.Fatal("invented source")
		}
		report, err := InspectSwitchyard(ctx, p, &f)
		if err != nil || report.Schema != SwitchyardDiagnosticInspectionSchema || report.ExecutionAttested || report.QualificationCredit {
			t.Fatal(report, err)
		}
		cohort := SwitchyardCorpus{Schema: SwitchyardCorpusSchema, Cases: []SwitchyardEvaluationInput{{ID: "finished", Prepared: p, Finished: &f, Expected: FixtureResponse()}, {ID: "unknown", Prepared: p, Expected: FixtureResponse()}}}
		eval, err := EvaluateSwitchyard(ctx, cohort)
		if err != nil || eval.Counts.Compared.ExactCases != 1 || eval.Counts.UnknownCases != 1 || eval.ContractSHA256 != hash(definitionFor(a)) {
			t.Fatal(eval, err)
		}
		exactP, exactF := switchyardAdmissionReceipts(t, 200, localReply(SwitchyardModel, string(quoteJSON(quoteFixture())), "stop"))
		mixed := copyJSON(cohort)
		mixed.Cases = append(mixed.Cases, SwitchyardEvaluationInput{ID: "exact", Prepared: exactP, Finished: &exactF, Expected: FixtureResponse()})
		if _, err := EvaluateSwitchyard(ctx, mixed); err == nil {
			t.Fatal("mixed source contracts accepted")
		}
		again, err := EvaluateSwitchyard(ctx, cohort)
		b, _ := json.Marshal(eval)
		c, _ := json.Marshal(again)
		if err != nil || !bytes.Equal(b, c) {
			t.Fatal("diagnostic replay changed")
		}
		for _, finished := range []*SwitchyardReceipt{nil, &f} {
			if _, err := RetainSwitchyard(ctx, "receiver-abcdefghijklmnop", "diagnostic", p, finished, time.Now()); err == nil {
				t.Fatal("diagnostic entered persistent ledger")
			}
		}
		if _, err := Start("receiver-abcdefghijklmnop", "diagnostic", a, "event", time.Now()); err == nil {
			t.Fatal("diagnostic entered recorded attempt ledger")
		}
		lp := LocalReceipt(p)
		lf := LocalReceipt(f)
		lp.Schema = LocalSchema
		lf.Schema = LocalSchema
		if _, err := InspectLocal(ctx, lp, &lf); err == nil {
			t.Fatal("diagnostic entered local inspection")
		}
		disguised := p
		disguised.Schema = SwitchyardSchema
		if _, err := InspectSwitchyard(ctx, disguised, nil); err == nil {
			t.Fatal("diagnostic relabeled as exact capture")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected request count", calls.Load())
	}
	before := calls.Load()
	if _, err := RunLocal(ctx, a, "event", LocalConfig{Endpoint: s.URL, Model: "ollama/fixture", MaxTokens: 1024}, "", func(LocalReceipt) error { return nil }); err == nil || calls.Load() != before {
		t.Fatal("local diagnostic sent")
	}
}
