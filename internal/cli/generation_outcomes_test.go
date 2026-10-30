package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
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

func generationOutcomeHTTP(t *testing.T, base string, intents candidategeneration.IntentHistory, raw []byte) (candidategeneration.OutcomeHistory, []candidategeneration.OutcomeArtifact) {
	t.Helper()
	ctx := context.Background()
	receiver := intents.ReceiverID
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", base, "--receiver-id", receiver)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	call := func(method, path, body string, auth bool, want int) []byte {
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
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want, string(b))
		}
		return b
	}
	var empty candidategeneration.OutcomeHistory
	if err := json.Unmarshal(run("candidate-generation-outcomes"), &empty); err != nil || empty.Validate() != nil || len(empty.Outcomes) != 0 || len(empty.UnreportedRequestIDs) != 2 {
		t.Fatal(empty, err)
	}
	source := "package candidate\r\nfunc Entry(p int64)bool{return p<=10000}\r\n"
	good, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: source})
	outputs := [][]byte{good, {0xff, 0, 0xfe}, {}, []byte(`{"schema":"mantik-candidate-generation-output-v1","source":`)}
	all := []candidategeneration.OutcomeArtifact{}
	var firstReport candidategeneration.Report
	for n, output := range outputs {
		var i candidategeneration.Intent
		if n < 2 {
			i = intents.Intents[n]
		} else {
			c := intents.Intents[0].Command
			c.RequestID = []string{"generation-empty", "generation-partial"}[n-2]
			body, _ := json.Marshal(candidategeneration.Submission{Command: c, Dataset: raw})
			if err := json.Unmarshal(call("POST", "/v1/candidate-generation-intents", string(body), true, 200), &i); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := candidategeneration.NewRecording(i.Command.Plan, raw, output, i.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "recording.json")
		encoded, _ := json.Marshal(rec)
		if err := os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"candidate-generation-outcome-report", "--id", i.Command.RequestID, "--intent-sha256", i.SHA256, "--plan-sha256", rec.Plan.SHA256, "--recording-sha256", rec.SHA256, "--recording", file}
		var o, retry candidategeneration.Outcome
		if err := json.Unmarshal(run(args...), &o); err != nil || o.VerifyIntent(i) != nil {
			t.Fatal(o, err)
		}
		if err := json.Unmarshal(run(args...), &retry); err != nil || !reflect.DeepEqual(o, retry) {
			t.Fatal("HTTP retry changed generation report", err)
		}
		a, err := candidategeneration.NewOutcomeArtifact(i, o, rec)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, a)
		if n == 0 {
			firstReport = candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: receiver, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: rec}
		}
		flags := []string{"candidate-generation-outcome", "--id", o.RequestID, "--intent-sha256", i.SHA256, "--plan-sha256", rec.Plan.SHA256, "--recording-sha256", rec.SHA256, "--outcome-sha256", o.SHA256}
		if err := json.Unmarshal(run(flags...), &retry); err != nil || !reflect.DeepEqual(o, retry) {
			t.Fatal("inspection changed outcome", err)
		}
		var got candidategeneration.Recording
		if err := json.Unmarshal(run(append(append([]string{}, flags...), "--artifact", "recording")...), &got); err != nil || !reflect.DeepEqual(got, rec) {
			t.Fatal("recording export changed", err)
		}
		if !bytes.Equal(run(append(append([]string{}, flags...), "--artifact", "output")...), output) {
			t.Fatal("exact output changed")
		}
		if rec.Candidate != nil {
			if string(run(append(append([]string{}, flags...), "--artifact", "source")...)) != source {
				t.Fatal("source CRLF changed")
			}
			var exported struct {
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal(run(append(append([]string{}, flags...), "--artifact", "candidate")...), &exported); err != nil || exported.SHA256 != rec.Candidate.SHA256 {
				t.Fatal("candidate export changed", err)
			}
		} else {
			for _, kind := range []string{"candidate", "source"} {
				var out, diag bytes.Buffer
				bad := append(append([]string{}, flags...), "--artifact", kind, "--api-url", base, "--receiver-id", receiver)
				if err := Run(ctx, bad, &out, &diag); err == nil || out.Len() != 0 {
					t.Fatal("rejected output invented source", err)
				}
			}
		}
		if b, _ := os.ReadFile(file); !bytes.Equal(b, encoded) {
			t.Fatal("recording input rewritten")
		}
	}
	var h candidategeneration.OutcomeHistory
	if err := json.Unmarshal(run("candidate-generation-outcomes"), &h); err != nil || h.Validate() != nil || len(h.Outcomes) != 4 || len(h.UnreportedRequestIDs) != 0 {
		t.Fatal(h, err)
	}
	// The caller's earlier intent-history snapshot stays unchanged.
	if len(intents.Intents) != 2 {
		t.Fatal("input history changed")
	}
	id := all[0].Outcome.RequestID
	body, _ := json.Marshal(firstReport)
	for _, path := range []string{"/v1/candidate-generation-outcomes", "/v1/candidate-generation-outcomes/" + id, "/v1/candidate-generation-outcomes/" + id + "/recording"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
		call("GET", path+"?%zz", "", true, 422)
	}
	var direct candidategeneration.Outcome
	if err := json.Unmarshal(call("GET", "/v1/candidate-generation-outcomes/"+id, "", true, 200), &direct); err != nil || !reflect.DeepEqual(direct, all[0].Outcome) {
		t.Fatal("direct receipt changed", err)
	}
	call("POST", "/v1/candidate-generation-outcomes", string(body), false, 401)
	call("POST", "/v1/candidate-generation-outcomes?ignored=true", string(body), true, 422)
	call("GET", "/v1/candidate-generation-outcomes/missing", "", true, 404)
	call("GET", "/v1/candidate-generation-outcomes/missing/recording", "", true, 404)
	call("POST", "/v1/candidate-generation-outcomes/"+id+"/launch", "{}", true, 404)
	for _, bad := range []string{string(body) + "{}", strings.Replace(string(body), `"schema":`, `"Schema":`, 1), strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1), strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1)} {
		call("POST", "/v1/candidate-generation-outcomes", bad, true, 422)
	}
	for _, kind := range []string{"receiver", "intent", "output", "missing"} {
		bad := firstReport
		switch kind {
		case "receiver":
			bad.ReceiverID = "receiver-other_other_other"
		case "intent":
			bad.IntentSHA256 = strings.Repeat("f", 64)
		case "output":
			bad.Recording, _ = candidategeneration.NewRecording(firstReport.Recording.Plan, raw, []byte{}, firstReport.Recording.CapturedAt)
		case "missing":
			bad.RequestID = "missing"
		}
		b, _ := json.Marshal(bad)
		want := 409
		if kind == "missing" {
			want = 404
		}
		call("POST", "/v1/candidate-generation-outcomes", string(b), true, want)
	}
	return h, all
}
func TestGenerationOutcomeClientRejectsForgedResponses(t *testing.T) {
	s := storage.NewMemory()
	_, raw, pub := candidateHTTPFixture(t, s)
	ctx := context.Background()
	p, err := candidategeneration.NewPlan(pub.Sealed.Declaration, raw, "sha256:"+strings.Repeat("a", 64), "gpt-6.1-sol", "Synthetic threshold.", pub.PublishedAt)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.StartCandidateGeneration(ctx, candidategeneration.Command{ReceiverID: pub.ReceiverID, RequestID: "generation", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: p}, raw, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := candidategeneration.NewRecording(p, raw, []byte{0xff, 0, 0xfe}, i.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	r := candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: pub.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: rec}
	o, err := s.ReportCandidateGeneration(ctx, r, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CandidateGenerationOutcomeEvidence(ctx, r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "recording.json")
	encoded, _ := json.Marshal(rec)
	if err := os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--id", r.RequestID, "--intent-sha256", i.SHA256, "--plan-sha256", p.SHA256, "--recording-sha256", rec.SHA256}
	for _, kind := range []string{"receiver", "request", "intent", "plan", "outcome-pin", "recording", "qualified", "history-null", "history-missing", "history-owner", "history-authorship", "unknown", "duplicate", "case", "trailing", "oversize", "report-receipt", "report-intent"} {
		t.Run(kind, func(t *testing.T) {
			var body []byte
			args := append([]string{"candidate-generation-outcome"}, flags...)
			args = append(args, "--outcome-sha256", o.SHA256, "--artifact", "output")
			if strings.HasPrefix(kind, "history-") {
				v := h
				v.Outcomes = append([]candidategeneration.Outcome{}, h.Outcomes...)
				switch kind {
				case "history-null":
					v.Outcomes = nil
				case "history-missing":
					v.UnreportedRequestIDs = []string{r.RequestID}
				case "history-owner":
					v.ReceiverID = "receiver-other_other_other"
				case "history-authorship":
					v.ModelAuthorshipVerified = true
				}
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
				args = []string{"candidate-generation-outcomes"}
			} else if strings.HasPrefix(kind, "report-") {
				args = append([]string{"candidate-generation-outcome-report"}, flags...)
				args = append(args, "--recording", file)
				bad := o
				bad.OutputSHA256 = strings.Repeat("f", 64)
				bad.SHA256 = ""
				bad.SHA256 = workerplan.Hash(bad)
				body, _ = json.Marshal(bad)
			} else {
				b, _ := json.Marshal(a)
				var v candidategeneration.OutcomeArtifact
				_ = json.Unmarshal(b, &v)
				switch kind {
				case "receiver":
					v.Outcome.ReceiverID = "receiver-other_other_other"
				case "request":
					v.Outcome.RequestID = "other"
				case "intent":
					v.Outcome.IntentSHA256 = strings.Repeat("f", 64)
				case "plan":
					v.Outcome.PlanSHA256 = strings.Repeat("f", 64)
				case "outcome-pin":
					v.Outcome.RecordedAt = v.Outcome.RecordedAt.Add(time.Second)
				case "recording":
					v.Recording.Output = []byte{0xfe}
					v.Recording.OutputBytes = 1
					v.Recording.OutputSHA256 = workerplan.Digest(v.Recording.Output)
					v.Recording.SHA256 = ""
					v.Recording.SHA256 = workerplan.Hash(v.Recording)
				case "qualified":
					v.Outcome.QualificationCredit = true
				}
				v.Outcome.SHA256 = ""
				v.Outcome.SHA256 = workerplan.Hash(v.Outcome)
				body, _ = json.Marshal(v)
				switch kind {
				case "unknown":
					body = []byte(strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1))
				case "duplicate":
					body = []byte(strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1))
				case "case":
					body = []byte(strings.Replace(string(body), `"schema":`, `"Schema":`, 1))
				case "trailing":
					body = append(body, []byte("{}")...)
				case "oversize":
					body = bytes.Repeat([]byte(" "), candidategeneration.MaxOutcomeArtifactBytes+1)
				}
			}
			var posts atomic.Int32
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "POST" {
					posts.Add(1)
				}
				if strings.Contains(req.URL.Path, "/candidate-generation-intents/") {
					v := i
					if kind == "report-intent" {
						v.Command.RequestID = "other"
						v.SHA256 = ""
						v.SHA256 = workerplan.Hash(v)
					}
					_ = json.NewEncoder(w).Encode(v)
					return
				}
				_, _ = w.Write(body)
			})))
			defer server.Close()
			var out, diag bytes.Buffer
			args = append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID)
			if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("forged response emitted", kind, err)
			}
			if kind == "report-intent" && posts.Load() != 0 {
				t.Fatal("wrong intent caused admission")
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1); w.WriteHeader(500) })))
	defer server.Close()
	for _, args := range [][]string{{"candidate-generation-outcome-report", "--id", r.RequestID, "--intent-sha256", i.SHA256, "--plan-sha256", p.SHA256, "--recording-sha256", strings.Repeat("f", 64), "--recording", file}, {"candidate-generation-outcome", "--id", r.RequestID}, {"candidate-generation-outcomes", "--id", "filtered"}, {"candidate-generation-outcome-report", "unexpected"}} {
		var out, diag bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID)
		if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("bad preflight accepted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid preflight contacted service")
	}
}
