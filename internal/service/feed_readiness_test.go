package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestFeedPolicyStrictConfiguration(t *testing.T) {
	store := storage.NewMemory()
	s, _ := store.FeedCollectors(context.Background())
	valid := fmt.Sprintf(`{"schema":"mantik-feed-readiness-policy-v1","receiver_id":%q,"required_registrations":["spy-iex"]}`, s.ReceiverID)
	for _, raw := range []string{"null", "{}", valid + "{}", strings.Replace(valid, "policy-v1", "policy-v2", 1), strings.Replace(valid, "[\"spy-iex\"]", "[]", 1), strings.Replace(valid, "[\"spy-iex\"]", "[\"spy-iex\",\"spy-iex\"]", 1), strings.Replace(valid, "\"schema\"", "\"unknown\"", 1), strings.Repeat(" ", 16385) + valid} {
		if _, err := ReadFeedReadinessPolicy(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid policy accepted: %.100s", raw)
		}
	}
	if _, err := ReadFeedReadinessPolicy(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
}

type feedPolicyFixture struct {
	store    *storage.Memory
	clock    *fakeClock
	policy   *FeedReadinessPolicy
	fence    feedcollector.Fence
	quote    alpaca.Recording
	calendar alpaca.Recording
}

func newFeedPolicyFixture(t *testing.T) feedPolicyFixture {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemory()
	clock := newFakeClock(time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC))
	snapshot, _ := store.FeedCollectors(ctx)
	reg := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "policy-test-journal", AccessRef: "manufactured-access", LeaseSeconds: 300}
	for _, command := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "owner"}} {
		if _, err := store.FeedCollectorCommand(ctx, snapshot.ReceiverID, command, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	quote := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "quote", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.Now(), EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, RawRef: "journal:policy-test-journal:capture-quote-r1.json", Original: `{"quotes":{"SPY":{"t":"2026-10-15T13:59:59Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`, MaxQuoteAgeSeconds: 60}
	quote.SHA256 = alpaca.Digest(quote.Original)
	calendar := quote
	calendar.CaptureID = "calendar"
	calendar.Capability = "market_calendar"
	calendar.Feed = "regular_session"
	calendar.Start = "2026-10-15"
	calendar.End = "2026-10-15"
	calendar.RawRef = "journal:policy-test-journal:capture-calendar-r1.json"
	calendar.Original = `[{"date":"2026-10-15","open":"09:30","close":"16:00"}]`
	calendar.SHA256 = alpaca.Digest(calendar.Original)
	f := feedPolicyFixture{store, clock, &FeedReadinessPolicy{Schema: "mantik-feed-readiness-policy-v1", ReceiverID: snapshot.ReceiverID, RequiredRegistrations: []string{reg.ID}}, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "owner", Generation: 1}, quote, calendar}
	for _, record := range []alpaca.Recording{quote, calendar} {
		f.retain(t, record, true)
	}
	return f
}
func (f feedPolicyFixture) retain(t *testing.T, record alpaca.Recording, publish bool) {
	t.Helper()
	ctx := context.Background()
	normalized, err := alpaca.Normalize(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range normalized.Inputs {
		if _, err := f.store.SubmitCollectorResearch(ctx, f.policy.ReceiverID, f.fence, input, f.clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	if publish {
		raw, _ := json.Marshal(record)
		if _, err := f.store.PublishFeedEvidence(ctx, f.policy.ReceiverID, f.fence.RegistrationID, recordingKey(record), f.fence, raw, f.clock.Now, publicationCheck(f.fence.RegistrationID, f.fence, record)); err != nil {
			t.Fatal(err)
		}
	}
}
func (f feedPolicyFixture) control(t *testing.T, op, id, owner string, generation uint64) {
	t.Helper()
	if _, err := f.store.FeedCollectorCommand(context.Background(), f.policy.ReceiverID, feedcollector.Command{RequestID: id, Operation: op, RegistrationID: f.fence.RegistrationID, OwnerID: owner, Generation: generation}, f.clock.Now); err != nil {
		t.Fatal(err)
	}
}
func (f feedPolicyFixture) advance(d time.Duration) {
	f.clock.mu.Lock()
	f.clock.at = f.clock.at.Add(d)
	f.clock.mu.Unlock()
}

func TestConfiguredFeedReadinessFailureBoundaries(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*testing.T, *feedPolicyFixture)
		code   string
	}{
		{"fresh", func(*testing.T, *feedPolicyFixture) {}, "validated_feed_evidence_current"},
		{"closed_session", func(t *testing.T, f *feedPolicyFixture) {
			f.advance(7 * time.Hour)
			f.control(t, "claim", "closed-session-owner", "closed-owner", 0)
		}, "validated_feed_evidence_current"},
		{"fresh_replacement", func(t *testing.T, f *feedPolicyFixture) {
			f.control(t, "stop", "stop", "owner", 1)
			f.control(t, "claim", "replacement", "replacement", 0)
			f.fence.OwnerID = "replacement"
			f.fence.Generation = 2
			f.advance(time.Second)
			r := f.quote
			r.CaptureID = "replacement-quote"
			r.ReceivedAt = f.clock.Now()
			r.RawRef = "journal:policy-test-journal:capture-replacement-quote-r1.json"
			f.retain(t, r, true)
		}, "validated_feed_evidence_current"},
		{"stale_quote", func(t *testing.T, f *feedPolicyFixture) { f.advance(61 * time.Second) }, "stale_quote"},
		{"expired", func(t *testing.T, f *feedPolicyFixture) { f.advance(300 * time.Second) }, "expired"},
		{"stopped", func(t *testing.T, f *feedPolicyFixture) { f.control(t, "stop", "stop", "owner", 1) }, "stopped"},
		{"replacement_generation", func(t *testing.T, f *feedPolicyFixture) {
			f.control(t, "stop", "stop", "owner", 1)
			f.control(t, "claim", "replacement", "replacement", 0)
		}, "quote_admission_previous_generation"},
		{"missing_required", func(t *testing.T, f *feedPolicyFixture) {
			f.policy.RequiredRegistrations = append(f.policy.RequiredRegistrations, "another-required")
		}, "feed_registration_missing"},
		{"wrong_receiver", func(t *testing.T, f *feedPolicyFixture) {
			s, _ := storage.NewMemory().FeedCollectors(context.Background())
			f.policy.ReceiverID = s.ReceiverID
		}, "feed_receiver_mismatch"},
		{"clock_backwards", func(t *testing.T, f *feedPolicyFixture) { f.advance(-time.Second) }, "clock_uncertain"},
		{"unpublished_newer_quote", func(t *testing.T, f *feedPolicyFixture) {
			f.advance(time.Second)
			r := f.quote
			r.CaptureID = "newer"
			r.ReceivedAt = f.clock.Now()
			r.RawRef = "journal:policy-test-journal:capture-newer-r1.json"
			f.retain(t, r, false)
		}, "quote_publication_pending"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFeedPolicyFixture(t)
			scenario.change(t, &f)
			handler, err := HandlerWithFeedReadiness(f.store, "policy-token", f.clock, f.policy)
			if err != nil {
				t.Fatal(err)
			}
			// Configuration is copied and cannot be changed by a caller after construction.
			f.policy.RequiredRegistrations[0] = "changed-after-construction"
			r := httptest.NewRequest("GET", "/readyz?scope=feed_coverage", nil)
			r.Header.Set("Authorization", "Bearer policy-token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var report domain.ReadinessReport
			if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			cap := report.Capabilities["feed_coverage"]
			ready := scenario.name == "fresh" || scenario.name == "closed_session" || scenario.name == "fresh_replacement"
			expected := 503
			if ready {
				expected = 200
			}
			if w.Code != expected || report.Ready != ready || cap.Ready != ready || cap.Code != scenario.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if report.Mode != "paper_fixture" || report.Scope != "feed_coverage" {
				t.Fatal(report)
			}
			r = httptest.NewRequest("GET", "/v1/feed-readiness-policy", nil)
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatal("policy inspection bypassed authentication")
			}
			for _, path := range []string{"/v1/readiness/feed_coverage", "/v1/operations", "/v1/feed-readiness-policy"} {
				r = httptest.NewRequest("GET", path, nil)
				r.Header.Set("Authorization", "Bearer policy-token")
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(scenario.code)) && path != "/v1/feed-readiness-policy" {
					t.Fatal(path, w.Body.String())
				}
			}
		})
	}
}

type policyEvidenceOutage struct{ *storage.Memory }

func (s policyEvidenceOutage) FeedEvidence(context.Context) (storage.FeedEvidenceSnapshot, error) {
	return storage.FeedEvidenceSnapshot{}, errors.New("private backend failure")
}
func TestFeedPolicyEvidenceOutageIsScoped(t *testing.T) {
	f := newFeedPolicyFixture(t)
	handler, err := HandlerWithFeedReadiness(policyEvidenceOutage{f.store}, "", f.clock, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/readyz?scope=feed_coverage", 503}, {"/readyz", 200}, {"/healthz", 200}, {"/readyz?scope=process", 200}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, test.path, nil))
		if w.Code != test.status || strings.Contains(w.Body.String(), "private backend failure") {
			t.Fatal(test.path, w.Body.String())
		}
	}
}

func TestFeedPolicyNeverGatesFixtureOperation(t *testing.T) {
	f := newFeedPolicyFixture(t)
	for _, capability := range []string{"action_delivery", "protective_monitoring"} {
		if err := f.store.ReportWorker(context.Background(), domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: capability, WorkerID: "test-" + capability, At: f.clock.Now(), LastSuccessAt: f.clock.Now(), IntervalMillis: 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	f.control(t, "stop", "stop", "owner", 1)
	before, _ := f.store.Snapshot(context.Background())
	handler, err := HandlerWithFeedReadiness(f.store, "", f.clock, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz?scope=fixture_operation", "/v1/feed-collectors/spy-iex/coverage"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if path == "/readyz?scope=fixture_operation" {
			var report domain.ReadinessReport
			_ = json.Unmarshal(w.Body.Bytes(), &report)
			if !report.Ready || report.Capabilities["feed_coverage"].Ready {
				t.Fatal(report)
			}
		} else {
			var report serverFeedCoverage
			_ = json.Unmarshal(w.Body.Bytes(), &report)
			if !report.GlobalReadinessConfigured || !report.RequiredByReadinessPolicy || report.CollectorLivenessVerified || report.QualificationCredit {
				t.Fatal(report)
			}
		}
	}
	after, _ := f.store.Snapshot(context.Background())
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		t.Fatal("readiness inspection mutated execution state")
	}
}

type policyCorruptEvidence struct{ *storage.Memory }

func (s policyCorruptEvidence) FeedEvidence(ctx context.Context) (storage.FeedEvidenceSnapshot, error) {
	snapshot, err := s.Memory.FeedEvidence(ctx)
	for key, record := range snapshot.Records {
		record.Recording = []byte(`{}`)
		snapshot.Records[key] = record
		break
	}
	return snapshot, err
}
func TestFeedPolicyRejectsCorruptedRetainedHistory(t *testing.T) {
	f := newFeedPolicyFixture(t)
	handler, err := HandlerWithFeedReadiness(policyCorruptEvidence{f.store}, "", f.clock, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/readyz?scope=feed_coverage", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "feed_evidence_invalid") {
		t.Fatal(w.Code, w.Body.String())
	}
}
