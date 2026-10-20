package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type candidateHTTPBackend interface {
	storage.Store
	storage.NewsStore
	storage.ReviewStore
	storage.HypothesisStore
	storage.CandidateStore
}

func candidateHTTPFixture(t *testing.T, s candidateHTTPBackend) (candidate.Package, []byte, hypothesis.Publication) {
	t.Helper()
	ctx := context.Background()
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, news.Fixture(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.StartReview(ctx, catalog.ReceiverID, "review", admitted.Matches[0].CaseID, 1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	claim := a.Attempt.Pin.Group.Claims[0]
	d := hypothesis.Fixture()
	d.Origins = []hypothesis.Origin{{Reference: claim.Reference, ClaimKey: claim.Claim.Key, RecordingSHA256: claim.Claim.RecordingSHA256}}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: claim.Claim.Key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{claim.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{claim.Claim.Key}}}})
	if _, err := s.CompleteReview(ctx, catalog.ReceiverID, "review", []json.RawMessage{tool, proposal}, time.Now); err != nil {
		t.Fatal(err)
	}
	pub, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "publication", "review", d, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := dataset.FixtureDataset()
	raw, _ := json.Marshal(data)
	raw = append(raw, '\n')
	p, err := candidate.New([]byte("invalid Go retained for a later failure\n"), d, raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return p, raw, pub
}

func TestCandidateRegistryCLIAndHTTP(t *testing.T) {
	candidateHTTPContracts(t, storage.NewMemory(), nil)
}

// This test is run only by the disposable integration harness, never against a
// development URL. Its own database is created inside that harness's container.
func TestPostgresContractsCandidateRegistryHTTP(t *testing.T) {
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	db := "mantik_test_candidate_http_" + strings.ToLower(rand.Text())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
			t.Error(err)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + db
	s, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	candidateHTTPContracts(t, s, func() candidateHTTPBackend {
		s.Close()
		next, err := storage.OpenPostgres(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(next.Close)
		return next
	})
}

func candidateHTTPContracts(t *testing.T, s candidateHTTPBackend, reopen func() candidateHTTPBackend) {
	ctx := context.Background()
	p, raw, pub := candidateHTTPFixture(t, s)
	before, _ := s.Snapshot(ctx)
	server := httptest.NewServer(service.Handler(s, "candidate-http-token"))
	defer func() { server.Close() }()
	t.Setenv("MANTIK_API_TOKEN", "candidate-http-token")
	dir := t.TempDir()
	candidateFile, dataFile := filepath.Join(dir, "candidate.json"), filepath.Join(dir, "dataset.json")
	encoded, _ := json.Marshal(p)
	if err := os.WriteFile(candidateFile, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diagnostics bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID)
		if err := Run(ctx, args, &out, &diagnostics); err != nil {
			t.Fatal(args, err, diagnostics.String())
		}
		return out.Bytes()
	}
	var empty candidatelineage.Registry
	if err := json.Unmarshal(run("candidate-registrations"), &empty); err != nil || empty.Validate() != nil || len(empty.Registrations) != 0 {
		t.Fatal(empty, err)
	}
	args := []string{"candidate-register", "--id", "root", "--publication", pub.ID, "--candidate", candidateFile, "--candidate-sha256", p.SHA256, "--dataset", dataFile}
	var first, retry candidatelineage.Registration
	if err := json.Unmarshal(run(args...), &first); err != nil || first.Validate() != nil || first.Candidate.SHA256 != p.SHA256 {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("retry changed", err)
	}
	inspect := []string{"candidate-registration", "--id", "root", "--registration-sha256", first.SHA256}
	if err := json.Unmarshal(run(inspect...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("inspection changed", err)
	}
	export := []string{"candidate-dataset", "--id", "root", "--registration-sha256", first.SHA256, "--format", "raw"}
	if !bytes.Equal(run(export...), raw) {
		t.Fatal("export changed original whitespace/bytes")
	}
	var artifact candidatelineage.Artifact
	if err := json.Unmarshal(run("candidate-dataset", "--id", "root", "--registration-sha256", first.SHA256), &artifact); err != nil || artifact.Validate() != nil || !bytes.Equal(artifact.Dataset, raw) {
		t.Fatal(artifact, err)
	}
	child := append([]string(nil), args...)
	child[2] = "child"
	child = append(child, "--parent", "root")
	var second candidatelineage.Registration
	if err := json.Unmarshal(run(child...), &second); err != nil || second.ParentSHA256 != first.SHA256 {
		t.Fatal(second, err)
	}
	var all candidatelineage.Registry
	if err := json.Unmarshal(run("candidate-registrations"), &all); err != nil || all.Validate() != nil || len(all.Registrations) != 2 || all.Registrations[1].ID != "child" {
		t.Fatal(all, err)
	}
	if reopen != nil {
		server.Close()
		s = reopen()
		server = httptest.NewServer(service.Handler(s, "candidate-http-token"))
		if err := json.Unmarshal(run(inspect...), &retry); err != nil || !reflect.DeepEqual(first, retry) {
			t.Fatal("HTTP restart changed registration", err)
		}
		if !bytes.Equal(run(export...), raw) {
			t.Fatal("HTTP restart changed exact bytes")
		}
		var restored candidatelineage.Registry
		if err := json.Unmarshal(run("candidate-registrations"), &restored); err != nil || !reflect.DeepEqual(all, restored) {
			t.Fatal("HTTP restart changed registry", err)
		}
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
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
	submission := candidatelineage.Submission{Command: candidatelineage.Command{ReceiverID: pub.ReceiverID, RequestID: "next", PublicationID: pub.ID, Candidate: p}, Dataset: raw}
	body, _ := json.Marshal(submission)
	for _, path := range []string{"/v1/candidates", "/v1/candidates/root", "/v1/candidates/root/dataset"} {
		call("GET", path, "", false, 401)
		call("GET", path+"?best=true", "", true, 422)
	}
	call("POST", "/v1/candidates", string(body), false, 401)
	call("GET", "/v1/candidates/missing", "", true, 404)
	call("GET", "/v1/candidates/missing/dataset", "", true, 404)
	call("POST", "/v1/candidates/root/launch", "{}", true, 404)
	call("POST", "/v1/candidates", strings.Replace(string(body), `"receiver_id":"`+pub.ReceiverID+`"`, `"receiver_id":"receiver-other_other_other"`, 1), true, 409)
	for _, bad := range []string{string(body) + "{}", strings.Replace(string(body), `"command":`, `"Command":`, 1), strings.Replace(string(body), `"command":`, `"execution_started":true,"command":`, 1), strings.Replace(string(body), `"dataset":`, `"dataset":"duplicate","dataset":`, 1), strings.Replace(string(body), `"dataset":`, `"dataset":"not-base64!","ignored":`, 1)} {
		call("POST", "/v1/candidates", bad, true, 422)
	}
	for _, bad := range [][]string{{"candidate-registration", "--id", "root"}, {"candidate-dataset", "--id", "root", "--registration-sha256", first.SHA256, "--format", "text"}, {"candidate-registrations", "--id", "root"}, {"candidate-register", "--id", "oops"}} {
		var out, diagnostics bytes.Buffer
		if err := Run(ctx, append(bad, "--api-url", server.URL, "--receiver-id", pub.ReceiverID), &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatal("invalid command emitted data", err)
		}
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("registry changed operational state")
	}
	if b, _ := os.ReadFile(dataFile); !bytes.Equal(b, raw) {
		t.Fatal("CLI rewrote input")
	}
}

func TestCandidateRegistryClientRejectsWrongPins(t *testing.T) {
	s := storage.NewMemory()
	p, raw, pub := candidateHTTPFixture(t, s)
	r, err := s.RegisterCandidate(context.Background(), candidatelineage.Command{ReceiverID: pub.ReceiverID, RequestID: "root", PublicationID: pub.ID, Candidate: p}, raw, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := s.CandidateRegistry(context.Background())
	a := candidatelineage.Artifact{Schema: candidatelineage.ArtifactSchema, Registration: r, Dataset: raw}
	for _, kind := range []string{"receiver", "registration", "artifact", "null-history", "qualified-history", "unknown-field", "duplicate-field", "trailing-document"} {
		t.Run(kind, func(t *testing.T) {
			var body []byte
			args := []string{"candidate-registration", "--id", "root", "--registration-sha256", r.SHA256}
			switch kind {
			case "receiver":
				v := r
				v.ReceiverID = "receiver-other_other_other"
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
			case "registration":
				v := r
				v.ID = "other"
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
			case "artifact":
				v := a
				v.Dataset = append(bytes.Clone(raw), '\n')
				body, _ = json.Marshal(v)
				args = []string{"candidate-dataset", "--id", "root", "--registration-sha256", r.SHA256, "--format", "raw"}
			case "null-history", "qualified-history":
				v := h
				if kind == "null-history" {
					v.Registrations = nil
				} else {
					v.QualificationCredit = true
				}
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(v)
				body, _ = json.Marshal(v)
				args = []string{"candidate-registrations"}
			default:
				body, _ = json.Marshal(r)
				if kind == "trailing-document" {
					body = append(body, []byte("{}")...)
				} else if kind == "unknown-field" {
					body = []byte(strings.Replace(string(body), `"schema":`, `"extra":true,"schema":`, 1))
				} else {
					body = []byte(strings.Replace(string(body), `"schema":`, `"schema":"duplicate","schema":`, 1))
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			var out, diagnostics bytes.Buffer
			args = append(args, "--api-url", server.URL, "--receiver-id", pub.ReceiverID)
			if err := Run(context.Background(), args, &out, &diagnostics); err == nil || out.Len() != 0 {
				t.Fatal("forged receipt emitted", err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, args := range [][]string{{"candidate-register", "--id", "bad"}, {"candidate-dataset", "--id", "root", "--registration-sha256", "wrong"}, {"candidate-registrations", "--api-url", "http://user:secret@localhost"}} {
		var out, diagnostics bytes.Buffer
		args = append(args, "--receiver-id", pub.ReceiverID)
		if !strings.Contains(strings.Join(args, " "), "--api-url") {
			args = append(args, "--api-url", server.URL)
		}
		if err := Run(context.Background(), args, &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatal("invalid preflight accepted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid preflight contacted server")
	}
}
