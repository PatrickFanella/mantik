package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// requireAttributionTargets reads every catalogue row through the targeted
// reader. Each must be the same row, and absent or retired decisions must be
// not found rather than an empty row.
func requireAttributionTargets(t *testing.T, s any, catalogue PaperAttributionCatalog, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, absent ...string) {
	t.Helper()
	target, ok := s.(PaperAttributionDecisionStore)
	if !ok {
		t.Fatal("backend lacks the targeted attribution reader")
	}
	ctx := t.Context()
	for _, row := range catalogue.Rows {
		got, err := target.PaperAttributionDecision(ctx, row.Decision.Command.ID, de, oe)
		if err != nil || !reflect.DeepEqual(got, row) {
			t.Fatalf("targeted attribution differs from the catalogue row for %s: %v\n got %+v\nwant %+v", row.Decision.Command.ID, err, got, row)
		}
	}
	for _, id := range append(absent, "absent-decision") {
		if got, err := target.PaperAttributionDecision(ctx, id, de, oe); !errors.Is(err, ErrPaperAttributionNotFound) || !reflect.DeepEqual(got, PaperAttributionRow{}) {
			t.Fatal("targeted attribution answered for an inactive decision", id, err)
		}
	}
	for _, bad := range []string{"", "bad id", "../x"} {
		if got, err := target.PaperAttributionDecision(ctx, bad, de, oe); !errors.Is(err, ErrPaperAttributionInvalid) || !reflect.DeepEqual(got, PaperAttributionRow{}) {
			t.Fatal("invalid targeted attribution identity", bad, err)
		}
	}
	if len(catalogue.Rows) > 0 {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if got, err := target.PaperAttributionDecision(canceled, catalogue.Rows[0].Decision.Command.ID, de, oe); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, PaperAttributionRow{}) {
			t.Fatal("canceled targeted attribution returned a row", err)
		}
	}
}

func TestReadDecisionHistoryFollowsCursorsToTheEnd(t *testing.T) {
	var queries []OwnedAllocationHistoryQuery
	pages := []OwnedAllocationHistoryPage{
		{Records: []OwnedAllocationHistoryRecord{{Sequence: 1}}, NextAfter: "first"},
		{Records: []OwnedAllocationHistoryRecord{{Sequence: 4}}, NextAfter: "second"},
		{Records: []OwnedAllocationHistoryRecord{{Sequence: 9}}},
	}
	got, err := readDecisionHistory(t.Context(), "receiver", "decision", func(q OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error) {
		queries = append(queries, q)
		return pages[len(queries)-1], nil
	})
	if err != nil || len(got) != 3 || got[0].Sequence != 1 || got[2].Sequence != 9 {
		t.Fatal("decision history lost a page", got, err)
	}
	for i, after := range []string{"", "first", "second"} {
		q := queries[i]
		if q.After != after || q.DecisionID != "decision" || q.ReceiverID != "receiver" || q.Limit != MaxOwnedAllocationHistoryPage {
			t.Fatal("decision history query", i, q)
		}
	}
	failed := errors.New("page failed")
	if got, err := readDecisionHistory(t.Context(), "receiver", "decision", func(q OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error) {
		if q.After != "" {
			return OwnedAllocationHistoryPage{}, failed
		}
		return pages[0], nil
	}); !errors.Is(err, failed) || got != nil {
		t.Fatal("failed later page returned partial history", err)
	}
	calls := 0
	if got, err := readDecisionHistory(t.Context(), "receiver", "decision", func(OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error) {
		calls++
		return pages[0], nil
	}); !errors.Is(err, ErrOwnedAllocationCapacity) || got != nil || calls != MaxOwnedAllocationContributionPages {
		t.Fatal("unbounded decision history chain", err, calls)
	}
}

func TestDecisionAdmissionReviewsRequireDenseGlobalSequences(t *testing.T) {
	d := PaperDecisionRecord{Command: PaperDecisionCommand{ID: "decision"}}
	source := OwnedAllocationCatalog{ReceiverID: "receiver"}
	other := func(id string, sequence int) OwnedAdmissionReview {
		return OwnedAdmissionReview{Command: OwnedAdmissionReviewCommand{ID: id, DecisionID: "other"}, Sequence: sequence}
	}
	if got, err := decisionAdmissionReviews(t.Context(), d, map[string]OwnedAdmissionReview{"a": other("a", 1), "b": other("b", 2)}, source); err != nil || len(got) != 0 {
		t.Fatal("dense sequences of other decisions rejected", err)
	}
	for name, all := range map[string]map[string]OwnedAdmissionReview{
		"duplicate": {"a": other("a", 1), "b": other("b", 1)},
		"gap":       {"a": other("a", 1), "b": other("b", 3)},
		"zero":      {"a": other("a", 0)},
	} {
		if got, err := decisionAdmissionReviews(t.Context(), d, all, source); !errors.Is(err, ErrOwnedAdmissionReviewInvalid) || got != nil {
			t.Fatal("targeted review chain accepted global sequence damage", name, err)
		}
	}
}
