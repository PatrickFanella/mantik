package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOwnedSocialResearchHTTP(t *testing.T) { ownedSocialResearchHTTP(t, storage.NewMemory()) }
func ownedSocialResearchHTTP(t *testing.T, s socialHTTPBackend) {
	t.Helper()
	ctx := context.Background()
	history, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := &ownedDecisionClock{at: time.Now().UTC().Add(time.Hour)}
	server := httptest.NewServer(service.HandlerWithClock(s, "research-fixture-token", clock))
	defer server.Close()
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		if v, ok := body.(string); ok {
			raw = []byte(v)
		}
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer research-fixture-token")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want, string(out))
		}
		return out
	}
	policy := socialwatch.PolicyCommand{ID: "research-watch-policy", ReceiverID: history.ReceiverID, Policy: socialwatch.Policy{ID: "research-watch", Provider: "recorded", AccountID: "research-source", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	var registered socialwatch.Receipt
	if e := json.Unmarshal(request("POST", "/v1/social-watches", policy, 201), &registered); e != nil {
		t.Fatal(e)
	}
	original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "research-source", PostID: "post"}, Revision: 1, Kind: "post", OccurredAt: clock.at, ObservedAt: clock.at, Text: "SPY retained research claim", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(original)
	c := socialwatch.CaptureCommand{ID: "research-recording", ReceiverID: history.ReceiverID, WatchID: policy.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: true}
	var receipt, retry socialwatch.Receipt
	if e := json.Unmarshal(request("POST", "/v1/social-recordings", c, 201), &receipt); e != nil || receipt.Entry.Capture.ResearchRequest == nil {
		t.Fatal(receipt, e)
	}
	clock.panicRead = true
	if e := json.Unmarshal(request("POST", "/v1/social-recordings", c, 200), &retry); e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, receipt.Entry) {
		t.Fatal(retry, e)
	}
	var all socialwatch.Catalog
	if e := json.Unmarshal(request("GET", "/v1/social-history", nil, 200), &all); e != nil || len(all.ResearchRequests) != 1 || !reflect.DeepEqual(all.ResearchRequests[0], *receipt.Entry.Capture.ResearchRequest) {
		t.Fatal(all, e)
	}
	var retained socialwatch.CaptureRecord
	if e := json.Unmarshal(request("GET", "/v1/social-recordings/"+c.ID, nil, 200), &retained); e != nil || !reflect.DeepEqual(retained, *receipt.Entry.Capture) {
		t.Fatal(e)
	}
	clock.panicRead = false
	for _, field := range []string{`"account_id":"caller-account",`, `"research_request":{},`, `"dispatch_available":true,`, `"admission_available":true,`} {
		request("POST", "/v1/social-recordings", strings.Replace(string(mustCopyJSON(t, c)), `"id":`, field+`"id":`, 1), 422)
	}
	// Select an already retained source through the real typed MCP transport.
	t.Setenv("MANTIK_API_TOKEN", "research-fixture-token")
	plain := original
	plain.Source.PostID = "retained-source"
	plain.Text = "Retained source selected later"
	plainRaw, _ := json.Marshal(plain)
	recordOnly := c
	recordOnly.ID = "retained-source"
	recordOnly.RequestResearch = false
	recordOnly.Original = string(plainRaw)
	recordOnly.OriginalSHA256 = workerplan.Digest(plainRaw)
	var sourceReceipt socialwatch.Receipt
	if e := json.Unmarshal(request("POST", "/v1/social-recordings", recordOnly, 201), &sourceReceipt); e != nil {
		t.Fatal(e)
	}
	selected := socialwatch.ResearchCommand{ID: "typed-retained-research", ReceiverID: history.ReceiverID, CaptureID: recordOnly.ID, CaptureSHA256: sourceReceipt.Entry.Capture.SHA256}
	session := mcpTestSession(t, server.URL, history.ReceiverID)
	session.init(t)
	response := session.request(t, "select-retained", "tools/call", map[string]any{"name": "request_social_research", "arguments": selected})
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	var selectedOwner struct {
		HTTPStatus int                 `json:"http_status"`
		Body       socialwatch.Receipt `json:"body"`
	}
	if json.Unmarshal(response["result"], &result) != nil || result.IsError || len(result.Content) != 1 || json.Unmarshal([]byte(result.Content[0].Text), &selectedOwner) != nil || selectedOwner.HTTPStatus != 201 || selectedOwner.Body.Entry.Research == nil {
		t.Fatal(response)
	}
	issued := selectedOwner.Body.Entry.Research.Request
	if issued.CaptureID != recordOnly.ID || issued.OriginalSHA256 != recordOnly.OriginalSHA256 || issued.DispatchAvailable || issued.AdmissionAvailable || issued.ExecutionAvailable || issued.QualificationCredit {
		t.Fatal(issued)
	}
	clock.panicRead = true
	response = session.request(t, "retry-retained", "tools/call", map[string]any{"name": "request_social_research", "arguments": selected})
	if json.Unmarshal(response["result"], &result) != nil || result.IsError || json.Unmarshal([]byte(result.Content[0].Text), &selectedOwner) != nil || selectedOwner.HTTPStatus != 200 || !selectedOwner.Body.Duplicate {
		t.Fatal(response)
	}
	var readback socialwatch.ResearchRequest
	if e := json.Unmarshal(request("GET", "/v1/social-research-requests/"+issued.ID, nil, 200), &readback); e != nil || !reflect.DeepEqual(readback, issued) {
		t.Fatal(readback, e)
	}
	response = session.request(t, "inspect-retained", "tools/call", map[string]any{"name": "inspect_record", "arguments": map[string]string{"kind": "social_research_request", "id": issued.ID}})
	if json.Unmarshal(response["result"], &result) != nil || result.IsError || !strings.Contains(result.Content[0].Text, recordOnly.OriginalSHA256) {
		t.Fatal(response)
	}
	retained = socialwatch.CaptureRecord{}
	if e := json.Unmarshal(request("GET", "/v1/social-recordings/"+recordOnly.ID, nil, 200), &retained); e != nil || !reflect.DeepEqual(retained, *sourceReceipt.Entry.Capture) {
		t.Fatal("selected source rewritten", e)
	}
	clock.panicRead = false
	for _, field := range []string{`"original":"caller",`, `"account_id":"caller",`, `"admission_available":true,`, `"dispatch_available":true,`} {
		request("POST", "/v1/social-research-requests", strings.Replace(string(mustCopyJSON(t, selected)), `"id":`, field+`"id":`, 1), 422)
	}
	wrong := selected
	wrong.ID = "wrong-selection"
	wrong.CaptureSHA256 = strings.Repeat("0", 64)
	request("POST", "/v1/social-research-requests", wrong, 409)
	absent := selected
	absent.ID = "absent-selection"
	absent.CaptureID = "absent"
	request("POST", "/v1/social-research-requests", absent, 404)
	request("GET", "/v1/social-research-requests/"+issued.ID+"?at=future", nil, 422)
	unauthorized, _ := http.Post(server.URL+"/v1/social-recordings", "application/json", bytes.NewReader(mustCopyJSON(t, c)))
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatal(unauthorized.StatusCode)
	}
}
