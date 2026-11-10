package storage

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// expectedAllocationContribution derives one decision's accounting from the
// complete replayed catalogue with the paper attribution rule, and counts the
// originals that reference the decision.
func expectedAllocationContribution(t *testing.T, decision string, all OwnedAllocationCatalog) (PaperEconomicAttribution, int) {
	t.Helper()
	choices := []PaperAllocationAttribution{}
	references := 0
	for _, r := range all.Records {
		ids, err := allocationFinancialDecisions(retainedOwnedAllocation{Record: r})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ids, decision) {
			references++
		}
		if r.Entry.Plan == nil {
			continue
		}
		for _, choice := range r.Entry.Plan.Decisions {
			if choice.Proposal.ID == decision {
				choices = append(choices, PaperAllocationAttribution{Status: choice.Status, RequestedFundingCents: choice.Proposal.NotionalCents})
			}
		}
	}
	for _, e := range all.Executions {
		ids, err := allocationFinancialDecisions(retainedOwnedAllocation{Execution: &e})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ids, decision) {
			references++
		}
	}
	return paperExecutionAttribution(decision, choices, all), references
}

func sortedEffects(effects []PaperExecutionEffect) []PaperExecutionEffect {
	out := slices.Clone(effects)
	slices.SortFunc(out, func(a, b PaperExecutionEffect) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.RecordID, b.RecordID), cmp.Compare(a.EffectID, b.EffectID))
	})
	return out
}

// requireContributionTotal compares the assembled page chain with the full
// replay. Effects with one clock may be listed in either record order.
func requireContributionTotal(t *testing.T, decision string, pages []OwnedAllocationContributionPage, limit int, all OwnedAllocationCatalog) OwnedAllocationContributionTotal {
	t.Helper()
	total, err := AssembleOwnedAllocationContribution(all.ReceiverID, decision, limit, pages)
	if err != nil {
		t.Fatal("assemble native contribution pages", decision, err)
	}
	want, references := expectedAllocationContribution(t, decision, all)
	got := PaperEconomicAttribution{Status: total.Status, ReservedCents: total.ReservedCents, FilledCostCents: total.FilledCostCents, ClosedCostCents: total.ClosedCostCents, NetProceedsCents: total.NetProceedsCents, ReleasedCents: total.ReleasedCents, RealizedPnLCents: total.RealizedPnLCents}
	want.Effects, got.Effects = sortedEffects(want.Effects), sortedEffects(total.Effects)
	if (want.Effects == nil) != (total.Effects == nil) || !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded contribution total differs from full replay for %s:\n got %+v\nwant %+v", decision, got, want)
	}
	unsigned := total
	unsigned.SHA256 = ""
	if total.SealedRecords+total.ActiveRecords != references || total.JournalSequence != len(all.Records)+len(all.Executions) || total.QualificationCredit || total.SHA256 != workerplan.Hash(unsigned) {
		t.Fatal("contribution total lost or repeated an original reference", decision, total.SealedRecords, total.ActiveRecords, references)
	}
	return total
}

// verifyAllocationContributions runs with the checkpoint contracts' actual
// owner records on both backends, including after approved retirement.
func verifyAllocationContributions(t *testing.T, s ownedAllocationBackend, de PaperDecisionEvaluator, all OwnedAllocationCatalog) {
	t.Helper()
	store := s.(OwnedAllocationContributionStore)
	ctx := t.Context()
	decisions := map[string]bool{}
	for _, r := range all.Records {
		for _, pin := range r.Command.Decisions {
			decisions[pin.ID] = true
		}
	}
	if len(decisions) == 0 {
		t.Fatal("contribution contract needs a referenced decision")
	}
	read := func(decision string, limit int) []OwnedAllocationContributionPage {
		q := OwnedAllocationContributionQuery{ReceiverID: all.ReceiverID, DecisionID: decision, Limit: limit}
		pages := []OwnedAllocationContributionPage{}
		for {
			p, err := store.OwnedAllocationContribution(ctx, q, de)
			if err != nil || p.Validate(q) != nil || (len(pages) == 0) != (p.Active != nil) {
				t.Fatal("native contribution page", decision, len(pages), err)
			}
			pages = append(pages, p)
			if p.NextAfter == "" {
				return pages
			}
			q.After = p.NextAfter
		}
	}
	var paged []OwnedAllocationContributionPage
	var decision string
	for id := range decisions {
		one := read(id, 1)
		whole := requireContributionTotal(t, id, read(id, MaxOwnedAllocationHistoryPage), MaxOwnedAllocationHistoryPage, all)
		stepped := requireContributionTotal(t, id, one, 1, all)
		whole.Pages, whole.SHA256, stepped.Pages, stepped.SHA256 = 0, "", 0, ""
		if !reflect.DeepEqual(whole, stepped) {
			t.Fatal("page size changed a decision total", id)
		}
		if len(one) > len(paged) {
			paged, decision = one, id
		}
	}
	if len(paged) < 2 {
		t.Fatal("contribution contract needs a multi-page decision")
	}
	q := OwnedAllocationContributionQuery{ReceiverID: all.ReceiverID, DecisionID: decision, Limit: 1}
	for name, chain := range map[string][]OwnedAllocationContributionPage{
		"unfinished": paged[:len(paged)-1],
		"no first":   paged[1:],
		"repeated":   append(slices.Clone(paged[:1]), paged...),
		"reordered":  append([]OwnedAllocationContributionPage{paged[1], paged[0]}, paged[2:]...),
		"empty":      {},
	} {
		if got, err := AssembleOwnedAllocationContribution(all.ReceiverID, decision, 1, chain); !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, OwnedAllocationContributionTotal{}) {
			t.Fatal("incomplete page chain became a decision total", name, err)
		}
	}
	if _, err := AssembleOwnedAllocationContribution(all.ReceiverID, "another-decision", 1, paged); !errors.Is(err, ErrOwnedAllocationInvalid) {
		t.Fatal("page chain assembled for another decision", err)
	}
	for name, mutate := range map[string]func(*OwnedAllocationContributionPage){
		"subtotal":      func(p *OwnedAllocationContributionPage) { p.Subtotal.FilledCostCents++ },
		"sequence":      func(p *OwnedAllocationContributionPage) { p.Records[0].Sequence = p.ThroughSequence + 1 },
		"kind":          func(p *OwnedAllocationContributionPage) { p.Records[0].Kind = "grant" },
		"active":        func(p *OwnedAllocationContributionPage) { p.Active = nil },
		"cursor":        func(p *OwnedAllocationContributionPage) { p.NextAfter = "" },
		"decision":      func(p *OwnedAllocationContributionPage) { p.DecisionID = "another-decision" },
		"qualification": func(p *OwnedAllocationContributionPage) { p.QualificationCredit = true },
		"effect": func(p *OwnedAllocationContributionPage) {
			p.Records[0].Effects = []PaperExecutionEffect{{RecordID: "another-record", EffectID: "effect", Kind: "fill", AmountCents: 1}}
			p.Subtotal.FilledCostCents++
		},
	} {
		bad := clone(paged[0])
		mutate(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if name != "cursor" && bad.Validate(q) == nil {
			t.Fatal("self-rehashed contribution page accepted", name)
		}
		if _, err := AssembleOwnedAllocationContribution(all.ReceiverID, decision, 1, append([]OwnedAllocationContributionPage{bad}, paged[1:]...)); err == nil {
			t.Fatal("changed contribution page assembled", name)
		}
	}
	for _, bad := range []OwnedAllocationContributionQuery{
		{ReceiverID: all.ReceiverID, Limit: 1},
		{ReceiverID: all.ReceiverID, DecisionID: decision, Limit: 0},
		{ReceiverID: all.ReceiverID, DecisionID: "another-decision", Limit: 1, After: paged[0].NextAfter},
		{ReceiverID: all.ReceiverID, DecisionID: decision, Limit: 2, After: "bad"},
	} {
		if got, err := store.OwnedAllocationContribution(ctx, bad, de); !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, OwnedAllocationContributionPage{}) {
			t.Fatal("invalid contribution query returned a page", err)
		}
	}
	if got, err := store.OwnedAllocationContribution(ctx, OwnedAllocationContributionQuery{ReceiverID: "receiver-abcdefghijklmnop", DecisionID: decision, Limit: 1}, de); !errors.Is(err, ErrReceiverMismatch) || !reflect.DeepEqual(got, OwnedAllocationContributionPage{}) {
		t.Fatal("contribution read crossed receivers", err)
	}
	q.DecisionID = "absent-decision"
	empty, err := store.OwnedAllocationContribution(ctx, q, de)
	if err != nil || empty.Validate(q) != nil || len(empty.Records) != 0 || len(empty.Active.Records) != 0 || empty.NextAfter != "" || empty.Subtotal != (OwnedAllocationContributionSums{}) {
		t.Fatal("absent decision fabricated a contribution", err)
	}
	if total, err := AssembleOwnedAllocationContribution(all.ReceiverID, q.DecisionID, 1, []OwnedAllocationContributionPage{empty}); err != nil || total.Status != "owned_execution_unavailable" || total.ReservedCents != nil || total.Effects != nil {
		t.Fatal("absent decision total manufactured zero economics", err)
	}
	q.DecisionID = decision
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := store.OwnedAllocationContribution(canceled, q, de); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, OwnedAllocationContributionPage{}) {
		t.Fatal("canceled contribution read returned a partial page", err)
	}
}

func TestMemoryAllocationContributionEmpty(t *testing.T) {
	m := NewMemory()
	q := OwnedAllocationContributionQuery{ReceiverID: m.receiverID, DecisionID: "decision-one", Limit: 2}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Fatal("empty journal requested native evaluation")
		return PaperDecisionEvaluation{}, nil
	}
	page, err := m.OwnedAllocationContribution(t.Context(), q, de)
	if err != nil || page.Validate(q) != nil || page.ThroughSequence != 0 || page.Active == nil || page.Active.ThroughSequence != 0 || len(page.Records) != 0 {
		t.Fatal("empty journal contribution", err)
	}
}

// verifyExecutedContribution checks real fill, close and release effects from
// the synthetic execution fixture: the assembled sealed and active pages must
// equal the attribution rule applied to the complete replayed catalogue.
func verifyExecutedContribution(t *testing.T, m *Memory, decision string, sealed []OwnedAllocationHistoryPage, all OwnedAllocationCatalog) {
	t.Helper()
	q := OwnedAllocationContributionQuery{ReceiverID: m.receiverID, DecisionID: decision, Limit: 1}
	pages := []OwnedAllocationContributionPage{}
	for i, history := range sealed {
		var active *OwnedAllocationContributionActive
		if i == 0 {
			var err error
			if active, err = activeAllocationContribution(decision, history.ThroughSequence, m.ownedAllocations, all); err != nil {
				t.Fatal("active executed contribution", err)
			}
		}
		page, err := finishAllocationContribution(t.Context(), q, history, active)
		if err != nil {
			t.Fatal("executed contribution page", i, err)
		}
		pages = append(pages, page)
		q.After = page.NextAfter
	}
	total := requireContributionTotal(t, decision, pages, 1, all)
	if total.Status != "position_closed" || *total.FilledCostCents == 0 || *total.ClosedCostCents != *total.FilledCostCents || total.RealizedPnLCents == nil || total.SealedRecords == 0 {
		t.Fatalf("executed fixture no longer exercises a sealed closed position: %+v", total)
	}
	kinds := map[string]bool{}
	for _, e := range total.Effects {
		kinds[e.Kind] = true
	}
	if !kinds["fill"] || !kinds["close"] {
		t.Fatal("executed contribution lost an effect kind", kinds)
	}
}
