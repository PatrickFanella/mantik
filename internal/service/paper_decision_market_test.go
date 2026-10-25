package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Manufactured inspector inputs only. Actual custody is exercised through the
// Memory/PostgreSQL source-store and authenticated HTTP contracts.
func marketInspectorFixture(t *testing.T) storage.PaperDecisionSources {
	t.Helper()
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	raw := `{"quotes":{"SPY":{"t":"2026-10-21T13:59:59Z","ap":100,"bp":99,"as":5,"bs":7}}}`
	r := alpaca.Recording{Currency: "USD", Schema: alpaca.RecordingSchema, CaptureID: "quote", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: at, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "journal:journal-one:capture-quote-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	normalized, err := alpaca.Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	i := normalized.Inputs[0]
	fence := feedcollector.Fence{RegistrationID: "spy", OwnerID: "owner", Generation: 1}
	state := feedcollector.InitialState()
	state.Entries["spy"] = feedcollector.Entry{Registration: feedcollector.Registration{ID: "spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: r.EvidenceClass, JournalID: "journal-one", AccessRef: r.AccessRef, LeaseSeconds: 300}}
	state.Admissions[i.ID] = feedcollector.Admission{Fence: fence, InputSHA256: feedcollector.InputDigest(i), At: at}
	recording, _ := json.Marshal(r)
	b := storage.PaperDecisionBasis{Feed: storage.FeedEvidenceSnapshot{ReceiverID: "receiver-abcdefghijklmnop", Collectors: state, Routing: &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{i.ID: {Input: i}}}, Records: map[string]storage.FeedEvidenceRecord{"spy:quote-r1": {RegistrationID: "spy", Key: "quote-r1", Fence: fence, PublishedAt: at, Recording: recording}}}}
	d := storage.PaperDecisionRecord{Schema: "mantik-owned-paper-decision-v1", Command: storage.PaperDecisionCommand{ID: "decision", ReceiverID: b.Feed.ReceiverID, StrategyID: "strategy", StrategySHA256: strings.Repeat("a", 64), RegistrationID: "spy"}, FinalizedAt: at, Status: "finalized", Evaluation: storage.PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "manufactured_inspector_input", PriceCents: 10000, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: i.ID}}}, Authority: "service_finalized_research_proposal_only"}
	d.BasisSHA256 = workerplan.Hash(b)
	d.SHA256 = workerplan.Hash(d)
	return storage.PaperDecisionSources{Decision: d, Basis: b}
}
func TestDecisionMarketInspectorRejectsInconsistentDimensions(t *testing.T) {
	s := marketInspectorFixture(t)
	got, err := inspectPaperDecisionMarket(context.Background(), s)
	if err != nil || got.Quote == nil || got.Quote.ProviderBidSize != 7 || got.Quote.ShareQuantitiesAvailable || got.ExecutionAvailable {
		t.Fatal(got, err)
	}
	for name, change := range map[string]func(*storage.PaperDecisionSources){
		"future_publication": func(s *storage.PaperDecisionSources) {
			r := s.Basis.Feed.Records["spy:quote-r1"]
			r.PublishedAt = r.PublishedAt.Add(time.Second)
			s.Basis.Feed.Records["spy:quote-r1"] = r
		},
		"wrong_record_key": func(s *storage.PaperDecisionSources) {
			r := s.Basis.Feed.Records["spy:quote-r1"]
			delete(s.Basis.Feed.Records, "spy:quote-r1")
			s.Basis.Feed.Records["spy:other"] = r
		},
		"missing_raw_source": func(s *storage.PaperDecisionSources) { s.Basis.Feed.Records = map[string]storage.FeedEvidenceRecord{} },
		"foreign_provider": func(s *storage.PaperDecisionSources) {
			e := s.Basis.Feed.Collectors.Entries["spy"]
			e.Registration.Provider = "other"
			s.Basis.Feed.Collectors.Entries["spy"] = e
		},
		"future_admission": func(s *storage.PaperDecisionSources) {
			id := s.Decision.Evaluation.Coverage.Quote.InputID
			a := s.Basis.Feed.Collectors.Admissions[id]
			a.At = a.At.Add(time.Second)
			s.Basis.Feed.Collectors.Admissions[id] = a
		},
		"changed_bid_bytes": func(s *storage.PaperDecisionSources) {
			v := s.Basis.Feed.Records["spy:quote-r1"]
			var r alpaca.Recording
			_ = json.Unmarshal(v.Recording, &r)
			r.Original = strings.Replace(r.Original, `"bp":99`, `"bp":98`, 1)
			r.SHA256 = alpaca.Digest(r.Original)
			v.Recording, _ = json.Marshal(r)
			s.Basis.Feed.Records["spy:quote-r1"] = v
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := marketInspectorFixture(t)
			change(&s)
			s.Decision.BasisSHA256 = workerplan.Hash(s.Basis)
			s.Decision.SHA256 = ""
			s.Decision.SHA256 = workerplan.Hash(s.Decision)
			if _, err := inspectPaperDecisionMarket(context.Background(), s); err == nil {
				t.Fatal("inconsistent dimensions accepted")
			}
		})
	}
}

type marketBodyStore struct {
	*storage.Memory
	called bool
}

func (s *marketBodyStore) PaperDecisionSources(context.Context, string, storage.PaperDecisionEvaluator) (storage.PaperDecisionSources, error) {
	s.called = true
	return storage.PaperDecisionSources{}, storage.ErrPaperDecisionInvalid
}
func TestDecisionMarketRejectsUnknownLengthBody(t *testing.T) {
	s := &marketBodyStore{Memory: storage.NewMemory()}
	mux := http.NewServeMux()
	paperDecisionMarketRoutes(mux, s)
	r := httptest.NewRequest("GET", "/v1/paper-decisions/decision/market-evidence", strings.NewReader(`{"shares":100000}`))
	r.ProtoMajor = 2
	r.ContentLength = -1
	r.TransferEncoding = nil
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 422 || s.called {
		t.Fatal(w.Code, s.called)
	}
}
