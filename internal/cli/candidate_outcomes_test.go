package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type outcomeHTTPClock struct {
	service.WallClock
	at time.Time
}

func (c outcomeHTTPClock) Now() time.Time { return c.at }

// Runs against Memory and disposable PostgreSQL. The synthetic report is built
// from the exact backend intent; this is transport evidence, not worker execution.
func candidateOutcomeHTTP(t *testing.T, s candidateHTTPBackend, i candidateattempt.Intent) (candidateevidence.OutcomeRecord, candidateevidence.Capture) {
	t.Helper()
	c := cliCaptureForIntent(t, i)
	server := httptest.NewServer(service.HandlerWithClock(s, "candidate-http-token", outcomeHTTPClock{at: i.CreatedAt.Add(time.Minute)}))
	defer server.Close()
	raw, _ := json.Marshal(c)
	file := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", i.Command.ReceiverID, "--id", i.Command.RequestID, "--intent-sha256", i.SHA256, "--capture-sha256", c.SHA256)
		if err := Run(context.Background(), args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	var first, retry candidateevidence.OutcomeRecord
	args := []string{"candidate-worker-outcome-report", "--capture", file}
	if err := json.Unmarshal(run(args...), &first); err != nil || first.VerifyCapture(i, c) != nil || first.Status != "incomplete" {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("report retry changed", err)
	}
	inspect := []string{"candidate-worker-outcome", "--outcome-sha256", first.SHA256}
	if err := json.Unmarshal(run(inspect...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("read changed", err)
	}
	for name, want := range map[string][]byte{"stdout": c.Stdout, "stderr": c.Stderr} {
		if !bytes.Equal(run(append(append([]string{}, inspect...), "--artifact", name)...), want) {
			t.Fatal("binary output changed", name)
		}
	}
	var read candidateevidence.Capture
	if err := json.Unmarshal(run(append(append([]string{}, inspect...), "--artifact", "capture")...), &read); err != nil || !reflect.DeepEqual(c, read) {
		t.Fatal("capture changed", err)
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer candidate-http-token")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			b, _ := io.ReadAll(res.Body)
			t.Fatal(path, res.StatusCode, want, string(b))
		}
	}
	report := candidateevidence.Report{ReceiverID: i.Command.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Capture: c}
	body, _ := json.Marshal(report)
	if parsed, err := candidateevidence.ReadReport(bytes.NewReader(body)); err != nil || !reflect.DeepEqual(parsed, report) {
		t.Fatal("strict report roundtrip changed", err)
	}
	envelope, err := candidateevidence.NewArtifact(first, c)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope)
	if parsed, err := candidateevidence.ReadArtifact(bytes.NewReader(encoded)); err != nil || !reflect.DeepEqual(parsed, envelope) {
		t.Fatal("strict artifact roundtrip changed", err)
	}

	for _, path := range []string{"/v1/candidate-worker-outcomes", "/v1/candidate-worker-outcomes/" + i.Command.RequestID, "/v1/candidate-worker-outcomes/" + i.Command.RequestID + "/capture"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
		call("GET", path+"?%zz", "", true, 422)
	}
	call("POST", "/v1/candidate-worker-outcomes", string(body), false, 401)
	call("POST", "/v1/candidate-worker-outcomes?best=true", string(body), true, 422)
	call("GET", "/v1/candidate-worker-outcomes/missing", "", true, 404)
	call("GET", "/v1/candidate-worker-outcomes/missing/capture", "", true, 404)
	call("GET", "/v1/candidate-worker-outcomes", "", true, 200)
	call("POST", "/v1/candidate-worker-outcomes/"+i.Command.RequestID+"/launch", "{}", true, 404)
	for _, bad := range []string{string(body) + "{}", strings.Replace(string(body), `"receiver_id":`, `"Receiver_id":`, 1), strings.Replace(string(body), `"capture":`, `"qualification_credit":true,"capture":`, 1), strings.Replace(string(body), `"capture":`, `"capture":null,"capture":`, 1), strings.Replace(string(body), `"stdout":`, `"stdout":"not-base64!","unknown":`, 1)} {
		call("POST", "/v1/candidate-worker-outcomes", bad, true, 422)
	}
	// A new self-consistent owner conflicts with the already retained report.
	changed := c
	changed.Acquisition.ID = "different-owner"
	changed.Acquisition.ContainerName = "mantik-worker-different-owner"
	changed.Acquisition.SHA256 = ""
	changed.Acquisition.SHA256 = workerplan.Hash(changed.Acquisition)
	outcome := *c.Attempt.Outcome
	outcome.Receipt.AcquisitionSHA256 = changed.Acquisition.SHA256
	outcome.Receipt.SHA256 = ""
	outcome.Receipt.SHA256 = workerplan.Hash(outcome.Receipt)
	outcome.SHA256 = ""
	outcome.SHA256 = workerplan.Hash(outcome)
	changed.Attempt.Outcome = &outcome
	changed.SHA256 = ""
	changed.SHA256 = workerplan.Hash(changed)
	if changed.Validate() != nil {
		t.Fatal("invalid changed-owner fixture")
	}
	report.Capture = changed
	body, _ = json.Marshal(report)
	call("POST", "/v1/candidate-worker-outcomes", string(body), true, 409)

	for _, kind := range []string{"unknown", "foreign", "substituted"} {
		other := i
		want := 409
		switch kind {
		case "unknown":
			other.Command.RequestID = "missing"
			want = 404
		case "foreign":
			other.Command.ReceiverID = "receiver-other_other_other"
		case "substituted":
			other.Sequence++
		}
		other.SHA256 = ""
		other.SHA256 = workerplan.Hash(other)
		invented := cliCaptureForIntent(t, other)
		report := candidateevidence.Report{ReceiverID: other.Command.ReceiverID, RequestID: other.Command.RequestID, IntentSHA256: other.SHA256, Capture: invented}
		if report.Validate() != nil {
			t.Fatal("invalid synthetic substitute", kind)
		}
		body, _ := json.Marshal(report)
		call("POST", "/v1/candidate-worker-outcomes", string(body), true, want)
	}
	for _, bad := range [][]string{{"candidate-worker-outcome"}, {"candidate-worker-outcome", "--outcome-sha256", first.SHA256, "--artifact", "raw"}, {"candidate-worker-outcome-report"}} {
		var out, diag bytes.Buffer
		if err := Run(context.Background(), append(bad, "--api-url", server.URL, "--receiver-id", i.Command.ReceiverID, "--id", i.Command.RequestID, "--intent-sha256", i.SHA256, "--capture-sha256", c.SHA256), &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid command emitted", bad, err)
		}
	}

	var out, diag bytes.Buffer
	if err := Run(context.Background(), []string{"candidate-worker-outcomes", "--api-url", server.URL, "--receiver-id", i.Command.ReceiverID}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	var history candidateevidence.History
	if err := json.Unmarshal(out.Bytes(), &history); err != nil || history.Validate() != nil || len(history.IntentHistory.Intents) != 2 || len(history.Outcomes) != 1 || history.Outcomes[0] != first || !reflect.DeepEqual(history.UnreportedRequestIDs, []string{"intent-second"}) {
		t.Fatal("HTTP coherent history changed", err)
	}
	return first, c
}

func TestCandidateOutcomeClientPinsAndStrictResponses(t *testing.T) {
	c := cliCaptureFixture(t)
	i := *c.Attempt.Intent.ServiceIntent
	r, err := candidateevidence.RetainOutcome(i, c, i.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a, err := candidateevidence.NewArtifact(r, c)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "capture.json")
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"receipt-sha", "intent-pin", "capture-pin", "receiver-pin", "request-pin", "record-pin", "report-mismatch", "qualified", "artifact-binding", "trailing", "unknown", "duplicate", "case", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			var body []byte
			v := r
			artifact := "outcome"
			switch kind {
			case "report-mismatch":
				v.ReasonCode = "different_reason"
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
			case "qualified":
				v.QualificationCredit = true
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
			case "artifact-binding":
				b := a
				b.Capture = cliCaptureFixture(t)
				b.SHA256 = ""
				b.SHA256 = workerplan.Hash(b)
				body, _ = json.Marshal(b)
				artifact = "stdout"
			case "record-pin":
				v.SHA256 = strings.Repeat("d", 64)
			}
			if body == nil {
				body, _ = json.Marshal(v)
			}
			switch kind {
			case "trailing":
				body = append(body, []byte("{}")...)
			case "unknown":
				body = []byte(strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1))
			case "duplicate":
				body = []byte(strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1))
			case "case":
				body = []byte(strings.Replace(string(body), `"schema":`, `"Schema":`, 1))
			case "oversized":
				body = append(body, bytes.Repeat([]byte(" "), candidateevidence.MaxOutcomeBytes)...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			pins := map[string]string{"--intent-sha256": i.SHA256, "--capture-sha256": c.SHA256, "--receiver-id": r.ReceiverID, "--id": r.RequestID, "--outcome-sha256": r.SHA256}
			for name, flag := range map[string]string{"receipt-sha": "--outcome-sha256", "intent-pin": "--intent-sha256", "capture-pin": "--capture-sha256", "receiver-pin": "--receiver-id", "request-pin": "--id"} {
				if kind == name {
					pins[flag] = strings.Repeat("d", 64)
					if name == "receiver-pin" {
						pins[flag] = "receiver-other_other_other"
					}
				}
			}
			args := []string{"candidate-worker-outcome", "--api-url", server.URL, "--artifact", artifact}
			for k, v := range pins {
				args = append(args, k, v)
			}
			if kind == "report-mismatch" {
				args = []string{"candidate-worker-outcome-report", "--capture", file, "--api-url", server.URL, "--receiver-id", r.ReceiverID, "--id", r.RequestID, "--intent-sha256", r.IntentSHA256, "--capture-sha256", c.SHA256}
			}
			var out, diag bytes.Buffer
			if err := Run(context.Background(), args, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("unverified response emitted", err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, kind := range []string{"capture", "receiver", "request", "intent"} {
		receiver, id, intentSHA, captureSHA := r.ReceiverID, r.RequestID, r.IntentSHA256, c.SHA256
		switch kind {
		case "capture":
			captureSHA = strings.Repeat("d", 64)
		case "receiver":
			receiver = "receiver-other_other_other"
		case "request":
			id = "other"
		case "intent":
			intentSHA = strings.Repeat("d", 64)
		}
		args := []string{"candidate-worker-outcome-report", "--api-url", server.URL, "--receiver-id", receiver, "--id", id, "--intent-sha256", intentSHA, "--capture-sha256", captureSHA, "--capture", file}
		var out, diag bytes.Buffer
		if err := Run(context.Background(), args, &out, &diag); err == nil || calls.Load() != 0 || out.Len() != 0 {
			t.Fatal("changed local pin reached service", kind, err)
		}
	}
}
