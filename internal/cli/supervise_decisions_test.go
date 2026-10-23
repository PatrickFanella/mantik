package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Envelope-only UI fixtures are not finalized service decisions or qualification.
func decisionViewFixture(id string) storage.PaperDecisionRecord {
	d := storage.PaperDecisionRecord{Schema: "mantik-owned-paper-decision-v1", Command: storage.PaperDecisionCommand{ID: id, ReceiverID: "receiver-abcdefghijklmnop", StrategyID: "strategy", StrategySHA256: strings.Repeat("a", 64), RegistrationID: "feed"}, FinalizedAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Status: "finalized", Evaluation: storage.PaperDecisionEvaluation{Action: "abstain", ReasonCode: "fixture"}, BasisSHA256: strings.Repeat("b", 64), Authority: "service_finalized_research_proposal_only"}
	d.SHA256 = workerplan.Hash(d)
	return d
}
func TestDecisionSupervisionNavigationAndStaleOwner(t *testing.T) {
	d1, d2 := decisionViewFixture("one"), decisionViewFixture("two")
	page := storage.PaperDecisionCatalog{ReceiverID: d1.Command.ReceiverID, Records: []storage.PaperDecisionRecord{d1, d2}}
	calls, inspections := 0, 0
	fetch := func(context.Context) (storage.PaperDecisionCatalog, error) {
		calls++
		if calls == 2 {
			return page, errors.New("offline")
		}
		if calls == 3 {
			v := page
			v.ReceiverID = "receiver-ponmlkjihgfedcba"
			return v, nil
		}
		return page, nil
	}
	inspect := func(_ context.Context, d storage.PaperDecisionRecord, kind string) (any, error) {
		inspections++
		if d.Command.ID != "two" || kind != "e" {
			t.Fatal(d, kind)
		}
		return nil, errors.New("offline")
	}
	var out bytes.Buffer
	input := "e two\nn\ne two\np\nd one\nd ../operations\nexecute now\nr\nd one\nn\nr\ne one\nr\nd one\nq\n"
	if err := superviseDecisions(context.Background(), io.NopCloser(strings.NewReader(input)), &out, 1, fetch, inspect); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || inspections != 1 || !strings.Contains(out.String(), "STALE") || !strings.Contains(out.String(), "UNAVAILABLE") || !strings.Contains(out.String(), `"id": "one"`) {
		t.Fatal(calls, inspections, out.String())
	}
}
func TestDecisionSupervisionInvalidCatalogue(t *testing.T) {
	d := decisionViewFixture("one")
	for _, mutate := range []func(*storage.PaperDecisionCatalog){
		func(v *storage.PaperDecisionCatalog) { v.Records = nil },
		func(v *storage.PaperDecisionCatalog) { v.Records = append(v.Records, v.Records[0]) },
		func(v *storage.PaperDecisionCatalog) { v.Records[0].SHA256 = strings.Repeat("c", 64) },
		func(v *storage.PaperDecisionCatalog) { v.ReceiverID = "receiver-ponmlkjihgfedcba" },
	} {
		v := storage.PaperDecisionCatalog{ReceiverID: d.Command.ReceiverID, Records: []storage.PaperDecisionRecord{d}}
		mutate(&v)
		var out bytes.Buffer
		err := superviseDecisions(context.Background(), io.NopCloser(strings.NewReader("d one\ne one\nq\n")), &out, 10, func(context.Context) (storage.PaperDecisionCatalog, error) { return v, nil }, func(context.Context, storage.PaperDecisionRecord, string) (any, error) {
			t.Fatal("invalid catalogue issued inspection")
			return nil, nil
		})
		if err != nil || !strings.Contains(out.String(), "STALE") || strings.Contains(out.String(), "FINALIZED RESEARCH DECISIONS") {
			t.Fatal(out.String(), err)
		}
	}
}
func TestDecisionSupervisionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in, writer := io.Pipe()
	defer writer.Close()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- superviseDecisions(ctx, in, io.Discard, 10, func(context.Context) (storage.PaperDecisionCatalog, error) {
			close(started)
			return storage.PaperDecisionCatalog{ReceiverID: "receiver-abcdefghijklmnop", Records: []storage.PaperDecisionRecord{}}, nil
		}, nil)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal read did not cancel")
	}
}
func TestDecisionInspectionRejectsSubstitution(t *testing.T) {
	d := decisionViewFixture("one")
	t.Setenv("MANTIK_API_TOKEN", "terminal-token")
	for _, kind := range []string{"e", "m"} {
		for _, field := range []string{"decision", "basis", "owner", "authority", "hash"} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer terminal-token" || r.ContentLength != 0 {
					t.Error("unexpected request", r.Method, r.ContentLength)
				}
				if kind == "e" {
					v := service.DecisionExplanation{Schema: "mantik-finalized-decision-explanation-v1", Method: "deterministic-retained-decision-facts-v1", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, StrategyID: d.Command.StrategyID, StrategySHA256: d.Command.StrategySHA256, RegistrationID: d.Command.RegistrationID, FinalizedAt: d.FinalizedAt, Action: d.Evaluation.Action, ReasonCode: d.Evaluation.ReasonCode, Statements: []string{}, RecordPath: "/v1/paper-decisions/one", Authority: "finalized_research_decision_explanation_only"}
					switch field {
					case "decision":
						v.DecisionSHA256 = strings.Repeat("d", 64)
					case "basis":
						v.BasisSHA256 = strings.Repeat("d", 64)
					case "owner":
						v.ReceiverID = "receiver-ponmlkjihgfedcba"
					case "authority":
						v.AdmissionAvailable = true
					}
					v.SHA256 = workerplan.Hash(v)
					if field == "hash" {
						v.SHA256 = strings.Repeat("d", 64)
					}
					_ = emit(w, v)
				} else {
					v := service.PaperDecisionMarketEvidence{Schema: "mantik-owned-decision-market-evidence-v1", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, RegistrationID: d.Command.RegistrationID, FinalizedAt: d.FinalizedAt, Authority: "service_owned_recorded_market_dimensions_only"}
					switch field {
					case "decision":
						v.DecisionSHA256 = strings.Repeat("d", 64)
					case "basis":
						v.BasisSHA256 = strings.Repeat("d", 64)
					case "owner":
						v.ReceiverID = "receiver-ponmlkjihgfedcba"
					case "authority":
						v.ExecutionAvailable = true
					}
					v.SHA256 = workerplan.Hash(v)
					if field == "hash" {
						v.SHA256 = strings.Repeat("d", 64)
					}
					_ = emit(w, v)
				}
			}))
			value, err := fetchDecisionInspection(context.Background(), srv.URL, d, kind)
			srv.Close()
			if !errors.Is(err, errDecisionReadback) || value != nil {
				t.Fatal(kind, field, value, err)
			}
		}
	}
}
