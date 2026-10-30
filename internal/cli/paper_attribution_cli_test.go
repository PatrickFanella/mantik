package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func paperAttributionCLIContract(t *testing.T, base string, all storage.PaperAttributionCatalog, candidate, abstain storage.PaperAttributionRow) {
	t.Helper()
	// The enclosing HTTP contract sets the same token and uses real service
	// finalization/assessment owners, once for Memory and once for PostgreSQL.
	var out, diag bytes.Buffer
	run := func(extra ...string) error {
		out.Reset()
		diag.Reset()
		args := []string{"paper-attribution", "--api-url", base, "--receiver-id", all.ReceiverID}
		return Run(context.Background(), append(args, extra...), &out, &diag)
	}
	if err := run("--format", "json"); err != nil {
		t.Fatal(err, diag.String())
	}
	var got storage.PaperAttributionCatalog
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal("catalogue differs", err)
	}
	for _, row := range []storage.PaperAttributionRow{candidate, abstain} {
		if err := run("--id", row.Decision.Command.ID, "--record-sha256", row.SHA256, "--format", "json"); err != nil {
			t.Fatal(err)
		}
		var got storage.PaperAttributionRow
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, row) {
			t.Fatal("selected row differs", err)
		}
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Finalized no-action decision; not a workflow failure.", "waiting_horizon", "not_applicable", "quote_delta_cents=unavailable", "not trading P&L", "fees=unavailable", "realized_pnl=unavailable", "funding bound, not incurred expense", "admission_unavailable", "not_calibrated_forecast", "economic_baselines_unavailable", "no independent sample count", "ADMISSION_READINESS", "EVALUATION_PLANS", "declaration_unavailable", "qualification=false", "relevant_evaluation_receipt_unavailable", "owned_execution_evidence_unavailable"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if err := run("--id", candidate.Decision.Command.ID, "--record-sha256", strings.Repeat("0", 64)); err == nil || out.Len() != 0 {
		t.Fatal("wrong pin printed output")
	}
	legacyRaw, _ := json.Marshal(candidate)
	var legacy storage.PaperAttributionRow
	_ = json.Unmarshal(legacyRaw, &legacy)
	legacy.AdmissionReviewHistory = nil
	legacy.Allocations[0].AdmissionReadiness = nil
	legacy.Allocations[0].ExecutionSafety = nil
	legacy.Allocations[0].EvaluationContext = nil
	legacy.SHA256 = ""
	legacy.SHA256 = workerplan.Hash(legacy)
	if validateAttributionRow(legacy, all.ReceiverID) != nil {
		t.Fatal("legacy comparison rejected")
	}
	// Rehashed malformed receipts must not turn unsupported economic assertions
	// into a valid presentation, even if every envelope hash is recomputed.
	for name, mutate := range map[string]func(*storage.PaperAttributionRow){
		"plan authority": func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.Authority = "admission_grant" },
		"plan strategy": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].EvaluationContext.Binding.StrategySHA256 = strings.Repeat("0", 64)
		},
		"plan duplicate pins": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].EvaluationContext.Binding.Status = "declared_not_evaluated"
			r.Allocations[0].EvaluationContext.Binding.Declarations = []storage.OwnedDecisionPin{{ID: "same", SHA256: strings.Repeat("0", 64)}, {ID: "same", SHA256: strings.Repeat("0", 64)}}
		},
		"plan grant":         func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.AdmissionAvailable = true },
		"plan qualification": func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.QualificationCredit = true },
		"plan source":        func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.SourceSHA256 = "invalid" },
		"plan decision": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].EvaluationContext.Binding.DecisionSHA256 = strings.Repeat("0", 64)
		},
		"plan policy":       func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.Binding.PolicyID = "other" },
		"plan status":       func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.Binding.Status = "evaluated" },
		"plan missing pins": func(r *storage.PaperAttributionRow) { r.Allocations[0].EvaluationContext.Binding.Declarations = nil },
		"readiness grant":   func(r *storage.PaperAttributionRow) { r.Allocations[0].AdmissionReadiness.AdmissionAvailable = true },
		"readiness decision": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].AdmissionReadiness.DecisionSHA256 = strings.Repeat("0", 64)
		},
		"readiness policy": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].AdmissionReadiness.Binding.PolicySHA256 = strings.Repeat("0", 64)
		},
		"readiness comparison": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].AdmissionReadiness.Binding.ComparisonID = "other"
		},
		"readiness cost": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].AdmissionReadiness.Binding.ExecutionCostSHA256 = ""
		},
		"readiness gate":   func(r *storage.PaperAttributionRow) { r.Allocations[0].AdmissionReadiness.Gates[2].Status = "verified" },
		"readiness expiry": func(r *storage.PaperAttributionRow) { r.Allocations[0].AdmissionReadiness.Status = "expired" },
		"readiness clock": func(r *storage.PaperAttributionRow) {
			r.Allocations[0].AdmissionReadiness.CheckedAt = r.Allocations[0].AdmissionReadiness.CheckedAt.Add(1)
		},
		"qualification": func(r *storage.PaperAttributionRow) { r.QualificationCredit = true },
		"zero fee":      func(r *storage.PaperAttributionRow) { v := int64(0); r.Economics.FeesCents = &v },
		"wrong owner":   func(r *storage.PaperAttributionRow) { r.Decision.Command.ReceiverID = "receiver-aaaaaaaaaaaaaaaa" },
		"observation pin": func(r *storage.PaperAttributionRow) {
			r.Observations[0].Command.DecisionSHA256 = strings.Repeat("0", 64)
			r.Observations[0].SHA256 = ""
			r.Observations[0].SHA256 = workerplan.Hash(r.Observations[0])
		},
		"missing observations": func(r *storage.PaperAttributionRow) { r.Observations = nil },
		"execution":            func(r *storage.PaperAttributionRow) { r.Economics.Status = "executed" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(candidate)
			var bad storage.PaperAttributionRow
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			if c := bad.Allocations[0].AdmissionReadiness; c != nil {
				c.SHA256 = ""
				c.SHA256 = workerplan.Hash(*c)
			}
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			if validateAttributionRow(bad, all.ReceiverID) == nil {
				t.Fatal("invalid rehashed row accepted")
			}
		})
	}
}

func TestPaperAttributionCLIEmptyAndFailures(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "attribution-token")
	store := storage.NewMemory()
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(store, "attribution-token")))
	defer server.Close()
	snapshot, err := store.ResearchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	receiver := snapshot.ID
	var out, diag bytes.Buffer
	args := []string{"paper-attribution", "--api-url", server.URL, "--receiver-id", receiver}
	if err := Run(context.Background(), args, &out, &diag); err != nil || !strings.Contains(out.String(), "No finalized decisions retained; insufficient evidence.") {
		t.Fatal(err, out.String())
	}
	for _, extra := range [][]string{{"--id", "../bad", "--record-sha256", strings.Repeat("0", 64)}, {"--id", "missing"}, {"--record-sha256", strings.Repeat("0", 64)}, {"--format", "csv"}, {"extra"}, {"--api-url", server.URL + "?"}, {"--receiver-id", "wrong"}} {
		out.Reset()
		if err := Run(context.Background(), append(append([]string{}, args...), extra...), &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal(extra, err)
		}
	}
	t.Setenv("MANTIK_API_TOKEN", "wrong")
	out.Reset()
	if err := Run(context.Background(), args, &out, &diag); err == nil || out.Len() != 0 {
		t.Fatal("auth failure hidden")
	}
	t.Setenv("MANTIK_API_TOKEN", "attribution-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, args, &out, &diag); err == nil {
		t.Fatal("canceled request accepted")
	}
	for _, body := range []string{`{}`, `{"schema":"unknown"}`, strings.Repeat(" ", storage.MaxPaperAttributionBytes+1)} {
		stub := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })))
		out.Reset()
		err := Run(context.Background(), []string{"paper-attribution", "--api-url", stub.URL, "--receiver-id", receiver}, &out, &diag)
		stub.Close()
		if err == nil || out.Len() != 0 {
			t.Fatal("malformed/oversize response printed")
		}
	}
	if err := Run(context.Background(), []string{"paper-attribution", "--help"}, io.Discard, &diag); err != nil {
		t.Fatal(err)
	}
}

func assertAttributionReviewPresentation(t *testing.T, owner string, candidate storage.PaperAttributionRow) {
	t.Helper()
	for name, mutate := range map[string]func(*storage.PaperAdmissionReviewHistory){
		"current":   func(h *storage.PaperAdmissionReviewHistory) { h.CurrentID = "other" },
		"grant":     func(h *storage.PaperAdmissionReviewHistory) { h.Records[0].AdmissionAvailable = true },
		"authority": func(h *storage.PaperAdmissionReviewHistory) { h.Authority = "admitted" },
		"comparison": func(h *storage.PaperAdmissionReviewHistory) {
			h.Records[0].Command.ComparisonSHA256 = strings.Repeat("0", 64)
		},
		"decision": func(h *storage.PaperAdmissionReviewHistory) {
			h.Records[0].Command.DecisionSHA256 = strings.Repeat("0", 64)
		},
		"readiness": func(h *storage.PaperAdmissionReviewHistory) { h.Records[0].Readiness.Gates[0].ReasonCode = "other" },
		"status":    func(h *storage.PaperAdmissionReviewHistory) { h.Records[0].Status = "admitted" },
		"reason":    func(h *storage.PaperAdmissionReviewHistory) { h.Records[0].ReasonCode = "qualified" },
		"triggers":  func(h *storage.PaperAdmissionReviewHistory) { h.Records[0].ReconsiderTriggers = []string{} },
		"clock": func(h *storage.PaperAdmissionReviewHistory) {
			h.Records[0].ReviewedAt = h.Records[0].ComparisonAt.Add(-1)
		},
		"predecessor": func(h *storage.PaperAdmissionReviewHistory) {
			h.Records[0].Command.PreviousID = "other"
			h.Records[0].Command.PreviousSHA256 = strings.Repeat("0", 64)
		},
		"duplicate":   func(h *storage.PaperAdmissionReviewHistory) { h.Records = append(h.Records, h.Records[0]) },
		"nil history": func(h *storage.PaperAdmissionReviewHistory) { h.Records = nil },
	} {
		t.Run("review presentation "+name, func(t *testing.T) {
			raw, _ := json.Marshal(candidate)
			var bad storage.PaperAttributionRow
			_ = json.Unmarshal(raw, &bad)
			h := bad.AdmissionReviewHistory
			mutate(h)
			for i := range h.Records {
				h.Records[i].SHA256 = ""
				h.Records[i].SHA256 = workerplan.Hash(h.Records[i])
			}
			ids := []string{}
			if h.CurrentID != "" {
				ids = []string{h.CurrentID}
			}
			h.SourceSHA256 = workerplan.Hash(storage.OwnedAdmissionReviewCatalog{ReceiverID: owner, Records: h.Records, CurrentIDs: ids})
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			stub := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(bad) })))
			defer stub.Close()
			var out, diag bytes.Buffer
			err := Run(context.Background(), []string{"paper-attribution", "--api-url", stub.URL, "--receiver-id", owner, "--id", bad.Decision.Command.ID, "--record-sha256", bad.SHA256}, &out, &diag)
			if err == nil || out.Len() != 0 {
				t.Fatal("rehashed invalid review printed output", err, out.String())
			}
		})
	}
}
