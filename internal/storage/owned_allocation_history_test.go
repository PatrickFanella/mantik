package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Both original checkpoint contracts call this with actual owner records,
// including after the fixture's approved decision retirement.
func verifyAllocationHistoryPages(t *testing.T, s ownedAllocationBackend, de PaperDecisionEvaluator, all OwnedAllocationCatalog) OwnedAllocationHistoryPage {
	t.Helper()
	store := s.(OwnedAllocationHistoryStore)
	q := OwnedAllocationHistoryQuery{ReceiverID: all.ReceiverID, Limit: 2}
	first, err := store.OwnedAllocationHistory(t.Context(), q, de)
	if err != nil || first.Validate(q) != nil || first.ThroughSequence < 3 || first.NextAfter == "" {
		t.Fatal("original allocation history page", err)
	}
	if again, err := store.OwnedAllocationHistory(t.Context(), q, de); err != nil || !reflect.DeepEqual(again, first) {
		t.Fatal("history read changed original cut", err)
	}
	var got []OwnedAllocationRecord
	for page := first; ; {
		if err := page.Validate(q); err != nil || page.ThroughSequence != first.ThroughSequence || page.CheckpointSHA256 != first.CheckpointSHA256 {
			t.Fatal("history cursor changed boundary", err)
		}
		for _, r := range page.Records {
			if r.Kind != "comparison" || r.Comparison == nil || r.Execution != nil || r.Sequence != len(got)+1 {
				t.Fatal("history changed original kind or sequence")
			}
			got = append(got, *r.Comparison)
		}
		if page.NextAfter == "" {
			break
		}
		q.After = page.NextAfter
		page, err = store.OwnedAllocationHistory(t.Context(), q, de)
		if err != nil {
			t.Fatal("next native allocation page", err)
		}
	}
	if len(got) != first.ThroughSequence || len(got) >= len(all.Records) || !reflect.DeepEqual(got, all.Records[:len(got)]) {
		t.Fatal("history omitted a sealed original or included an active record")
	}
	verifyDecisionAllocationHistory(t, store, de, first, got)
	q.After = ""
	detached := clone(first)
	detached.Records[0].Comparison.Command.Decisions[0].ID = "caller-mutation"
	if again, err := store.OwnedAllocationHistory(t.Context(), q, de); err != nil || !reflect.DeepEqual(again, first) {
		t.Fatal("history page aliases the owner", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := store.OwnedAllocationHistory(canceled, q, de); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
		t.Fatal("canceled history returned a partial page", err)
	}
	for name, mutate := range map[string]func(*OwnedAllocationHistoryPage){
		"sequence":      func(p *OwnedAllocationHistoryPage) { p.Records[0].Sequence++ },
		"record":        func(p *OwnedAllocationHistoryPage) { p.Records[0].Comparison.At = p.Records[0].Comparison.At.Add(1) },
		"kind":          func(p *OwnedAllocationHistoryPage) { p.Records[0].Kind = "execution" },
		"qualification": func(p *OwnedAllocationHistoryPage) { p.QualificationCredit = true },
		"cursor":        func(p *OwnedAllocationHistoryPage) { p.NextAfter = "" },
	} {
		bad := clone(first)
		mutate(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if bad.Validate(q) == nil {
			t.Fatal("self-rehashed page accepted", name)
		}
	}
	for _, cursor := range []allocationHistoryCursor{
		{"mantik-allocation-history-cursor-v1", "another-owner", first.ThroughSequence, first.CheckpointSHA256, 2, ""},
		{"mantik-allocation-history-cursor-v1", first.ReceiverID, first.ThroughSequence, workerplan.Hash("other checkpoint"), 2, ""},
	} {
		raw, _ := json.Marshal(cursor)
		bad := q
		bad.After = base64.RawURLEncoding.EncodeToString(raw)
		if got, err := store.OwnedAllocationHistory(t.Context(), bad, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
			t.Fatal("cursor borrowed another receiver or checkpoint")
		}
	}
	return first
}

func verifyDecisionAllocationHistory(t *testing.T, store OwnedAllocationHistoryStore, de PaperDecisionEvaluator, unfiltered OwnedAllocationHistoryPage, expected []OwnedAllocationRecord) {
	t.Helper()
	decision := expected[0].Command.Decisions[0].ID
	q := OwnedAllocationHistoryQuery{ReceiverID: unfiltered.ReceiverID, Limit: 2, DecisionID: decision}
	first, err := store.OwnedAllocationHistory(t.Context(), q, de)
	if err != nil || first.Validate(q) != nil || first.Schema != "mantik-allocation-history-page-v2" || first.ThroughSequence != unfiltered.ThroughSequence || first.CheckpointSHA256 != unfiltered.CheckpointSHA256 {
		t.Fatal("decision-scoped original history", err)
	}
	var got []OwnedAllocationRecord
	var last OwnedAllocationHistoryPage
	for page := first; ; {
		if page.Validate(q) != nil {
			t.Fatal("decision history page changed its scope")
		}
		for _, r := range page.Records {
			if r.Comparison == nil || r.Execution != nil {
				t.Fatal("decision history changed original record kind")
			}
			got = append(got, *r.Comparison)
		}
		if page.NextAfter == "" {
			last = page
			break
		}
		q.After = page.NextAfter
		page, err = store.OwnedAllocationHistory(t.Context(), q, de)
		if err != nil {
			t.Fatal("next scoped original page", err)
		}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("decision filtering changed or omitted original comparisons")
	}
	for _, bad := range []OwnedAllocationHistoryQuery{
		{ReceiverID: q.ReceiverID, Limit: 2, After: first.NextAfter},
		{ReceiverID: q.ReceiverID, Limit: 2, DecisionID: "another-decision", After: first.NextAfter},
		{ReceiverID: q.ReceiverID, Limit: 2, DecisionID: decision, After: unfiltered.NextAfter},
	} {
		if got, err := store.OwnedAllocationHistory(t.Context(), bad, de); !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
			t.Fatal("history cursor borrowed another decision scope", err)
		}
	}
	q.After, q.DecisionID = "", "absent-decision"
	if empty, err := store.OwnedAllocationHistory(t.Context(), q, de); err != nil || empty.Validate(q) != nil || len(empty.Records) != 0 || empty.NextAfter != "" || empty.ThroughSequence != first.ThroughSequence {
		t.Fatal("missing decision filter fabricated or lost archive records", err)
	}
	// A valid original cannot be relabelled as another decision's financial
	// evidence by rehashing only the page presentation.
	last.DecisionID = q.DecisionID
	last.SHA256 = ""
	last.SHA256 = workerplan.Hash(last)
	if last.Validate(q) == nil {
		t.Fatal("rehashing the page reassigned original decision references")
	}
}

func TestMemoryAllocationHistoryEmptyAndInvalid(t *testing.T) {
	m := NewMemory()
	q := OwnedAllocationHistoryQuery{ReceiverID: m.receiverID, Limit: 2}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Fatal("empty history requested native evaluation")
		return PaperDecisionEvaluation{}, nil
	}
	page, err := m.OwnedAllocationHistory(t.Context(), q, de)
	if err != nil || page.Validate(q) != nil || page.ThroughSequence != 0 || len(page.Records) != 0 {
		t.Fatal("empty original allocation history", err)
	}
	for _, invalid := range []OwnedAllocationHistoryQuery{
		{ReceiverID: q.ReceiverID, Limit: 0},
		{ReceiverID: q.ReceiverID, Limit: 65},
		{ReceiverID: q.ReceiverID, Limit: 2, After: "bad"},
	} {
		if got, err := m.OwnedAllocationHistory(t.Context(), invalid, de); !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
			t.Fatal("invalid history query returned a page", err)
		}
	}
}

// The positive execution fixture supplies its explicit synthetic assessment.
// Original segment replay still rederives every fill, close and release from
// the fixture quote source; this does not establish a qualified model or run.
func verifyExecutedAllocationHistory(t *testing.T, f eligibilityFixture, expected OwnedExecutionRecord) {
	t.Helper()
	m := f.m
	m.mu.Lock()
	defer m.mu.Unlock()
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(t.Context(), f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	sources := paperEvaluationSources{policies: policies, decisions: decisions, strategies: strategies, retained: m.paperDecisions, declarations: declarations}
	verify := func(decision string) {
		q := OwnedAllocationHistoryQuery{ReceiverID: m.receiverID, Limit: 1, DecisionID: decision}
		found, count := false, 0
		for {
			selected, err := selectAllocationHistory(t.Context(), q, m.ownedAllocations.Checkpoint, func(n int) (OwnedAllocationCheckpoint, error) {
				return m.ownedAllocationIndex.Checkpoints[n], nil
			}, func(start, end int) ([]ownedAllocationMember, error) {
				result := []ownedAllocationMember{}
				for n := start + 1; n <= end; n++ {
					if decision != "" && !slices.Contains(m.ownedAllocationIndex.Financial[n], decision) {
						continue
					}
					result = append(result, m.ownedAllocationIndex.Members[n])
					if decision != "" && len(result) == q.Limit+1 {
						break
					}
				}
				return result, nil
			}, func(n int) (ownedAllocationSegment, error) { return m.ownedAllocationSegmentLocked(t.Context(), n) })
			if err != nil {
				t.Fatal(err)
			}
			page, err := finishAllocationHistory(t.Context(), q, selected, func(s ownedAllocationSegment) (OwnedAllocationCatalog, error) {
				return replayOwnedAllocationSegment(t.Context(), s, sources, f.exec, PaperEvaluationAssessmentCatalog{ReceiverID: m.receiverID, Records: []PaperEvaluationAssessment{f.complete}})
			})
			if err != nil || page.Validate(q) != nil {
				t.Fatal("native executed history page", err)
			}
			for _, r := range page.Records {
				count++
				if r.Kind == "execution" {
					if found || r.Execution == nil || !reflect.DeepEqual(*r.Execution, expected) {
						t.Fatal("execution original changed or repeated")
					}
					found = true
				}
			}
			if page.NextAfter == "" {
				if !found || count != page.ThroughSequence {
					t.Fatal("paged history omitted a sealed execution record")
				}
				break
			}
			q.After = page.NextAfter
		}
	}
	verify("")
	verify(f.command.Decisions[0].ID)
}
