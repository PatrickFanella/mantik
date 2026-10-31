package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/corporatecoverage"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/referencetest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type coverageSourceBackend interface {
	RetainJournalCorporateActionSource(context.Context, storage.CorporateActionSourceCommand, alpacarecord.CorporateActionAcquisition, func() time.Time) (storage.CorporateActionSourceReceipt, error)
	RetainCorporateReferenceSource(context.Context, storage.CorporateReferenceSourceCommand, func() time.Time) (storage.CorporateReferenceSourceReceipt, error)
}

// The fixture window is 2026-10-21 with a 10-day process lag. Every source is
// manufactured, acquired after the window closed, and retained through the
// owner methods that a configured journal reader would call.
const coverageExDate, coverageAmount = "2026-10-21", "1.903516"

var coverageClosed = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)

func coveragePlan() *storage.PaperEvaluationCoveragePlan {
	return &storage.PaperEvaluationCoveragePlan{Method: corporatecoverage.Method, ProcessLagDays: 10, Instruments: []storage.PaperEvaluationCoverageInstrument{{
		Symbol: "SPY", AlpacaJournalID: "journal-coverage", AlpacaRootCollectionIDs: []string{"coverage-alpaca"},
		PolygonJournalID: "polygon-journal-coverage", PolygonDividendsCollectionID: "spy-dividends", PolygonSplitsCollectionID: "spy-splits",
		IssuerJournalID: "issuer-journal-coverage", IssuerCollectionID: "spdr-workbook",
	}}}
}

func retainCoverageSources(t *testing.T, store any, owner string, clock *ownedDecisionClock) {
	t.Helper()
	s, ok := store.(coverageSourceBackend)
	if !ok {
		t.Fatal("backend lacks journal source custody")
	}
	ctx := context.Background()
	received := coverageClosed.Add(time.Hour)
	raw := fmt.Sprintf(`{"corporate_actions":{"cash_dividends":[{"cusip":"78462F103","ex_date":%q,"foreign":false,"id":"a-dividend","payable_date":"2026-10-31","process_date":"2026-10-31","rate":%s,"special":false,"symbol":"SPY"}]},"next_page_token":null}`, coverageExDate, coverageAmount)
	r := alpacarecord.Recording{Schema: alpacarecord.RecordingSchema, Currency: "USD", CaptureID: "coverage-alpaca", Revision: 1, Capability: "corporate_actions", Feed: alpacarecord.CorporateActionFeed, Symbols: []string{"SPY"}, Start: "2026-10-21", End: "2026-10-31", ReceivedAt: received, EvidenceClass: "prospective_market_unqualified", AccessRef: "manufactured-access", RawRef: "journal:journal-coverage:capture-coverage-alpaca-r1.json", Original: raw, SHA256: alpacarecord.Digest(raw), MaxQuoteAgeSeconds: 60}
	a := alpacarecord.CorporateActionAcquisition{Schema: "mantik-journal-corporate-acquisition-v1", Method: "retained-intent-attempts-capture-completion-v1", JournalID: "journal-coverage", CollectionID: r.CaptureID, IntentSHA256: strings.Repeat("a", 64), CompletionSHA256: strings.Repeat("b", 64), CaptureRef: r.RawRef, CaptureSHA256: strings.Repeat("c", 64), RecordingSHA256: workerplan.Hash(r), SourceSHA256: r.SHA256, RequestedAt: received.Add(-time.Second), ResponseAt: received, CompletedAt: received.Add(time.Second), AvailableAt: received.Add(time.Second), Attempts: []alpacarecord.CorporateActionAcquisitionAttempt{{Number: 1, StartSHA256: strings.Repeat("d", 64), ResponseSHA256: strings.Repeat("e", 64), StartedAt: received.Add(-time.Nanosecond), EndedAt: received, State: "response", HTTPStatus: 200}}, Authority: "journal_owned_recorded_acquisition_only"}
	a.SHA256 = workerplan.Hash(a)
	clock.at = received.Add(2 * time.Second)
	if _, err := s.RetainJournalCorporateActionSource(ctx, storage.CorporateActionSourceCommand{ID: "coverage-alpaca", ReceiverID: owner, Recording: r}, a, clock.Now); err != nil {
		t.Fatal(err)
	}
	requested := coverageClosed.Add(2 * time.Hour)
	p, err := polygonref.OpenJournal(filepath.Join(t.TempDir(), "polygon"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.ID = "polygon-journal-coverage" // the plan names this manufactured identity
	dividends := polygonref.Spec{ID: "spy-dividends", Method: polygonref.MethodDividends, Symbol: "SPY", Start: "2026-10-01", End: "2026-10-31", AccessRef: "manufactured"}
	splits := dividends
	splits.ID, splits.Method = "spy-splits", polygonref.MethodSplits
	referencetest.PolygonCollection(t, p, dividends, requested, []byte(`{"results":[{"cash_amount":`+coverageAmount+`,"currency":"USD","dividend_type":"CD","ex_dividend_date":"`+coverageExDate+`","id":"Pdividend","pay_date":"2026-10-31","ticker":"SPY"}],"status":"OK"}`))
	referencetest.PolygonCollection(t, p, splits, requested, []byte(`{"results":[],"status":"OK"}`))
	i, err := issuerdoc.OpenJournal(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	i.ID = "issuer-journal-coverage"
	referencetest.IssuerCollection(t, i, issuerdoc.Spec{ID: "spdr-workbook", Method: issuerdoc.MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured"}, requested, referencetest.SPDRWorkbook(t,
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: "10/21/2026", RecordDate: "10/21/2026", PayableDate: "10/31/2026", Dividend: coverageAmount},
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: "12/18/2026", RecordDate: "12/18/2026", PayableDate: "01/29/2027", Dividend: "1.888834"},
	))
	clock.at = requested.Add(time.Minute)
	for _, id := range []string{"spy-dividends", "spy-splits", "spdr-workbook"} {
		c := storage.CorporateReferenceSourceCommand{ID: "reference-" + id, ReceiverID: owner, Kind: storage.CorporateReferencePolygon}
		if id == "spdr-workbook" {
			_, a, raw, err := i.Retained(id)
			if err != nil {
				t.Fatal(err)
			}
			c.Kind, c.Issuer, c.Originals = storage.CorporateReferenceIssuer, &a, [][]byte{raw}
		} else {
			_, a, bodies, err := p.Retained(id)
			if err != nil {
				t.Fatal(err)
			}
			c.Polygon, c.Originals = &a, bodies
		}
		clock.at = clock.at.Add(time.Second)
		if _, err := s.RetainCorporateReferenceSource(ctx, c, clock.Now); err != nil {
			t.Fatal(id, err)
		}
	}
}

func TestOwnedCorporateCoverageAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m, portfolioFixtureOptions{holdout: true, delayed: true, benchmarks: true, causal: true, sampleDesign: true, search: true, coverage: true})
}
func TestPostgresContractsOwnedCorporateCoverageAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, portfolioFixtureOptions{holdout: true, delayed: true, benchmarks: true, causal: true, sampleDesign: true, search: true, coverage: true})
}

// The fixture holds positions for seconds inside 2026-10-21, after its
// 04:00 UTC ex-date boundary, so no action applies and total return equals
// observed equity.
func assertTotalReturnBenchmarks(t *testing.T, r storage.PaperEvaluationAssessment) {
	t.Helper()
	v := r.Portfolio.Variants[0]
	for _, tr := range []*storage.PaperEvaluationTotalReturn{v.BaseTotalReturn, v.StressTotalReturn} {
		if tr == nil || !tr.Strategy.Available() || len(tr.Strategy.Applied) != 0 || tr.Strategy.AdmissionAvailable || tr.Strategy.QualificationCredit {
			t.Fatal("covered instrument lacks total return", tr)
		}
	}
	for _, check := range r.Checks {
		if check.Name != "benchmark_comparison" {
			continue
		}
		// The strategy lost 7000 cents while the passive book gained 1000; the
		// predeclared minimum excess is zero, so the comparison fails.
		if check.Status != "failed" || check.ReasonCode != "total_return_excess_below_predeclared_minimum" || r.Status != "blocked" || deref(v.BaseTotalReturn.Strategy.NetCents) != "-7000" || passiveNet(v.BaseTotalReturn) != "observed_total_return 1000" {
			t.Fatal("covered total return did not decide the comparison", check, deref(v.BaseTotalReturn.Strategy.NetCents), passiveNet(v.BaseTotalReturn))
		}
	}
	if r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("total return granted authority", r)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func passiveNet(t *storage.PaperEvaluationTotalReturn) string {
	if t.Passive == nil {
		return "<no passive book>"
	}
	return t.Passive.Status + " " + deref(t.Passive.NetCents)
}

func TestOwnedTotalReturnAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m, portfolioFixtureOptions{holdout: true, delayed: true, benchmarks: true, causal: true, sampleDesign: true, search: true, coverage: true, totalReturn: true})
}
func TestPostgresContractsOwnedTotalReturnAssessmentHTTP(t *testing.T) {
	postgresPortfolioAssessmentHTTP(t, portfolioFixtureOptions{holdout: true, delayed: true, benchmarks: true, causal: true, sampleDesign: true, search: true, coverage: true, totalReturn: true})
}
