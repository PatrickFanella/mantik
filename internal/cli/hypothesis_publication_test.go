package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestHypothesisPublicationCLIAndAPI(t *testing.T) {
	s := storage.NewMemory()
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, r, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler(s, "publication-test-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "publication-test-token")
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(args, err)
		}
		return out.Bytes()
	}
	var empty hypothesis.Catalogue
	if err = json.Unmarshal(run("hypotheses"), &empty); err != nil || empty.Validate() != nil || empty.ReceiverID != catalog.ReceiverID || empty.PublicationCount != 0 {
		t.Fatal(empty, err)
	}
	var a review.View
	if err = json.Unmarshal(run("news-review-start", "--id", "review", "--receiver-id", catalog.ReceiverID, "--case", admitted.Matches[0].CaseID, "--version", "1"), &a); err != nil {
		t.Fatal(err)
	}
	c := a.Attempt.Pin.Group.Claims[0]
	d := hypothesis.Fixture()
	d.Origins = []hypothesis.Origin{{Reference: c.Reference, ClaimKey: c.Claim.Key, RecordingSHA256: c.Claim.RecordingSHA256}}
	steps := []investigation.Step{{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: c.Claim.Key}}, {Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{c.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{c.Claim.Key}}}}}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "steps.json")
	raw, _ := json.Marshal(steps)
	if err = os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run("news-review-complete", "--id", "review", "--receiver-id", catalog.ReceiverID, "--model-fixture", fixture)
	file := filepath.Join(dir, "declaration.json")
	raw, _ = json.Marshal(d)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var p hypothesis.Publication
	publish := []string{"hypothesis-publish", "--id", "publication", "--review", "review", "--receiver-id", catalog.ReceiverID, "--file", file}
	if err = json.Unmarshal(run(publish...), &p); err != nil || !p.OriginBindingVerified || p.Sealed.QualificationCredit {
		t.Fatal(p, err)
	}
	var q hypothesis.Question
	if err = json.Unmarshal(run("hypothesis", "--id", p.Sealed.ID), &q); err != nil || len(q.Publications) != 1 || !reflect.DeepEqual(q.Publications[0], p) {
		t.Fatal(q, err)
	}
	var discovered hypothesis.Catalogue
	if err = json.Unmarshal(run("hypotheses"), &discovered); err != nil || discovered.Validate() != nil || discovered.PublicationCount != 1 || discovered.QuestionCount != 1 || !reflect.DeepEqual(discovered.Questions[0], q) {
		t.Fatal(discovered, err)
	}
	// A complete service catalogue can feed an explicitly local recorded review.
	// Its new news claim refs belong to the local journal, not the old publication.
	journalPath := filepath.Join(dir, "local-journal")
	j, err := news.OpenJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Retain(r); err != nil {
		t.Fatal(err)
	}
	groups, err := j.Events(r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	localKey := groups.Groups[0].Claims[0].Claim.Key
	ref := d.Methods[0]
	reuseSteps := []investigation.Step{
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: localKey}},
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_native_method", MethodID: ref.ID, MethodSHA256: ref.SHA256}},
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_hypothesis", HypothesisID: p.Sealed.ID, DeclarationSHA256: p.Sealed.DeclarationSHA256}},
		{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"inspect_existing_question"}, ClaimKeys: []string{localKey}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{localKey}, Methods: d.Methods, Reuse: &question.Ref{ID: p.Sealed.ID, DeclarationSHA256: p.Sealed.DeclarationSHA256}}}},
	}
	reuseFile, catalogueFile := filepath.Join(dir, "reuse.json"), filepath.Join(dir, "catalogue.json")
	reuseRaw, _ := json.Marshal(reuseSteps)
	catalogueRaw, _ := json.Marshal(discovered)
	for path, content := range map[string][]byte{reuseFile: reuseRaw, catalogueFile: catalogueRaw} {
		if err = os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reuseArgs := []string{"research-review", "--journal", journalPath, "--event", groups.Groups[0].ID, "--as-of", r.ReceivedAt.Format(time.RFC3339Nano), "--model-fixture", reuseFile, "--type", investigation.ReuseTypeID, "--hypothesis-catalogue", catalogueFile}
	var reviewOutput bytes.Buffer
	if err = Run(ctx, reuseArgs, &reviewOutput, &reviewOutput); err != nil {
		t.Fatal(err)
	}
	var local struct {
		Result investigation.Result `json:"result"`
	}
	inventory, _ := discovered.ReviewInventory()
	if err = json.Unmarshal(reviewOutput.Bytes(), &local); err != nil || local.Result.Outcome != "propose_hypothesis" || local.Result.HypothesisInventorySHA256 != inventory.SHA256 || local.Result.HypothesisCatalogueSHA256 != discovered.SHA256 || local.Result.Proposal.Candidate.Reuse.ID != p.Sealed.ID || local.Result.QualificationCredit {
		t.Fatal(local, err)
	}
	for _, malformed := range [][]byte{
		[]byte(strings.Replace(string(catalogueRaw), `"schema":`, `"schema":"duplicate","schema":`, 1)),
		[]byte(strings.Replace(string(catalogueRaw), `"qualification_credit":false`, `"qualification_credit":true`, 1)),
		append(append([]byte{}, catalogueRaw...), []byte(` {}`)...),
	} {
		if err = os.WriteFile(catalogueFile, malformed, 0600); err != nil {
			t.Fatal(err)
		}
		reviewOutput.Reset()
		if err = Run(ctx, reuseArgs, &reviewOutput, &reviewOutput); err == nil || reviewOutput.Len() != 0 {
			t.Fatal("invalid catalogue emitted review", err)
		}
	}
	if err = os.WriteFile(catalogueFile, catalogueRaw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, badArgs := range [][]string{
		reuseArgs[:len(reuseArgs)-2],
		append(append([]string{}, reuseArgs...), "--type", investigation.MethodTypeID),
		append(append([]string{}, reuseArgs...), "--hypothesis-catalogue", dir),
	} {
		reviewOutput.Reset()
		if err = Run(ctx, badArgs, &reviewOutput, &reviewOutput); err == nil {
			t.Fatal("invalid catalogue/profile arguments accepted")
		}
	}
	for path, expected := range map[string][]byte{reuseFile: reuseRaw, catalogueFile: catalogueRaw} {
		if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, expected) {
			t.Fatal("review changed private input", path, err)
		}
	}
	var serverStart review.View
	if err = json.Unmarshal(run("news-review-start", "--id", "service-reuse", "--receiver-id", catalog.ReceiverID, "--case", admitted.Matches[0].CaseID, "--version", "1", "--type", investigation.ReuseTypeID), &serverStart); err != nil || serverStart.Attempt.HypothesisInventory == nil || serverStart.Attempt.HypothesisInventory.CatalogueSHA256 != discovered.SHA256 {
		t.Fatal(serverStart, err)
	}
	serverSteps := append([]investigation.Step{}, reuseSteps...)
	rawSteps, _ := json.Marshal(serverSteps)
	_ = json.Unmarshal(rawSteps, &serverSteps)
	serverSteps[0].Tool.ClaimKey = c.Claim.Key
	serverSteps[len(serverSteps)-1].Proposal.ClaimKeys = []string{c.Claim.Key}
	serverSteps[len(serverSteps)-1].Proposal.Candidate.EvidenceKeys = []string{c.Claim.Key}
	serverFile := filepath.Join(dir, "server-reuse.json")
	rawSteps, _ = json.Marshal(serverSteps)
	if err = os.WriteFile(serverFile, rawSteps, 0600); err != nil {
		t.Fatal(err)
	}
	var serverDone review.View
	if err = json.Unmarshal(run("news-review-complete", "--id", "service-reuse", "--receiver-id", catalog.ReceiverID, "--model-fixture", serverFile), &serverDone); err != nil || serverDone.Attempt.Result.Schema != investigation.ReuseSchema || serverDone.Attempt.Result.Outcome != "propose_hypothesis" || !review.ResultMatchesProfile(serverDone.Attempt, *serverDone.Attempt.Result) {
		t.Fatal(serverDone, err)
	}
	var serverPublication hypothesis.Publication
	if err = json.Unmarshal(run("hypothesis-publish", "--id", "service-reused", "--review", "service-reuse", "--receiver-id", catalog.ReceiverID, "--file", file), &serverPublication); err != nil || !serverPublication.QuestionReused || serverPublication.Sealed.ID != p.Sealed.ID {
		t.Fatal(serverPublication, err)
	}
	if content, err := os.ReadFile(file); err != nil || !bytes.Equal(content, raw) {
		t.Fatal("CLI changed declaration", err)
	}
	command := hypothesis.PublicationCommand{ReceiverID: catalog.ReceiverID, RequestID: "second", ReviewID: "review", Declaration: d}
	body, _ := json.Marshal(command)
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer publication-test-token")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(method, path, res.StatusCode, want)
		}
	}
	call("POST", "/v1/hypothesis-publications", string(body), false, 401)
	call("GET", "/v1/hypotheses/"+p.Sealed.ID, "", false, 401)
	call("GET", "/v1/hypotheses", "", false, 401)
	call("GET", "/v1/hypotheses?best=true", "", true, 422)
	call("GET", "/v1/hypotheses?limit=1", "", true, 422)
	call("POST", "/v1/news-reviews", `{"receiver_id":"`+catalog.ReceiverID+`","request_id":"forged-pin","case_id":"`+admitted.Matches[0].CaseID+`","version":1,"type_id":"news-reuse-review-v1","hypothesis_inventory":{}}`, true, 400)
	call("POST", "/v1/news-reviews/service-reuse/complete", `{"receiver_id":"`+catalog.ReceiverID+`","steps":[],"hypothesis_inventory":{}}`, true, 400)
	call("POST", "/v1/hypotheses", `{}`, true, 405)
	call("POST", "/v1/news-reviews", `{"receiver_id":"`+catalog.ReceiverID+`","request_id":"unsupported-reuse","case_id":"`+admitted.Matches[0].CaseID+`","version":1,"type_id":"news-reuse-review-v1"}`, true, 200)
	call("POST", "/v1/hypothesis-publications", strings.Replace(string(body), `"request_id":`, `"request_id":"forged","request_id":`, 1), true, 422)
	call("POST", "/v1/hypothesis-publications", strings.Replace(string(body), `"declaration":`, `"sealed":{"qualification_credit":true},"declaration":`, 1), true, 422)
	call("GET", "/v1/hypotheses/"+p.Sealed.ID+"?original=true", "", true, 422)
	call("GET", "/v1/hypotheses/hypothesis-"+strings.Repeat("0", 64), "", true, 404)
	call("POST", "/v1/hypothesis-publications", strings.Replace(string(body), `"review_id":"review"`, `"review_id":"missing"`, 1), true, 404)
	var beforeClose hypothesis.Catalogue
	if err = json.Unmarshal(run("hypotheses"), &beforeClose); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseNewsCase(ctx, catalog.ReceiverID, admitted.Matches[0].CaseID, 1, time.Now); err != nil {
		t.Fatal(err)
	}
	run(publish...)
	var closed hypothesis.Catalogue
	if err = json.Unmarshal(run("hypotheses"), &closed); err != nil || !reflect.DeepEqual(beforeClose, closed) {
		t.Fatal("closing a case changed discovery history", err)
	}
	call("POST", "/v1/hypothesis-publications", string(body), true, 409)
	for _, args := range [][]string{{"hypothesis"}, {"hypothesis-publish", "--id", "empty"}, {"hypotheses", "--id", p.Sealed.ID}, {"hypothesis", "--id", p.Sealed.ID, "--api-url", "https://example.invalid"}} {
		var out bytes.Buffer
		if err = Run(ctx, args, &out, &out); err == nil {
			t.Fatal("incomplete args accepted")
		}
	}
}

func TestHypothesisCatalogueCLIRejectsInvalidReceipt(t *testing.T) {
	c, err := hypothesis.MakeCatalogue("receiver-abcdefghijklmnop", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.PublicationCount = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(c)
	}))
	defer server.Close()
	var out bytes.Buffer
	if err = Run(context.Background(), []string{"hypotheses", "--api-url", server.URL}, &out, &out); err == nil || out.Len() != 0 {
		t.Fatal("corrupt catalogue emitted", out.String(), err)
	}
}
