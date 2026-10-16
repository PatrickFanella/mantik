package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func localReply(model, content, finish string) []byte {
	b, _ := json.Marshal(map[string]any{"id": "fixture-response", "object": "chat.completion", "created": 1, "model": model, "usage": map[string]int{"total_tokens": 10}, "choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": map[string]any{"role": "assistant", "content": content}}}})
	return b
}
func TestLocalExtractionRetainsBeforeSendAndChecksCandidates(t *testing.T) {
	const model = "ollama/fixture:small"
	var calls atomic.Int32
	var prepared atomic.Bool
	content, _ := json.Marshal(quoteFixture())
	envelope := localReply(model, string(content), "stop")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !prepared.Load() {
			t.Error("request preceded durable intent")
		}
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-Switchyard-Client") != "mantik-extraction" {
			t.Error("request contract")
		}
		var p localRequest
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.Model != model || p.Stream || p.MaxTokens != 512 || p.Temperature != 0 || p.ResponseFormat.Type != "json_object" || len(p.Messages) != 2 {
			t.Error("generation bounds")
		}
		var article news.Article
		if json.Unmarshal([]byte(p.Messages[1].Content), &article) != nil || article.Original != news.FixtureArticle().Original {
			t.Error("source changed")
		}
		if strings.Contains(p.Messages[0].Content, FixtureResponse().Claims[0].Assertion) {
			t.Error("gold annotation leaked into prompt")
		}
		w.Write(envelope)
	}))
	defer server.Close()
	var receipts []LocalReceipt
	result, err := RunLocal(context.Background(), news.FixtureArticle(), "fixture-event", LocalConfig{Endpoint: server.URL, Model: model, MaxTokens: 512}, "private-token", func(r LocalReceipt) error { receipts = append(receipts, copyJSON(r)); prepared.Store(true); return nil })
	if err != nil || calls.Load() != 1 || len(receipts) != 2 {
		t.Fatal("request/retention count", err, calls.Load(), len(receipts))
	}
	if receipts[0].Status != "prepared" || receipts[0].Disposition != "not_sent" || receipts[0].Result != nil || receipts[0].EventKey != "fixture-event" || result.Status != "finished" || result.Disposition != "response_complete" || result.Result.Outcome != "extracted" || result.QualificationCredit || result.Result.QualificationCredit {
		t.Fatal("receipt authority/state")
	}
	if result.Schema != LocalSchema || result.PromptVersion != "source-quotes-v2" || result.ModelArtifact == nil || result.Anchoring == nil || result.Anchoring.Status != "derived" || result.ModelArtifact.SHA256 != result.Anchoring.ModelArtifactSHA256 || result.Anchoring.CandidateArtifactSHA256 != result.Result.Artifact.SHA256 || result.Anchoring.ArticleSHA256 != result.ArticleSHA256 || result.Anchoring.EventKey != result.EventKey {
		t.Fatal("raw/derived provenance missing or changed")
	}
	modelBytes, _ := base64.StdEncoding.DecodeString(result.ModelArtifact.Base64)
	if string(modelBytes) != string(content) || result.Result.Artifact.RawBase64 == result.ModelArtifact.Base64 {
		t.Fatal("original quote response replaced by derived artifact")
	}
	if !reflect.DeepEqual(result, receipts[1]) || result.StartedAt.After(result.CompletedAt) || result.ArticleSHA256 != result.Result.ArticleSHA256 || result.ContractSHA256 != result.Result.ContractSHA256 {
		t.Fatal("receipt pins")
	}
	raw, _ := base64.StdEncoding.DecodeString(result.Response.Base64)
	if string(raw) != string(envelope) || !result.Response.Complete || result.Response.SHA256 != news.SHA256(string(envelope)) {
		t.Fatal("lost HTTP artifact")
	}
	for _, r := range receipts {
		if sealLocal(r).SHA256 != r.SHA256 {
			t.Fatal("hash mismatch")
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "private-token") {
			t.Fatal("token retained")
		}
	}
	// Sink receives detached receipts, and a failed initial write prevents HTTP.
	calls.Store(0)
	_, err = RunLocal(context.Background(), news.FixtureArticle(), "fixture-event", LocalConfig{Endpoint: server.URL, Model: model, MaxTokens: 512}, "", func(LocalReceipt) error { return errors.New("private sink error") })
	if err == nil || calls.Load() != 0 || strings.Contains(err.Error(), "private sink error") {
		t.Fatal("failed prepared persistence sent request", err)
	}
	calls.Store(0)
	n := 0
	_, err = RunLocal(context.Background(), news.FixtureArticle(), "fixture-event", LocalConfig{Endpoint: server.URL, Model: model, MaxTokens: 512}, "private-token", func(r LocalReceipt) error {
		n++
		if n == 2 {
			return errors.New("disk failure")
		}
		r.Config.Model = "changed"
		return nil
	})
	if err == nil || calls.Load() != 1 || n != 2 {
		t.Fatal("final persistence caused retry", err, calls.Load())
	}
}

func TestLocalExtractionFailureEvidenceAndNoRetries(t *testing.T) {
	const model = "ollama/fixture:small"
	good, _ := json.Marshal(quoteFixture())
	legacy, _ := json.Marshal(FixtureResponse())
	tests := []struct {
		name                string
		status              int
		body                []byte
		disposition, reason string
	}{
		{"http", 503, []byte("private upstream error"), "http_failed", "extractor_unavailable"},
		{"redirect", 302, []byte("redirect"), "http_failed", "extractor_unavailable"},
		{"malformed-envelope", 200, []byte("{broken"), "invalid_envelope", "extractor_unavailable"},
		{"different-model", 200, localReply("ollama/other", string(good), "stop"), "invalid_envelope", "extractor_unavailable"},
		{"duplicate-envelope-field", 200, []byte(`{"model":"x","model":"y"}`), "invalid_envelope", "extractor_unavailable"},
		{"truncated-generation", 200, localReply(model, string(good), "length"), "incomplete_completion", "extractor_incomplete"},
		{"legacy-offsets", 200, localReply(model, string(legacy), "stop"), "quote_anchoring_rejected", "extractor_unavailable"},
		{"malformed-content", 200, localReply(model, "{not-json", "stop"), "quote_anchoring_rejected", "extractor_unavailable"},
		{"oversize-http", 200, []byte(strings.Repeat("x", MaxLocalHTTPBytes+1)), "incomplete_http_capture", "extractor_incomplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://127.0.0.1:1/leak")
				w.WriteHeader(tt.status)
				w.Write(tt.body)
			}))
			defer server.Close()
			var saved []LocalReceipt
			r, err := RunLocal(context.Background(), news.FixtureArticle(), "event", LocalConfig{Endpoint: server.URL, Model: model, MaxTokens: 64}, "", func(r LocalReceipt) error { saved = append(saved, r); return nil })
			if err != nil || calls.Load() != 1 || len(saved) != 2 || r.Disposition != tt.disposition || r.Result.ReasonCode != tt.reason || r.Result.Outcome != "abstain" || len(r.Result.Claims) != 0 {
				t.Fatal("wrong failure evidence", err, r.Disposition, r.Result, calls.Load())
			}
			expected := tt.body
			if len(expected) > MaxLocalHTTPBytes {
				expected = expected[:MaxLocalHTTPBytes]
			}
			raw, _ := base64.StdEncoding.DecodeString(r.Response.Base64)
			if string(raw) != string(expected) || r.Response.Complete != (len(tt.body) <= MaxLocalHTTPBytes) {
				t.Fatal("failed response lost or mislabeled")
			}
		})
	}
}

func TestLocalExtractionRejectsRemoteAndCloudConfiguration(t *testing.T) {
	for _, endpoint := range []string{"https://api.example.com", "http://8.8.8.8:80", "http://127.0.0.1:1/path", "http://user:secret@127.0.0.1:1", "http://127.0.0.1:1?x=1", "http://127.0.0.1:1?", "http://127.0.0.1:1#x", "http://localhost:1", "http://127.0.0.1:0", "http://[fe80::1%25eth0]:1"} {
		c := LocalConfig{Endpoint: endpoint, Model: "ollama/fixture:small", MaxTokens: 64}
		if c.Validate() == nil {
			t.Fatal("invalid endpoint", endpoint)
		}
	}
	for _, model := range []string{"", "gpt-test", "ollama/kimi:cloud", "ollama/../other", "ollama/name\n"} {
		if (LocalConfig{Endpoint: "http://127.0.0.1:1", Model: model, MaxTokens: 64}).Validate() == nil {
			t.Fatal("invalid model", model)
		}
	}
	for _, endpoint := range []string{"http://10.0.0.50:11434", "https://[::1]:11434", "http://192.168.1.1:8000/"} {
		if (LocalConfig{Endpoint: endpoint, Model: "ollama/hermes3:8b", MaxTokens: 64}).Validate() != nil {
			t.Fatal("valid private endpoint", endpoint)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := 0
	_, err := RunLocal(ctx, news.FixtureArticle(), "event", LocalConfig{Endpoint: "http://127.0.0.1:1", Model: "ollama/fixture", MaxTokens: 64}, "", func(LocalReceipt) error { n++; return nil })
	if err != context.Canceled || n != 0 {
		t.Fatal("cancelled request retained or sent", err)
	}
}

func TestLocalExtractionCooperativeDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var saved []LocalReceipt
	r, err := RunLocal(ctx, news.FixtureArticle(), "event", LocalConfig{Endpoint: server.URL, Model: "ollama/fixture", MaxTokens: 64}, "", func(r LocalReceipt) error { saved = append(saved, r); return nil })
	if err != nil || calls.Load() != 1 || len(saved) != 2 || r.Result.ReasonCode != "extractor_timeout" || r.Response.Complete {
		t.Fatal("deadline not retained", err, r.Result)
	}
}
