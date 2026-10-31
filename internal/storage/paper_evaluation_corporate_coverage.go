package storage

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/corporatecoverage"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperCoverageEvaluationRecipe = "native-threshold-corporate-coverage-plan-v7"
const PaperCoverageAssessmentRecipe = "native-predeclared-corporate-coverage-v12"
const maxCoverageInstruments = 10
const maxCoverageAlpacaRoots = 8

var coverageJournalIdentity = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)

// Acquisition identities only. The operator names which retained collections
// will document each instrument's window; it cannot supply rows, matches or a
// coverage result. An issuer collection is declared exactly for instruments
// with a verified issuer method.
type PaperEvaluationCoverageInstrument struct {
	Symbol                       string   `json:"symbol"`
	AlpacaJournalID              string   `json:"alpaca_journal_id"`
	AlpacaRootCollectionIDs      []string `json:"alpaca_root_collection_ids"`
	PolygonJournalID             string   `json:"polygon_journal_id"`
	PolygonDividendsCollectionID string   `json:"polygon_dividends_collection_id"`
	PolygonSplitsCollectionID    string   `json:"polygon_splits_collection_id"`
	IssuerJournalID              string   `json:"issuer_journal_id,omitempty"`
	IssuerCollectionID           string   `json:"issuer_collection_id,omitempty"`
}
type PaperEvaluationCoveragePlan struct {
	Method         string                              `json:"method"`
	ProcessLagDays int                                 `json:"process_lag_days"`
	Instruments    []PaperEvaluationCoverageInstrument `json:"instruments"`
}

// The ex-date window is the declared observation window's UTC dates.
func coverageWindow(p PaperEvaluationPlan, symbol string) corporatecoverage.Window {
	return corporatecoverage.Window{Symbol: symbol, Start: p.WindowStart.UTC().Format(time.DateOnly), End: p.WindowEnd.UTC().Format(time.DateOnly), ProcessLagDays: p.CorporateCoverage.ProcessLagDays}
}

func validCoveragePlan(c PaperEvaluationDeclarationCommand) bool {
	p := c.Plan.CorporateCoverage
	if p == nil || p.Method != corporatecoverage.Method || c.Plan.SearchMultiplicity == nil || len(p.Instruments) < 1 || len(p.Instruments) > maxCoverageInstruments {
		return false
	}
	for i, in := range p.Instruments {
		if !coverageWindow(c.Plan, in.Symbol).Validate() || i > 0 && p.Instruments[i-1].Symbol >= in.Symbol {
			return false
		}
		ids := []string{in.AlpacaJournalID, in.PolygonJournalID, in.PolygonDividendsCollectionID, in.PolygonSplitsCollectionID}
		if len(in.AlpacaRootCollectionIDs) < 1 || len(in.AlpacaRootCollectionIDs) > maxCoverageAlpacaRoots || in.PolygonDividendsCollectionID == in.PolygonSplitsCollectionID {
			return false
		}
		roots := slices.Clone(in.AlpacaRootCollectionIDs)
		slices.Sort(roots)
		if len(slices.Compact(roots)) != len(in.AlpacaRootCollectionIDs) {
			return false
		}
		ids = append(ids, in.AlpacaRootCollectionIDs...)
		_, _, supported := issuerdoc.SupportedMethod(in.Symbol)
		if supported != (in.IssuerJournalID != "") || supported != (in.IssuerCollectionID != "") {
			return false
		}
		if supported {
			ids = append(ids, in.IssuerJournalID, in.IssuerCollectionID)
		}
		for _, id := range ids {
			if !coverageJournalIdentity.MatchString(id) {
				return false
			}
		}
	}
	return true
}

func coveragePlanSymbols(p *PaperEvaluationCoveragePlan, variants []paperstrategy.Record) error {
	if p == nil {
		return nil
	}
	want := map[string]bool{}
	for _, s := range variants {
		want[s.Command.Definition.Symbol] = true
	}
	if len(want) != len(p.Instruments) {
		return ErrPaperEvaluationDeclarationConflict
	}
	for _, in := range p.Instruments {
		if !want[in.Symbol] {
			return ErrPaperEvaluationDeclarationConflict
		}
	}
	return nil
}

type PaperEvaluationCoverageChain struct {
	RootCollectionID string                              `json:"root_collection_id"`
	Status           string                              `json:"status"`
	Pages            []PaperEvaluationCorporateSourcePin `json:"pages"`
}
type PaperEvaluationInstrumentCoverage struct {
	Plan                         PaperEvaluationCoverageInstrument `json:"plan"`
	StrategyIDs                  []string                          `json:"strategy_ids"`
	AlpacaChains                 []PaperEvaluationCoverageChain    `json:"alpaca_chains"`
	PolygonDividendsSourceSHA256 string                            `json:"polygon_dividends_source_sha256,omitempty"`
	PolygonSplitsSourceSHA256    string                            `json:"polygon_splits_source_sha256,omitempty"`
	IssuerSourceSHA256           string                            `json:"issuer_source_sha256,omitempty"`
	Result                       corporatecoverage.Result          `json:"result"`
}
type PaperEvaluationCorporateCoverage struct {
	Method                   string                              `json:"method"`
	DeclarationSHA256        string                              `json:"declaration_sha256"`
	AsOf                     time.Time                           `json:"as_of"`
	Instruments              []PaperEvaluationInstrumentCoverage `json:"instruments"`
	Status                   string                              `json:"status"`
	Authority                string                              `json:"authority"`
	EconomicApplicationReady bool                                `json:"economic_application_ready"`
	AdmissionAvailable       bool                                `json:"admission_available"`
	ExecutionAvailable       bool                                `json:"execution_available"`
	QualificationCredit      bool                                `json:"qualification_credit"`
	SHA256                   string                              `json:"sha256"`
}

// alpacaCoverageChains resolves each declared root in the assessment's own
// corporate cut and follows it with the same rules as chain inspection.
func alpacaCoverageChains(ctx context.Context, e PaperEvaluationEvidence, cut PaperEvaluationCorporateSourceCut, s paperEvaluationSources, plan PaperEvaluationCoverageInstrument) ([]corporatecoverage.AlpacaChain, []PaperEvaluationCoverageChain, error) {
	pages := []assessmentCorporatePage{}
	for _, pin := range cut.Sources {
		page, err := corporateCutPage(pin, s.corporateSources[pin.Sequence-1])
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, page)
	}
	chains, retained := []corporatecoverage.AlpacaChain{}, []PaperEvaluationCoverageChain{}
	for _, id := range plan.AlpacaRootCollectionIDs {
		roots := []int{}
		for i, p := range pages {
			if p.acquisition != nil && p.acquisition.JournalID == plan.AlpacaJournalID && p.acquisition.CollectionID == id && p.snapshot.PageToken == "" {
				roots = append(roots, i)
			}
		}
		entry := PaperEvaluationCoverageChain{RootCollectionID: id, Status: "root_unavailable_at_cut", Pages: []PaperEvaluationCorporateSourcePin{}}
		if len(roots) > 1 {
			entry.Status = "root_ambiguous"
		}
		if len(roots) != 1 {
			retained = append(retained, entry)
			continue
		}
		root := pages[roots[0]]
		start := PaperEvaluationCorporateChain{Pages: []PaperEvaluationCorporateChainPage{}, CandidateSources: []PaperEvaluationCorporateSourcePin{}}
		walked, err := walkAssessmentCorporateChain(ctx, start, pages, roots[0])
		if err != nil {
			return nil, nil, err
		}
		entry.Status = walked.Status
		chain := corporatecoverage.AlpacaChain{CollectionID: id, Symbols: slices.Clone(root.snapshot.Symbols), Start: root.snapshot.Start, End: root.snapshot.End, RequestedAt: root.acquisition.RequestedAt, Terminated: walked.PaginationTerminated, Records: []alpacarecord.CorporateActionRecord{}}
		for _, p := range walked.Pages {
			entry.Pages = append(entry.Pages, p.Source.Pin)
			for _, v := range pages {
				if v.metadata.Pin.Sequence == p.Source.Pin.Sequence {
					chain.Records = append(chain.Records, v.snapshot.Records...)
				}
			}
		}
		chains = append(chains, chain)
		retained = append(retained, entry)
	}
	return chains, retained, nil
}

// referenceAt returns the unique custody record for a journal collection
// retained by the cut clock, or nil when none was retained by then.
func referenceAt(s paperEvaluationSources, at time.Time, kind, journalID, collectionID string) *CorporateReferenceSourceRecord {
	for _, r := range s.referenceSources {
		if r.RetainedAt.After(at) || r.Kind != kind {
			continue
		}
		if kind == CorporateReferencePolygon && r.Polygon.JournalID == journalID && r.Polygon.Spec.ID == collectionID || kind == CorporateReferenceIssuer && r.Issuer.JournalID == journalID && r.Issuer.Spec.ID == collectionID {
			v := clone(r)
			return &v
		}
	}
	return nil
}

// ownedCorporateCoverage derives every instrument's window result from the
// actual owners at the assessment clock: the retained Alpaca cut and the
// reference custody prefix. Reference writers serialize strictly after
// retained assessments, so historical replay keeps the same prefix.
func ownedCorporateCoverage(ctx context.Context, e PaperEvaluationEvidence, cut PaperEvaluationCorporateSourceCut, s paperEvaluationSources) (PaperEvaluationCorporateCoverage, error) {
	d := e.Declaration
	p := d.Command.Plan.CorporateCoverage
	if !validCoveragePlan(d.Command) {
		return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
	}
	if _, err := corporateReferenceSourceCatalog(ctx, e.ReceiverID, s.referenceSources); err != nil {
		return PaperEvaluationCorporateCoverage{}, err
	}
	strategies := map[string][]string{}
	for _, v := range d.Command.Plan.Variants {
		strategy, err := paperstrategy.Select(s.strategies, v.ID)
		if err != nil || strategy.SHA256 != v.SHA256 {
			return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
		}
		symbol := strategy.Command.Definition.Symbol
		strategies[symbol] = append(strategies[symbol], v.ID)
	}
	out := PaperEvaluationCorporateCoverage{Method: corporatecoverage.Method, DeclarationSHA256: d.SHA256, AsOf: e.AsOf.UTC(), Instruments: []PaperEvaluationInstrumentCoverage{}}
	for _, plan := range p.Instruments {
		chains, retained, err := alpacaCoverageChains(ctx, e, cut, s, plan)
		if err != nil {
			return PaperEvaluationCorporateCoverage{}, err
		}
		item := PaperEvaluationInstrumentCoverage{Plan: plan, StrategyIDs: strategies[plan.Symbol], AlpacaChains: retained}
		in := corporatecoverage.Input{Window: coverageWindow(d.Command.Plan, plan.Symbol), Alpaca: chains}
		if r := referenceAt(s, e.AsOf, CorporateReferencePolygon, plan.PolygonJournalID, plan.PolygonDividendsCollectionID); r != nil {
			item.PolygonDividendsSourceSHA256 = r.SHA256
			in.PolygonDividends = &corporatecoverage.PolygonReference{SourceID: r.ID, RequestedAt: r.Polygon.RequestedAt, Snapshot: r.Snapshot}
		}
		if r := referenceAt(s, e.AsOf, CorporateReferencePolygon, plan.PolygonJournalID, plan.PolygonSplitsCollectionID); r != nil {
			item.PolygonSplitsSourceSHA256 = r.SHA256
			in.PolygonSplits = &corporatecoverage.PolygonReference{SourceID: r.ID, RequestedAt: r.Polygon.RequestedAt, Snapshot: r.Snapshot}
		}
		method, cusip, supported := issuerdoc.SupportedMethod(plan.Symbol)
		in.IssuerRequired, in.IssuerCUSIP = supported, cusip
		if r := referenceAt(s, e.AsOf, CorporateReferenceIssuer, plan.IssuerJournalID, plan.IssuerCollectionID); supported && r != nil {
			item.IssuerSourceSHA256 = r.SHA256
			in.Issuer = &corporatecoverage.IssuerReference{SourceID: r.ID, RequestedAt: r.Issuer.RequestedAt}
			if r.Issuer.Spec.Method == method {
				in.Issuer.Document = r.Document
			}
		}
		item.Result = corporatecoverage.Reconcile(in)
		out.Instruments = append(out.Instruments, item)
	}
	return sealCorporateCoverage(out)
}

func sealCorporateCoverage(out PaperEvaluationCorporateCoverage) (PaperEvaluationCorporateCoverage, error) {
	out.Status, out.Authority = "covered", "owner_derived_window_corporate_coverage_evidence_only"
	out.EconomicApplicationReady, out.AdmissionAvailable, out.ExecutionAvailable, out.QualificationCredit = false, false, false, false
	for _, in := range out.Instruments {
		if !in.Result.Covered() {
			out.Status = "unreconciled"
		}
	}
	out.SHA256 = ""
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

// Envelope validation rederives hashes and statuses from the retained result.
// It is not actual source replay; catalogue reads compare against the owners.
func retainedCorporateCoverage(e PaperEvaluationEvidence, c *PaperEvaluationCorporateCoverage) (PaperEvaluationCorporateCoverage, error) {
	p := e.Declaration.Command.Plan.CorporateCoverage
	if c == nil || !validCoveragePlan(e.Declaration.Command) || c.Method != corporatecoverage.Method || c.DeclarationSHA256 != e.Declaration.SHA256 || !c.AsOf.Equal(e.AsOf) || len(c.Instruments) != len(p.Instruments) {
		return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
	}
	assigned := 0
	for i, in := range c.Instruments {
		result := in.Result
		result.SHA256 = ""
		if !reflect.DeepEqual(in.Plan, p.Instruments[i]) || result.Window != coverageWindow(e.Declaration.Command.Plan, in.Plan.Symbol) || workerplan.Hash(result) != in.Result.SHA256 || len(in.StrategyIDs) == 0 {
			return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
		}
		assigned += len(in.StrategyIDs)
	}
	if assigned != len(e.Declaration.Command.Plan.Variants) {
		return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
	}
	want, err := sealCorporateCoverage(clone(*c))
	if err != nil || !reflect.DeepEqual(want, *c) {
		return PaperEvaluationCorporateCoverage{}, ErrPaperEvaluationAssessmentInvalid
	}
	return want, nil
}

// A covered window passes only the coverage check. Benchmark comparison stays
// unavailable until reconciled actions are applied to total return.
func applyCorporateCoverage(r *PaperEvaluationAssessment, c PaperEvaluationCorporateCoverage) {
	r.CorporateCoverage = &c
	covered := map[string]bool{}
	for _, in := range c.Instruments {
		for _, id := range in.StrategyIDs {
			covered[id] = in.Result.Covered()
		}
	}
	for i := range r.Checks {
		check := &r.Checks[i]
		switch check.Name {
		case "corporate_coverage":
			check.Status, check.ReasonCode = "unavailable", "corporate_actions_unreconciled_for_window"
			if covered[check.StrategyID] {
				check.Status, check.ReasonCode = "passed", "corporate_actions_reconciled_for_window"
			}
		case "benchmark_comparison":
			if check.Status == "unavailable" && covered[check.StrategyID] {
				check.ReasonCode = "reconciled_corporate_actions_not_yet_applied_to_total_return"
			}
		}
	}
}
