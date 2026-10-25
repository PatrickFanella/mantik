package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

// Uses actual comparison, review and attribution owners in both Memory and
// disposable PostgreSQL HTTP contracts. No caller record substitutes for replay.
func supervisionAllocationInspections(t *testing.T, request func(string, string, any, int) []byte, base, receiver, comparison, review, decision string) {
	t.Helper()
	beforeComparisons := request("GET", "/v1/allocation-comparisons", nil, 200)
	beforeReviews := request("GET", "/v1/admission-reviews", nil, 200)
	session := mcpTestSession(t, base, receiver)
	session.init(t)
	listed := session.request(t, 2, "tools/list", map[string]any{})
	var catalog struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Properties map[string]struct{ Enum []string }
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
			for _, kind := range tool.InputSchema.Properties["kind"].Enum {
				kinds[kind] = true
			}
		}
	}
	next := 3
	for _, spec := range []struct{ kind, id, path string }{
		{"allocation_comparison", comparison, "/v1/allocation-comparisons/" + comparison},
		{"admission_review", review, "/v1/admission-reviews/" + review},
		{"decision_attribution", decision, "/v1/paper-attribution/" + decision},
	} {
		if !kinds[spec.kind] {
			t.Fatal("missing MCP inspection", spec.kind)
		}
		expected := request("GET", spec.path, nil, 200)
		call := map[string]any{"tool": "inspect_record", "arguments": map[string]string{"kind": spec.kind, "id": spec.id}}
		rawCall, _ := json.Marshal(call)
		chunked, _ := http.NewRequest("POST", base+"/v1/supervision/calls", bytes.NewReader(rawCall))
		chunked.Header.Set("Authorization", "Bearer owned-decision-token")
		chunked.ContentLength, chunked.TransferEncoding = -1, []string{"chunked"}
		response, err := http.DefaultClient.Do(chunked)
		if err != nil {
			t.Fatal(err)
		}
		chunkedBody, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Equal(chunkedBody, expected) {
			t.Fatal("incoming framing changed owner GET", spec.kind, err)
		}
		via := request("POST", "/v1/supervision/calls", call, 200)
		if !bytes.Equal(via, expected) {
			t.Fatal("supervision changed actual owner record", spec.kind)
		}
		unauthenticated, err := http.Get(base + spec.path)
		if err != nil {
			t.Fatal(err)
		}
		unauthenticated.Body.Close()
		if unauthenticated.StatusCode != 401 {
			t.Fatal("owner inspection without auth", unauthenticated.StatusCode)
		}
		for _, test := range []struct {
			args   map[string]string
			status int
		}{
			{map[string]string{"kind": spec.kind, "id": spec.id}, 200},
			{map[string]string{"kind": spec.kind, "id": "missing"}, 404},
			{map[string]string{"kind": spec.kind, "id": "../operations"}, 422},
			{map[string]string{"kind": spec.kind, "id": "one%2fother"}, 422},
			{map[string]string{"kind": spec.kind, "id": spec.id, "at": "future"}, 422},
			{map[string]string{"kind": spec.kind, "id": spec.id, "admission_available": "true"}, 422},
			{map[string]string{"kind": spec.kind, "id": spec.id, "balance_cents": "1000"}, 422},
			{map[string]string{"kind": spec.kind, "id": spec.id, "effect": "invented"}, 422},
		} {
			reply := session.request(t, next, "tools/call", map[string]any{"name": "inspect_record", "arguments": test.args})
			next++
			var result struct {
				Content []struct{ Text string }
				IsError bool `json:"isError"`
			}
			if reply["error"] != nil || json.Unmarshal(reply["result"], &result) != nil || len(result.Content) != 1 {
				t.Fatal(reply)
			}
			var owned struct {
				HTTPStatus int             `json:"http_status"`
				Body       json.RawMessage `json:"body"`
			}
			if json.Unmarshal([]byte(result.Content[0].Text), &owned) != nil || owned.HTTPStatus != test.status || result.IsError != (test.status != 200) {
				t.Fatal(result)
			}
			if test.status == 200 {
				var a, b any
				if json.Unmarshal(expected, &a) != nil || json.Unmarshal(owned.Body, &b) != nil || !reflect.DeepEqual(a, b) {
					t.Fatal("MCP changed actual owner record", spec.kind)
				}
			}
		}
	}
	for _, path := range []string{"/v1/allocation-comparisons?", "/v1/allocation-comparisons/" + comparison + "?at=future", "/v1/allocation-comparisons/invalid.id"} {
		request("GET", path, nil, 422)
	}
	for _, path := range []string{"/v1/allocation-comparisons", "/v1/allocation-comparisons/" + comparison} {
		request("GET", path, map[string]string{"effect": "invented"}, 422)
	}
	if !bytes.Equal(beforeComparisons, request("GET", "/v1/allocation-comparisons", nil, 200)) || !bytes.Equal(beforeReviews, request("GET", "/v1/admission-reviews", nil, 200)) {
		t.Fatal("inspection mutated retained owner history")
	}
	session.in.Close()
	if err := <-session.done; err != nil {
		t.Fatal(err)
	}
}
