package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Self-consistent transport fixtures carry no independent execution authority.
func outcomeHistoryFixture(t *testing.T) candidateevidence.History {
	t.Helper()
	c := cliCaptureFixture(t)
	one := *c.Attempt.Intent.ServiceIntent
	two, three := one, one
	two.Command.RequestID = "two"
	two.Sequence = 2
	two.SHA256 = ""
	two.SHA256 = workerplan.Hash(two)
	three.Command.RequestID = "three"
	three.Sequence = 3
	three.SHA256 = ""
	three.SHA256 = workerplan.Hash(three)
	ih := candidateattempt.History{Schema: candidateattempt.HistorySchema, ReceiverID: one.Command.ReceiverID, Registrations: []candidateattempt.RegistrationPin{{ID: one.Command.RegistrationID, Sequence: 1, SHA256: one.Command.RegistrationSHA256, CandidateSHA256: one.Command.Job.Plan.Candidate.SHA256, RegisteredAt: one.CreatedAt}}, Intents: []candidateattempt.Intent{one, two, three}, Authority: "retained_candidate_worker_intents_only"}
	ih.SHA256 = workerplan.Hash(ih)
	capture := cliCaptureForIntent(t, two)
	r, err := candidateevidence.RetainOutcome(two, capture, two.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	h, err := candidateevidence.HistoryFor(ih, []candidateevidence.OutcomeRecord{r})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.UnreportedRequestIDs, []string{"one", "three"}) {
		t.Fatal("incorrect complement")
	}
	return h
}
func TestCandidateOutcomeHistoryClientStrictness(t *testing.T) {
	original := outcomeHistoryFixture(t)
	originalRaw, _ := json.Marshal(original)
	for _, kind := range []string{"valid", "receiver", "null-outcomes", "null-unreported", "null-intents", "missing", "extra", "reordered", "duplicate", "unreported-reported", "foreign-report", "intent-pin", "job-pin", "ordinal", "before-intent", "qualified", "record-qualified", "nested-qualified", "nested-reg-pin", "own-hash", "trailing", "duplicate-json", "unknown", "case", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			var h candidateevidence.History
			_ = json.Unmarshal(originalRaw, &h)
			switch kind {
			case "receiver":
				h.ReceiverID = "receiver-other_other_other"
			case "null-outcomes":
				h.Outcomes = nil
			case "null-unreported":
				h.UnreportedRequestIDs = nil
			case "null-intents":
				h.IntentHistory.Intents = nil
			case "missing":
				h.UnreportedRequestIDs = h.UnreportedRequestIDs[:1]
			case "extra":
				h.UnreportedRequestIDs = append(h.UnreportedRequestIDs, "missing")
			case "reordered":
				h.UnreportedRequestIDs[0], h.UnreportedRequestIDs[1] = h.UnreportedRequestIDs[1], h.UnreportedRequestIDs[0]
			case "duplicate":
				h.Outcomes = append(h.Outcomes, h.Outcomes[0])
			case "unreported-reported":
				h.UnreportedRequestIDs = append(h.UnreportedRequestIDs, "two")
			case "foreign-report":
				h.Outcomes[0].ReceiverID = "receiver-other_other_other"
			case "intent-pin":
				h.Outcomes[0].IntentSHA256 = strings.Repeat("d", 64)
			case "job-pin":
				h.Outcomes[0].JobSHA256 = strings.Repeat("d", 64)
			case "ordinal":
				h.Outcomes[0].IntentSequence = 1
			case "before-intent":
				h.Outcomes[0].RecordedAt = h.IntentHistory.Intents[1].CreatedAt.Add(-time.Second)
			case "qualified":
				h.QualificationCredit = true
			case "record-qualified":
				h.Outcomes[0].QualificationCredit = true
			case "nested-qualified":
				h.IntentHistory.HostStateKnown = true
			case "nested-reg-pin":
				h.IntentHistory.Registrations[0].SHA256 = strings.Repeat("d", 64)
			}
			// Rehash modified nested values so identity/binding checks must do the work.
			for n := range h.Outcomes {
				h.Outcomes[n].SHA256 = ""
				h.Outcomes[n].SHA256 = workerplan.Hash(h.Outcomes[n])
			}
			h.IntentHistory.SHA256 = ""
			h.IntentHistory.SHA256 = workerplan.Hash(h.IntentHistory)
			h.SHA256 = ""
			h.SHA256 = workerplan.Hash(h)
			if kind == "own-hash" {
				h.SHA256 = strings.Repeat("e", 64)
			}
			body, _ := json.Marshal(h)
			switch kind {
			case "trailing":
				body = append(body, []byte("{}")...)
			case "duplicate-json":
				body = []byte(strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1))
			case "unknown":
				body = []byte(strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1))
			case "case":
				body = []byte(strings.Replace(string(body), `"schema":`, `"Schema":`, 1))
			case "oversized":
				body = append(body, bytes.Repeat([]byte(" "), candidateevidence.MaxHistoryBytes)...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			var out, diag bytes.Buffer
			args := []string{"candidate-worker-outcomes", "--api-url", server.URL, "--receiver-id", original.ReceiverID}
			err := Run(context.Background(), args, &out, &diag)
			if kind != "valid" {
				if err == nil || out.Len() != 0 {
					t.Fatal("invalid history emitted", kind, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var read candidateevidence.History
			if json.Unmarshal(out.Bytes(), &read) != nil || !reflect.DeepEqual(read, original) {
				t.Fatal("history changed")
			}
			for _, flags := range [][]string{{"--id", "two"}, {"--artifact", "stdout"}, {"--capture", "ignored"}, {"positional"}} {
				out.Reset()
				if err := Run(context.Background(), append(append([]string{}, args...), flags...), &out, &diag); err == nil || out.Len() != 0 {
					t.Fatal("filtered/invalid history accepted")
				}
			}
		})
	}
}
