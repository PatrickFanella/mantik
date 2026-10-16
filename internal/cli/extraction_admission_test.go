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

func TestReviewedExtractionAdmissionCLI(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	if _, e := s.StartExtraction(ctx, receiver.ID, "reviewed", article, "reviewed-event", time.Now); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(extraction.FixtureResponse())
	a, e := s.CompleteExtraction(ctx, receiver.ID, "reviewed", raw, time.Now)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(service.Handler(s, "admission-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "admission-token")
	args := []string{"news-extraction-admit", "--api-url", server.URL, "--id", a.ID, "--receiver-id", receiver.ID, "--attempt-sha", a.SHA256, "--result-sha", a.Result.SHA256}
	var out, errs bytes.Buffer
	if e := Run(ctx, args, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("missing approval confirmation accepted")
	}
	if _, e := s.ExtractionAdmission(ctx, a.ID); e == nil {
		t.Fatal("CLI wrote before confirmation")
	}
	if e := Run(ctx, append(args, "--approve-candidates"), &out, &errs); e != nil {
		t.Fatal(e)
	}
	var approval extraction.Admission
	if json.Unmarshal(out.Bytes(), &approval) != nil || approval.ValidateMetadata() != nil || approval.QualificationCredit {
		t.Fatal(out.String())
	}
	out.Reset()
	if e := Run(ctx, []string{"news-extraction-admission", "--api-url", server.URL, "--id", a.ID}, &out, &errs); e != nil {
		t.Fatal(e)
	}
	var inspected extraction.Admission
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
		{"/v1/news-extractions/reviewed/admit", `{"receiver_id":"x","approval_code":"approve_claim_candidates_v1","claims":[]}`, 400},
		{"/v1/news-extractions/reviewed/admit", `{"receiver_id":"x","receiver_id":"y"}`, 400},
		{"/v1/news-extractions/reviewed/admit", `{"Receiver_ID":"x"}`, 400},
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
	forged.ArtifactSHA256 = strings.Repeat("f", 64)
	forged.SHA256 = ""
	sealed, _ := json.Marshal(forged)
	forged.SHA256 = news.SHA256(string(sealed))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/news-extractions/reviewed/admission":
			_ = json.NewEncoder(w).Encode(forged)
		case "/v1/news-extractions/reviewed":
			_ = json.NewEncoder(w).Encode(a)
		default:
			_ = json.NewEncoder(w).Encode(jsonNewsInspection{RecordKey: approval.NewsReceipt.RecordKey, Result: record.Result, Receipt: record.Receipt})
		}
	}))
	defer bad.Close()
	out.Reset()
	if e := Run(ctx, []string{"news-extraction-admission", "--api-url", bad.URL, "--id", a.ID}, &out, &errs); e == nil || out.Len() != 0 {
		t.Fatal("forged provenance printed")
	}
}
