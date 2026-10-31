package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/corporatecoverage"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/referencetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const coverageLagDays = 10

func coverageDeclarationCommand(t *testing.T, m *Memory) (PaperEvaluationDeclarationCommand, time.Time) {
	t.Helper()
	c, at := searchDeclarationCommand(t, m)
	c.ID, c.Plan.FamilyID, c.Recipe = "coverage-plan", "coverage-family", PaperCoverageEvaluationRecipe
	c.Plan.CorporateCoverage = &PaperEvaluationCoveragePlan{Method: corporatecoverage.Method, ProcessLagDays: coverageLagDays, Instruments: []PaperEvaluationCoverageInstrument{{
		Symbol: "SPY", AlpacaJournalID: "journal-manufactured", AlpacaRootCollectionIDs: []string{"coverage-alpaca"},
		PolygonJournalID: "polygon-journal-manufactured", PolygonDividendsCollectionID: "spy-dividends", PolygonSplitsCollectionID: "spy-splits",
		IssuerJournalID: "issuer-journal-manufactured", IssuerCollectionID: "spdr-workbook",
	}}}
	return c, at
}

// alpacaCoverageSource retains one terminated Alpaca page covering the whole
// process span, acquired after the window closed.
func alpacaCoverageSource(t *testing.T, m *Memory, owner string, w corporatecoverage.Window, exDate, rate string, received time.Time) {
	t.Helper()
	end, _ := time.Parse(time.DateOnly, w.End)
	processEnd := end.AddDate(0, 0, w.ProcessLagDays).Format(time.DateOnly)
	raw := fmt.Sprintf(`{"corporate_actions":{"cash_dividends":[{"cusip":"78462F103","ex_date":%q,"foreign":false,"id":"a-dividend","payable_date":%q,"process_date":%q,"rate":%s,"special":false,"symbol":"SPY"}]},"next_page_token":null}`, exDate, processEnd, processEnd, rate)
	journal, collection := "journal-manufactured", "coverage-alpaca"
	r := alpacarecord.Recording{Schema: alpacarecord.RecordingSchema, Currency: "USD", CaptureID: collection, Revision: 1, Capability: "corporate_actions", Feed: alpacarecord.CorporateActionFeed, Symbols: []string{"SPY"}, Start: w.Start, End: processEnd, ReceivedAt: received, EvidenceClass: "prospective_market_unqualified", AccessRef: "manufactured-access", RawRef: "journal:" + journal + ":capture-" + collection + "-r1.json", Original: raw, SHA256: alpacarecord.Digest(raw), MaxQuoteAgeSeconds: 60}
	a := alpacarecord.CorporateActionAcquisition{Schema: "mantik-journal-corporate-acquisition-v1", Method: "retained-intent-attempts-capture-completion-v1", JournalID: journal, CollectionID: collection, IntentSHA256: strings.Repeat("a", 64), CompletionSHA256: strings.Repeat("b", 64), CaptureRef: r.RawRef, CaptureSHA256: strings.Repeat("c", 64), RecordingSHA256: workerplan.Hash(r), SourceSHA256: r.SHA256, RequestedAt: received.Add(-time.Second), ResponseAt: received, CompletedAt: received.Add(time.Second), AvailableAt: received.Add(time.Second), Attempts: []alpacarecord.CorporateActionAcquisitionAttempt{{Number: 1, StartSHA256: strings.Repeat("d", 64), ResponseSHA256: strings.Repeat("e", 64), StartedAt: received.Add(-time.Nanosecond), EndedAt: received, State: "response", HTTPStatus: 200}}, Authority: "journal_owned_recorded_acquisition_only"}
	a.SHA256 = workerplan.Hash(a)
	if _, err := m.RetainJournalCorporateActionSource(context.Background(), CorporateActionSourceCommand{ID: "coverage-alpaca-source", ReceiverID: owner, Recording: r}, a, func() time.Time { return received.Add(2 * time.Second) }); err != nil {
		t.Fatal(err)
	}
}

// referenceCoverageSources retains Polygon dividends, splits and the sponsor
// workbook through real journals, requested after the window closed.
func referenceCoverageSources(t *testing.T, m *Memory, owner string, w corporatecoverage.Window, exDate, amount string, requested time.Time) {
	t.Helper()
	start, _ := time.Parse(time.DateOnly, w.Start)
	from, to := start.AddDate(0, 0, -30).Format(time.DateOnly), start.AddDate(0, 0, 60).Format(time.DateOnly)
	ex, _ := time.Parse(time.DateOnly, exDate)
	end, _ := time.Parse(time.DateOnly, w.End)
	pay := end.AddDate(0, 0, w.ProcessLagDays).Format(time.DateOnly)
	p, err := polygonref.OpenJournal(filepath.Join(t.TempDir(), "polygon"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.ID = "polygon-journal-manufactured"
	dividends := polygonref.Spec{ID: "spy-dividends", Method: polygonref.MethodDividends, Symbol: "SPY", Start: from, End: to, AccessRef: "manufactured"}
	splits := dividends
	splits.ID, splits.Method = "spy-splits", polygonref.MethodSplits
	referencetest.PolygonCollection(t, p, dividends, requested, []byte(`{"results":[{"cash_amount":`+amount+`,"currency":"USD","dividend_type":"CD","ex_dividend_date":"`+exDate+`","id":"Pdividend","pay_date":"`+pay+`","ticker":"SPY"}],"status":"OK"}`))
	referencetest.PolygonCollection(t, p, splits, requested, []byte(`{"results":[],"status":"OK"}`))
	i, err := issuerdoc.OpenJournal(filepath.Join(t.TempDir(), "issuer"))
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	i.ID = "issuer-journal-manufactured"
	us := func(d time.Time) string { return d.Format("01/02/2006") }
	later := end.AddDate(0, 3, 0)
	referencetest.IssuerCollection(t, i, issuerdoc.Spec{ID: "spdr-workbook", Method: issuerdoc.MethodSPDRWorkbook, Symbol: "SPY", CUSIP: "78462F103", AccessRef: "manufactured"}, requested, referencetest.SPDRWorkbook(t,
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: us(ex), RecordDate: us(ex), PayableDate: us(end.AddDate(0, 0, w.ProcessLagDays)), Dividend: amount},
		referencetest.SPDRDistribution{Ticker: "SPY", CUSIP: "78462F103", ExDate: us(later), RecordDate: us(later), PayableDate: us(later.AddDate(0, 0, 7)), Dividend: "1.888834"},
	))
	clock := requested.Add(time.Minute)
	for _, item := range []struct{ id, kind, collection string }{{"polygon-dividends", CorporateReferencePolygon, "spy-dividends"}, {"polygon-splits", CorporateReferencePolygon, "spy-splits"}, {"issuer-spy", CorporateReferenceIssuer, "spdr-workbook"}} {
		c := CorporateReferenceSourceCommand{ID: item.id, ReceiverID: owner, Kind: item.kind}
		if item.kind == CorporateReferencePolygon {
			_, a, bodies, err := p.Retained(item.collection)
			if err != nil {
				t.Fatal(err)
			}
			c.Polygon, c.Originals = &a, bodies
		} else {
			_, a, raw, err := i.Retained(item.collection)
			if err != nil {
				t.Fatal(err)
			}
			c.Issuer, c.Originals = &a, [][]byte{raw}
		}
		clock = clock.Add(time.Second)
		retained := clock
		if _, err := m.RetainCorporateReferenceSource(context.Background(), c, func() time.Time { return retained }); err != nil {
			t.Fatal(item.id, err)
		}
	}
}

func coverageFixture(t *testing.T, amount string) (*Memory, PaperEvaluationEvidence, corporatecoverage.Window) {
	t.Helper()
	m := NewMemory()
	c, at := coverageDeclarationCommand(t, m)
	d, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if d.Record.Schema != "mantik-owned-paper-evaluation-declaration-v9" || !slices.Contains(d.Record.PlannedChecks, "corporate_coverage") {
		t.Fatal("coverage plan not declared prospectively", d.Record.Schema, d.Record.PlannedChecks)
	}
	w := coverageWindow(c.Plan, "SPY")
	end, _ := time.Parse(time.DateOnly, w.End)
	closed := end.Add(24 * time.Hour)
	alpacaCoverageSource(t, m, c.ReceiverID, w, w.Start, "1.903516", closed.Add(time.Hour))
	referenceCoverageSources(t, m, c.ReceiverID, w, w.Start, amount, closed.Add(2*time.Hour))
	return m, PaperEvaluationEvidence{ReceiverID: c.ReceiverID, Declaration: d.Record, AsOf: closed.Add(4 * time.Hour)}, w
}

func coverageAt(t *testing.T, m *Memory, e PaperEvaluationEvidence) PaperEvaluationCorporateCoverage {
	t.Helper()
	cut, err := ownedCorporateEvaluationCut(t.Context(), e, m.corporateActionSources)
	if err != nil {
		t.Fatal(err)
	}
	strategies, _ := m.PaperStrategies(t.Context())
	got, err := ownedCorporateCoverage(t.Context(), e, cut, paperEvaluationSources{corporateSources: m.corporateActionSources, referenceSources: m.corporateReferenceSources, strategies: strategies})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCorporateCoverageReconcilesDeclaredSourcesAtTheCut(t *testing.T) {
	m, e, w := coverageFixture(t, "1.903516")
	got := coverageAt(t, m, e)
	in := got.Instruments[0]
	if got.Status != "covered" || !in.Result.Covered() || len(in.Result.Dividends) != 1 || !in.Result.Dividends[0].IssuerConfirmed || in.Result.Window != w || !slices.Equal(in.StrategyIDs, []string{"baseline-v1", "variant-v1"}) || len(in.AlpacaChains) != 1 || in.AlpacaChains[0].Status != "page_chain_terminated" || in.IssuerSourceSHA256 == "" || got.EconomicApplicationReady || got.AdmissionAvailable || got.QualificationCredit {
		t.Fatalf("%+v", got)
	}
	if retained, err := retainedCorporateCoverage(e, &got); err != nil || !reflect.DeepEqual(retained, got) {
		t.Fatal("retained coverage did not rederive", err)
	}
	r := PaperEvaluationAssessment{Status: "incomplete", Checks: []PaperEvaluationCheck{{StrategyID: "baseline-v1", Name: "corporate_coverage", Status: "unavailable"}, {StrategyID: "baseline-v1", Name: "benchmark_comparison", Status: "unavailable"}}}
	applyCorporateCoverage(&r, got)
	if r.Checks[0].Status != "passed" || r.Checks[1].Status != "unavailable" || r.Checks[1].ReasonCode != "reconciled_corporate_actions_not_yet_applied_to_total_return" || r.Status != "incomplete" {
		t.Fatal("coverage passed more than its own check", r.Checks)
	}
	for name, change := range map[string]func(*PaperEvaluationCorporateCoverage){
		"status": func(c *PaperEvaluationCorporateCoverage) { c.Instruments[0].Result.Status = "unreconciled" },
		"window": func(c *PaperEvaluationCorporateCoverage) { c.Instruments[0].Result.Window.ProcessLagDays++ },
		"plan":   func(c *PaperEvaluationCorporateCoverage) { c.Instruments[0].Plan.PolygonSplitsCollectionID = "other" },
		"strategies": func(c *PaperEvaluationCorporateCoverage) {
			c.Instruments[0].StrategyIDs = c.Instruments[0].StrategyIDs[:1]
		},
		"economic": func(c *PaperEvaluationCorporateCoverage) { c.EconomicApplicationReady = true },
		"clock":    func(c *PaperEvaluationCorporateCoverage) { c.AsOf = c.AsOf.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(got)
			change(&bad)
			if _, err := retainedCorporateCoverage(e, &bad); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
				t.Fatal("tampered coverage accepted", err)
			}
		})
	}
}

func TestCorporateCoverageFailsClosedOnDisagreementAndLateSources(t *testing.T) {
	m, e, _ := coverageFixture(t, "1.903517")
	got := coverageAt(t, m, e)
	reasons := got.Instruments[0].Result.Reasons
	if got.Status != "unreconciled" || !slices.Contains(reasons, "alpaca_dividend_unmatched") || !slices.Contains(reasons, "polygon_dividend_unmatched") {
		t.Fatal("amount disagreement covered", got.Status, reasons)
	}
	r := PaperEvaluationAssessment{Checks: []PaperEvaluationCheck{{StrategyID: "variant-v1", Name: "corporate_coverage", Status: "unavailable"}}}
	applyCorporateCoverage(&r, got)
	if r.Checks[0].Status != "unavailable" || r.Checks[0].ReasonCode != "corporate_actions_unreconciled_for_window" {
		t.Fatal(r.Checks)
	}
	m, e, _ = coverageFixture(t, "1.903516")
	early := e
	early.AsOf = m.corporateReferenceSources[0].RetainedAt.Add(-time.Nanosecond)
	got = coverageAt(t, m, early)
	if got.Status != "unreconciled" || !slices.Contains(got.Instruments[0].Result.Reasons, "polygon_dividends_missing") || got.Instruments[0].PolygonDividendsSourceSHA256 != "" {
		t.Fatal("reference retained after the cut entered it", got.Instruments[0].Result.Reasons)
	}
}

func TestCoveragePlanIsProspectiveAndBound(t *testing.T) {
	m := NewMemory()
	c, at := coverageDeclarationCommand(t, m)
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"missing": func(c *PaperEvaluationDeclarationCommand) { c.Plan.CorporateCoverage = nil },
		"method":  func(c *PaperEvaluationDeclarationCommand) { c.Plan.CorporateCoverage.Method = "other" },
		"lag": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.CorporateCoverage.ProcessLagDays = corporatecoverage.MaxProcessLagDays + 1
		},
		"no search bound": func(c *PaperEvaluationDeclarationCommand) { c.Plan.SearchMultiplicity = nil },
		"issuer omitted": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.CorporateCoverage.Instruments[0].IssuerJournalID, c.Plan.CorporateCoverage.Instruments[0].IssuerCollectionID = "", ""
		},
		"same polygon collection": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.CorporateCoverage.Instruments[0].PolygonSplitsCollectionID = "spy-dividends"
		},
		"duplicate root": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.CorporateCoverage.Instruments[0].AlpacaRootCollectionIDs = []string{"a", "a"}
		},
		"too many roots": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.CorporateCoverage.Instruments[0].AlpacaRootCollectionIDs = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(c)
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid coverage plan accepted")
			}
		})
	}
	other := clone(c)
	other.Plan.CorporateCoverage.Instruments[0].Symbol = "QQQ"
	other.Plan.CorporateCoverage.Instruments[0].IssuerJournalID, other.Plan.CorporateCoverage.Instruments[0].IssuerCollectionID = "", ""
	if _, err := m.DeclarePaperEvaluation(t.Context(), other, func() time.Time { return at.Add(time.Second) }); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("coverage plan for another instrument accepted", err)
	}
	strategies, _ := m.PaperStrategies(t.Context())
	policies, _ := m.AllocationPolicies(t.Context())
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, at.Add(time.Second), "mantik-owned-paper-evaluation-declaration-v8"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("coverage plan downgrade accepted", err)
	}
}
