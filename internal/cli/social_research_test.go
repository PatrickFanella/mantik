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
	unauthorized, _ := http.Post(server.URL+"/v1/social-recordings", "application/json", bytes.NewReader(mustCopyJSON(t, c)))
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatal(unauthorized.StatusCode)
	}
}
