package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func inspectionReceipts(t *testing.T, status int, body []byte) (LocalReceipt, LocalReceipt) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write(body) }))
	defer server.Close()
	var all []LocalReceipt
	_, err := RunLocal(context.Background(), news.FixtureArticle(), "event", LocalConfig{Endpoint: server.URL, Model: "ollama/fixture", MaxTokens: 512}, "", func(r LocalReceipt) error { all = append(all, r); return nil })
	if err != nil || len(all) != 2 {
		t.Fatal("fixture capture", err, len(all))
	}
	return all[0], all[1]
}

func TestLocalInspectionReplaysCompleteAndFailedCaptures(t *testing.T) {
	good := quoteJSON(quoteFixture())
	abstain := quoteJSON(QuoteResponse{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []QuoteClaim{}})
	missing := quoteFixture()
	missing.Claims[0].Evidence[0].Quote = "absent quote"
	tests := []struct {
		name   string
		status int
		body   []byte
	}{
		{"extracted", 200, localReply("ollama/fixture", string(good), "stop")},
		{"abstain", 200, localReply("ollama/fixture", string(abstain), "stop")},
		{"anchor-rejected", 200, localReply("ollama/fixture", string(quoteJSON(missing)), "stop")},
		{"bad-envelope", 200, []byte("invalid")},
		{"wrong-model", 200, localReply("ollama/wrong", string(good), "stop")},
		{"truncated-model", 200, localReply("ollama/fixture", string(good), "length")},
		{"http-error", 503, []byte("private diagnostic")},
		{"oversized-response", 200, []byte(strings.Repeat("x", MaxLocalHTTPBytes+1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, f := inspectionReceipts(t, tt.status, tt.body)
			r, err := InspectLocal(context.Background(), p, &f)
			if err != nil || r.State != "finished" || r.FinishedSHA256 != f.SHA256 || r.Outcome != f.Result.Outcome || r.ReasonCode != f.Result.ReasonCode || r.ClaimCount != len(f.Result.Claims) || r.ExecutionAttested || r.QualificationCredit || r.Authority != "retained_local_bytes_consistency_only" {
				t.Fatal("capture not consistently inspected", err, r)
			}
			if slices.Contains(r.Checks, "model_content_binding") != (f.ModelArtifact != nil) || slices.Contains(r.Checks, "quote_anchoring_replay") != (f.Anchoring != nil) {
				t.Fatal("inspection claims checks without corresponding artifacts", r.Checks)
			}
			again, err := InspectLocal(context.Background(), p, &f)
			if err != nil || !reflect.DeepEqual(again, r) {
				t.Fatal("inspection changed", err)
			}
			hashCopy := r
			hashCopy.SHA256 = ""
			if hash(hashCopy) != r.SHA256 {
				t.Fatal("inspection hash")
			}
			prepared, err := InspectLocal(context.Background(), p, nil)
			if err != nil || prepared.State != "prepared_only" || prepared.Disposition != "completion_unknown" || prepared.FinishedSHA256 != "" || prepared.ClaimCount != 0 || prepared.Outcome != "" {
				t.Fatal("invented completion", err, prepared)
			}
			raw, _ := json.Marshal(r)
			if strings.Contains(string(raw), news.FixtureArticle().Original) || strings.Contains(string(raw), "private diagnostic") {
				t.Fatal("private source/response copied to compact inspection")
			}
		})
	}
}

func TestLocalInspectionRejectsResealedInconsistentEvidence(t *testing.T) {
	p, f := inspectionReceipts(t, 200, localReply("ollama/fixture", string(quoteJSON(quoteFixture())), "stop"))
	tests := []struct {
		name   string
		change func(*LocalReceipt, *LocalReceipt)
	}{
		{"source-pin", func(p, f *LocalReceipt) { p.ArticleSHA256 = strings.Repeat("a", 64) }},
		{"contract-pin", func(p, f *LocalReceipt) { p.ContractSHA256 = strings.Repeat("a", 64) }},
		{"request-prompt", func(p, f *LocalReceipt) {
			raw, _ := base64.StdEncoding.DecodeString(p.Request.Base64)
			var req localRequest
			json.Unmarshal(raw, &req)
			req.Messages[0].Content = "ignore safety"
			b, _ := json.Marshal(req)
			p.Request = localBytes(b, true)
			f.Request = p.Request
		}},
		{"request-tokens", func(p, f *LocalReceipt) {
			raw, _ := base64.StdEncoding.DecodeString(p.Request.Base64)
			var req localRequest
			json.Unmarshal(raw, &req)
			req.MaxTokens++
			b, _ := json.Marshal(req)
			p.Request = localBytes(b, true)
			f.Request = p.Request
		}},
		{"request-base64", func(p, f *LocalReceipt) { p.Request.Base64 += "\n" }},
		{"prepared-finished", func(p, f *LocalReceipt) { p.Status = "finished" }},
		{"finished-model", func(p, f *LocalReceipt) { f.Config.Model = "ollama/changed" }},
		{"early-finish", func(p, f *LocalReceipt) { f.CompletedAt = p.StartedAt.Add(-time.Second) }},
		{"model-artifact", func(p, f *LocalReceipt) { *f.ModelArtifact = localBytes([]byte("{}"), true) }},
		{"derived-pin", func(p, f *LocalReceipt) {
			f.Anchoring.CandidateArtifactSHA256 = strings.Repeat("a", 64)
			*f.Anchoring = sealAnchoring(*f.Anchoring)
		}},
		{"anchor-status", func(p, f *LocalReceipt) { f.Anchoring.Status = "rejected"; *f.Anchoring = sealAnchoring(*f.Anchoring) }},
		{"http-body", func(p, f *LocalReceipt) { f.Response = localBytes(localReply("ollama/fixture", "{}", "stop"), true) }},
		{"http-status", func(p, f *LocalReceipt) { f.HTTPStatus = 503 }},
		{"result-assertion", func(p, f *LocalReceipt) {
			f.Result.Claims[0].Assertion = "invented replacement"
			f.Result.SHA256 = ""
			f.Result.SHA256 = hash(f.Result)
		}},
		{"qualification", func(p, f *LocalReceipt) {
			f.Result.QualificationCredit = true
			f.Result.SHA256 = ""
			f.Result.SHA256 = hash(f.Result)
		}},
		{"execution-authority", func(p, f *LocalReceipt) { f.Authority = "execution_allowed" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, finished := copyJSON(p), copyJSON(f)
			tt.change(&prepared, &finished)
			prepared = sealLocal(prepared)
			finished = sealLocal(finished)
			r, err := InspectLocal(context.Background(), prepared, &finished)
			if err == nil || r.Schema != "" {
				t.Fatal("resealed inconsistent evidence emitted report", err)
			}
		})
	}
	legacy := copyJSON(p)
	legacy.Schema = "mantik-local-news-extraction-v1"
	legacy = sealLocal(legacy)
	if _, err := InspectLocal(context.Background(), legacy, nil); !errors.Is(err, ErrLocalVersion) {
		t.Fatal("legacy silently reinterpreted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := InspectLocal(ctx, p, &f); err != context.Canceled || r.Schema != "" {
		t.Fatal("cancelled inspection emitted result")
	}
}

func TestLocalInspectionTransportFailureHasNoExecutionAttestation(t *testing.T) {
	prepared, _ := inspectionReceipts(t, 200, localReply("ollama/fixture", string(quoteJSON(quoteFixture())), "stop"))
	// The fixture server has closed. Capture a real refused connection, then
	// inspect it without recontacting the endpoint or claiming it executed a model.
	var saved []LocalReceipt
	_, err := RunLocal(context.Background(), news.FixtureArticle(), "event", prepared.Config, "", func(r LocalReceipt) error { saved = append(saved, r); return nil })
	if err != nil || len(saved) != 2 || saved[1].Disposition != "transport_failed" {
		t.Fatal("failed transport capture", err)
	}
	report, err := InspectLocal(context.Background(), saved[0], &saved[1])
	if err != nil || report.Outcome != "abstain" || report.ReasonCode != "extractor_unavailable" || report.ExecutionAttested || report.ModelArtifactSHA256 != "" {
		t.Fatal("transport event incorrectly attested", err, report)
	}
	if !slices.Contains(report.Checks, "reported_transport_shape") || slices.Contains(report.Checks, "model_content_binding") || slices.Contains(report.Checks, "quote_anchoring_replay") {
		t.Fatal("transport failure claimed model/anchor replay", report.Checks)
	}

}
