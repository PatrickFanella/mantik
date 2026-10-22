package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type mcpSession struct {
	in   *io.PipeWriter
	dec  *json.Decoder
	done chan error
}

func mcpTestSession(t *testing.T, base, receiver string) *mcpSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := runSupervisionMCP(ctx, []string{"--api-url", base, "--receiver-id", receiver}, inR, outW, io.Discard)
		outW.Close()
		done <- err
	}()
	t.Cleanup(func() { inW.Close(); cancel(); outR.Close() })
	return &mcpSession{inW, json.NewDecoder(outR), done}
}
func (s *mcpSession) write(t *testing.T, v any) {
	t.Helper()
	if err := json.NewEncoder(s.in).Encode(v); err != nil {
		t.Fatal(err)
	}
}
func (s *mcpSession) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var r map[string]json.RawMessage
	if err := s.dec.Decode(&r); err != nil {
		t.Fatal(err)
	}
	if string(r["jsonrpc"]) != `"2.0"` {
		t.Fatal(r)
	}
	return r
}
func (s *mcpSession) request(t *testing.T, id any, method string, params any) map[string]json.RawMessage {
	s.write(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return s.read(t)
}
func (s *mcpSession) init(t *testing.T) {
	r := s.request(t, 1, "initialize", map[string]any{"protocolVersion": supervisionMCPVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "contract-client", "version": "v1"}})
	if r["error"] != nil || !bytes.Contains(r["result"], []byte(supervisionMCPVersion)) {
		t.Fatal(r)
	}
	s.write(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}
func TestSupervisionMCPServiceOwnersAndErrors(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "mcp-token")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	server := httptest.NewServer(service.Handler(store, "mcp-token"))
	defer server.Close()
	s := mcpTestSession(t, server.URL, receiver.ID)
	if r := s.request(t, "before", "tools/list", map[string]any{}); r["error"] == nil {
		t.Fatal("pre-init tool call admitted")
	}
	s.init(t)
	r := s.request(t, 2, "tools/list", map[string]any{})
	var listed struct {
		Tools []mcpTool `json:"tools"`
	}
	if json.Unmarshal(r["result"], &listed) != nil || len(listed.Tools) != 5 {
		t.Fatal(r)
	}
	for _, tool := range listed.Tools {
		schema := tool.InputSchema.(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatal(tool)
		}
	}
	command := socialwatch.PolicyCommand{ID: "mcp-watch-start", ReceiverID: receiver.ID, Policy: socialwatch.Policy{ID: "mcp-watch", Provider: "recorded", AccountID: "analyst", Symbols: []string{"ALPHA"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	r = s.request(t, 3, "tools/call", map[string]any{"name": "change_watch_policy", "arguments": command})
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(r["result"], &result) != nil || result.IsError || len(result.Content) != 1 {
		t.Fatal(r)
	}
	var owner struct {
		HTTPStatus int                 `json:"http_status"`
		Body       socialwatch.Receipt `json:"body"`
	}
	if json.Unmarshal([]byte(result.Content[0].Text), &owner) != nil || owner.HTTPStatus != 201 || owner.Body.Entry.Policy == nil {
		t.Fatal(result)
	}
	history, err := store.SocialHistory(context.Background())
	if err != nil || len(history.Watches) != 1 {
		t.Fatal(history, err)
	}
	// Every write still goes through owner CAS/idempotence and fixed behavior.
	r = s.request(t, 4, "tools/call", map[string]any{"name": "change_watch_policy", "arguments": command})
	_ = json.Unmarshal(r["result"], &result)
	_ = json.Unmarshal([]byte(result.Content[0].Text), &owner)
	if result.IsError || owner.HTTPStatus != 200 || !owner.Body.Duplicate {
		t.Fatal(result)
	}
	command.Policy.Behavior = "configured_rule"
	command.Policy.RuleID = "forbidden"
	r = s.request(t, 5, "tools/call", map[string]any{"name": "change_watch_policy", "arguments": command})
	_ = json.Unmarshal(r["result"], &result)
	if !result.IsError || !strings.Contains(result.Content[0].Text, `"http_status":422`) {
		t.Fatal(result)
	}
	r = s.request(t, 6, "tools/call", map[string]any{"name": "inspect_record", "arguments": map[string]string{"kind": "watch", "id": "mcp-watch"}})
	_ = json.Unmarshal(r["result"], &result)
	if result.IsError || !strings.Contains(result.Content[0].Text, "mcp-watch") {
		t.Fatal(result)
	}
	for _, args := range []map[string]string{{"kind": "watch", "id": "../secret"}, {"kind": "watch", "id": "missing"}, {"kind": "balance", "id": "mcp-watch"}} {
		r = s.request(t, 7, "tools/call", map[string]any{"name": "inspect_record", "arguments": args})
		_ = json.Unmarshal(r["result"], &result)
		if !result.IsError {
			t.Fatal(r)
		}
	}
	if r = s.request(t, 8, "tools/call", map[string]any{"name": "public_balance_writer", "arguments": map[string]any{}}); r["error"] == nil {
		t.Fatal("unknown tool admitted")
	}
	s.in.Close()
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}
func TestSupervisionMCPCancelNoRetry(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "mcp-token")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	handler := service.Handler(store, "mcp-token")
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/supervision/calls" {
			posts.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	s := mcpTestSession(t, server.URL, receiver.ID)
	s.init(t)
	s.write(t, map[string]any{"jsonrpc": "2.0", "id": "cancel-me", "method": "tools/call", "params": map[string]any{"name": "inspect_record", "arguments": map[string]string{"kind": "watch", "id": "missing"}}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	r := s.request(t, "cancel-me", "tools/call", map[string]any{"name": "inspect_record", "arguments": map[string]string{"kind": "watch", "id": "missing"}})
	if r["error"] == nil {
		t.Fatal("duplicate active ID dispatched")
	}
	s.write(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "cancel-me"}})
	r = s.read(t)
	if !bytes.Contains(r["result"], []byte(`"isError":true`)) || !bytes.Contains(r["result"], []byte("uncertain")) {
		t.Fatal(r)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP context not canceled")
	}
	if posts.Load() != 1 {
		t.Fatal("call retried", posts.Load())
	}
	s.in.Close()
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}
func TestSupervisionMCPInvalidTransportAndReceiver(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "mcp-token")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	server := httptest.NewServer(service.Handler(store, "mcp-token"))
	defer server.Close()
	for _, raw := range []string{`{`, `[]`, `{"jsonrpc":"2.0","id":null,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`} {
		var out bytes.Buffer
		err := runSupervisionMCP(context.Background(), []string{"--api-url", server.URL, "--receiver-id", receiver.ID}, strings.NewReader(raw+"\n"), &out, io.Discard)
		if err != nil || !bytes.Contains(out.Bytes(), []byte(`"error"`)) {
			t.Fatal(err, out.String())
		}
	}
	var out bytes.Buffer
	if err := runSupervisionMCP(context.Background(), []string{"--api-url", server.URL, "--receiver-id", receiver.ID}, strings.NewReader(strings.Repeat("x", maxMCPMessage+1)+"\n"), &out, io.Discard); err == nil {
		t.Fatal("oversize line accepted")
	}
	s := mcpTestSession(t, server.URL, "receiver-aaaaaaaaaaaaaaaa")
	r := s.request(t, 1, "initialize", map[string]any{"protocolVersion": supervisionMCPVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "wrong", "version": "v1"}})
	if r["error"] == nil || bytes.Contains(r["error"], []byte("mcp-token")) {
		t.Fatal(r)
	}
	s.in.Close()
	<-s.done
}
func TestSupervisionMCPStdioProcess(t *testing.T) {
	if os.Getenv("MANTIK_MCP_TEST_CHILD") == "1" {
		if err := Run(context.Background(), []string{"supervision-mcp", "--api-url", os.Getenv("MANTIK_MCP_TEST_URL"), "--receiver-id", os.Getenv("MANTIK_MCP_TEST_RECEIVER")}, os.Stdout, os.Stderr); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	t.Setenv("MANTIK_API_TOKEN", "mcp-token")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	server := httptest.NewServer(service.Handler(store, "mcp-token"))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSupervisionMCPStdioProcess$")
	if binary := os.Getenv("MANTIK_MCP_TEST_BINARY"); binary != "" {
		child = exec.CommandContext(ctx, binary, "supervision-mcp", "--api-url", server.URL, "--receiver-id", receiver.ID)
	}
	child.Env = append(os.Environ(), "MANTIK_MCP_TEST_CHILD=1", "MANTIK_MCP_TEST_URL="+server.URL, "MANTIK_MCP_TEST_RECEIVER="+receiver.ID)
	child.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"process-test","version":"v1"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":"tools","method":"tools/list","params":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	if err := child.Run(); err != nil {
		t.Fatal(err, stderr.String(), stdout.String())
	}
	decoder := json.NewDecoder(&stdout)
	for range 2 {
		var reply map[string]any
		if err := decoder.Decode(&reply); err != nil || reply["error"] != nil {
			t.Fatal(reply, err)
		}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("stdout contained non-protocol output", extra)
	}
}

func TestSupervisionMCPRedirectIsToolError(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "mcp-token")
	store := storage.NewMemory()
	receiver, _ := store.ResearchSnapshot(context.Background())
	handler := service.Handler(store, "mcp-token")
	var followed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unexpected-target" {
			followed.Add(1)
			t.Error("redirect followed")
			return
		}
		if r.URL.Path == "/v1/supervision/calls" {
			w.Header().Set("Location", "/unexpected-target")
			w.WriteHeader(http.StatusTemporaryRedirect)
			io.WriteString(w, `{"error":"redirect refused"}`)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	s := mcpTestSession(t, server.URL, receiver.ID)
	s.init(t)
	r := s.request(t, 2, "tools/call", map[string]any{"name": "inspect_record", "arguments": map[string]string{"kind": "watch", "id": "missing"}})
	if !bytes.Contains(r["result"], []byte(`"isError":true`)) || !bytes.Contains(r["result"], []byte(`http_status`)) {
		t.Fatal("redirect treated as success", string(r["result"]))
	}
	if followed.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	s.in.Close()
	if err := <-s.done; err != nil {
		t.Fatal(err)
	}
}
