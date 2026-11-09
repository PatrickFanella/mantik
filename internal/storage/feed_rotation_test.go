package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

type rotationBackend interface {
	PaperFeedAuditStore
	paperFeedBackend
	FeedRetentionStore
}

func TestMemoryFeedRotation(t *testing.T) { feedRotationContract(t, NewMemory()) }
func TestPostgresContractsFeedRotation(t *testing.T) {
	feedRotationContract(t, timerPostgres(t, "feedrotation"))
}

func feedRotationContract(t *testing.T, s rotationBackend) {
	t.Helper()
	ctx := t.Context()
	base := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	at := base
	now := func() time.Time { return at }
	noClock := func() time.Time { t.Fatal("retired retry sampled new time"); return time.Time{} }
	feed, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := feed.ReceiverID
	def := strategyDefinition("rotation")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, now)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "rotation-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	register := feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}
	originalRegister, err := s.FeedCollectorCommand(ctx, owner, register, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}, now); err != nil {
		t.Fatal(err)
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "rotation_fixture"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "rotation_fixture"}, nil
	}
	check := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	evidenceCheck := func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }
	var oldInput domain.RoutingInput
	var oldRaw json.RawMessage
	recording := func(n int, capability string, received time.Time) alpaca.Recording {
		r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: fmt.Sprintf("capture-%d", n), Capability: capability, Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: received, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, MaxQuoteAgeSeconds: 60}
		r.RawRef = fmt.Sprintf("journal:%s:capture-%s-r1.json", reg.JournalID, r.CaptureID)
		if capability == "market_calendar" {
			r.Feed = "regular_session"
			r.Start = "2026-11-09"
			r.End = r.Start
			r.Original = `[{"date":"2026-11-09","open":"09:30","close":"16:00"}]`
		} else {
			r.Original = fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99.99,"as":10,"bs":10}}}`, received.Add(-time.Second).Format(time.RFC3339Nano))
		}
		r.SHA256 = alpaca.Digest(r.Original)
		return r
	}
	publish := func(n int, capability string) {
		t.Helper()
		r := recording(n, capability, at)
		normalized, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range normalized.Inputs {
			if _, err := s.SubmitCollectorResearch(ctx, owner, fence, input, now); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(r)
		if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, fmt.Sprintf("capture-%d-r1", n), fence, raw, now, evidenceCheck); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			oldRaw = raw
			oldInput = normalized.Inputs[0]
		}
	}
	command := PaperDecisionCommand{ID: "old", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	var originalDecision PaperDecisionReceipt
	var originalOutcome PaperOutcomeReceipt
	for n := range 7 {
		at = base.Add(time.Duration(n) * time.Second)
		capability := "latest_quotes"
		if n == 0 || n == 5 {
			capability = "market_calendar"
		}
		publish(n, capability)
		if n == 2 {
			at = at.Add(time.Millisecond)
			originalDecision, err = s.FinalizePaperDecision(ctx, command, now, de)
			if err != nil {
				t.Fatal(err)
			}
			at = at.Add(time.Millisecond)
			originalOutcome, err = s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "old-outcome", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: originalDecision.Record.SHA256}, now, de, oe)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	pending := recording(8, "latest_quotes", base.Add(1500*time.Millisecond))
	pendingInputs, err := alpaca.Normalize(pending)
	if err != nil {
		t.Fatal(err)
	}
	at = base.Add(7 * time.Second)
	for _, input := range pendingInputs.Inputs {
		if _, err := s.SubmitCollectorResearch(ctx, owner, fence, input, now); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		// Exercise a populated schema-103 predecessor before the first move.
		dropFeedRetentionSchema(t, pg)
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=104"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated retention upgrade", err)
		}
		if got, err := pg.FeedEvidence(ctx); err != nil || !reflect.DeepEqual(got, before) {
			t.Fatal("retention upgrade changed original feed", err)
		}
		if got, err := pg.PaperDecisions(ctx, de); err != nil || !reflect.DeepEqual(got.Records[0], originalDecision.Record) {
			t.Fatal("retention upgrade changed original paper", err)
		}
	}
	at = base.Add(8 * time.Second)
	canceled, cancel := context.WithCancel(ctx)
	cancelEval := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
		cancel()
		return de(c, b, at)
	}
	if receipt, err := s.RotateFeed(canceled, owner, now, check, cancelEval, oe); err == nil || receipt != (FeedArchiveReceipt{}) {
		t.Fatal("canceled replay acknowledged retirement", receipt, err)
	}
	cancel()
	if active, err := s.FeedEvidence(ctx); err != nil || !reflect.DeepEqual(active, before) {
		t.Fatal("canceled replay changed active ownership", err)
	}
	if _, err := s.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("canceled retirement left archive", err)
	}
	if pg, ok := s.(*Postgres); ok {
		for _, sql := range []string{"CREATE FUNCTION reject_retirement_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture retirement COMMIT failure'; END; $$", "CREATE CONSTRAINT TRIGGER reject_retirement_commit AFTER INSERT ON mantik_feed_retention DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_retirement_commit()"} {
			if _, err := pg.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		if receipt, err := s.RotateFeed(ctx, owner, now, check, de, oe); err == nil || !strings.Contains(err.Error(), "fixture retirement COMMIT failure") || receipt != (FeedArchiveReceipt{}) {
			t.Fatal("failed COMMIT acknowledged retirement", receipt, err)
		}
		if active, err := s.FeedEvidence(ctx); err != nil || !reflect.DeepEqual(active, before) {
			t.Fatal("failed retirement COMMIT changed active ownership", err)
		}
		if _, err := s.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("failed retirement COMMIT left archive", err)
		}
		if _, err := s.PaperFeedCheckpoint(ctx, 1, de, oe); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("failed retirement COMMIT left custody", err)
		}
		if _, err := pg.pool.Exec(ctx, "DROP TRIGGER reject_retirement_commit ON mantik_feed_retention; DROP FUNCTION reject_retirement_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.RotateFeed(ctx, owner, now, check, de, oe)
	if err != nil || r.Retention == nil || r.Retention.PublicationsRetired < 4 || r.Retention.AdmissionsRetired < 3 || r.Retention.CommandsRetired != 1 || r.PaperCustody == nil {
		t.Fatal("rotation did not retire archived working set", r, err)
	}
	after, err := s.FeedEvidence(ctx)
	if err != nil || after.Retention == nil || validateRetainedFeed(after) != nil || len(after.Records) != 2 || len(after.Collectors.Commands) != 1 || !reflect.DeepEqual(before.Collectors.Entries, after.Collectors.Entries) || !reflect.DeepEqual(before.Routing.Runtime, after.Routing.Runtime) || !reflect.DeepEqual(before.Routing.Cases, after.Routing.Cases) {
		t.Fatal("rotation changed live coverage or routing", err)
	}
	for _, input := range pendingInputs.Inputs {
		if _, active := after.Collectors.Admissions[input.ID]; !active {
			t.Fatal("pending unpublished admission was retired")
		}
	}
	if _, active := after.Collectors.Admissions[oldInput.ID]; active {
		t.Fatal("old admission remained active")
	}
	if pg, ok := s.(*Postgres); ok {
		assertFeedRetirementOldWriterFences(t, pg, before, r, originalDecision.Record, originalOutcome.Record, at.Add(time.Second))
	}
	if got, err := s.PaperDecisions(ctx, de); err != nil || len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], originalDecision.Record) {
		t.Fatal("retirement changed finalized decision", err)
	}
	if got, err := s.PaperOutcomes(ctx, de, oe); err != nil || len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], originalOutcome.Record) {
		t.Fatal("retirement changed outcome", err)
	}
	if got, err := s.FeedCollectorCommand(ctx, owner, register, noClock); err != nil || !reflect.DeepEqual(got, originalRegister) {
		t.Fatal("retired command lost original receipt", err)
	}
	if got, err := s.SubmitCollectorResearch(ctx, owner, fence, oldInput, noClock); err != nil || !got.Duplicate {
		t.Fatal("retired admission was resubmitted", err)
	}
	if got, err := s.PublishFeedEvidence(ctx, owner, reg.ID, "capture-0-r1", fence, oldRaw, noClock, evidenceCheck); err != nil || !got.Duplicate || !got.PublishedAt.Equal(base) {
		t.Fatal("retired publication was republished", err)
	}
	if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, "capture-0-r1", fence, json.RawMessage(`{"changed":true}`), noClock, evidenceCheck); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatal("retired key accepted changed data", err)
	}
	retry, err := s.RotateFeed(ctx, owner, noClock, check, de, oe)
	if err != nil || !retry.Duplicate || retry.Retention == nil || !retry.Retention.Duplicate || retry.Retention.SHA256 != r.Retention.SHA256 {
		t.Fatal("unchanged rotation moved cut", retry, err)
	}
	at = base.Add(9 * time.Second)
	publish(7, "latest_quotes")
	command.ID = "new"
	at = at.Add(time.Millisecond)
	newDecision, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal("new decision after retirement", err)
	}
	// A new seal must not turn unretired source corruption into trusted history.
	live, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalTail := live.Records[reg.ID+":capture-7-r1"]
	tampered := originalTail
	tampered.Recording = json.RawMessage(`{"capability":"latest_quotes","received_at":"2026-11-09T15:00:09Z","changed":true}`)
	setTail := func(value FeedEvidenceRecord) {
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.evidence[reg.ID+":capture-7-r1"] = value
			m.mu.Unlock()
		} else {
			raw, _ := json.Marshal(value)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_feed_evidence SET payload=$1 WHERE registration_id=$2 AND capture_key='capture-7-r1'", raw, reg.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	setTail(tampered)
	if _, err := s.PaperDecisions(ctx, de); err == nil {
		t.Fatal("source seal hid changed active evidence")
	}
	setTail(originalTail)
	at = at.Add(time.Millisecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "new-outcome", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: newDecision.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal("new outcome after retirement", err)
	}
	// A new observation of the old decision must recover the original forward
	// window, rather than treating retired sources as missing observations.
	current, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	full := clone(before)
	full.Collectors.Entries, full.Collectors.LastClock = current.Collectors.Entries, current.Collectors.LastClock
	for key, value := range current.Records {
		full.Records[key] = value
	}
	for key, value := range current.Collectors.Admissions {
		full.Collectors.Admissions[key] = value
		full.Routing.Inputs[key] = current.Routing.Inputs[key]
	}
	strategies, err := s.PaperStrategies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allBasis, err := decisionBasis(originalDecision.Record.Command, strategies, full)
	if err != nil {
		t.Fatal(err)
	}
	forward := PaperOutcomeCommand{ID: "old-forward", ReceiverID: owner, DecisionID: originalDecision.Record.Command.ID, DecisionSHA256: originalDecision.Record.SHA256}
	at = at.Add(time.Millisecond)
	basis := PaperOutcomeBasis{originalDecision.Record, withFeedPrefix(originalDecision.Record.Command, allBasis, full, at, originalDecision.Record.FinalizedAt)}
	basis.Observation, err = canonicalPaperBasis(basis.Observation)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := paperOutcomeForSchema(paperOutcomeSchemaV4, forward, basis, at, oe)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := s.AssessPaperOutcome(ctx, forward, now, de, oe)
	if err != nil || !reflect.DeepEqual(actual.Record, expected) {
		t.Fatal("retired forward window differs from full history", actual.Record.BasisSHA256, expected.BasisSHA256, err)
	}
	// Delayed publication can lower a forward window into retired history,
	// even when the decision itself was finalized after retirement.
	at = base.Add(9500 * time.Millisecond)
	pendingRaw, _ := json.Marshal(pending)
	if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, "capture-8-r1", fence, pendingRaw, now, evidenceCheck); err != nil {
		t.Fatal("pending publication lost original admission", err)
	}
	current, err = s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	full.Collectors.Entries, full.Collectors.LastClock = current.Collectors.Entries, current.Collectors.LastClock
	for key, value := range current.Records {
		full.Records[key] = value
	}
	for key, value := range current.Collectors.Admissions {
		full.Collectors.Admissions[key] = value
		full.Routing.Inputs[key] = current.Routing.Inputs[key]
	}
	newFull, err := decisionBasis(newDecision.Record.Command, strategies, full)
	if err != nil {
		t.Fatal(err)
	}
	late := PaperOutcomeCommand{ID: "new-late", ReceiverID: owner, DecisionID: newDecision.Record.Command.ID, DecisionSHA256: newDecision.Record.SHA256}
	at = base.Add(9600 * time.Millisecond)
	lateBasis := PaperOutcomeBasis{newDecision.Record, withFeedPrefix(newDecision.Record.Command, newFull, full, at, newDecision.Record.FinalizedAt)}
	lateBasis.Observation, err = canonicalPaperBasis(lateBasis.Observation)
	if err != nil {
		t.Fatal(err)
	}
	lateExpected, err := paperOutcomeForSchema(paperOutcomeSchemaV4, late, lateBasis, at, oe)
	if err != nil {
		t.Fatal(err)
	}
	lateActual, err := s.AssessPaperOutcome(ctx, late, now, de, oe)
	if err != nil || !reflect.DeepEqual(lateActual.Record, lateExpected) {
		t.Fatal("late publication lost archived forward evidence", lateActual.Record.BasisSHA256, lateExpected.BasisSHA256, err)
	}
	at = base.Add(10 * time.Second)
	// A coordinated paper/seal rehash still cannot replace original archived
	// source bytes during a full audit or while issuing the next checkpoint.
	var saved retainedPaperOutcome
	if m, ok := s.(*Memory); ok {
		saved = clone(m.paperOutcomes[forward.ID])
	} else {
		var raw []byte
		if err := s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_outcomes WHERE id=$1", forward.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
	}
	forged := clone(saved)
	oldKey := reg.ID + ":capture-0-r1"
	fakePublication := forged.Basis.Observation.Feed.Records[oldKey]
	var fakeRecording alpaca.Recording
	if err := json.Unmarshal(fakePublication.Recording, &fakeRecording); err != nil {
		t.Fatal(err)
	}
	fakeRecording.Original += " "
	fakeRecording.SHA256 = alpaca.Digest(fakeRecording.Original)
	fakePublication.Recording, _ = json.Marshal(fakeRecording)
	forged.Basis.Observation.Feed.Records[oldKey] = fakePublication
	forged.Basis.Observation, err = canonicalPaperBasis(forged.Basis.Observation)
	if err != nil {
		t.Fatal(err)
	}
	forged.Record.BasisSHA256 = workerplan.Hash(forged.Basis)
	forged.Record.SHA256 = ""
	forged.Record.SHA256 = workerplan.Hash(forged.Record)
	setOutcome := func(value retainedPaperOutcome) {
		pin := outcomeFeedSourcePin(value)
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.paperOutcomes[forward.ID] = value
			m.paperFeedSourceSeals["outcome:"+forward.ID] = pin
			m.mu.Unlock()
		} else {
			raw, _ := json.Marshal(value)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_outcomes SET payload=$1 WHERE id=$2", raw, forward.ID); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(pin)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_feed_source_seals SET record_sha256=$1,basis_sha256=$2,payload=$3 WHERE kind='outcome' AND record_id=$4", pin.RecordSHA256, pin.BasisSHA256, raw, pin.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	setOutcome(forged)
	if _, err := s.AuditPaperFeed(ctx, de, oe); err == nil {
		t.Fatal("self-rehashed paper and seal replaced original evidence")
	}
	if receipt, err := s.RotateFeed(ctx, owner, now, check, de, oe); err == nil || receipt != (FeedArchiveReceipt{}) {
		t.Fatal("false source seal committed next retirement", receipt, err)
	}
	if _, err := s.FeedArchive(ctx, 2); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("failed original replay left partial archive", err)
	}
	setOutcome(saved)
	second, err := s.RotateFeed(ctx, owner, now, check, de, oe)
	if err != nil || second.Retention == nil || second.Retention.Sequence != 2 || second.Header.Schema != "mantik-feed-archive-v2" {
		t.Fatal("second bounded rotation failed", second, err)
	}
	if got, err := s.PaperDecisions(ctx, de); err != nil || len(got.Records) != 2 {
		t.Fatal("successive retirement lost paper decisions", err)
	}
	if got, err := s.PaperOutcomes(ctx, de, oe); err != nil || len(got.Records) != 4 {
		t.Fatal("successive retirement lost outcomes", err)
	}
	if got, err := s.RotateFeed(ctx, owner, noClock, check, de, oe); err != nil || got.Retention == nil || !got.Retention.Duplicate {
		t.Fatal("second retirement retry", err)
	}
	at = base.Add(11 * time.Second)
	forward.ID = "old-forward-again"
	allBasis, err = decisionBasis(originalDecision.Record.Command, strategies, full)
	if err != nil {
		t.Fatal(err)
	}
	basis = PaperOutcomeBasis{originalDecision.Record, withFeedPrefix(originalDecision.Record.Command, allBasis, full, at, originalDecision.Record.FinalizedAt)}
	basis.Observation, err = canonicalPaperBasis(basis.Observation)
	if err != nil {
		t.Fatal(err)
	}
	expected, err = paperOutcomeForSchema(paperOutcomeSchemaV4, forward, basis, at, oe)
	if err != nil {
		t.Fatal(err)
	}
	actual, err = s.AssessPaperOutcome(ctx, forward, now, de, oe)
	if err != nil || !reflect.DeepEqual(actual.Record, expected) {
		t.Fatal("multiple archived segments changed forward observation", actual.Record.BasisSHA256, expected.BasisSHA256, err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 1, de, oe); err != nil {
		t.Fatal("old checkpoint changed after retirement", err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 2, de, oe); err != nil {
		t.Fatal("bounded checkpoint replay", err)
	}
	if audit, err := s.AuditFeedArchiveSources(ctx); err != nil || audit.Archives != 2 {
		t.Fatal("retired source audit", audit, err)
	}
	if audit, err := s.AuditPaperFeed(ctx, de, oe); err != nil || audit.Status != "verified" || audit.Decisions != 2 || audit.Outcomes != 5 || audit.Checkpoints != 2 {
		t.Fatal("original paper source audit", audit, err)
	}
	if pg, ok := s.(*Postgres); ok {
		retained := clone(second.Retention)
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=104"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated retention DDL replay", err)
		}
		if got, err := pg.RotateFeed(ctx, owner, noClock, check, de, oe); err != nil || got.Retention.SHA256 != retained.SHA256 {
			t.Fatal("DDL replay changed retirement", err)
		}
		var migrationSHA string
		if err := pg.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=104").Scan(&migrationSHA); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=104", strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatal("retention migration checksum ignored", err)
		}
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=104", migrationSHA); err != nil {
			t.Fatal(err)
		}
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		if _, err := s.PaperDecisions(ctx, de); err != nil {
			t.Fatal("restart lost retirement custody", err)
		}
		if audit, err := s.AuditPaperFeed(ctx, de, oe); err != nil || audit.Outcomes != 5 {
			t.Fatal("restored retirement source audit", audit, err)
		}
	}
	// Retiring while older and newer source writers contend must preserve every
	// command receipt, whether it committed before or after the rotation cut.
	var ticks atomic.Int64
	ticks.Store(at.UnixNano())
	concurrentClock := func() time.Time { return time.Unix(0, ticks.Add(1)).UTC() }
	var writers sync.WaitGroup
	start := make(chan struct{})
	for n := range 8 {
		writers.Go(func() {
			<-start
			command := feedcollector.Command{RequestID: fmt.Sprintf("concurrent-renew-%d", n), Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
			receipt, err := s.FeedCollectorCommand(ctx, owner, command, concurrentClock)
			if err != nil {
				t.Error("source writer racing retirement", err)
				return
			}
			if retry, err := s.FeedCollectorCommand(ctx, owner, command, concurrentClock); err != nil || !reflect.DeepEqual(retry, receipt) {
				t.Error("racing source lost original receipt", err)
			}
		})
	}
	writers.Go(func() {
		<-start
		if _, err := s.RotateFeed(ctx, owner, concurrentClock, check, de, oe); err != nil {
			t.Error("rotation racing source writer", err)
		}
	})
	close(start)
	writers.Wait()
	if t.Failed() {
		return
	}
	if _, err := s.RotateFeed(ctx, owner, concurrentClock, check, de, oe); err != nil {
		t.Fatal("final concurrent source cut", err)
	}
	if audit, err := s.AuditPaperFeed(ctx, de, oe); err != nil || audit.Decisions != 2 || audit.Outcomes != 5 {
		t.Fatal("concurrent rotation lost original sources", audit, err)
	}
	// Archive-body loss is visible to audit without becoming a dependency of
	// established paper reads, which replay their retained finalized bases.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.feedArchives[0].Source.ReceiverID = "receiver-corrupt"
		m.mu.Unlock()
	} else {
		if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_feed_archives SET payload=convert_to('{}','UTF8') WHERE sequence=1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PaperDecisions(ctx, de); err != nil {
		t.Fatal("paper decision read depended on historical archive body", err)
	}
	if _, err := s.PaperOutcomes(ctx, de, oe); err != nil {
		t.Fatal("paper outcome read depended on historical archive body", err)
	}
	if _, err := s.AuditPaperFeed(ctx, de, oe); err == nil {
		t.Fatal("archive corruption was hidden from source audit")
	}

}

func assertFeedRetirementOldWriterFences(t *testing.T, pg *Postgres, before FeedEvidenceSnapshot, receipt FeedArchiveReceipt, decision PaperDecisionRecord, outcome PaperOutcomeRecord, at time.Time) {
	t.Helper()
	ctx := t.Context()
	if _, err := pg.pool.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=payload-'archive_sha256' WHERE singleton"); err == nil || !strings.Contains(err.Error(), "retention owner root") {
		t.Fatal("old collector writer lost retirement root", err)
	}
	old, err := newFeedArchive(before, receipt.Header, at, func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(old.FeedArchiveHeader)
	raw, _ := json.Marshal(old)
	if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_feed_archives(sequence,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5)", old.Sequence, old.SourceSHA256, old.SHA256, header, raw); err == nil || !strings.Contains(err.Error(), "retention owner root") {
		t.Fatal("old archive writer lost retirement root", err)
	}
	for _, kind := range []string{"decision", "outcome"} {
		var body []byte
		var id, table string
		if kind == "decision" {
			decision.Command.ID, decision.FinalizedAt = "old-writer-decision", at
			decision.SHA256 = ""
			decision.SHA256 = workerplan.Hash(decision)
			body, _ = json.Marshal(retainedPaperDecision{Record: decision})
			id, table = decision.Command.ID, "mantik_paper_decisions"
		} else {
			var retained retainedPaperOutcome
			var original []byte
			if err := pg.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_outcomes WHERE id=$1", outcome.Command.ID).Scan(&original); err != nil || json.Unmarshal(original, &retained) != nil {
				t.Fatal("old writer outcome scope fixture", err)
			}
			outcome.Command.ID, outcome.EvaluatedAt = "old-writer-outcome", at
			outcome.SHA256 = ""
			outcome.SHA256 = workerplan.Hash(outcome)
			body, _ = json.Marshal(retainedPaperOutcome{Record: outcome, Basis: retained.Basis})
			id, table = outcome.Command.ID, "mantik_paper_outcomes"
		}
		tx, err := pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+"(id,payload) VALUES($1,$2)", id, body); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal("fixture did not reach deferred source seal", err)
		}
		if err := tx.Commit(ctx); err == nil || !strings.Contains(err.Error(), "source seal missing") {
			t.Fatal("older paper writer committed without source custody", kind, err)
		}
		var count int
		if err := pg.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE id=$1", id).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed old writer left paper record", kind, err)
		}
	}
}
