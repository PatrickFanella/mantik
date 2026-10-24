package service

import (
	"context"
	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Manufactured inspector sources only. Actual owner custody and historical
// replay are verified by the shared Memory/Postgres and real HTTP contracts.
func outcomeMarketFixture(t *testing.T) storage.PaperOutcomeSources {
	t.Helper()
	d := marketInspectorFixture(t)
	b := storage.PaperOutcomeBasis{Decision: d.Decision, Observation: d.Basis}
	at := d.Decision.FinalizedAt
	due := at.Add(10 * time.Second)
	add := func(id string, occurred, received, published time.Time, bid int64) string {
		raw := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":%d,"as":5,"bs":7}}}`, occurred.Format(time.RFC3339Nano), bid)
		rec := alpaca.Recording{Currency: "USD", Schema: alpaca.RecordingSchema, CaptureID: id, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: received, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "journal:journal-one:capture-" + id + "-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
		n, e := alpaca.Normalize(rec)
		if e != nil {
			t.Fatal(e)
		}
		input := n.Inputs[0]
		fence := feedcollector.Fence{RegistrationID: "spy", OwnerID: "owner", Generation: 1}
		b.Observation.Feed.Collectors.Admissions[input.ID] = feedcollector.Admission{Fence: fence, InputSHA256: feedcollector.InputDigest(input), At: received}
		b.Observation.Feed.Routing.Inputs[input.ID] = domain.RetainedRoutingInput{Input: input}
		payload, _ := json.Marshal(rec)
		key := id + "-r1"
		b.Observation.Feed.Records["spy:"+key] = storage.FeedEvidenceRecord{RegistrationID: "spy", Key: key, Fence: fence, PublishedAt: published, Recording: payload}
		return input.ID
	}
	add("during", at.Add(2*time.Second), at.Add(2*time.Second), at.Add(2*time.Second), 99)
	add("same-occurrence", at.Add(2*time.Second), at.Add(3*time.Second), at.Add(3*time.Second), 98)
	add("delayed", due.Add(-time.Second), due.Add(time.Second), due.Add(2*time.Second), 99)
	selected := add("forward", due.Add(time.Second), due.Add(3*time.Second), due.Add(3*time.Second), 99)
	add("crossed", at.Add(4*time.Second), at.Add(4*time.Second), at.Add(4*time.Second), 101)
	raw := b.Observation.Feed.Records["spy:forward-r1"]
	var rec alpaca.Recording
	if e := json.Unmarshal(raw.Recording, &rec); e != nil {
		t.Fatal(e)
	}
	r := storage.PaperOutcomeRecord{Schema: "mantik-owned-paper-outcome-v1", Command: storage.PaperOutcomeCommand{ID: "outcome", ReceiverID: b.Decision.Command.ReceiverID, DecisionID: b.Decision.Command.ID, DecisionSHA256: b.Decision.SHA256}, EvaluatedAt: due.Add(5 * time.Second), Recipe: storage.PaperOutcomeRecipe, Evaluation: storage.PaperOutcomeEvaluation{Status: "observed", ReasonCode: "manufactured_observation_only", DueAt: due, WindowEnd: due.Add(time.Minute), EntryPriceCents: 10000, ObservedPriceCents: 10000, InputID: selected, CaptureSHA256: rec.SHA256, OccurredAt: due.Add(time.Second), PublishedAt: raw.PublishedAt, AvailableAt: raw.PublishedAt}, BasisSHA256: workerplan.Hash(b), Authority: "service_owned_forward_quote_outcome_only"}
	r.SHA256 = workerplan.Hash(r)
	return storage.PaperOutcomeSources{Outcome: r, Basis: b}
}
func TestOutcomeMarketRetainsTemporalAndDimensionGaps(t *testing.T) {
	s := outcomeMarketFixture(t)
	out, err := inspectPaperOutcomeMarket(context.Background(), s)
	if err != nil || len(out.Rows) != 6 || out.Status != "retained_quote_observations" || out.ContinuousCoverageAvailable || out.ShareQuantitiesAvailable || out.AdmissionAvailable || out.ExecutionAvailable || out.QualificationCredit {
		t.Fatal(out, err)
	}
	byKey := map[string]PaperOutcomeMarketRow{}
	for _, r := range out.Rows {
		byKey[r.CaptureKey] = r
		if r.Quote != nil && (r.Quote.ShareQuantitiesAvailable || r.Quote.SizeUnit != "provider_native_unverified") {
			t.Fatal("inferred share units", r)
		}
	}
	if byKey["spy:quote-r1"].TemporalStatus != "before_finalization" || byKey["spy:during-r1"].TemporalStatus != "within_holding_window" || byKey["spy:delayed-r1"].TemporalStatus != "published_or_admitted_after_horizon" || byKey["spy:forward-r1"].TemporalStatus != "after_horizon" || !byKey["spy:forward-r1"].SelectedForwardQuote {
		t.Fatal("temporal statuses", byKey)
	}
	if byKey["spy:during-r1"].OccurrenceSHA256 != byKey["spy:same-occurrence-r1"].OccurrenceSHA256 || byKey["spy:during-r1"].InputID == byKey["spy:same-occurrence-r1"].InputID {
		t.Fatal("recapture inflated occurrence identity", byKey)
	}
	if byKey["spy:crossed-r1"].Quote != nil || byKey["spy:crossed-r1"].DimensionStatus != "quote_dimensions_unavailable" {
		t.Fatal("quarantine lost", byKey)
	}
	again, e := inspectPaperOutcomeMarket(context.Background(), s)
	if e != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("map order changed readback", again, e)
	}
	// Required calendar/session evidence can arrive after the quote itself.
	delayed := outcomeMarketFixture(t)
	delayed.Outcome.Evaluation.AvailableAt = delayed.Outcome.Evaluation.AvailableAt.Add(time.Second)
	delayed.Outcome.SHA256 = ""
	delayed.Outcome.SHA256 = workerplan.Hash(delayed.Outcome)
	bundle, e := inspectPaperOutcomeMarket(context.Background(), delayed)
	if e != nil || !bundle.SelectedObservationAvailableAt.Equal(delayed.Outcome.Evaluation.AvailableAt) {
		t.Fatal("quote availability replaced outcome evidence availability", bundle, e)
	}
	saved := out.SHA256
	out.SHA256 = ""
	if workerplan.Hash(out) != saved {
		t.Fatal("output hash")
	}
}
func TestOutcomeMarketRejectsRehashedSourceChanges(t *testing.T) {
	for name, change := range map[string]func(*storage.PaperOutcomeSources){
		"future_publication": func(s *storage.PaperOutcomeSources) {
			r := s.Basis.Observation.Feed.Records["spy:during-r1"]
			r.PublishedAt = s.Outcome.EvaluatedAt.Add(time.Nanosecond)
			s.Basis.Observation.Feed.Records["spy:during-r1"] = r
		},
		"wrong_source_key": func(s *storage.PaperOutcomeSources) {
			r := s.Basis.Observation.Feed.Records["spy:during-r1"]
			delete(s.Basis.Observation.Feed.Records, "spy:during-r1")
			s.Basis.Observation.Feed.Records["spy:wrong"] = r
		},
		"missing_selected_capture": func(s *storage.PaperOutcomeSources) { delete(s.Basis.Observation.Feed.Records, "spy:forward-r1") },
		"changed_selected_price":   func(s *storage.PaperOutcomeSources) { s.Outcome.Evaluation.ObservedPriceCents++ },
		"future_admission": func(s *storage.PaperOutcomeSources) {
			id := s.Outcome.Evaluation.InputID
			r := s.Basis.Observation.Feed.Collectors.Admissions[id]
			r.At = s.Outcome.EvaluatedAt.Add(time.Nanosecond)
			s.Basis.Observation.Feed.Collectors.Admissions[id] = r
		},
		"changed_bid": func(s *storage.PaperOutcomeSources) {
			p := s.Basis.Observation.Feed.Records["spy:forward-r1"]
			var r alpaca.Recording
			_ = json.Unmarshal(p.Recording, &r)
			r.Original = strings.Replace(r.Original, `"bp":99`, `"bp":98`, 1)
			r.SHA256 = alpaca.Digest(r.Original)
			p.Recording, _ = json.Marshal(r)
			s.Basis.Observation.Feed.Records["spy:forward-r1"] = p
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := outcomeMarketFixture(t)
			change(&s)
			s.Outcome.BasisSHA256 = workerplan.Hash(s.Basis)
			s.Outcome.SHA256 = ""
			s.Outcome.SHA256 = workerplan.Hash(s.Outcome)
			got, e := inspectPaperOutcomeMarket(context.Background(), s)
			if e == nil || !reflect.DeepEqual(got, PaperOutcomeMarketEvidence{}) {
				t.Fatal("rehashed source accepted", got, e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, e := inspectPaperOutcomeMarket(ctx, outcomeMarketFixture(t)); e == nil || !reflect.DeepEqual(got, PaperOutcomeMarketEvidence{}) {
		t.Fatal("cancelled inspector", got, e)
	}
}

type outcomeMarketBodyStore struct {
	*storage.Memory
	called bool
}

func (s *outcomeMarketBodyStore) PaperOutcomeSources(context.Context, string, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperOutcomeSources, error) {
	s.called = true
	return storage.PaperOutcomeSources{}, storage.ErrPaperOutcomeInvalid
}
func TestOutcomeMarketRejectsCallerEvidence(t *testing.T) {
	s := &outcomeMarketBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperOutcomeMarketRoutes(mux, s)
	for _, path := range []string{"/v1/paper-outcomes/outcome/market-evidence", "/v1/paper-outcomes/outcome/market-evidence?at=future", "/v1/paper-outcomes/invalid.id/market-evidence"} {
		r := httptest.NewRequest("GET", path, strings.NewReader(`{"shares":100000}`))
		r.ProtoMajor = 2
		r.ContentLength = -1
		r.TransferEncoding = nil
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 422 || s.called {
			t.Fatal(w.Code, s.called)
		}
	}
}
