package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const supervisionMCPVersion = "2025-06-18"
const maxMCPMessage = 24 << 10
const maxMCPResult = 2 << 20

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema any             `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
}
type mcpBridge struct {
	base, receiver, token string
	client                *http.Client
	tools                 []mcpTool
}

// One bounded request, no retries. HTTP outcomes stay inside tool results;
// transport/auth/size failures provide no assertion that a mutation did not run.
func (b *mcpBridge) http(ctx context.Context, path string, body any) (int, json.RawMessage, error) {
	method := http.MethodGet
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		in = bytes.NewReader(raw)
		method = http.MethodPost
	}
	u, _ := url.Parse(b.base)
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), in)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("Mantik request unavailable; outcome may be uncertain; no retry was sent")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxMCPResult+1))
	if err != nil || len(raw) > maxMCPResult || !json.Valid(raw) || !utf8.Valid(raw) {
		return 0, nil, errors.New("invalid or oversized Mantik response; outcome may be uncertain; no retry was sent")
	}
	return res.StatusCode, json.RawMessage(raw), nil
}
func (b *mcpBridge) verifyReceiver(ctx context.Context) error {
	status, raw, err := b.http(ctx, "/v1/research-receiver", nil)
	if err != nil {
		return err
	}
	var receiver domain.ResearchSnapshot
	if status != 200 || workerplan.ReadBoundedShape(raw, &receiver, maxMCPResult) != nil || receiver.ID != b.receiver {
		return errors.New("Mantik receiver identity or authentication mismatch")
	}
	return nil
}
func mcpID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "s:" + s, s != ""
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return "", false
	}
	return "n:" + strconv.FormatInt(n, 10), true
}
func runSupervisionMCP(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("supervision-mcp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "Mantik HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	token := os.Getenv("MANTIK_API_TOKEN")
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || !storage.ValidReceiverID(*receiver) || token == "" || fs.NArg() != 0 {
		return errors.New("supervision-mcp requires an HTTP origin, receiver ID and MANTIK_API_TOKEN")
	}
	b := &mcpBridge{base: *base, receiver: *receiver, token: token, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// stdin belongs to this stdio server. Closing an owned pipe on cancellation
	// releases its scanner; byte readers used by tests need no close.
	stop := context.AfterFunc(ctx, func() {
		if c, ok := in.(io.Closer); ok {
			_ = c.Close()
		}
	})
	defer stop()
	var outputMu sync.Mutex
	var outputErr error
	send := func(id json.RawMessage, result any, code int, message string) {
		reply := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			reply["error"] = map[string]any{"code": code, "message": message}
		} else {
			reply["result"] = result
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		if outputErr == nil {
			outputErr = json.NewEncoder(out).Encode(reply)
			if outputErr != nil {
				cancel()
			}
		}
	}
	protocolError := func(id json.RawMessage, code int, message string) { send(id, nil, code, message) }
	var workers sync.WaitGroup
	var pendingMu sync.Mutex
	pending := map[string]context.CancelFunc{}
	initialized, ready := false, false
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxMCPMessage)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var msg mcpMessage
		if !utf8.Valid(raw) || workerplan.ReadBoundedShape(raw, &msg, maxMCPMessage) != nil {
			code := -32700
			if json.Valid(raw) {
				code = -32600
			}
			protocolError(nil, code, "invalid JSON-RPC message")
			continue
		}
		key, idValid := mcpID(msg.ID)
		if msg.JSONRPC != "2.0" || msg.Method == "" || len(msg.ID) > 0 && !idValid {
			protocolError(nil, -32600, "invalid JSON-RPC request")
			continue
		}
		if len(msg.ID) == 0 {
			switch msg.Method {
			case "notifications/initialized":
				if initialized {
					ready = true
				}
			case "notifications/cancelled":
				var c struct {
					RequestID json.RawMessage `json:"requestId"`
					Reason    string          `json:"reason,omitempty"`
				}
				if workerplan.ReadBoundedShape(msg.Params, &c, maxMCPMessage) == nil {
					if k, ok := mcpID(c.RequestID); ok {
						pendingMu.Lock()
						if stop := pending[k]; stop != nil {
							stop()
						}
						pendingMu.Unlock()
					}
				}
			}
			continue
		}
		if msg.Method == "ping" {
			send(msg.ID, map[string]any{}, 0, "")
			continue
		}
		if msg.Method == "initialize" {
			var p struct {
				ProtocolVersion string                     `json:"protocolVersion"`
				Capabilities    map[string]json.RawMessage `json:"capabilities"`
				ClientInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
					Title   string `json:"title,omitempty"`
				} `json:"clientInfo"`
			}
			if initialized || workerplan.ReadBoundedShape(msg.Params, &p, maxMCPMessage) != nil || p.ProtocolVersion == "" || p.Capabilities == nil || p.ClientInfo.Name == "" || p.ClientInfo.Version == "" {
				protocolError(msg.ID, -32602, "invalid initialize parameters")
				continue
			}
			if err := b.discover(ctx); err != nil {
				protocolError(msg.ID, -32603, err.Error())
				continue
			}
			initialized = true
			send(msg.ID, map[string]any{"protocolVersion": supervisionMCPVersion, "capabilities": map[string]any{"tools": map[string]bool{"listChanged": false}}, "serverInfo": map[string]string{"name": "mantik-supervision", "version": "v1"}, "instructions": "Tools use existing Mantik owners. No model generation, trading grants or qualification. Mutating calls retain owner request IDs; uncertain outcomes must be read back before retry."}, 0, "")
			continue
		}
		if !ready {
			protocolError(msg.ID, -32000, "initialize and notifications/initialized are required")
			continue
		}
		if msg.Method == "tools/list" {
			var p struct {
				Cursor string          `json:"cursor,omitempty"`
				Meta   json.RawMessage `json:"_meta,omitempty"`
			}
			params := msg.Params
			if len(params) == 0 {
				params = json.RawMessage(`{}`)
			}
			if workerplan.ReadBoundedShape(params, &p, maxMCPMessage) != nil || p.Cursor != "" {
				protocolError(msg.ID, -32602, "unsupported tool cursor or parameters")
				continue
			}
			send(msg.ID, map[string]any{"tools": b.tools}, 0, "")
			continue
		}
		if msg.Method != "tools/call" {
			protocolError(msg.ID, -32601, "method not found")
			continue
		}
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta,omitempty"`
		}
		if workerplan.ReadBoundedShape(msg.Params, &call, maxMCPMessage) != nil || len(call.Arguments) == 0 || bytes.TrimSpace(call.Arguments)[0] != '{' {
			protocolError(msg.ID, -32602, "invalid tool call")
			continue
		}
		known := false
		for _, tool := range b.tools {
			if tool.Name == call.Name {
				known = true
			}
		}
		if !known {
			protocolError(msg.ID, -32602, "unknown tool")
			continue
		}
		pendingMu.Lock()
		if pending[key] != nil || len(pending) >= 8 {
			pendingMu.Unlock()
			protocolError(msg.ID, -32000, "duplicate active request ID or pending request limit")
			continue
		}
		taskCtx, taskCancel := context.WithCancel(ctx)
		pending[key] = taskCancel
		pendingMu.Unlock()
		id := append(json.RawMessage(nil), msg.ID...)
		workers.Go(func() {
			defer func() { taskCancel(); pendingMu.Lock(); delete(pending, key); pendingMu.Unlock() }()
			status := 0
			var body json.RawMessage
			err := b.verifyReceiver(taskCtx)
			if err == nil {
				status, body, err = b.http(taskCtx, "/v1/supervision/calls", map[string]any{"tool": call.Name, "arguments": call.Arguments})
			}
			var text string
			failed := status < 200 || status >= 300
			if err != nil {
				text = err.Error()
				failed = true
			} else {
				raw, _ := json.Marshal(map[string]any{"http_status": status, "body": body})
				text = string(raw)
			}
			send(id, map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": failed}, 0, "")
		})
	}
	workers.Wait()
	if outputErr != nil {
		return outputErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return scanner.Err()
}

func (b *mcpBridge) discover(ctx context.Context) error {
	if err := b.verifyReceiver(ctx); err != nil {
		return err
	}
	status, raw, err := b.http(ctx, "/v1/supervision/tools", nil)
	if err != nil {
		return err
	}
	var c struct {
		Schema string `json:"schema"`
		Tools  []struct {
			Name             string `json:"name"`
			ArgumentContract string `json:"argument_contract"`
			Mutates          bool   `json:"mutates"`
		} `json:"tools"`
		ModelUsed           bool `json:"model_used"`
		QualificationCredit bool `json:"qualification_credit"`
	}
	if status != 200 || workerplan.ReadBoundedShape(raw, &c, maxMCPResult) != nil || c.Schema != "mantik-supervision-tools-v1" || c.Tools == nil || len(c.Tools) > len(supervisionMCPSchemas()) || c.ModelUsed || c.QualificationCredit {
		return errors.New("invalid Mantik supervision catalogue")
	}
	b.tools = []mcpTool{}
	seen := map[string]bool{}
	for _, t := range c.Tools {
		schema, known := supervisionMCPSchemas()[t.Name]
		if !known || seen[t.Name] || t.Mutates != (t.Name != "inspect_record") {
			return errors.New("unsupported Mantik supervision tool")
		}
		seen[t.Name] = true
		b.tools = append(b.tools, mcpTool{Name: t.Name, Description: t.ArgumentContract, InputSchema: schema, Annotations: map[string]bool{"readOnlyHint": !t.Mutates, "destructiveHint": t.Mutates, "idempotentHint": false, "openWorldHint": false}})
	}
	return nil
}
