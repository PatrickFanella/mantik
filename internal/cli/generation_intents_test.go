package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
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

// Shared by memory and disposable PostgreSQL; the caller repeats full readback
// after reopening the PostgreSQL connection.
func generationIntentHTTP(t *testing.T, base string, pub hypothesis.Publication, raw []byte) (candidategeneration.Intent, candidategeneration.IntentHistory) {
	t.Helper()
	ctx := context.Background()
	p, err := candidategeneration.NewPlan(pub.Sealed.Declaration, raw, "sha256:"+strings.Repeat("a", 64), "gpt-6.1-sol", "Propose a synthetic threshold.", pub.PublishedAt)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pf, df := filepath.Join(dir, "plan.json"), filepath.Join(dir, "dataset.json")
	encoded, _ := json.Marshal(p)
	if err := os.WriteFile(pf, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(df, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", base, "--receiver-id", pub.ReceiverID)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	var empty candidategeneration.IntentHistory
	if err := json.Unmarshal(run("candidate-generation-intents"), &empty); err != nil || empty.Validate() != nil || len(empty.Intents) != 0 {
		t.Fatal(empty, err)
	}
	args := []string{"candidate-generation-intent-start", "--id", "generation-first", "--publication", pub.ID, "--publication-sha256", workerplan.Hash(pub), "--plan", pf, "--plan-sha256", p.SHA256, "--dataset", df}
	var first, retry candidategeneration.Intent
	if err := json.Unmarshal(run(args...), &first); err != nil || first.Validate() != nil || first.Command.Plan.SHA256 != p.SHA256 || first.ProviderCalled || first.SourceGenerated || first.QualificationCredit {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("retry changed generation", err)
	}
	inspect := []string{"candidate-generation-intent", "--id", first.Command.RequestID, "--intent-sha256", first.SHA256, "--plan-sha256", p.SHA256}
	if err := json.Unmarshal(run(inspect...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("selected generation changed", err)
	}
	var exported candidategeneration.Plan
	if err := json.Unmarshal(run(append(append([]string{}, inspect...), "--artifact", "plan")...), &exported); err != nil || !reflect.DeepEqual(p, exported) {
		t.Fatal("plan changed", err)
	}
	if string(run(append(append([]string{}, inspect...), "--artifact", "prompt")...)) != p.Prompt {
		t.Fatal("prompt bytes changed")
	}
	if !bytes.Equal(run(append(append([]string{}, inspect...), "--artifact", "dataset")...), raw) {
		t.Fatal("dataset bytes changed")
	}
	second := append([]string{}, args...)
	second[2] = "generation-second"
	run(second...)
	var h candidategeneration.IntentHistory
	if err := json.Unmarshal(run("candidate-generation-intents"), &h); err != nil || h.Validate() != nil || len(h.Intents) != 2 || h.Intents[0].SHA256 != first.SHA256 {
		t.Fatal(h, err)
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
	submission := candidategeneration.Submission{Command: first.Command, Dataset: raw}
	body, _ := json.Marshal(submission)
	for _, path := range []string{"/v1/candidate-generation-intents", "/v1/candidate-generation-intents/generation-first", "/v1/candidate-generation-intents/generation-first/dataset"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
		call("GET", path+"?%zz", "", true, 422)
	}
	var direct candidategeneration.Intent
	if err := json.Unmarshal(call("GET", "/v1/candidate-generation-intents/generation-first", "", true, 200), &direct); err != nil || !reflect.DeepEqual(first, direct) {
		t.Fatal("direct selected changed", err)
	}
	call("POST", "/v1/candidate-generation-intents", string(body), false, 401)
	call("POST", "/v1/candidate-generation-intents?ignored=true", string(body), true, 422)
	call("GET", "/v1/candidate-generation-intents/missing", "", true, 404)
	call("GET", "/v1/candidate-generation-intents/missing/dataset", "", true, 404)
	call("POST", "/v1/candidate-generation-intents/generation-first/launch", "{}", true, 404)
	for _, bad := range []string{string(body) + "{}", strings.Replace(string(body), `"command":`, `"Command":`, 1), strings.Replace(string(body), `"command":`, `"provider_called":true,"command":`, 1), strings.Replace(string(body), `"dataset":`, `"dataset":null,"dataset":`, 1), strings.Replace(string(body), `"dataset":`, `"dataset":"invalid!","extra":`, 1)} {
		call("POST", "/v1/candidate-generation-intents", bad, true, 422)
	}
	wrong := submission
	wrong.Command.PublicationSHA256 = strings.Repeat("f", 64)
	b, _ := json.Marshal(wrong)
	call("POST", "/v1/candidate-generation-intents", string(b), true, 409)
	wrong = submission
	wrong.Command.ReceiverID = "receiver-other_other_other"
	b, _ = json.Marshal(wrong)
	call("POST", "/v1/candidate-generation-intents", string(b), true, 409)
	wrong = submission
	wrong.Command.RequestID = "missing-pub"
	wrong.Command.PublicationID = "missing"
	b, _ = json.Marshal(wrong)
	call("POST", "/v1/candidate-generation-intents", string(b), true, 404)
	wrong = submission
	wrong.Dataset = append(bytes.Clone(raw), ' ')
	b, _ = json.Marshal(wrong)
	call("POST", "/v1/candidate-generation-intents", string(b), true, 422)
	for _, bad := range [][]string{{"candidate-generation-intent", "--id", first.Command.RequestID}, {"candidate-generation-intents", "--id", "filtered"}, append(append([]string{}, inspect...), "--artifact", "output"), {"candidate-generation-intent-start", "--id", "invalid"}} {
		var out, diag bytes.Buffer
		if err := Run(ctx, append(bad, "--api-url", base, "--receiver-id", pub.ReceiverID), &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid command emitted", err)
		}
	}
	if b, _ := os.ReadFile(df); !bytes.Equal(b, raw) {
		t.Fatal("dataset input rewritten")
	}
	if b, _ := os.ReadFile(pf); !bytes.Equal(b, encoded) {
		t.Fatal("plan input rewritten")
	}
	return first, h
}
func TestGenerationIntentClientRejectsForgedReceipts(t *testing.T) {
	s := storage.NewMemory()
	_, raw, pub := candidateHTTPFixture(t, s)
	ctx := context.Background()
	p, err := candidategeneration.NewPlan(pub.Sealed.Declaration, raw, "sha256:"+strings.Repeat("a", 64), "gpt-6.1-sol", "Synthetic threshold.", pub.PublishedAt)
	if err != nil {
		t.Fatal(err)
	}
	c := candidategeneration.Command{ReceiverID: pub.ReceiverID, RequestID: "generation", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: p}
	i, err := s.StartCandidateGeneration(ctx, c, raw, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CandidateGenerationIntentEvidence(ctx, c.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"receiver", "request", "plan", "intent-pin", "dataset", "qualified", "history-null", "history-gap", "history-provider", "history-owner", "unknown", "duplicate", "case", "trailing", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			args := []string{"candidate-generation-intent", "--id", c.RequestID, "--intent-sha256", i.SHA256, "--plan-sha256", p.SHA256, "--artifact", "dataset"}
			v := a
			v.Intent.Command.Plan = p
			var body []byte
			switch kind {
			case "history-null", "history-gap", "history-provider", "history-owner":
				hist := h
				hist.Intents = append([]candidategeneration.Intent{}, h.Intents...)
				switch kind {
				case "history-null":
					hist.Intents = nil
				case "history-gap":
					hist.Intents[0].Sequence = 2
					hist.Intents[0].SHA256 = ""
					hist.Intents[0].SHA256 = workerplan.Hash(hist.Intents[0])
				case "history-provider":
					hist.ProviderCalled = true
				case "history-owner":
					hist.ReceiverID = "receiver-other_other_other"
				}
				hist.SHA256 = ""
				hist.SHA256 = workerplan.Hash(hist)
				body, _ = json.Marshal(hist)
				args = []string{"candidate-generation-intents"}
			default:
				switch kind {
				case "receiver":
					v.Intent.Command.ReceiverID = "receiver-other_other_other"
				case "request":
					v.Intent.Command.RequestID = "other"
				case "plan":
					v.Intent.Command.Plan.ModelLabel = "other-model"
					v.Intent.Command.Plan.SHA256 = ""
					v.Intent.Command.Plan.SHA256 = workerplan.Hash(v.Intent.Command.Plan)
				case "intent-pin":
					v.Intent.CreatedAt = v.Intent.CreatedAt.Add(time.Second)
				case "dataset":
					v.Dataset = append(bytes.Clone(raw), ' ')
				case "qualified":
					v.Intent.QualificationCredit = true
				}
				v.Intent.SHA256 = ""
				v.Intent.SHA256 = workerplan.Hash(v.Intent)
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
					body = bytes.Repeat([]byte(" "), candidategeneration.MaxIntentArtifactBytes+1)
				}
			}
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })))
			defer server.Close()
			var out, diag bytes.Buffer
			args = append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID)
			if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("forged generation emitted", err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })))
	defer server.Close()
	dir := t.TempDir()
	pf, df := filepath.Join(dir, "plan.json"), filepath.Join(dir, "data.json")
	encoded, _ := json.Marshal(p)
	if err := os.WriteFile(pf, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(df, append(bytes.Clone(raw), ' '), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{strings.Repeat("f", 64), p.SHA256} {
		var out, diag bytes.Buffer
		args := []string{"candidate-generation-intent-start", "--api-url", server.URL, "--receiver-id", pub.ReceiverID, "--id", c.RequestID, "--publication", pub.ID, "--publication-sha256", workerplan.Hash(pub), "--plan", pf, "--plan-sha256", sha, "--dataset", df}
		if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid preflight accepted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid preflight contacted server")
	}
}
