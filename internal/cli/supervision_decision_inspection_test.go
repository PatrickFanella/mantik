package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// Called with actual finalized decisions and retained raw market sources by
// both the Memory and disposable PostgreSQL owner contracts. No imported
// explanation, quote or caller claim can replace those owner reads.
func supervisionDecisionInspections(t *testing.T, base, receiver, id string, explanation, market []byte) {
	t.Helper()
	session := mcpTestSession(t, base, receiver)
	session.init(t)
	listed := session.request(t, 2, "tools/list", map[string]any{})
	var catalog struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Properties struct{ Kind struct{ Enum []string } }
			}
			Annotations map[string]bool
		}
	}
	if err := json.Unmarshal(listed["result"], &catalog); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, tool := range catalog.Tools {
		if tool.Name == "inspect_record" {
			if !tool.Annotations["readOnlyHint"] || tool.Annotations["destructiveHint"] {
				t.Fatal(tool)
			}
			for _, kind := range tool.InputSchema.Properties.Kind.Enum {
				kinds[kind] = true
			}
		}
	}
	nextID := 3
	for _, spec := range []struct {
		kind string
		raw  []byte
	}{{"decision_explanation", explanation}, {"decision_market_evidence", market}} {
		if !kinds[spec.kind] {
			t.Fatal("inspection missing from MCP schema", spec.kind)
		}
		body := map[string]any{"tool": "inspect_record", "arguments": map[string]string{"kind": spec.kind, "id": id}}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(context.Background(), "POST", base+"/v1/supervision/calls", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer owned-decision-token")
		// Incoming transport framing must not leak into the fixed owner GET.
		req.ContentLength, req.TransferEncoding = -1, []string{"chunked"}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		read, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Equal(read, spec.raw) {
			t.Fatal("HTTP changed owner evidence", response.StatusCode, string(read), err)
		}
		for _, test := range []struct {
			args map[string]string
			want int
		}{
			{map[string]string{"kind": spec.kind, "id": id}, 200},
			{map[string]string{"kind": spec.kind, "id": "missing"}, 404},
			{map[string]string{"kind": spec.kind, "id": "../operations"}, 422},
			{map[string]string{"kind": spec.kind, "id": "one%2fexplanation"}, 422},
			{map[string]string{"kind": spec.kind, "id": id, "at": "future"}, 422},
			{map[string]string{"kind": spec.kind, "id": id, "explanation": "execute now"}, 422},
			{map[string]string{"kind": spec.kind, "id": id, "quote": "replacement"}, 422},
		} {
			reply := session.request(t, nextID, "tools/call", map[string]any{"name": "inspect_record", "arguments": test.args})
			nextID++
			var result struct {
				Content []struct{ Text string }
				IsError bool `json:"isError"`
			}
			if reply["error"] != nil || json.Unmarshal(reply["result"], &result) != nil || len(result.Content) != 1 {
				t.Fatal(reply)
			}
			var owner struct {
				HTTPStatus int             `json:"http_status"`
				Body       json.RawMessage `json:"body"`
			}
			if json.Unmarshal([]byte(result.Content[0].Text), &owner) != nil || owner.HTTPStatus != test.want || result.IsError != (test.want != 200) {
				t.Fatal(result)
			}
			if test.want == 200 {
				var expected, actual any
				if json.Unmarshal(spec.raw, &expected) != nil || json.Unmarshal(owner.Body, &actual) != nil || !reflect.DeepEqual(expected, actual) {
					t.Fatal("MCP changed owner evidence", string(owner.Body))
				}
			}
		}
	}
	session.in.Close()
	if err := <-session.done; err != nil {
		t.Fatal(err)
	}
}
