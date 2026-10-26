package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
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
	if json.Unmarshal(r["result"], &listed) != nil || len(listed.Tools) != 7 {
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

func TestCopySupervisionMCP(t *testing.T) { copySupervisionMCP(t, storage.NewMemory()) }

// The same transport contract also runs in the disposable PostgreSQL HTTP test.
// Calls reach the existing source owner; there is no adapter-owned state.
func copySupervisionMCP(t *testing.T, store socialHTTPBackend) {
	t.Helper()
	t.Setenv("MANTIK_API_TOKEN", "copy-mcp-token")
	ctx := context.Background()
	receiver, err := store.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	clock := &ownedDecisionClock{at: at}
	server := httptest.NewServer(service.HandlerWithClock(store, "copy-mcp-token", clock))
	defer server.Close()
	session := mcpTestSession(t, server.URL, receiver.ReceiverID)
	session.init(t)
	requestID := 20
	call := func(name string, args any, want int) json.RawMessage {
		t.Helper()
		requestID++
		r := session.request(t, requestID, "tools/call", map[string]any{"name": name, "arguments": args})
		var result struct {
			Content []struct{ Text string }
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(r["result"], &result) != nil || len(result.Content) != 1 {
			t.Fatal(r)
		}
		var owner struct {
			HTTPStatus int             `json:"http_status"`
			Body       json.RawMessage `json:"body"`
		}
		if json.Unmarshal([]byte(result.Content[0].Text), &owner) != nil || owner.HTTPStatus != want || result.IsError != (want >= 400) {
			t.Fatal(result)
		}
		return owner.Body
	}
	command := socialwatch.PolicyCommand{ID: "mcp-copy-start", ReceiverID: receiver.ReceiverID, Policy: socialwatch.Policy{ID: "mcp-copy", Provider: "recorded", AccountID: "mcp-copy-analyst", Symbols: []string{"SPY"}, Behavior: "bounded_paper_follow", MaxObservationDelaySeconds: 60, Enabled: true, Follow: &socialwatch.FollowBounds{MaxOrderNotionalCents: 10000, MaxExposureCents: 50000, StopDistanceBPS: 100, MaxHoldingSeconds: 3600}}}
	var first socialwatch.Receipt
	if json.Unmarshal(call("change_copy_watch_policy", command, 201), &first) != nil || first.Entry.Policy == nil {
		t.Fatal(first)
	}
	clock.panicRead = true
	var retry socialwatch.Receipt
	if json.Unmarshal(call("change_copy_watch_policy", command, 200), &retry) != nil || !retry.Duplicate || !reflect.DeepEqual(first.Entry, retry.Entry) {
		t.Fatal(retry)
	}
	clock.panicRead = false
	// The old social tool cannot silently acquire bounded-follow capability.
	call("change_watch_policy", command, 422)
	read := call("inspect_record", map[string]string{"kind": "copy_watch", "id": command.Policy.ID}, 200)
	direct := func(path string) []byte {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer copy-mcp-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 {
			t.Fatal(err, res.StatusCode, string(raw))
		}
		return raw
	}
	if !bytes.Equal(bytes.TrimSpace(read), bytes.TrimSpace(direct("/v1/copy-watches/"+command.Policy.ID))) {
		t.Fatal("watch owner read diverged")
	}
	original := socialwatch.Original{Schema: socialwatch.CopyRecordingSchema, Source: socialwatch.SourceRef{Provider: "recorded", AccountID: command.Policy.AccountID, PostID: "order-one"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Unverified recorded order", Symbols: []string{"SPY"}, Activity: &socialwatch.Activity{Kind: "observed_order", State: "open", SourceSymbol: "SPY", Symbol: "SPY", MappingVersion: "asserted-v1", Side: "buy", QuantityUnits: 10, QuantityUnitClaim: "shares", PriceCents: 10000}}
	raw, _ := json.Marshal(original)
	capture := socialwatch.CaptureCommand{ID: "mcp-copy-source", ReceiverID: receiver.ReceiverID, WatchID: command.Policy.ID, PolicySHA256: first.Entry.Policy.SHA256, EvidenceClass: "recorded_copy_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
	if _, err := store.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, clock.Now); err != nil {
		t.Fatal(err)
	}
	// Fixed source selectors return actual retained records without asking the clock.
	original.Schema = "mantik-social-recording-v1"
	original.Source.PostID = "social-one"
	original.Activity = nil
	socialRaw, _ := json.Marshal(original)
	socialCapture := capture
	socialCapture.ID, socialCapture.EvidenceClass = "mcp-social-source", "recorded_social_unqualified"
	socialCapture.Original, socialCapture.OriginalSHA256 = string(socialRaw), workerplan.Digest(socialRaw)
	if _, err := store.ApplySocialCommand(ctx, socialwatch.Command{Capture: &socialCapture}, clock.Now); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := store.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock.panicRead = true
	var sourceReads [][]byte
	for _, pair := range []struct{ kind, id, path string }{
		{"copy_record", capture.ID, "/v1/copy-recordings/" + capture.ID},
		{"social_record", socialCapture.ID, "/v1/social-recordings/" + socialCapture.ID},
	} {
		via := call("inspect_record", map[string]string{"kind": pair.kind, "id": pair.id}, 200)
		if !bytes.Equal(bytes.TrimSpace(via), bytes.TrimSpace(direct(pair.path))) {
			t.Fatal("MCP source owner read diverged")
		}
		sourceReads = append(sourceReads, append([]byte(nil), via...))
		call("inspect_record", map[string]string{"kind": pair.kind, "id": "missing"}, 404)
		call("inspect_record", map[string]string{"kind": pair.kind, "id": "../operations"}, 422)
		call("inspect_record", map[string]string{"kind": pair.kind, "id": pair.id, "path": "/v1/operations"}, 422)
		call("inspect_record", map[string]any{"kind": pair.kind, "id": pair.id, "execution_available": true}, 422)
	}
	call("inspect_record", map[string]string{"kind": "social_record", "id": capture.ID}, 404)
	call("inspect_record", map[string]string{"kind": "copy_record", "id": socialCapture.ID}, 404)
	clock.panicRead = false
	sourceAfter, err := store.SocialHistory(ctx)
	if err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatal("MCP inspection mutated owner", err)
	}
	read = call("inspect_record", map[string]string{"kind": "copy_applicability", "id": capture.ID}, 200)
	if !bytes.Equal(bytes.TrimSpace(read), bytes.TrimSpace(direct("/v1/copy-recordings/"+capture.ID+"/applicability"))) {
		t.Fatal("applicability owner read diverged")
	}
	var applicability socialwatch.Applicability
	if json.Unmarshal(read, &applicability) != nil || applicability.ExecutionAvailable || applicability.QualificationCredit {
		t.Fatal(string(read))
	}
	// Both fixed inspection aliases select the same retained owner, not a second journal.
	social := call("inspect_record", map[string]string{"kind": "social_applicability", "id": capture.ID}, 200)
	if !bytes.Equal(read, social) {
		t.Fatal("source aliases diverged")
	}
	bad := command
	bad.ID = "stale-copy-update"
	bad.Policy.Enabled = false
	call("change_copy_watch_policy", bad, 409)
	bad = command
	bad.ID = "missing-copy-bounds"
	bad.Policy.Follow = nil
	call("change_copy_watch_policy", bad, 422)
	bad = command
	bad.ID = "oversized-copy-order"
	bounds := *command.Policy.Follow
	bounds.MaxOrderNotionalCents = bounds.MaxExposureCents + 1
	bad.Policy.Follow = &bounds
	call("change_copy_watch_policy", bad, 422)
	bad = command
	bad.ID = "copy-rule"
	bad.Policy.Behavior = "configured_rule"
	bad.Policy.Follow = nil
	bad.Policy.RuleID = "rule"
	call("change_copy_watch_policy", bad, 422)
	bad = command
	bad.ID = "wrong-copy-owner"
	bad.ReceiverID = "receiver-abcdefghijklmnop"
	call("change_copy_watch_policy", bad, 409)
	asserted := map[string]any{}
	raw, _ = json.Marshal(command)
	_ = json.Unmarshal(raw, &asserted)
	asserted["admission"] = true
	call("change_copy_watch_policy", asserted, 422)
	delete(asserted, "admission")
	asserted["policy"].(map[string]any)["follow"].(map[string]any)["execution_available"] = true
	call("change_copy_watch_policy", asserted, 422)
	call("inspect_record", map[string]string{"kind": "copy_applicability", "id": "../operations"}, 422)
	// Disable with the actual predecessor and read the changed applicability through MCP.
	command.ID = "mcp-copy-disable"
	command.PreviousSHA256 = first.Entry.Policy.SHA256
	command.Policy.Enabled = false
	call("change_copy_watch_policy", command, 201)
	clock.panicRead = true
	for i, pair := range []struct{ kind, id string }{{"copy_record", capture.ID}, {"social_record", socialCapture.ID}} {
		if !bytes.Equal(sourceReads[i], call("inspect_record", map[string]string{"kind": pair.kind, "id": pair.id}, 200)) {
			t.Fatal("watch change rewrote original source")
		}
	}
	clock.panicRead = false
	read = call("inspect_record", map[string]string{"kind": "copy_applicability", "id": capture.ID}, 200)
	if json.Unmarshal(read, &applicability) != nil || applicability.Status != "watch_policy_changed" {
		t.Fatal(string(read))
	}
	history, err := store.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range history.History {
		if entry.Policy != nil && entry.Policy.Command.Policy.ID == command.Policy.ID {
			found++
		}
	}
	if found != 2 {
		t.Fatal("invalid calls changed policy journal", found)
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accountsAfter, err := store.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountsAfter) {
		t.Fatal("supervision changed capital or operations", err)
	}
	session.in.Close()
	if err := <-session.done; err != nil {
		t.Fatal(err)
	}
}
