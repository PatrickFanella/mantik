package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceReviewBackend interface {
	decisionTimeBackend
	SocialWatchStore
	PaperAccountStore
	PaperAttributionStore
}

func TestMemorySourceBoundDecisionReviews(t *testing.T) { sourceBoundDecisionReviews(t, NewMemory()) }
func TestPostgresContractsSourceBoundDecisionReviews(t *testing.T) {
	sourceBoundDecisionReviews(t, timerPostgres(t, "sourcedecisionreview"))
}
func sourceBoundDecisionReviews(t *testing.T, s sourceReviewBackend) {
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	clock := func() time.Time { return at }
	initial, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	def := strategyDefinition("membership-strategy")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, clock)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "membership-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	input := collectorInput(at, "membership-input")
	if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock); err != nil {
		t.Fatal(err)
	}
	publish := func(id string) {
		t.Helper()
		if _, e := s.PublishFeedEvidence(ctx, owner, reg.ID, id, fence, json.RawMessage(`{"fixture":"membership"}`), clock, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); e != nil {
			t.Fatal(e)
		}
	}
	publish("membership-raw")
	command := PaperDecisionCommand{ID: "membership-decision", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "membership_fixture_only"}, nil
	}
	at = at.Add(time.Second)
	if _, err = s.FinalizePaperDecision(ctx, command, clock, eval); err != nil {
		t.Fatal(err)
	}

	decision, err := s.PaperDecisionSources(ctx, command.ID, eval)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { panic("source review retry clock") }
	legacyCommand := DecisionReviewCommand{ID: "legacy-review", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: decision.Decision.SHA256, Explanation: "Opaque manual fixture", Claims: []DecisionReviewClaim{{Field: "action", Value: "enter"}}}
	at = at.Add(time.Second)
	legacy, err := s.ReviewPaperDecision(ctx, legacyCommand, clock, eval)
	if err != nil || legacy.Record.Source != nil || legacy.Record.Schema != "mantik-finalized-decision-review-v1" {
		t.Fatal(legacy, err)
	}
	legacyBytes, _ := json.Marshal(legacy.Record)
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{}, ErrPaperOutcomeInvalid // no outcomes in this fixture
	}
	for _, kind := range []string{"social", "copy"} {
		policy := socialPolicy(owner)
		policy.ID = kind + "-policy"
		policy.Policy.ID = kind + "-watch"
		policy.Policy.AccountID = kind + "-account"
		policy.Policy.Behavior = "research"
		at = at.Add(time.Second)
		registered, e := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, clock)
		if e != nil {
			t.Fatal(e)
		}
		original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: policy.Policy.AccountID, PostID: kind + "-post"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Unverified SPY claim", Symbols: []string{"SPY"}}
		class := "recorded_social_unqualified"
		if kind == "copy" {
			original.Schema = socialwatch.CopyRecordingSchema
			original.Activity = &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "assertion-v1", Side: "buy", QuantityUnits: 10, QuantityUnitClaim: "shares", PriceCents: 10000}
			class = "recorded_copy_unqualified"
		}
		raw, _ := json.Marshal(original)
		capture := socialwatch.CaptureCommand{ID: kind + "-first", ReceiverID: owner, WatchID: policy.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: class, Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
		captured, e := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, clock)
		if e != nil {
			t.Fatal(e)
		}
		c := DecisionReviewCommand{ID: kind + "-review", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: decision.Decision.SHA256, Claims: []DecisionReviewClaim{{Field: "action", Value: "enter"}}, Source: &DecisionReviewSourceCommand{CaptureID: capture.ID, CaptureSHA256: captured.Entry.Capture.SHA256}}
		if _, e = s.ReviewPaperDecision(ctx, c, clock, eval); e == nil {
			t.Fatal("research policy authorized decision review")
		}
		policy.ID = kind + "-enable-review"
		policy.PreviousSHA256 = registered.Entry.Policy.SHA256
		policy.Policy.Behavior = "decision_review"
		at = at.Add(time.Second)
		registered, e = s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, clock)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.ReviewPaperDecision(ctx, c, clock, eval); e == nil {
			t.Fatal("changed policy repaired old capture")
		}
		original.Revision = 2
		original.Kind = "edit"
		original.OccurredAt = at
		original.ObservedAt = at
		raw, _ = json.Marshal(original)
		capture.ID = kind + "-current"
		capture.PolicySHA256 = registered.Entry.Policy.SHA256
		capture.Original = string(raw)
		capture.OriginalSHA256 = workerplan.Digest(raw)
		captured, e = s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, clock)
		if e != nil {
			t.Fatal(e)
		}
		if !captured.Entry.Capture.SourceAccepted || captured.Entry.Capture.SourceStatus != "retained" {
			t.Fatal("invalid source fixture", captured.Entry.Capture.SourceStatus)
		}
		c.Source = &DecisionReviewSourceCommand{CaptureID: capture.ID, CaptureSHA256: captured.Entry.Capture.SHA256}
		bad := clone(c)
		bad.Explanation = "Caller cannot replace source provenance"
		if _, e = s.ReviewPaperDecision(ctx, bad, clock, eval); e == nil {
			t.Fatal("source review accepted caller prose")
		}
		bad = clone(c)
		bad.Source.CaptureSHA256 = strings.Repeat("a", 64)
		if _, e = s.ReviewPaperDecision(ctx, bad, clock, eval); e == nil {
			t.Fatal("source pin mismatch accepted")
		}
		at = at.Add(time.Second)
		if pg, ok := s.(*Postgres); ok {
			baseline, e := s.DecisionReviews(ctx, eval)
			if e != nil {
				t.Fatal(e)
			}
			_, e = pg.pool.Exec(ctx, `CREATE FUNCTION reject_source_review_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'source review deferred rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_source_review_commit AFTER INSERT ON mantik_decision_reviews DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_source_review_commit()`)
			if e != nil {
				t.Fatal(e)
			}
			failed, e := s.ReviewPaperDecision(ctx, c, clock, eval)
			if _, drop := pg.pool.Exec(ctx, "DROP TRIGGER reject_source_review_commit ON mantik_decision_reviews; DROP FUNCTION reject_source_review_commit()"); drop != nil {
				t.Fatal(drop)
			}
			if e == nil || !reflect.DeepEqual(failed, DecisionReviewReceipt{}) {
				t.Fatal("failed COMMIT returned source review", failed, e)
			}
			actual, e := s.DecisionReviews(ctx, eval)
			if e != nil || !reflect.DeepEqual(actual, baseline) {
				t.Fatal("failed COMMIT changed review journal", e)
			}
		}
		var clocks atomic.Int32
		now := func() time.Time { clocks.Add(1); return at }
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, e := s.ReviewPaperDecision(ctx, c, now, eval); e != nil {
					t.Error(e)
				}
			})
			wg.Go(func() {
				if _, e := s.PaperAttribution(ctx, eval, oe); e != nil {
					t.Error("concurrent claim attribution snapshot", e)
				}
			})
		}
		wg.Wait()
		if clocks.Load() != 1 {
			t.Fatal("duplicate issuance", clocks.Load())
		}
		issued, e := s.ReviewPaperDecision(ctx, c, noClock, eval)
		if e != nil || !issued.Duplicate || issued.Record.Source == nil {
			t.Fatal(issued, e)
		}
		b := issued.Record.Source
		if issued.Record.Schema != "mantik-finalized-decision-review-v2" || b.Kind != kind || b.CaptureSHA256 != captured.Entry.Capture.SHA256 || b.OriginalSHA256 != capture.OriginalSHA256 || b.PolicySHA256 != capture.PolicySHA256 || b.ClaimGroupSHA256 != captured.Entry.Capture.ClaimGroupSHA256 || b.ClaimsExtractionVerified || b.ProviderAccessVerified || issued.Record.ProseVerified || issued.Record.ModelUsed || issued.Record.AdmissionAvailable || issued.Record.ExecutionAvailable || issued.Record.QualificationCredit || issued.Record.CaseID == "" {
			t.Fatal("invalid source-bound review", issued)
		}
		if kind == "social" {
			// The same actual retained source is reviewed against separately
			// finalized decisions. Equal clocks prove no cross-owner ordering.
			for i, relation := range []string{"retained_at_decision_clock_order_unproven", "retained_before_decision_clock"} {
				next := command
				next.ID = []string{"equal-clock-decision", "later-clock-decision"}[i]
				finalized := captured.Entry.Capture.RetainedAt.Add(time.Duration(i) * time.Second)
				dec, e := s.FinalizePaperDecision(ctx, next, func() time.Time { return finalized }, eval)
				if e != nil {
					t.Fatal(e)
				}
				review := clone(c)
				review.ID, review.DecisionID, review.DecisionSHA256 = next.ID+"-review", next.ID, dec.Record.SHA256
				if _, e = s.ReviewPaperDecision(ctx, review, clock, eval); e != nil {
					t.Fatal(e)
				}
				catalog, e := s.PaperAttribution(ctx, eval, oe)
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, row := range catalog.Rows {
					if row.Decision.Command.ID == next.ID {
						h := row.ClaimReviewHistory
						if h == nil || h.Validate(row.Decision) != nil || len(h.SourceTimings) != 1 || h.SourceTimings[0].ClockRelation != relation || h.SourceTimings[0].DecisionInputVerified {
							t.Fatal("clock relation invented ordering/causality", h)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("missing claim attribution")
				}
			}
		}
		duplicate := clone(c)
		duplicate.ID = kind + "-echo"
		if _, e = s.ReviewPaperDecision(ctx, duplicate, clock, eval); e == nil {
			t.Fatal("same source group dispatched twice for decision")
		}
		// Preserve the old source cut: a new write at its review time fails, while
		// an exact historical source retry remains clock free.
		disabled := clone(policy)
		disabled.ID = kind + "-disable"
		disabled.PreviousSHA256 = registered.Entry.Policy.SHA256
		disabled.Policy.Enabled = false
		failed, e := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &disabled}, clock)
		if e == nil || !reflect.DeepEqual(failed, socialwatch.Receipt{}) {
			t.Fatal("source write entered retained review cut", e)
		}
		if retry, e := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, noClock); e != nil || !retry.Duplicate {
			t.Fatal("source exact retry changed", e)
		}
		at = at.Add(time.Second)
		if _, e = s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &disabled}, clock); e != nil {
			t.Fatal(e)
		}
		retry, e := s.ReviewPaperDecision(ctx, c, noClock, eval)
		if e != nil || !reflect.DeepEqual(retry, issued) {
			t.Fatal("later disabled policy rewrote historical review", e)
		}
		// A self-hashed substituted provenance binding cannot replace actual owners.
		set := func(r DecisionReviewRecord) {
			t.Helper()
			if m, ok := s.(*Memory); ok {
				m.mu.Lock()
				m.decisionReviews[c.ID] = clone(r)
				m.mu.Unlock()
			} else {
				raw, _ := json.Marshal(r)
				if _, e := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_decision_reviews SET payload=$1 WHERE id=$2", raw, c.ID); e != nil {
					t.Fatal(e)
				}
			}
		}
		forged := clone(issued.Record)
		forged.Source.OriginalSHA256 = strings.Repeat("b", 64)
		forged.SHA256 = ""
		forged.SHA256 = workerplan.Hash(forged)
		set(forged)
		if _, e = s.DecisionReviews(ctx, eval); e == nil {
			t.Error("self-hashed source substitution accepted")
		}
		if got, e := s.PaperAttribution(ctx, eval, oe); e == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
			t.Error("claim owner corruption returned attribution", got, e)
		}
		set(issued.Record)
		if pg, ok := s.(*Postgres); ok {
			request, cancel := context.WithCancel(ctx)
			tracer := &cancelCollectorCommit{cancel: cancel}
			config := pg.pool.Config()
			config.ConnConfig.Tracer = tracer
			pool, e := pgxpool.NewWithConfig(ctx, config)
			if e != nil {
				t.Fatal(e)
			}
			failed, e := (&Postgres{pool: pool}).ReviewPaperDecision(request, c, noClock, eval)
			pool.Close()
			cancel()
			if !tracer.fired.Load() || e == nil || !reflect.DeepEqual(failed, DecisionReviewReceipt{}) {
				t.Fatal("cancelled retry COMMIT returned review", failed, e)
			}
		}
	}
	final, err := s.DecisionReviews(ctx, eval)
	if err != nil || len(final.Records) != 5 {
		t.Fatal(final, err)
	}
	attribution, err := s.PaperAttribution(ctx, eval, oe)
	if err != nil || attribution.Schema != "mantik-owned-paper-attribution-v5" {
		t.Fatal(attribution, err)
	}
	requireAttributionTargets(t, s, attribution, eval, oe)
	for _, row := range attribution.Rows {
		h := row.ClaimReviewHistory
		if h == nil || h.Validate(row.Decision) != nil {
			t.Fatal("invalid claim history", h)
		}
		if row.Decision.Command.ID == command.ID {
			if len(h.Records) != 3 || len(h.SourceTimings) != 2 {
				t.Fatal("source reviews omitted", h)
			}
			for _, timing := range h.SourceTimings {
				if timing.ClockRelation != "retained_after_decision_clock" || timing.DecisionInputVerified {
					t.Fatal("late review source treated as decision input", timing)
				}
			}
		}
	}
	old, err := s.ReviewPaperDecision(ctx, legacyCommand, noClock, eval)
	raw, _ := json.Marshal(old.Record)
	if err != nil || string(raw) != string(legacyBytes) {
		t.Fatal("legacy bytes/retry changed", err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("source reviews changed operations", err)
	}
	accountAfter, err := s.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accountAfter, accounts) {
		t.Fatal("source reviews changed capital", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		actual, e := reopened.DecisionReviews(ctx, eval)
		if e != nil || !reflect.DeepEqual(actual, final) {
			t.Fatal("reopen changed source reviews", e)
		}
		actualAttribution, e := reopened.PaperAttribution(ctx, eval, oe)
		if e != nil || !reflect.DeepEqual(actualAttribution, attribution) {
			t.Fatal("reopen changed claim attribution", e)
		}
		requireAttributionTargets(t, reopened, attribution, eval, oe)
		sourceReviewMigrationContract(t, pg, final.Records[0].Command.ID)
	}
	// A real archive supplies the decision after fixture-only approved
	// retirement. Reviews keep their actual social/copy source cuts and cases.
	at = at.Add(time.Second)
	archive, err := s.(PaperRecordArchiveStore).ArchivePaperRecords(ctx, owner, clock, eval, oe)
	if err != nil || archive.Header.Sequence != 1 {
		t.Fatal("review dependency archive", err)
	}
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.paperRecordRetiredEpochs = map[string]int64{"decision:" + command.ID: 1}
		delete(m.paperDecisions, command.ID)
		m.mu.Unlock()
	} else if _, err := s.(*Postgres).pool.Exec(ctx, `ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs;
UPDATE mantik_paper_record_epochs SET retired_epoch=1 WHERE kind='decision' AND record_id='membership-decision';
ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs;
DELETE FROM mantik_paper_decisions WHERE id='membership-decision'`); err != nil {
		t.Fatal(err)
	}
	retained, err := s.DecisionReviews(ctx, eval)
	if err != nil || !reflect.DeepEqual(retained, final) {
		t.Fatal("retired decision changed source-backed findings or cases", err)
	}
	if retry, err := s.ReviewPaperDecision(ctx, legacyCommand, noClock, eval); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, legacy.Record) {
		t.Fatal("retired dependency changed clock-free review retry", err)
	}
	newCommand := clone(legacyCommand)
	newCommand.ID = "archived-decision-review"
	at = at.Add(time.Second)
	newReview, err := s.ReviewPaperDecision(ctx, newCommand, clock, eval)
	if err != nil || newReview.Duplicate || newReview.Record.AdmissionAvailable || newReview.Record.ExecutionAvailable || newReview.Record.QualificationCredit {
		t.Fatal("review of actual archived decision", err)
	}
	current, err := s.DecisionReviews(ctx, eval)
	if err != nil || len(current.Records) != len(final.Records)+1 {
		t.Fatal("new review lost retained source-backed history", err)
	}
	// The retired decision leaves the attribution rows; its claim reviews stay
	// in the replayed review sources and every active row is unchanged.
	retiredAttribution, err := s.PaperAttribution(ctx, eval, oe)
	if err != nil || len(retiredAttribution.Rows) != len(attribution.Rows)-1 {
		t.Fatal("attribution after retiring a reviewed decision", err)
	}
	for _, row := range retiredAttribution.Rows {
		if row.Decision.Command.ID == command.ID {
			t.Fatal("retired decision kept an attribution row")
		}
		found := false
		for _, before := range attribution.Rows {
			if before.Decision.Command.ID == row.Decision.Command.ID {
				found = reflect.DeepEqual(before, row)
			}
		}
		if !found {
			t.Fatal("retiring a reviewed decision changed an active attribution row", row.Decision.Command.ID)
		}
	}
	requireAttributionTargets(t, s, retiredAttribution, eval, oe, command.ID)
	changed := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
		v, err := eval(c, b, at)
		if c.ID == command.ID {
			v.ReasonCode = "changed_archived_native_evaluation"
		}
		return v, err
	}
	if got, err := s.DecisionReviews(ctx, changed); err == nil || !reflect.DeepEqual(got, DecisionReviewCatalog{}) {
		t.Fatal("archive custody replaced native decision replay for reviews", err)
	}
	if got, err := s.PaperAttribution(ctx, changed, oe); err == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
		t.Fatal("attribution trusted an archived reviewed decision without native replay", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if got, err := reopened.DecisionReviews(ctx, eval); err != nil || !reflect.DeepEqual(got, current) {
			t.Fatal("reopen lost archived review dependencies", err)
		}
	}
}

func sourceReviewMigrationContract(t *testing.T, p *Postgres, id string) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	var socialBefore []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_social_watch_journal WHERE singleton").Scan(&socialBefore); err != nil {
		t.Fatal(err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_decision_reviews WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=59; COMMENT ON TABLE mantik_decision_reviews IS 'schema58 source review fixture'; COMMENT ON TABLE mantik_social_watch_journal IS 'schema58 social review fixture'; CREATE FUNCTION reject_source_review_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=59 THEN RAISE EXCEPTION 'source review migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_source_review_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_source_review_migration()`); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("migration failure ignored")
	}
	var max int
	var marker string
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&max); err != nil || max != 58 {
		t.Fatal("source review fence migration partially committed", max, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_decision_reviews'::regclass)").Scan(&marker); err != nil || marker != "schema58 source review fixture" {
		t.Fatal("source review fence comment escaped rollback", marker, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_social_watch_journal'::regclass)").Scan(&marker); err != nil || marker != "schema58 social review fixture" {
		t.Fatal("social clock fence escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_source_review_migration ON mantik_schema; DROP FUNCTION reject_source_review_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	var after []byte
	if err = p.pool.QueryRow(ctx, "SELECT payload FROM mantik_decision_reviews WHERE id=$1", id).Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote review payload", err)
	}
	var socialAfter []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_social_watch_journal WHERE singleton").Scan(&socialAfter); err != nil || !reflect.DeepEqual(socialBefore, socialAfter) {
		t.Fatal("migration rewrote social journal", err)
	}
	var hash string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=59").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=59"); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("source review fence checksum corruption accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=59", hash); err != nil {
		t.Fatal(err)
	}
}
