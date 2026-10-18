package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestReviewedSwitchyardCaptureAdmissionCLI(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	a := switchyardAdmissionCLIFixture(t, s, receiver.ID, article)
	server := httptest.NewServer(service.Handler(s, "admission-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "admission-token")
	args := []string{"news-switchyard-admit", "--api-url", server.URL, "--id", a.ID, "--receiver-id", receiver.ID, "--capture-sha", a.SHA256, "--result-sha", a.Finished.Result.SHA256}
	var out, errs bytes.Buffer
	if e := Run(ctx, args, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("missing approval confirmation accepted")
	}
	if _, e := s.SwitchyardCaptureAdmission(ctx, a.ID); e == nil {
		t.Fatal("CLI wrote before confirmation")
	}
	if e := Run(ctx, append(args, "--approve-candidates"), &out, &errs); e != nil {
		t.Fatal(e)
	}
	var approval extraction.SwitchyardAdmission
	if json.Unmarshal(out.Bytes(), &approval) != nil || approval.ValidateMetadata() != nil || approval.QualificationCredit {
		t.Fatal(out.String())
	}
	out.Reset()
	if e := Run(ctx, []string{"news-switchyard-admission", "--api-url", server.URL, "--id", a.ID}, &out, &errs); e != nil {
		t.Fatal(e)
	}
	var inspected extraction.SwitchyardAdmission
	_ = json.Unmarshal(out.Bytes(), &inspected)
	if !reflect.DeepEqual(inspected, approval) {
		t.Fatal("inspection changed approval")
	}
	record, e := s.NewsRecord(ctx, approval.NewsReceipt.RecordKey)
	if e != nil || approval.Validate(a, record) != nil {
		t.Fatal("missing authoritative provenance", e)
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v1/news-switchyard-captures/reviewed/admit", `{"receiver_id":"x","approval_code":"approve_switchyard_claim_candidates_v1","claims":[]}`, 400},
		{"/v1/news-switchyard-captures/reviewed/admit", `{"receiver_id":"x","receiver_id":"y"}`, 400},
		{"/v1/news-switchyard-captures/reviewed/admit", `{"Receiver_ID":"x"}`, 400},
	} {
		req, _ := http.NewRequest("POST", server.URL+tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Authorization", "Bearer admission-token")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatal(tc.path, res.StatusCode)
		}
	}
	// Ordinary admission cannot accept a fabricated extraction provenance record.
	requestRaw, _ := json.Marshal(record.Recording)
	req, _ := http.NewRequest("POST", server.URL+"/v1/news-recordings?receiver_id="+receiver.ID, bytes.NewReader(requestRaw))
	req.Header.Set("Authorization", "Bearer admission-token")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = res.Body.Close()
	if res.StatusCode != 422 {
		t.Fatal("generic admission accepted extraction provenance", res.StatusCode)
	}
	// Even a resealed metadata response must match the targeted retained links.
	forged := approval
	forged.Provenance.ModelArtifactSHA256 = strings.Repeat("f", 64)
	forged.SHA256 = ""
	sealed, _ := json.Marshal(forged)
	forged.SHA256 = news.SHA256(string(sealed))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/news-switchyard-captures/reviewed/admission":
			_ = json.NewEncoder(w).Encode(forged)
		case "/v1/news-switchyard-captures/reviewed":
			_ = json.NewEncoder(w).Encode(a)
		default:
			_ = json.NewEncoder(w).Encode(jsonNewsInspection{RecordKey: approval.NewsReceipt.RecordKey, Result: record.Result, Receipt: record.Receipt})
		}
	}))
	defer bad.Close()
	out.Reset()
	if e := Run(ctx, []string{"news-switchyard-admission", "--api-url", bad.URL, "--id", a.ID}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("forged provenance printed")
	}
}

func switchyardAdmissionCLIFixture(t *testing.T, s *storage.Memory, receiver string, article news.Article) extraction.SwitchyardCapture {
	t.Helper()
	f := extraction.FixtureResponse()
	q := extraction.QuoteResponse{Outcome: f.Outcome, ReasonCode: f.ReasonCode, Claims: []extraction.QuoteClaim{}}
	for _, c := range f.Claims {
		claim := extraction.QuoteClaim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []extraction.Quote{}}
		for _, e := range c.Evidence {
			claim.Evidence = append(claim.Evidence, extraction.Quote{Quote: e.Quote})
		}
		q.Claims = append(q.Claims, claim)
	}
	content, _ := json.Marshal(q)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "openai/gpt-6.1-sol", "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	}))
	var all []extraction.SwitchyardReceipt
	_, e := extraction.RunSwitchyard(context.Background(), article, "reviewed-event", extraction.SwitchyardConfig{Endpoint: model.URL, Model: "openai/gpt-6.1-sol", MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r extraction.SwitchyardReceipt) error { all = append(all, r); return nil })
	model.Close()
	if e != nil || len(all) != 2 {
		t.Fatal(e)
	}
	a, e := s.ImportSwitchyardCapture(context.Background(), receiver, "reviewed", all[0], &all[1], time.Now)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
