package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestConversationOwnerReadbackRetriesAndModelOutage(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "operator")
	t.Setenv("MANTIK_MODEL_TOKEN", "model-secret")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(t.Context())
	var posts atomic.Int64
	h := service.Handler(store, "operator")
	api := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/supervision/calls" {
			posts.Add(1)
		}
		h.ServeHTTP(w, r)
	})))
	defer api.Close()
	policy := map[string]any{"id": "conversation-command", "receiver_id": receiver.ID, "previous_sha256": "", "policy": map[string]any{"id": "conversation-watch", "provider": "fixture", "account_id": "example", "symbols": []string{"SPY"}, "behavior": "research", "rule_id": "", "max_observation_delay_seconds": 60, "enabled": true}}
	call := conversationCall{Tool: "change_watch_policy"}
	call.Arguments, _ = json.Marshal(policy)
	inspect := conversationCall{Tool: "inspect_record", Arguments: json.RawMessage(`{"kind":"watch","id":"conversation-watch"}`)}
	var requests atomic.Int64
	model := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer model-secret" {
			t.Error("wrong model transport")
		}
		var request struct {
			Messages []conversationMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid request")
		}
		n := requests.Add(1)
		if n > 1 && !strings.Contains(request.Messages[len(request.Messages)-2].Content, "Retained owner result") {
			t.Error("owner result missing from conversation")
		}
		if n == 4 {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		selected := call
		if n == 3 {
			selected = inspect
		}
		content, _ := json.Marshal(selected)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	})))
	defer model.Close()
	parent := t.TempDir()
	config := filepath.Join(parent, "model.json")
	raw, _ := json.Marshal(map[string]any{"endpoint": model.URL, "model": "openai/gpt-6.1-sol", "max_tokens": 1024, "reasoning_effort": "low"})
	os.WriteFile(config, raw, 0600)
	capture := filepath.Join(parent, "session")
	var out bytes.Buffer
	err := runConverse(t.Context(), []string{"--api-url", api.URL, "--receiver-id", receiver.ID, "--model-config", config, "--capture-dir", capture, "--allow-writes"}, strings.NewReader("Create the research watch\nRepeat the exact request\nInspect that watch\nInspect again\n"), &out, io.Discard)
	if err == nil || requests.Load() != 4 || posts.Load() != 3 {
		t.Fatal(err, requests.Load(), posts.Load())
	}
	if !strings.Contains(out.String(), `"http_status": 201`) || !strings.Contains(out.String(), `"duplicate": true`) || !strings.Contains(out.String(), "conversation-watch") {
		t.Fatal(out.String())
	}
	history, e := store.SocialHistory(t.Context())
	if e != nil || len(history.Watches) != 1 {
		t.Fatal(history, e)
	}
	before := posts.Load()
	if e := runConverse(t.Context(), []string{"--api-url", api.URL, "--receiver-id", receiver.ID, "--model-config", config, "--capture-dir", capture, "--allow-writes"}, strings.NewReader("Repeat\n"), io.Discard, io.Discard); e == nil || posts.Load() != before {
		t.Fatal("reopened interrupted session", e)
	}
	files, _ := os.ReadDir(capture)
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(capture, f.Name()))
		if bytes.Contains(b, []byte("model-secret")) || bytes.Contains(b, []byte("Bearer operator")) {
			t.Fatal("credential retained", f.Name())
		}
	}
	if _, e := os.Stat(filepath.Join(capture, "turn-0004-owner-intent.json")); !os.IsNotExist(e) {
		t.Fatal("outage sent a tool", e)
	}
}

func TestConversationRejectsModelAuthorityAndAmbiguity(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "operator")
	for _, tc := range []struct {
		name, content string
		write         bool
	}{
		{"unknown-tool", `{"tool":"write_balance","arguments":{}}`, true},
		{"duplicate", `{"tool":"inspect_record","tool":"change_watch_policy","arguments":{}}`, true},
		{"duplicate-arguments", `{"tool":"inspect_record","arguments":{"kind":"watch","id":"one","id":"two"}}`, true},
		{"wrong-receiver", `{"tool":"change_watch_policy","arguments":{"receiver_id":"receiver-abcdefghijklmnop"}}`, true},
		{"read-only", `{"tool":"change_watch_policy","arguments":{}}`, false},
		{"narrative", `{"explanation":"trade now"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemory()
			receiver, _ := store.ResearchSnapshot(t.Context())
			var posts atomic.Int64
			h := service.Handler(store, "operator")
			api := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/supervision/calls" {
					posts.Add(1)
				}
				h.ServeHTTP(w, r)
			})))
			defer api.Close()
			model := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": tc.content}}}})
			})))
			defer model.Close()
			parent := t.TempDir()
			config := filepath.Join(parent, "model.json")
			raw, _ := json.Marshal(map[string]any{"endpoint": model.URL, "model": "openai/gpt-6.1-sol", "max_tokens": 1024, "reasoning_effort": "low"})
			os.WriteFile(config, raw, 0600)
			args := []string{"--api-url", api.URL, "--receiver-id", receiver.ID, "--model-config", config, "--capture-dir", filepath.Join(parent, "capture")}
			if tc.write {
				args = append(args, "--allow-writes")
			}
			if err := runConverse(t.Context(), args, strings.NewReader("Inspect\n"), io.Discard, io.Discard); err == nil || posts.Load() != 0 {
				t.Fatal("untrusted response dispatched", err, posts.Load())
			}
		})
	}
}

func TestConversationRetainsUncertainCommittedOwnerCall(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "operator")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(t.Context())
	h := service.Handler(store, "operator")
	var posts atomic.Int64
	api := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/supervision/calls" {
			h.ServeHTTP(w, r)
			return
		}
		posts.Add(1)
		recorded := httptest.NewRecorder()
		h.ServeHTTP(recorded, r)
		if recorded.Code != 201 {
			t.Error(recorded.Code, recorded.Body.String())
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})))
	defer api.Close()
	model := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{"tool": "change_watch_policy", "arguments": map[string]any{"id": "uncertain-command", "receiver_id": receiver.ID, "previous_sha256": "", "policy": map[string]any{"id": "uncertain-watch", "provider": "fixture", "account_id": "example", "symbols": []string{"SPY"}, "behavior": "research", "rule_id": "", "max_observation_delay_seconds": 60, "enabled": true}}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	})))
	defer model.Close()
	parent := t.TempDir()
	config := filepath.Join(parent, "model.json")
	raw, _ := json.Marshal(map[string]any{"endpoint": model.URL, "model": "openai/gpt-6.1-sol", "max_tokens": 1024, "reasoning_effort": "low"})
	os.WriteFile(config, raw, 0600)
	capture := filepath.Join(parent, "capture")
	err := runConverse(t.Context(), []string{"--api-url", api.URL, "--receiver-id", receiver.ID, "--model-config", config, "--capture-dir", capture, "--allow-writes"}, strings.NewReader("Create the watch\nRetry it\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "uncertain") || posts.Load() != 1 {
		t.Fatal(err, posts.Load())
	}
	history, e := store.SocialHistory(t.Context())
	if e != nil || len(history.Watches) != 1 {
		t.Fatal(history, e)
	}
	if _, e := os.Stat(filepath.Join(capture, "turn-0001-owner-uncertain.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(capture, "turn-0001-owner-result.json")); !os.IsNotExist(e) {
		t.Fatal("invented a result", e)
	}
}
