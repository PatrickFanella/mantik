package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Called by both the memory and disposable PostgreSQL HTTP contracts. The
// returned receipts are inspected again after the PostgreSQL connection reopens.
func candidateIntentHTTP(t *testing.T, base string, root, child candidatelineage.Registration, raw []byte) (candidateattempt.Intent, candidateattempt.History) {
	t.Helper()
	ctx := context.Background()
	p, err := candidatetest.New(root.Candidate, raw, []bool{true, true, false, false})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(p, raw)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "job.json")
	encoded, _ := json.Marshal(j)
	if err := os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", base, "--receiver-id", root.ReceiverID)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	var empty candidateattempt.History
	if err := json.Unmarshal(run("candidate-worker-intents"), &empty); err != nil || empty.Validate() != nil || len(empty.Intents) != 0 || len(empty.Registrations) != 0 {
		t.Fatal(empty, err)
	}
	args := []string{"candidate-worker-intent-start", "--id", "intent-first", "--registration", root.ID, "--registration-sha256", root.SHA256, "--job", file, "--job-sha256", j.SHA256}
	var first, retry candidateattempt.Intent
	if err := json.Unmarshal(run(args...), &first); err != nil || first.Validate() != nil || first.Command.Job.SHA256 != j.SHA256 || first.ExecutionStarted || first.HostStateKnown || first.QualificationCredit {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("HTTP retry changed intent", err)
	}
	if err := json.Unmarshal(run("candidate-worker-intent", "--id", "intent-first", "--intent-sha256", first.SHA256), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("intent inspection changed", err)
	}
	var job candidateexec.Job
	if err := json.Unmarshal(run("candidate-worker-intent", "--id", "intent-first", "--intent-sha256", first.SHA256, "--artifact", "job"), &job); err != nil || !reflect.DeepEqual(j, job) {
		t.Fatal("exported job changed", err)
	}
	secondArgs := append([]string(nil), args...)
	secondArgs[2] = "intent-second"
	secondArgs[4] = child.ID
	secondArgs[6] = child.SHA256
	var second candidateattempt.Intent
	if err := json.Unmarshal(run(secondArgs...), &second); err != nil || second.Command.Job.SHA256 != first.Command.Job.SHA256 || second.SHA256 == first.SHA256 {
		t.Fatal("distinct registration/request conflated", err)
	}
	var history candidateattempt.History
	if err := json.Unmarshal(run("candidate-worker-intents"), &history); err != nil || history.Validate() != nil || len(history.Intents) != 2 || len(history.Registrations) != 2 {
		t.Fatal(history, err)
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer candidate-http-token")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			b, _ := io.ReadAll(res.Body)
			t.Fatal(path, res.StatusCode, want, string(b))
		}
	}
	command := first.Command
	command.RequestID = "http-next"
	body, _ := json.Marshal(command)
	for _, path := range []string{"/v1/candidate-worker-intents", "/v1/candidate-worker-intents/intent-first"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
	}
	call("POST", "/v1/candidate-worker-intents", string(body), false, 401)
	call("GET", "/v1/candidate-worker-intents/missing", "", true, 404)
	call("POST", "/v1/candidate-worker-intents/intent-first/launch", "{}", true, 404)
	call("POST", "/v1/candidate-worker-intents/intent-first/cancel", "{}", true, 404)
	for _, bad := range []string{string(body) + "{}", strings.Replace(string(body), `"receiver_id":`, `"Receiver_id":`, 1), strings.Replace(string(body), `"job":`, `"execution_started":true,"job":`, 1), strings.Replace(string(body), `"job":`, `"job":null,"job":`, 1)} {
		call("POST", "/v1/candidate-worker-intents", bad, true, 422)
	}
	wrong := first.Command
	wrong.RegistrationSHA256 = strings.Repeat("a", 64)
	wrongBody, _ := json.Marshal(wrong)
	call("POST", "/v1/candidate-worker-intents", string(wrongBody), true, 409)
	wrong = command
	wrong.ReceiverID = "receiver-other_other_other"
	wrongBody, _ = json.Marshal(wrong)
	call("POST", "/v1/candidate-worker-intents", string(wrongBody), true, 409)
	wrong = command
	wrong.RegistrationID = "missing"
	wrongBody, _ = json.Marshal(wrong)
	call("POST", "/v1/candidate-worker-intents", string(wrongBody), true, 404)
	for _, bad := range [][]string{{"candidate-worker-intent", "--id", "intent-first"}, {"candidate-worker-intent", "--id", "intent-first", "--intent-sha256", first.SHA256, "--artifact", "raw"}, {"candidate-worker-intents", "--id", "filtered"}, {"candidate-worker-intent-start", "--id", "invalid"}} {
		var out, diag bytes.Buffer
		if err := Run(ctx, append(bad, "--api-url", base, "--receiver-id", root.ReceiverID), &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid intent command emitted data", err)
		}
	}
	return first, history
}

func TestCandidateIntentClientRejectsForgedReceipts(t *testing.T) {
	s := storage.NewMemory()
	p, raw, pub := candidateHTTPFixture(t, s)
	ctx := context.Background()
	r, err := s.RegisterCandidate(ctx, candidatelineage.Command{ReceiverID: pub.ReceiverID, RequestID: "root", PublicationID: pub.ID, Candidate: p}, raw, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := candidatetest.New(p, raw, []bool{true, true, false, false})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.StartCandidateWorker(ctx, candidateattempt.Command{ReceiverID: r.ReceiverID, RequestID: "intent", RegistrationID: r.ID, RegistrationSHA256: r.SHA256, Job: j}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.CandidateWorkerIntentHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"receiver", "request", "qualified", "history-null", "history-pin", "history-extra", "trailing", "unknown", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{"candidate-worker-intent", "--id", "intent", "--intent-sha256", i.SHA256}
			var body []byte
			switch kind {
			case "receiver", "request", "qualified":
				v := i
				if kind == "receiver" {
					v.Command.ReceiverID = "receiver-other_other_other"
				} else if kind == "request" {
					v.Command.RequestID = "other"
				} else {
					v.QualificationCredit = true
				}
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
			case "history-null", "history-pin", "history-extra":
				v := h
				if kind == "history-null" {
					v.Intents = nil
				} else if kind == "history-pin" {
					v.Registrations = append([]candidateattempt.RegistrationPin(nil), h.Registrations...)
					v.Registrations[0].SHA256 = strings.Repeat("a", 64)
				} else {
					v.HostStateKnown = true
				}
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
				args = []string{"candidate-worker-intents"}
			default:
				body, _ = json.Marshal(i)
				if kind == "trailing" {
					body = append(body, []byte("{}")...)
				} else if kind == "unknown" {
					body = []byte(strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1))
				} else {
					body = []byte(strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1))
				}
			}
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })))
			defer server.Close()
			var out, diag bytes.Buffer
			args = append(args, "--api-url", server.URL, "--receiver-id", r.ReceiverID)
			if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("forged intent receipt emitted", err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "job.json")
	encoded, _ := json.Marshal(j)
	if err := os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	args := []string{"candidate-worker-intent-start", "--api-url", server.URL, "--receiver-id", r.ReceiverID, "--id", "invalid", "--registration", r.ID, "--registration-sha256", r.SHA256, "--job", file, "--job-sha256", strings.Repeat("a", 64)}
	if err := Run(ctx, args, &out, &diag); err == nil || calls.Load() != 0 || out.Len() != 0 {
		t.Fatal("changed job pin reached service", err)
	}
}
