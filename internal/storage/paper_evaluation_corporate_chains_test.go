package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type chainBackend interface {
	journalAssessmentBackend
	PaperEvaluationCorporateChainStore
}
type corporateChainFixture struct {
	s           chainBackend
	de          PaperDecisionEvaluator
	oe          PaperOutcomeEvaluator
	declaration PaperEvaluationDeclaration
	at          time.Time
	step        int
}

func newCorporateChainFixture(t *testing.T, s chainBackend) *corporateChainFixture {
	t.Helper()
	plan, at := declarationFixture(t, s, "prospective_market_unqualified")
	d, err := s.DeclarePaperEvaluation(context.Background(), plan, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Error("unexpected decision evaluation")
		return PaperDecisionEvaluation{}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Error("unexpected outcome evaluation")
		return PaperOutcomeEvaluation{}, nil
	}
	return &corporateChainFixture{s, de, oe, d.Record, at, 2}
}
func (f *corporateChainFixture) retain(t *testing.T, c CorporateActionSourceCommand, a alpacarecord.CorporateActionAcquisition) {
	t.Helper()
	at := f.at.Add(time.Duration(f.step) * time.Second)
	f.step++
	if _, err := f.s.RetainJournalCorporateActionSource(context.Background(), c, a, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
}
func (f *corporateChainFixture) assess(t *testing.T, id string) PaperEvaluationAssessment {
	t.Helper()
	at := f.at.Add(time.Duration(f.step) * time.Second)
	f.step++
	d := f.declaration
	c := PaperEvaluationAssessmentCommand{ID: id, ReceiverID: d.Command.ReceiverID, DeclarationID: d.Command.ID, DeclarationSHA256: d.SHA256}
	r, err := f.s.AssessPaperEvaluation(context.Background(), c, func() time.Time { return at }, f.de, f.oe)
	if err != nil {
		t.Fatal(err)
	}
	return r.Record
}
func (f *corporateChainFixture) chain(t *testing.T, assessment, root string) PaperEvaluationCorporateChain {
	t.Helper()
	r, err := f.s.PaperEvaluationCorporatePageChain(context.Background(), assessment, root, f.de, f.oe)
	if err != nil {
		t.Fatal(err)
	}
	if r.ProviderAttested || r.CoverageComplete || r.EconomicApplicationReady || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("page links became authority", r)
	}
	return r
}
func chainSource(owner, id, token, next, event string, requested time.Time) (CorporateActionSourceCommand, alpacarecord.CorporateActionAcquisition) {
	c, a := journalCorporateSourceFixture(owner, id)
	var cursor any
	if next != "" {
		cursor = next
	}
	raw, _ := json.Marshal(map[string]any{"corporate_actions": map[string]any{"cash_dividends": []any{map[string]any{"id": event}}}, "next_page_token": cursor})
	c.Recording.PageToken = token
	c.Recording.Original = string(raw)
	c.Recording.SHA256 = alpacarecord.Digest(string(raw))
	c.Recording.ReceivedAt = requested.Add(time.Second)
	a.RequestedAt = requested
	a.ResponseAt = c.Recording.ReceivedAt
	a.CompletedAt = requested.Add(2 * time.Second)
	a.AvailableAt = a.CompletedAt
	a.Attempts[0].StartedAt = requested.Add(time.Nanosecond)
	a.Attempts[0].EndedAt = a.ResponseAt
	a.SourceSHA256 = c.Recording.SHA256
	a.RecordingSHA256 = workerplan.Hash(c.Recording)
	a.SHA256 = ""
	a.SHA256 = workerplan.Hash(a)
	return c, a
}
func TestMemoryAssessmentCorporateChains(t *testing.T) { assessmentCorporateChains(t, NewMemory()) }
func TestPostgresContractsAssessmentCorporateChains(t *testing.T) {
	assessmentCorporateChains(t, timerPostgres(t, "assessmentchains"))
}
func assessmentCorporateChains(t *testing.T, s chainBackend) {
	t.Helper()
	ctx := context.Background()
	f := newCorporateChainFixture(t, s)
	owner := f.declaration.Command.ReceiverID
	requested := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	root, rootA := chainSource(owner, "chain-root", "", "next", "root-event", requested)
	f.retain(t, root, rootA)
	old := f.assess(t, "cut-before-next")
	early := f.chain(t, old.Command.ID, root.ID)
	if early.Status != "next_page_unavailable_at_cut" || early.PaginationTerminated || len(early.Pages) != 1 || early.Pages[0].Source.AcquisitionSHA256 != rootA.SHA256 || !early.Pages[0].RequestedAt.Equal(rootA.RequestedAt) {
		t.Fatal(early)
	}
	next, nextA := chainSource(owner, "chain-next", "next", "", "next-event", requested.Add(3*time.Second))
	var wg sync.WaitGroup
	readErrors := make(chan error, 4)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			r, e := s.PaperEvaluationCorporatePageChain(ctx, old.Command.ID, root.ID, f.de, f.oe)
			if e == nil && !reflect.DeepEqual(r, early) {
				e = fmt.Errorf("concurrent append changed historical chain")
			}
			readErrors <- e
		}
	}()
	f.retain(t, next, nextA)
	wg.Wait()
	close(readErrors)
	for e := range readErrors {
		if e != nil {
			t.Fatal(e)
		}
	}
	newer := f.assess(t, "cut-with-next")
	got := f.chain(t, newer.Command.ID, root.ID)
	if got.Status != "page_chain_terminated" || !got.PaginationTerminated || len(got.Pages) != 2 || got.RecordsObserved != 2 || got.AssessmentSHA256 != newer.SHA256 || got.SourceCutSHA256 != newer.CorporateSourceCut.SHA256 || got.RootSourceSHA256 != got.Pages[0].Source.Pin.SHA256 {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(f.chain(t, old.Command.ID, root.ID), early) {
		t.Fatal("later page entered an earlier assessment")
	}
	if r := f.chain(t, old.Command.ID, next.ID); r.Status != "root_unavailable_at_cut" || len(r.Pages) != 0 || r.RootSourceSHA256 != "" {
		t.Fatal("later root leaked", r)
	}
	if _, err := s.PaperEvaluationCorporatePageChain(ctx, newer.Command.ID, next.ID, f.de, f.oe); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("continuation used as root", err)
	}
	if _, err := s.PaperEvaluationCorporatePageChain(ctx, "missing", root.ID, f.de, f.oe); !errors.Is(err, ErrPaperEvaluationAssessmentNotFound) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PaperEvaluationCorporatePageChain(cancelled, newer.Command.ID, root.ID, f.de, f.oe); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	got.Pages[0].Source.Symbols[0] = "MUTATED"
	again := f.chain(t, newer.Command.ID, root.ID)
	if again.Pages[0].Source.Symbols[0] != "SPY" {
		t.Fatal("chain aliased source owner")
	}
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		r, err := reopened.PaperEvaluationCorporatePageChain(ctx, newer.Command.ID, root.ID, f.de, f.oe)
		if err != nil || !reflect.DeepEqual(r, again) {
			t.Fatal("reopen changed chain", err)
		}
	}
}
func TestAssessmentCorporateChainUnavailableStates(t *testing.T) {
	for _, kind := range []string{"ambiguous", "noncausal", "overlap", "cycle", "scope", "journal", "manual", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			f := newCorporateChainFixture(t, NewMemory())
			owner := f.declaration.Command.ReceiverID
			at := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
			nextToken := "next"
			if kind == "terminal" {
				nextToken = ""
			}
			root, a := chainSource(owner, "root", "", nextToken, "root-event", at)
			if kind == "manual" {
				clock := f.at.Add(2 * time.Second)
				f.step++
				if _, err := f.s.RetainCorporateActionSource(context.Background(), root, func() time.Time { return clock }); err != nil {
					t.Fatal(err)
				}
			} else {
				f.retain(t, root, a)
			}
			if kind != "terminal" && kind != "manual" {
				when := at.Add(3 * time.Second)
				if kind == "noncausal" {
					when = a.AvailableAt
				}
				event, nextCursor := "next-event", ""
				if kind == "overlap" {
					event = "root-event"
				}
				if kind == "cycle" {
					nextCursor = "next"
				}
				c, ca := chainSource(owner, "page", "next", nextCursor, event, when)
				if kind == "scope" {
					c.Recording.AccessRef = "other-access"
				}
				if kind == "journal" {
					ca.JournalID = "journal-other"
					c.Recording.RawRef = "journal:" + ca.JournalID + ":capture-" + c.Recording.CaptureID + "-r1.json"
					ca.CaptureRef = c.Recording.RawRef
				}
				ca.RecordingSHA256 = workerplan.Hash(c.Recording)
				ca.SHA256 = ""
				ca.SHA256 = workerplan.Hash(ca)
				f.retain(t, c, ca)
				if kind == "ambiguous" {
					c, ca = chainSource(owner, "another-page", "next", "", "another-event", when)
					f.retain(t, c, ca)
				}
			}
			r := f.assess(t, "cut")
			got := f.chain(t, r.Command.ID, root.ID)
			want := map[string]string{"ambiguous": "ambiguous_next_page", "noncausal": "next_page_acquisition_not_causal", "overlap": "overlapping_source_ids", "cycle": "cursor_cycle", "scope": "next_page_unavailable_at_cut", "journal": "next_page_unavailable_at_cut", "manual": "acquisition_custody_unavailable", "terminal": "page_chain_terminated"}[kind]
			if got.Status != want || got.PaginationTerminated != (kind == "terminal") {
				t.Fatal(kind, got)
			}
			if kind == "ambiguous" && len(got.CandidateSources) != 2 {
				t.Fatal(got)
			}
			if kind == "manual" && (got.Pages[0].RequestedAt != nil || got.Pages[0].AvailableAt != nil) {
				t.Fatal("manual page invented clocks")
			}
		})
	}
}
func TestAssessmentCorporateChainPageBound(t *testing.T) {
	f := newCorporateChainFixture(t, NewMemory())
	owner := f.declaration.Command.ReceiverID
	at := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	for i := 0; i < MaxCorporateActionSources; i++ {
		token := ""
		if i > 0 {
			token = fmt.Sprintf("cursor-%d", i)
		}
		c, a := chainSource(owner, fmt.Sprintf("page-%d", i), token, fmt.Sprintf("cursor-%d", i+1), fmt.Sprintf("event-%d", i), at.Add(time.Duration(i)*3*time.Second))
		f.retain(t, c, a)
	}
	r := f.assess(t, "bounded-cut")
	got := f.chain(t, r.Command.ID, "page-0")
	if got.Status != "page_bound_reached" || len(got.Pages) != MaxCorporateActionSources || got.PaginationTerminated {
		t.Fatal(got)
	}
}
