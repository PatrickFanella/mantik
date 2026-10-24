package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Called by the shared Memory/Postgres assessment acceptance contract after an
// actual owner-replayed comparison with selected or excluded decision evidence.
func admissionReviewContracts(t *testing.T, s assessmentBackend, comparison OwnedAllocationRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, selected bool) {
	t.Helper()
	ctx := context.Background()
	backend := s.(OwnedAdmissionReviewStore)
	noClock := func() time.Time { t.Fatal("review retry read clock"); return time.Time{} }
	readiness := comparison.AdmissionChecks[0]
	c := OwnedAdmissionReviewCommand{ID: "first-review", ReceiverID: comparison.Command.ReceiverID, ComparisonID: comparison.Command.ID, ComparisonSHA256: comparison.SHA256, DecisionID: readiness.DecisionID, DecisionSHA256: readiness.DecisionSHA256}
	now := func() time.Time { return comparison.At.Add(time.Nanosecond) }
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_review_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'review commit fixture rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_review_commit AFTER INSERT ON mantik_owned_admission_reviews DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_review_commit()`); err != nil {
			t.Fatal(err)
		}
		got, e := backend.ReviewOwnedAdmission(ctx, c, now, de, oe)
		if e == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
			t.Fatal("commit failure acknowledged", got, e)
		}
		all, e := backend.OwnedAdmissionReviews(ctx, de, oe)
		if e != nil || len(all.Records) != 0 {
			t.Fatal("commit failure retained", all, e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_review_commit ON mantik_owned_admission_reviews; DROP FUNCTION reject_review_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := backend.ReviewOwnedAdmission(ctx, c, now, de, oe)
	status := "deferred"
	if selected {
		status = "pending"
	}
	if err != nil || first.Duplicate || first.Record.Status != status || first.Record.Sequence != 1 || !reflect.DeepEqual(first.Record.Readiness, readiness) || first.Record.AdmissionAvailable || first.Record.ExecutionAvailable || first.Record.QualificationCredit {
		t.Fatal("review did not retain non-grant", first, err)
	}
	retry, err := backend.ReviewOwnedAdmission(ctx, c, noClock, de, oe)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, first.Record) {
		t.Fatal("review retry", retry, err)
	}
	wrong := c
	wrong.ComparisonSHA256 = strings.Repeat("f", 64)
	if got, e := backend.ReviewOwnedAdmission(ctx, wrong, noClock, de, oe); !errors.Is(e, ErrOwnedAdmissionReviewConflict) || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
		t.Fatal("changed review identity", got, e)
	}
	wrong = c
	wrong.ID = "bad-predecessor"
	wrong.PreviousID = "missing"
	wrong.PreviousSHA256 = strings.Repeat("f", 64)
	if _, e := backend.ReviewOwnedAdmission(ctx, wrong, noClock, de, oe); !errors.Is(e, ErrOwnedAdmissionReviewConflict) {
		t.Fatal("predecessor accepted", e)
	}
	// Two reassessments of the same current review cannot branch the lifecycle.
	var wg sync.WaitGroup
	var receipts [2]OwnedAdmissionReviewReceipt
	var errs [2]error
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := c
			next.ID = []string{"reassess-a", "reassess-b"}[i]
			next.PreviousID = c.ID
			next.PreviousSHA256 = first.Record.SHA256
			receipts[i], errs[i] = backend.ReviewOwnedAdmission(ctx, next, now, de, oe)
		}(i)
	}
	wg.Wait()
	wins := 0
	var current OwnedAdmissionReview
	for i, e := range errs {
		if e == nil {
			wins++
			current = receipts[i].Record
		} else if !errors.Is(e, ErrOwnedAdmissionReviewConflict) {
			t.Fatal("reassessment race", e)
		}
	}
	if wins != 1 {
		t.Fatal("review chain branched", wins, errs)
	}
	expiredCommand := c
	expiredCommand.ID = "expiry-review"
	expiredCommand.PreviousID = current.Command.ID
	expiredCommand.PreviousSHA256 = current.SHA256
	expired, err := backend.ReviewOwnedAdmission(ctx, expiredCommand, func() time.Time { return readiness.ValidUntil }, de, oe)
	if err != nil || expired.Record.Status != "expired" || expired.Record.ReasonCode != "finalized_proposal_expired" || !reflect.DeepEqual(expired.Record.ReconsiderTriggers, []string{"new_finalized_decision"}) {
		t.Fatal("exact expiry boundary", expired, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, e := backend.ReviewOwnedAdmission(cancelled, c, noClock, de, oe); !errors.Is(e, context.Canceled) || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
		t.Fatal("cancelled review", got, e)
	}
	if got, e := backend.OwnedAdmissionReviews(ctx, de, nil); e == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewCatalog{}) {
		t.Fatal("missing outcome owner", got, e)
	}
	// Existing source clocks and the review chain's own clock both fence backdates.
	backdated := expiredCommand
	backdated.ID = "backdated-review"
	backdated.PreviousID = expired.Record.Command.ID
	backdated.PreviousSHA256 = expired.Record.SHA256
	if _, e := backend.ReviewOwnedAdmission(ctx, backdated, now, de, oe); !errors.Is(e, ErrOwnedAdmissionReviewInvalid) {
		t.Fatal("backdated review", e)
	}
	all, err := backend.OwnedAdmissionReviews(ctx, de, oe)
	if err != nil || len(all.Records) != 3 || !reflect.DeepEqual(all.CurrentIDs, []string{expiredCommand.ID}) {
		t.Fatal("review chain readback", all, err)
	}
	saved := all.Records[0]
	set := func(r OwnedAdmissionReview) {
		switch b := s.(type) {
		case *Memory:
			b.admissionReviews[r.Command.ID] = clone(r)
		case *Postgres:
			raw, _ := json.Marshal(r)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_owned_admission_reviews SET payload=$1 WHERE id=$2", raw, r.Command.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, mutate := range []func(*OwnedAdmissionReview){func(r *OwnedAdmissionReview) { r.Status = "admitted"; r.AdmissionAvailable = true }, func(r *OwnedAdmissionReview) { r.Readiness.Gates[2].Status = "passed" }, func(r *OwnedAdmissionReview) {
		r.Command.PreviousID = "invented"
		r.Command.PreviousSHA256 = strings.Repeat("f", 64)
	}} {
		damaged := clone(saved)
		mutate(&damaged)
		damaged.SHA256 = ""
		damaged.SHA256 = workerplan.Hash(damaged)
		set(damaged)
		if got, e := backend.OwnedAdmissionReviews(ctx, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewCatalog{}) {
			t.Fatal("rehashed fabricated review", got, e)
		}
		if got, e := backend.ReviewOwnedAdmission(ctx, c, noClock, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
			t.Fatal("corrupt review retry", got, e)
		}
		set(saved)
	}
	// An intact review envelope does not excuse corruption of its actual source.
	var journal ownedAllocationJournal
	switch b := s.(type) {
	case *Memory:
		journal = clone(b.ownedAllocations)
	case *Postgres:
		var raw []byte
		if e := b.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(raw, &journal); e != nil {
			t.Fatal(e)
		}
	}
	setJournal := func(j ownedAllocationJournal) {
		switch b := s.(type) {
		case *Memory:
			b.ownedAllocations = clone(j)
		case *Postgres:
			raw, _ := json.Marshal(j)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1 WHERE singleton", raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	damaged := clone(journal)
	damaged.Records[len(damaged.Records)-1].Record.AdmissionChecks[0].AdmissionAvailable = true
	setJournal(damaged)
	if got, e := backend.ReviewOwnedAdmission(ctx, c, noClock, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAdmissionReviewReceipt{}) {
		t.Fatal("review trusted source envelope", got, e)
	}
	setJournal(journal)
	retry, err = backend.ReviewOwnedAdmission(ctx, c, noClock, de, oe)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, first.Record) {
		t.Fatal("restore changed original review", retry, err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review mutated operational state", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		got, e := reopened.OwnedAdmissionReviews(ctx, de, oe)
		if e != nil || !reflect.DeepEqual(got, all) {
			t.Fatal("restart lost review chain", got, e)
		}
		admissionReviewMigrationContract(t, pg)
	}
}
func admissionReviewMigrationContract(t *testing.T, p *Postgres) {
	ctx := context.Background()
	upgrade := timerPostgres(t, "admissionreviewupgrade")
	if _, e := upgrade.pool.Exec(ctx, `DROP TABLE mantik_owned_admission_reviews; DELETE FROM mantik_schema WHERE version>=53; CREATE FUNCTION reject_review_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=53 THEN RAISE EXCEPTION 'review migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_review_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_review_migration()`); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e == nil {
		t.Fatal("migration failure accepted")
	}
	var exists bool
	if e := upgrade.pool.QueryRow(ctx, "SELECT to_regclass('mantik_owned_admission_reviews') IS NOT NULL").Scan(&exists); e != nil || exists {
		t.Fatal("review DDL not rolled back", exists, e)
	}
	if _, e := upgrade.pool.Exec(ctx, "DROP TRIGGER reject_review_migration ON mantik_schema; DROP FUNCTION reject_review_migration()"); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var digest string
	if e := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=53").Scan(&digest); e != nil {
		t.Fatal(e)
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=53"); e != nil {
		t.Fatal(e)
	}
	if e := p.migrate(ctx); e == nil {
		t.Fatal("review checksum ignored")
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=53", digest); e != nil {
		t.Fatal(e)
	}
}
