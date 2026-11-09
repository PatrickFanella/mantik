package service

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestFeedRotationPreservesNativeOutcomeHTTP(t *testing.T) {
	f := newQuoteFixture(t)
	ctx := t.Context()
	f.at = f.at.Add(time.Second)
	f.quote("entry", 10000)
	command := storage.PaperDecisionCommand{ID: "rotation-decision", ReceiverID: f.owner, StrategyID: f.definition.ID, StrategySHA256: f.strategy.Record.SHA256, RegistrationID: f.reg.ID}
	decision, err := f.store.FinalizePaperDecision(ctx, command, f.now, evaluateOwnedPaperDecision)
	if err != nil || decision.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal("native fixture did not finalize entry proposal", decision, err)
	}
	f.at = f.at.Add(time.Second)
	f.quote("older", 10001)
	f.at = f.at.Add(time.Second)
	f.publish(f.recording("new-calendar", "market_calendar", "regular_session", f.calendarRaw, f.date))
	f.at = f.at.Add(time.Second)
	f.quote("latest", 10002)
	before, err := f.store.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.at = f.at.Add(time.Nanosecond)
	clock := &fakeClock{at: f.at}
	handler := HandlerWithClock(f.store, "rotation-fixture-token", clock)
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		clock.mu.Lock()
		clock.at = f.at
		clock.mu.Unlock()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer rotation-fixture-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var rotated storage.FeedArchiveReceipt
	if err := json.Unmarshal(request("POST", "/v1/feed-archives/rotate", map[string]string{"receiver_id": f.owner}, 201), &rotated); err != nil || rotated.Retention == nil || rotated.Retention.PublicationsRetired != 3 {
		t.Fatal("HTTP rotation did not retire old published evidence", rotated, err)
	}
	after, err := f.store.FeedEvidence(ctx)
	if err != nil || len(after.Records) != 2 {
		t.Fatal("old source remained active", err)
	}
	request("POST", "/v1/feed-archives/rotate", map[string]string{"receiver_id": f.owner}, 200)
	request("POST", "/v1/feed-archives/rotate?", map[string]string{"receiver_id": f.owner}, 422)
	request("POST", "/v1/feed-archives/rotate", map[string]any{"receiver_id": f.owner, "sources": before}, 422)
	request("GET", "/v1/feed-archives/paper-custody-audit?repair=true", nil, 422)
	request("GET", "/v1/feed-archives/paper-custody-audit", map[string]any{}, 422)
	private := httptest.NewRecorder()
	handler.ServeHTTP(private, httptest.NewRequest("POST", "/v1/feed-archives/rotate", bytes.NewReader([]byte(`{}`))))
	if private.Code != 401 {
		t.Fatal("retirement bypassed authentication", private.Code)
	}
	var selected storage.PaperDecisionRecord
	if err := json.Unmarshal(request("GET", "/v1/paper-decisions/"+command.ID, nil, 200), &selected); err != nil || !reflect.DeepEqual(selected, decision.Record) {
		t.Fatal("retirement changed old finalized decision", err)
	}
	// The forward quote arrives after the original lease expires. The owner
	// takes a new generation; original retired source fences remain historical.
	f.at = decision.Record.FinalizedAt.Add(time.Duration(f.definition.Safety.MaxHoldingSeconds)*time.Second + time.Second)
	lease, err := f.store.FeedCollectorCommand(ctx, f.owner, feedcollector.Command{RequestID: "resume", Operation: "claim", RegistrationID: f.reg.ID, OwnerID: f.fence.OwnerID}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.fence.Generation = lease.Entry.Lease.Generation
	f.publish(f.recording("forward-calendar", "market_calendar", "regular_session", f.calendarRaw, f.date))
	f.quote("forward", 10100)
	current, err := f.store.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	full := before
	full.Collectors.Entries, full.Collectors.LastClock = current.Collectors.Entries, current.Collectors.LastClock
	for key, value := range current.Records {
		full.Records[key] = value
	}
	for key, value := range current.Collectors.Admissions {
		full.Collectors.Admissions[key] = value
		full.Routing.Inputs[key] = current.Routing.Inputs[key]
	}
	want, err := evaluateOwnedPaperOutcome(storage.PaperOutcomeBasis{Decision: decision.Record, Observation: storage.PaperDecisionBasis{Strategy: f.strategy.Record, Feed: full}}, f.at)
	if err != nil || want.Status != "observed" || want.PriceDeltaCents != 100 {
		t.Fatal("full-history reference did not observe forward quote", want, err)
	}
	var outcome storage.PaperOutcomeReceipt
	outCommand := storage.PaperOutcomeCommand{ID: "rotation-forward", ReceiverID: f.owner, DecisionID: command.ID, DecisionSHA256: decision.Record.SHA256}
	if err := json.Unmarshal(request("POST", "/v1/paper-outcomes", outCommand, 201), &outcome); err != nil || outcome.Record.Validate() != nil || !reflect.DeepEqual(outcome.Record.Evaluation, want) {
		t.Fatal("retired evidence changed native forward observation", outcome, err)
	}
	var audit storage.PaperFeedAudit
	if err := json.Unmarshal(request("GET", "/v1/feed-archives/paper-custody-audit", nil, 200), &audit); err != nil || audit.Status != "verified" || audit.Decisions != 1 || audit.Outcomes != 1 || audit.AdmissionAvailable || audit.ExecutionAvailable || audit.QualificationCredit {
		t.Fatal("original-source audit missing or granted authority", audit, err)
	}
	var archived storage.PaperRecordArchiveReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-record-archives", map[string]string{"receiver_id": f.owner}, 201), &archived); err != nil || archived.Header.Validate() != nil || archived.Header.Decisions != 1 || archived.Header.Outcomes != 1 {
		t.Fatal("native paper archive custody", archived, err)
	}
	request("POST", "/v1/paper-record-archives", map[string]string{"receiver_id": f.owner}, 200)
	request("POST", "/v1/paper-record-archives?", map[string]string{"receiver_id": f.owner}, 422)
	request("POST", "/v1/paper-record-archives", map[string]any{"receiver_id": f.owner, "records": []any{}}, 422)
	request("GET", "/v1/paper-record-archives/2", nil, 404)
	request("GET", "/v1/paper-record-archives?limit=0", nil, 422)
	request("GET", "/v1/paper-record-archives?after=0&after=1", nil, 422)
	request("GET", "/v1/paper-record-archives/source?kind=decision&kind=review&id="+command.ID, nil, 422)
	request("GET", "/v1/paper-record-archives/audit?repair=true", nil, 422)
	request("GET", "/v1/paper-record-archives/audit", map[string]string{}, 422)
	var selectedArchive storage.PaperRecordArchive
	if err := json.Unmarshal(request("GET", "/v1/paper-record-archives/1", nil, 200), &selectedArchive); err != nil || selectedArchive.Validate() != nil || selectedArchive.SHA256 != archived.Header.SHA256 {
		t.Fatal("native archived observation replay", err)
	}
	var original storage.PaperArchivedRecord
	if err := json.Unmarshal(request("GET", "/v1/paper-record-archives/source?kind=decision&id="+command.ID, nil, 200), &original); err != nil || original.Record.RecordSHA256 != decision.Record.SHA256 || original.Record.CreatedEpoch != 0 {
		t.Fatal("native original paper ID readback", original, err)
	}
	var paperAudit storage.PaperRecordArchiveAudit
	if err := json.Unmarshal(request("GET", "/v1/paper-record-archives/audit", nil, 200), &paperAudit); err != nil || paperAudit.Status != "verified" || paperAudit.Records != 2 || paperAudit.Archives != 1 || paperAudit.AdmissionAvailable || paperAudit.ExecutionAvailable || paperAudit.QualificationCredit {
		t.Fatal("native paper original-source audit", paperAudit, err)
	}
	private = httptest.NewRecorder()
	handler.ServeHTTP(private, httptest.NewRequest("GET", "/v1/paper-record-archives/1", nil))
	if private.Code != 401 {
		t.Fatal("paper archive bypassed authentication", private.Code)
	}
	var epochCustody storage.FeedArchiveReceipt
	if err := json.Unmarshal(request("POST", "/v1/feed-archives", map[string]string{"receiver_id": f.owner}, 201), &epochCustody); err != nil || epochCustody.PaperCustody == nil || epochCustody.PaperCustody.Header.PaperArchiveSequence != 1 || epochCustody.PaperCustody.Header.Schema != "mantik-paper-feed-checkpoint-v2" {
		t.Fatal("native epoch-bound source custody", epochCustody, err)
	}
	request("GET", "/v1/feed-archives/paper-custody/2", nil, 200)
}
