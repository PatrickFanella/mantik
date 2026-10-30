package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func generatedCommand(a candidategeneration.OutcomeArtifact, id, parent string) generatedcandidate.Command {
	return generatedcandidate.Command{ReceiverID: a.Intent.Command.ReceiverID, RequestID: id, ParentID: parent, GenerationRequestID: a.Outcome.RequestID, IntentSHA256: a.Intent.SHA256, OutcomeSHA256: a.Outcome.SHA256, RecordingSHA256: a.Recording.SHA256, PlanSHA256: a.Recording.Plan.SHA256, CandidateSHA256: a.Outcome.CandidateSHA256}
}
func generatedRegisterFlags(c generatedcandidate.Command) []string {
	return []string{"generated-candidate-register", "--id", c.RequestID, "--parent", c.ParentID, "--generation-id", c.GenerationRequestID, "--intent-sha256", c.IntentSHA256, "--outcome-sha256", c.OutcomeSHA256, "--recording-sha256", c.RecordingSHA256, "--plan-sha256", c.PlanSHA256, "--candidate-sha256", c.CandidateSHA256}
}
func generatedInspectFlags(a generatedcandidate.Artifact) []string {
	return []string{"generated-candidate", "--id", a.Registration.ID, "--link-sha256", a.Link.SHA256, "--registration-sha256", a.Registration.SHA256}
}
func generatedCandidateHTTP(t *testing.T, base string, all []candidategeneration.OutcomeArtifact) (generatedcandidate.Registry, []generatedcandidate.Artifact) {
	t.Helper()
	ctx := context.Background()
	receiver := all[0].Outcome.ReceiverID
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		if err := Run(ctx, append(args, "--api-url", base, "--receiver-id", receiver), &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
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
	var empty generatedcandidate.Registry
	if err := json.Unmarshal(run("generated-candidates"), &empty); err != nil || empty.Validate() != nil || len(empty.Links) != 0 || len(empty.UnlinkedRegistrationIDs) != 2 {
		t.Fatal(empty, err)
	}
	artifacts := []generatedcandidate.Artifact{}
	parent := "child"
	for _, id := range []string{"generated-root", "generated-child"} {
		c := generatedCommand(all[0], id, parent)
		var a, retry generatedcandidate.Artifact
		if err := json.Unmarshal(run(generatedRegisterFlags(c)...), &a); err != nil || a.Validate() != nil || a.Link.Command != c {
			t.Fatal(a, err)
		}
		if err := json.Unmarshal(run(generatedRegisterFlags(c)...), &retry); err != nil || !reflect.DeepEqual(a, retry) {
			t.Fatal("retry changed artifact", err)
		}
		for kind, want := range map[string]any{"artifact": a, "link": a.Link, "registration": a.Registration, "generation": a.Generation, "recording": a.Generation.Recording, "candidate": a.Registration.Candidate} {
			b := run(append(generatedInspectFlags(a), "--artifact", kind)...)
			var got any
			expected, _ := json.Marshal(want)
			var value any
			_ = json.Unmarshal(expected, &value)
			if json.Unmarshal(b, &got) != nil || !reflect.DeepEqual(got, value) {
				t.Fatal("export changed", kind)
			}
		}
		if !bytes.Equal(run(append(generatedInspectFlags(a), "--artifact", "source")...), []byte(a.Registration.Candidate.Source.Text)) || !bytes.Equal(run(append(generatedInspectFlags(a), "--artifact", "output")...), a.Generation.Recording.Output) {
			t.Fatal("exact bytes changed")
		}
		var legacy candidatelineage.Registration
		if err := json.Unmarshal(run("candidate-registration", "--id", id, "--registration-sha256", a.Registration.SHA256), &legacy); err != nil || !reflect.DeepEqual(legacy, a.Registration) {
			t.Fatal("legacy view changed", err)
		}
		artifacts = append(artifacts, a)
		parent = id
	}
	var h generatedcandidate.Registry
	if err := json.Unmarshal(run("generated-candidates"), &h); err != nil || h.Validate() != nil || len(h.Links) != 2 || !reflect.DeepEqual(h.UnlinkedRegistrationIDs, []string{"root", "child"}) {
		t.Fatal("mixed discovery", err)
	}
	c := generatedCommand(all[0], "next-generated", "child")
	b, _ := json.Marshal(c)
	body := string(b)
	for _, path := range []string{"/v1/generated-candidates", "/v1/generated-candidates/generated-root"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
		call("GET", path+"?%", "", true, 422)
	}
	call("POST", "/v1/generated-candidates", body, false, 401)
	call("POST", "/v1/generated-candidates?best=true", body, true, 422)
	call("GET", "/v1/generated-candidates/missing", "", true, 404)
	call("GET", "/v1/generated-candidates/root", "", true, 404)
	call("POST", "/v1/generated-candidates/generated-root/launch", "{}", true, 404)
	bad := c
	bad.ReceiverID = "receiver-other_other_other"
	encoded, _ := json.Marshal(bad)
	call("POST", "/v1/generated-candidates", string(encoded), true, 409)
	bad = c
	bad.RequestID = "root"
	encoded, _ = json.Marshal(bad)
	call("POST", "/v1/generated-candidates", string(encoded), true, 409)
	bad = c
	bad.OutcomeSHA256 = strings.Repeat("f", 64)
	encoded, _ = json.Marshal(bad)
	call("POST", "/v1/generated-candidates", string(encoded), true, 409)
	for n, a := range all {
		if n == 0 {
			continue
		}
		bad = generatedCommand(a, "rejected", "child")
		bad.CandidateSHA256 = strings.Repeat("f", 64)
		encoded, _ = json.Marshal(bad)
		call("POST", "/v1/generated-candidates", string(encoded), true, 409)
	}
	for _, invalid := range []string{body + "{}", strings.Replace(body, `"receiver_id":`, `"Receiver_id":`, 1), strings.Replace(body, `"request_id":`, `"request_id":"duplicate","request_id":`, 1), strings.Replace(body, `"receiver_id":`, `"source":"replace","receiver_id":`, 1), strings.Repeat(" ", generatedcandidate.MaxCommandBytes) + body, "null"} {
		call("POST", "/v1/generated-candidates", invalid, true, 422)
	}
	return h, artifacts
}
func TestGeneratedCandidateClientRejectsHostileResponses(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "generated-client-test-token")
	ctx := context.Background()
	s := storage.NewMemory()
	p, raw, pub := candidateHTTPFixture(t, s)
	plan, err := candidategeneration.NewPlan(p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256, "recorded-fixture", "finite sample", pub.PublishedAt)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.StartCandidateGeneration(ctx, candidategeneration.Command{ReceiverID: pub.ReceiverID, RequestID: "generation", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: plan}, raw, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: "package candidate\r\nfunc Entry(p int64)bool{return p<=10000}\r\n"})
	rec, err := candidategeneration.NewRecording(plan, raw, output, i.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.ReportCandidateGeneration(ctx, candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: pub.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: rec}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := candidategeneration.NewOutcomeArtifact(i, o, rec)
	if err != nil {
		t.Fatal(err)
	}
	c := generatedCommand(gen, "generated", "")
	a, err := s.RegisterGeneratedCandidate(ctx, c, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.GeneratedCandidateRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	register := append(generatedRegisterFlags(c), "--receiver-id", pub.ReceiverID)
	inspect := append(generatedInspectFlags(a), "--receiver-id", pub.ReceiverID)
	history := []string{"generated-candidates", "--receiver-id", pub.ReceiverID}
	for _, mode := range []string{"artifact", "command", "source", "authority", "unknown", "duplicate", "case", "trailing", "registry", "receiver", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			changed := a
			args := inspect
			var payload any = changed
			switch mode {
			case "artifact":
				changed.Link.RegistrationSHA256 = strings.Repeat("a", 64)
			case "command":
				different := c
				different.RequestID = "different-request"
				args = append(generatedRegisterFlags(different), "--receiver-id", pub.ReceiverID)
			case "source":
				changed.Registration.Candidate.Source.Text = "changed"
			case "authority":
				changed.Link.ModelAuthorshipVerified = true
			case "registry":
				v := h
				v.UnlinkedRegistrationIDs = append(v.UnlinkedRegistrationIDs, "invented")
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				payload = v
				args = history
			case "receiver":
				payload = h
				args = []string{"generated-candidates", "--receiver-id", "receiver-other_other_other"}
			}
			if mode != "registry" && mode != "receiver" {
				payload = changed
			}
			body, _ := json.Marshal(payload)
			switch mode {
			case "unknown":
				body = bytes.Replace(body, []byte(`"schema":`), []byte(`"ignored":true,"schema":`), 1)
			case "duplicate":
				body = bytes.Replace(body, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1)
			case "case":
				body = bytes.Replace(body, []byte(`"schema":`), []byte(`"Schema":`), 1)
			case "trailing":
				body = append(body, []byte("{}")...)
			case "oversize":
				body = append(bytes.Repeat([]byte(" "), generatedcandidate.MaxArtifactBytes), body...)
			}
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })))
			defer server.Close()
			var out, diag bytes.Buffer
			if err := Run(ctx, append(args, "--api-url", server.URL), &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("hostile data emitted", mode, err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })))
	defer server.Close()
	for _, args := range [][]string{{"generated-candidate-register", "--id", "missing"}, {"generated-candidate", "--id", "generated"}, {"generated-candidates", "extra"}, append(append([]string{}, inspect...), "--artifact", "dataset"), append(append([]string{}, register...), "--source", "replacement")} {
		var out, diag bytes.Buffer
		if err := Run(ctx, append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID), &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid arguments emitted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid local arguments contacted service")
	}
	for _, command := range []string{"generated-candidate-register", "generated-candidate", "generated-candidates"} {
		var out, diag bytes.Buffer
		if err := Run(ctx, []string{command, "--help"}, &out, &diag); err != nil || diag.Len() == 0 {
			t.Fatal("missing help", err)
		}
	}
}
