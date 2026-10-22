package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func decisionReviewContracts(t *testing.T, s decisionBackend, decision PaperDecisionRecord, at time.Time, eval PaperDecisionEvaluator) {
	t.Helper()
	ctx := context.Background()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := DecisionReviewCommand{ID: "review-first", ReceiverID: decision.Command.ReceiverID, DecisionID: decision.Command.ID, DecisionSHA256: decision.SHA256, Explanation: "Original prose: guaranteed profit. This is unverified.\n", Claims: []DecisionReviewClaim{{Field: "action", Value: "enter"}, {Field: "realized_pnl_cents", Value: "100000"}}}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_decision_review() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_decision_review BEFORE INSERT ON mantik_decision_reviews FOR EACH ROW EXECUTE FUNCTION reject_decision_review()`); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReviewPaperDecision(ctx, c, func() time.Time { return at }, eval); err == nil {
			t.Fatal("failed insert committed")
		}
		empty, e := s.DecisionReviews(ctx, eval)
		if e != nil || len(empty.Records) != 0 || len(empty.Cases) != 0 {
			t.Fatal("partial case after SQL failure", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_decision_review ON mantik_decision_reviews; DROP FUNCTION reject_decision_review()"); err != nil {
			t.Fatal(err)
		}
	}
	var clocks atomic.Int32
	clock := func() time.Time { clocks.Add(1); return at }
	noClock := func() time.Time { panic("review retry read clock") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.ReviewPaperDecision(ctx, c, clock, eval); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if clocks.Load() != 1 {
		t.Fatal(clocks.Load())
	}
	all, err := s.DecisionReviews(ctx, eval)
	if err != nil || len(all.Records) != 1 || len(all.Cases) != 1 {
		t.Fatal(all, err)
	}
	first := all.Records[0]
	if first.Status != "review_required" || first.ProseVerified || first.ModelUsed || first.AdmissionAvailable || first.ExecutionAvailable || first.QualificationCredit || first.Command.Explanation != c.Explanation || first.Findings[0].Status != "contradiction" || first.Findings[1].Status != "unsupported_field" || first.Findings[1].Expected != nil || *first.Findings[0].Expected != decision.Evaluation.Action {
		t.Fatal(first)
	}
	r, err := s.ReviewPaperDecision(ctx, c, noClock, eval)
	if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Record, first) {
		t.Fatal(r, err)
	}
	r.Record.Command.Claims[0].Value = "mutated"
	*r.Record.Findings[0].Expected = "mutated"
	changed := clone(c)
	changed.Explanation += " changed"
	if _, err = s.ReviewPaperDecision(ctx, changed, noClock, eval); !errors.Is(err, ErrDecisionReviewConflict) {
		t.Fatal(err)
	}
	changed = clone(c)
	changed.ID = "bad-pin"
	changed.DecisionSHA256 = workerplan.Hash("other")
	if _, err = s.ReviewPaperDecision(ctx, changed, clock, eval); !errors.Is(err, ErrDecisionReviewConflict) {
		t.Fatal(err)
	}
	changed = clone(c)
	changed.ID = "bad-owner"
	changed.ReceiverID = "receiver-other_other_other"
	if _, err = s.ReviewPaperDecision(ctx, changed, noClock, eval); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	changed = clone(c)
	changed.ID = "missing"
	changed.DecisionID = "missing"
	if _, err = s.ReviewPaperDecision(ctx, changed, noClock, eval); !errors.Is(err, ErrDecisionReviewNotFound) {
		t.Fatal(err)
	}
	changed = clone(c)
	changed.ID = "rollback-clock"
	if _, err = s.ReviewPaperDecision(ctx, changed, func() time.Time { return at.Add(-time.Second) }, eval); !errors.Is(err, ErrDecisionReviewInvalid) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ReviewPaperDecision(cancelled, c, noClock, eval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	matching := clone(c)
	matching.ID = "matching"
	matching.Claims = []DecisionReviewClaim{{Field: "action", Value: decision.Evaluation.Action}}
	matching.Explanation = "Opaque prose can still be false even if this explicit action claim matches."
	match, err := s.ReviewPaperDecision(ctx, matching, clock, eval)
	if err != nil || match.Record.Status != "claims_consistent" || match.Record.CaseID != "" || match.Record.ProseVerified {
		t.Fatal(match, err)
	}
	second := clone(c)
	second.ID = "review-second"
	second.Claims = second.Claims[1:]
	if _, err = s.ReviewPaperDecision(ctx, second, clock, eval); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < MaxDecisionReviews; i++ {
		next := clone(matching)
		next.ID = fmt.Sprintf("review-%02d", i)
		if _, err = s.ReviewPaperDecision(ctx, next, clock, eval); err != nil {
			t.Fatal(err)
		}
	}
	full, err := s.DecisionReviews(ctx, eval)
	if err != nil || len(full.Records) != MaxDecisionReviews || len(full.Cases) != 1 || !reflect.DeepEqual(full.Cases[0].ReviewIDs, []string{"review-first", "review-second"}) {
		t.Fatal(full, err)
	}
	if _, err = s.ReviewPaperDecision(ctx, c, noClock, eval); err != nil {
		t.Fatal(err)
	}
	overflow := clone(c)
	overflow.ID = "overflow"
	if _, err = s.ReviewPaperDecision(ctx, overflow, noClock, eval); !errors.Is(err, ErrDecisionReviewCapacity) {
		t.Fatal(err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review changed operations", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		got, e := reopened.DecisionReviews(ctx, eval)
		reopened.Close()
		if e != nil || !reflect.DeepEqual(got, full) {
			t.Fatal("restart changed reviews", e)
		}
	}
	write := func(r DecisionReviewRecord) {
		t.Helper()
		if m, ok := s.(*Memory); ok {
			m.decisionReviews[c.ID] = clone(r)
		} else {
			raw, _ := json.Marshal(r)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_decision_reviews SET payload=$1 WHERE id=$2", raw, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mutate := range []func(*DecisionReviewRecord){func(r *DecisionReviewRecord) { r.ProseVerified = true }, func(r *DecisionReviewRecord) { r.ExecutionAvailable = true }, func(r *DecisionReviewRecord) { r.Findings[0].Status = "supported" }, func(r *DecisionReviewRecord) { *r.Findings[0].Expected = "enter" }, func(r *DecisionReviewRecord) { r.CaseID = "other-case" }, func(r *DecisionReviewRecord) { r.Status = "claims_consistent" }} {
		bad := clone(first)
		mutate(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		write(bad)
		if _, err = s.DecisionReviews(ctx, eval); err == nil {
			t.Fatal("self-rehashed review forgery accepted")
		}
		if _, err = s.ReviewPaperDecision(ctx, matching, noClock, eval); err == nil {
			t.Fatal("retry hid unrelated corrupted review")
		}
	}
	write(first)
}

func TestDecisionReviewCommandBounds(t *testing.T) {
	c := DecisionReviewCommand{ID: "review", ReceiverID: "receiver-example_example0", DecisionID: "decision", DecisionSHA256: workerplan.Hash("decision"), Claims: []DecisionReviewClaim{{Field: "action", Value: "abstain"}}}
	if c.Validate() != nil {
		t.Fatal("valid command")
	}
	for _, change := range []func(*DecisionReviewCommand){func(c *DecisionReviewCommand) { c.Claims = nil }, func(c *DecisionReviewCommand) { c.Claims = append(c.Claims, c.Claims[0]) }, func(c *DecisionReviewCommand) { c.Claims[0].Value = string([]byte{255}) }, func(c *DecisionReviewCommand) { c.Explanation = string(make([]byte, 8193)) }, func(c *DecisionReviewCommand) { c.Claims[0].Field = "execute-now" }} {
		bad := clone(c)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid command accepted")
		}
	}
}
