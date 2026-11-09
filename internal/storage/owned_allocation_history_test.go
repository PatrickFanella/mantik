package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
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
		{"mantik-allocation-history-cursor-v1", "another-owner", first.ThroughSequence, first.CheckpointSHA256, 2},
		{"mantik-allocation-history-cursor-v1", first.ReceiverID, first.ThroughSequence, workerplan.Hash("other checkpoint"), 2},
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
	q := OwnedAllocationHistoryQuery{ReceiverID: m.receiverID, Limit: 1}
	found, count := false, 0
	for {
		selected, err := selectAllocationHistory(t.Context(), q, m.ownedAllocations.Checkpoint, func(n int) (OwnedAllocationCheckpoint, error) {
			return m.ownedAllocationIndex.Checkpoints[n], nil
		}, func(start, end int) ([]ownedAllocationMember, error) {
			result := []ownedAllocationMember{}
			for n := start + 1; n <= end; n++ {
				result = append(result, m.ownedAllocationIndex.Members[n])
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
