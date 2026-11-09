package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestValidatedFeedCoveragePublicationAndClock(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	clock := &fakeClock{at: time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)}
	handler := HandlerWithClock(store, "evidence-api-token", clock)
	request := func(method, path string, body any, expected int) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer evidence-api-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	initial, _ := store.FeedCollectors(ctx)
	receiver := initial.ReceiverID
	registration := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-test", AccessRef: "manufactured-access", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: registration.ID, Registration: &registration}, {RequestID: "claim", Operation: "claim", RegistrationID: registration.ID, OwnerID: "owner"}} {
		if _, err := store.FeedCollectorCommand(ctx, receiver, c, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: registration.ID, OwnerID: "owner", Generation: 1}
	quoteRaw := `{"quotes":{"SPY":{"t":"2026-10-15T13:59:59Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`
	quote := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "quote", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.Now(), EvidenceClass: registration.EvidenceClass, AccessRef: registration.AccessRef, RawRef: "journal:journal-test:capture-quote-r1.json", Original: quoteRaw, SHA256: alpaca.Digest(quoteRaw), MaxQuoteAgeSeconds: 60}
	calendar := quote
	calendar.CaptureID = "calendar"
	calendar.Capability = "market_calendar"
	calendar.Feed = "regular_session"
	calendar.Start = "2026-10-15"
	calendar.End = "2026-10-15"
	calendar.RawRef = "journal:journal-test:capture-calendar-r1.json"
	calendar.Original = `[{"date":"2026-10-15","open":"09:30","close":"16:00"}]`
	calendar.SHA256 = alpaca.Digest(calendar.Original)
	publish := func(r alpaca.Recording, expected int) {
		t.Helper()
		request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": receiver, "fence": fence, "recording": r}, expected)
	}
	// Source-custody capabilities must not inherit quote/calendar publication
	// merely because they normalize successfully with no routing inputs.
	corporate := quote
	corporate.CaptureID, corporate.Capability, corporate.Feed = "corporate", "corporate_actions", alpaca.CorporateActionFeed
	corporate.Start, corporate.End = "2026-10-15", "2026-10-15"
	corporate.RawRef = "journal:journal-test:capture-corporate-r1.json"
	corporate.Original = `{"corporate_actions":{},"next_page_token":null}`
	corporate.SHA256 = alpaca.Digest(corporate.Original)
	if normalized, err := alpaca.Normalize(corporate); err != nil || normalized.CorporateActions == nil {
		t.Fatal("manufactured source custody did not normalize", err)
	}
	beforeCorporate, _ := store.FeedEvidence(ctx)
	publish(corporate, 422)
	afterCorporate, _ := store.FeedEvidence(ctx)
	beforeRaw, _ := json.Marshal(beforeCorporate)
	afterRaw, _ := json.Marshal(afterCorporate)
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("unsupported source publication changed service ownership")
	}
	publish(quote, 409) // raw validation alone cannot invent admitted history.
	for _, r := range []alpaca.Recording{quote, calendar} {
		normalized, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range normalized.Inputs {
			if _, err := store.SubmitCollectorResearch(ctx, receiver, fence, i, clock.Now); err != nil {
				t.Fatal(err)
			}
		}
		publish(r, 200)
	}
	w := request("GET", "/v1/feed-collectors/spy-iex/coverage", nil, 200)
	var report serverFeedCoverage
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Evidence.Session.Status != "open" || !report.Evidence.Quote.RegularSessionEligible || !report.OperationalEvidenceSatisfied || report.GlobalReadinessConfigured || report.CollectorLivenessVerified || report.QualificationCredit {
		t.Fatal(w.Body.String())
	}
	var archived storage.FeedArchiveReceipt
	created := request("POST", "/v1/feed-archives", map[string]any{"receiver_id": receiver}, 201)
	if err := json.Unmarshal(created.Body.Bytes(), &archived); err != nil || archived.Header.Validate() != nil || archived.Header.Publications != 2 || archived.Header.Admissions < 2 || archived.Header.Sequence != 1 || archived.Duplicate {
		t.Fatal("feed archive did not bind source owners", created.Body.String(), err)
	}
	request("POST", "/v1/feed-archives", map[string]any{"receiver_id": receiver}, 200)
	for _, key := range []struct{ kind, id string }{{"publication", registration.ID + ":quote-r1"}, {"command", "register"}, {"input", report.Evidence.Quote.InputID}, {"admission", report.Evidence.Quote.InputID}} {
		var original storage.FeedArchivedSource
		response := request("GET", "/v1/feed-archives/source?kind="+key.kind+"&id="+key.id, nil, 200)
		if err := json.Unmarshal(response.Body.Bytes(), &original); err != nil || original.Validate() != nil || original.Kind != key.kind || original.ID != key.id || original.Archive != archived.Header {
			t.Fatal("archive identity lookup changed source", key, err)
		}
	}
	request("GET", "/v1/feed-archives/source-audit", nil, 200)
	request("GET", "/v1/feed-archives/source?kind=command&id=absent", nil, 404)
	request("GET", "/v1/feed-archives/source?kind=command&id=register&id=other", nil, 422)
	request("GET", "/v1/feed-archives/source?kind=unsupported&id=register", nil, 422)
	request("GET", "/v1/feed-archives/source-audit?repair=true", nil, 422)
	request("POST", "/v1/feed-archives?prune=true", map[string]any{"receiver_id": receiver}, 422)
	request("POST", "/v1/feed-archives", map[string]any{"receiver_id": receiver, "source": "caller"}, 422)
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest("GET", "/v1/feed-archives/1", nil))
	if unauthenticated.Code != 401 {
		t.Fatal("private archive bypassed authentication", unauthenticated.Code)
	}
	request("GET", "/v1/feed-archives/0", nil, 422)
	request("GET", "/v1/feed-archives/2", nil, 404)
	request("GET", "/v1/feed-archives/1?at=caller", nil, 422)
	request("GET", "/v1/feed-archives?limit=0", nil, 422)
	request("GET", "/v1/feed-archives?limit=1&limit=2", nil, 422)
	request("GET", "/v1/feed-archives?after=-1", nil, 422)
	request("GET", "/v1/feed-archives?after=%zz", nil, 422)
	request("GET", "/readyz?scope=feed_coverage", nil, 503)
	request("GET", "/v1/feed-collectors/spy-iex/coverage?at=2026-01-01", nil, 400)
	changed := quote
	changed.MaxQuoteAgeSeconds = 30
	publish(changed, 409)
	bad := quote
	bad.SHA256 = "wrong"
	publish(bad, 422)
	clock.mu.Lock()
	clock.at = clock.at.Add(time.Nanosecond)
	clock.mu.Unlock()
	if _, err := store.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "stop", Operation: "stop", RegistrationID: registration.ID, OwnerID: fence.OwnerID, Generation: 1}, clock.Now); err != nil {
		t.Fatal(err)
	}
	publish(quote, 200) // exact original publication retry is readback only.
	// An old archive is replayed at its own cut, after the live lease stops.
	// No current clock or live source overwrite can rewrite this readback.
	var historical storage.FeedArchive
	archivedRead := request("GET", "/v1/feed-archives/1", nil, 200)
	if err := json.Unmarshal(archivedRead.Body.Bytes(), &historical); err != nil || historical.Validate() != nil || historical.FeedArchiveHeader != archived.Header || historical.Source.Collectors.Entries[registration.ID].Lease.Stopped || len(historical.Source.Records) != 2 {
		t.Fatal("archived source cut changed with current lease", err)
	}
	w = request("GET", "/v1/feed-collectors/spy-iex/coverage", nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if report.OperationalEvidenceSatisfied || report.LeaseStatus != "stopped" || !report.Evidence.RequiredEvidenceSatisfied {
		t.Fatal(w.Body.String())
	}
	clock.mu.Lock()
	clock.at = clock.at.Add(61 * time.Second)
	clock.mu.Unlock()
	w = request("GET", "/v1/feed-collectors/spy-iex/coverage", nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if report.Evidence.Quote.Usable || report.Evidence.RequiredEvidenceSatisfied {
		t.Fatal("stale price revived", w.Body.String())
	}
	state, _ := store.Snapshot(ctx)
	if state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 {
		t.Fatal("publication changed economics")
	}
	// A newer admitted but unpublished quote must suppress the older published
	// quote, including when all capture bytes and timestamps remain valid.
	if _, err := store.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "takeover", Operation: "claim", RegistrationID: registration.ID, OwnerID: "replacement"}, clock.Now); err != nil {
		t.Fatal(err)
	}
	current := feedcollector.Fence{RegistrationID: registration.ID, OwnerID: "replacement", Generation: 2}
	fresh := quote
	fresh.CaptureID = "fresh-again"
	fresh.ReceivedAt = clock.Now()
	fresh.RawRef = "journal:journal-test:capture-fresh-again-r1.json"
	fresh.Original = `{"quotes":{"SPY":{"t":"2026-10-15T14:01:00Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`
	fresh.SHA256 = alpaca.Digest(fresh.Original)
	freshNormalized, err := alpaca.Normalize(fresh)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range freshNormalized.Inputs {
		if _, err := store.SubmitCollectorResearch(ctx, receiver, current, i, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": receiver, "fence": current, "recording": fresh}, 200)
	w = request("GET", "/v1/feed-collectors/spy-iex/coverage", nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if !report.Evidence.Quote.Usable || !report.OperationalEvidenceSatisfied {
		t.Fatal("fresh current-generation baseline unavailable", w.Body.String())
	}
	clock.mu.Lock()
	clock.at = clock.at.Add(time.Second)
	clock.mu.Unlock()
	newer := quote
	newer.CaptureID = "unpublished"
	newer.ReceivedAt = clock.Now()
	newer.RawRef = "journal:journal-test:capture-unpublished-r1.json"
	newer.Original = `{"quotes":{"SPY":{"t":"2026-10-15T14:01:00Z","ap":0,"bp":0,"as":0,"bs":0}}}`
	newer.SHA256 = alpaca.Digest(newer.Original)
	normalized, err := alpaca.Normalize(newer)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range normalized.Inputs {
		if _, err := store.SubmitCollectorResearch(ctx, receiver, current, i, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	w = request("GET", "/v1/feed-collectors/spy-iex/coverage", nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if report.Evidence.Quote.Code != "quote_publication_pending" || report.Evidence.Quote.Usable || report.OperationalEvidenceSatisfied {
		t.Fatal("unpublished newer evidence fell back to older quote", w.Body.String())
	}
}
