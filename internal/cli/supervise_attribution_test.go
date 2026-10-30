package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Envelope fixtures exercise UI binding; actual service owner cases live in the
// shared Memory/PostgreSQL HTTP lifecycle contract. These establish no grant.
func attributionViewFixture(d storage.PaperDecisionRecord) storage.PaperAttributionRow {
	records := []storage.OwnedAdmissionReview{}
	source := storage.OwnedAdmissionReviewCatalog{ReceiverID: d.Command.ReceiverID, Records: records, CurrentIDs: []string{}}
	row := storage.PaperAttributionRow{Decision: d, Method: "native-paper-threshold-v1", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", DefinitionSHA256: strings.Repeat("c", 64), ForecastStatus: "native_predicate_not_calibrated_forecast", ObservationStatus: "not_assessed", Observations: []storage.PaperOutcomeRecord{}, Allocations: []storage.PaperAllocationAttribution{}, Economics: storage.PaperEconomicAttribution{Status: "owned_execution_unavailable", BaselineStatus: "economic_baselines_unavailable"}, AdmissionReviewHistory: &storage.PaperAdmissionReviewHistory{SourceSHA256: workerplan.Hash(source), Records: records, Authority: "service_owned_non_grant_admission_review_history_only"}}
	row.SHA256 = workerplan.Hash(row)
	return row
}
func TestDecisionAttributionInspectionNavigation(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "attribution-view-token")
	one, two := decisionViewFixture("one"), decisionViewFixture("two")
	row := attributionViewFixture(two)
	requests := 0
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/v1/paper-attribution/two" || r.URL.RawQuery != "" || r.ContentLength != 0 || r.Header.Get("Authorization") != "Bearer attribution-view-token" {
			t.Error("unexpected attribution request", r.Method, r.URL.Path, r.ContentLength)
		}
		_ = json.NewEncoder(w).Encode(row)
	})))
	defer server.Close()
	fetches := 0
	fetch := func(context.Context) (storage.PaperDecisionCatalog, error) {
		fetches++
		page := storage.PaperDecisionCatalog{ReceiverID: one.Command.ReceiverID, Records: []storage.PaperDecisionRecord{one, two}}
		if fetches == 2 {
			return page, errors.New("offline")
		}
		if fetches == 3 {
			page.ReceiverID = "receiver-ponmlkjihgfedcba"
		}
		return page, nil
	}
	var out bytes.Buffer
	input := "a two\na ../operations\na two extra\nn\na two\nr\na two\nr\na two\nq\n"
	if err := superviseDecisions(context.Background(), io.NopCloser(strings.NewReader(input)), &out, 1, fetch, func(ctx context.Context, d storage.PaperDecisionRecord, kind string) (any, error) {
		return fetchDecisionInspection(ctx, server.URL, d, kind)
	}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || fetches != 3 || strings.Count(out.String(), `"admission_review_history"`) != 1 || !strings.Contains(out.String(), "a ID attribution") || !strings.Contains(out.String(), "STALE") || !strings.Contains(out.String(), "Choose an ID from the displayed page") {
		t.Fatal(requests, fetches, out.String())
	}
}
func TestDecisionAttributionInspectionRejectsSubstitution(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "attribution-view-token")
	d := decisionViewFixture("one")
	for name, mutate := range map[string]func(*storage.PaperAttributionRow){
		"decision":             func(r *storage.PaperAttributionRow) { r.Decision.Command.ID = "other" },
		"basis":                func(r *storage.PaperAttributionRow) { r.Decision.BasisSHA256 = strings.Repeat("d", 64) },
		"finalized facts":      func(r *storage.PaperAttributionRow) { r.Decision.Evaluation.ReasonCode = "changed" },
		"owner":                func(r *storage.PaperAttributionRow) { r.Decision.Command.ReceiverID = "receiver-ponmlkjihgfedcba" },
		"qualification":        func(r *storage.PaperAttributionRow) { r.QualificationCredit = true },
		"economics":            func(r *storage.PaperAttributionRow) { n := int64(0); r.Economics.FeesCents = &n },
		"history grant":        func(r *storage.PaperAttributionRow) { r.AdmissionReviewHistory.AdmissionAvailable = true },
		"history head":         func(r *storage.PaperAttributionRow) { r.AdmissionReviewHistory.CurrentID = "other" },
		"missing observations": func(r *storage.PaperAttributionRow) { r.Observations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			row := attributionViewFixture(d)
			mutate(&row)
			row.Decision.SHA256 = ""
			row.Decision.SHA256 = workerplan.Hash(row.Decision)
			row.SHA256 = ""
			row.SHA256 = workerplan.Hash(row)
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(row) })))
			defer server.Close()
			result, err := fetchDecisionInspection(context.Background(), server.URL, d, "a")
			if !errors.Is(err, errDecisionReadback) || result != nil {
				t.Fatal("rehashed attribution substitution accepted", result, err)
			}
		})
	}
	// A valid response is detached and returned in full, including explicit null
	// economics and empty history. It is still a synthetic UI fixture.
	row := attributionViewFixture(d)
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(row) })))
	defer server.Close()
	got, err := fetchDecisionInspection(context.Background(), server.URL, d, "a")
	if err != nil || !reflect.DeepEqual(got, row) {
		t.Fatal(got, err)
	}
}
func TestDecisionAttributionInspectionTransportFailures(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "attribution-view-token")
	d := decisionViewFixture("one")
	for _, status := range []int{401, 404, 503} {
		server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(attributionViewFixture(d))
		})))
		got, err := fetchDecisionInspection(context.Background(), server.URL, d, "a")
		server.Close()
		if err == nil || got != nil {
			t.Fatal(status, got, err)
		}
	}
	raw, _ := json.Marshal(attributionViewFixture(d))
	for _, body := range []string{string(raw) + `{}`, strings.TrimSuffix(string(raw), "}") + `,"unexpected":true}`, strings.Repeat(" ", storage.MaxPaperAttributionBytes+1)} {
		server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })))
		got, err := fetchDecisionInspection(context.Background(), server.URL, d, "a")
		server.Close()
		if err == nil || got != nil {
			t.Fatal("unbounded or malformed attribution accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := fetchDecisionInspection(ctx, "http://127.0.0.1:1", d, "a"); err == nil || got != nil {
		t.Fatal(got, err)
	}
}

func TestDecisionAttributionInspectionCancellation(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "attribution-view-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })))
	defer server.Close()
	d := decisionViewFixture("one")
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- superviseDecisions(ctx, io.NopCloser(strings.NewReader("a one\nq\n")), &out, 1, func(context.Context) (storage.PaperDecisionCatalog, error) {
			return storage.PaperDecisionCatalog{ReceiverID: d.Command.ReceiverID, Records: []storage.PaperDecisionRecord{d}}, nil
		}, func(ctx context.Context, d storage.PaperDecisionRecord, kind string) (any, error) {
			return fetchDecisionInspection(ctx, server.URL, d, kind)
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("attribution request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || strings.Contains(out.String(), `"admission_review_history"`) {
			t.Fatal("canceled attribution retained a result", err, out.String())
		}
	case <-time.After(time.Second):
		t.Fatal("attribution inspection did not cancel")
	}
}
