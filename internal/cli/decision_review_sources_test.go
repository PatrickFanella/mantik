package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func sourceDecisionReviewHTTP(t *testing.T, request func(string, string, any, int) []byte, clock *ownedDecisionClock, d storage.PaperDecisionRecord) {
	t.Helper()
	for _, kind := range []string{"social", "copy"} {
		clock.at = clock.at.Add(time.Second)
		policy := socialwatch.PolicyCommand{ID: "http-" + kind + "-review-policy", ReceiverID: d.Command.ReceiverID, Policy: socialwatch.Policy{ID: "http-" + kind + "-review-watch", Provider: "recorded", AccountID: "http-" + kind + "-account", Symbols: []string{"SPY"}, Behavior: "decision_review", MaxObservationDelaySeconds: 60, Enabled: true}}
		var registered socialwatch.Receipt
		if err := json.Unmarshal(request("POST", "/v1/"+kind+"-watches", policy, 201), &registered); err != nil {
			t.Fatal(err)
		}
		original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: policy.Policy.AccountID, PostID: "http-" + kind + "-post"}, Revision: 1, Kind: "post", OccurredAt: clock.at, ObservedAt: clock.at, Text: "Opaque unverified source statement", Symbols: []string{"SPY"}}
		class := "recorded_social_unqualified"
		if kind == "copy" {
			class = "recorded_copy_unqualified"
			original.Schema = socialwatch.CopyRecordingSchema
			original.Activity = &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "assertion-v1", Side: "buy", QuantityUnits: 10, QuantityUnitClaim: "shares", PriceCents: 10000}
		}
		raw, _ := json.Marshal(original)
		capture := socialwatch.CaptureCommand{ID: "http-" + kind + "-source", ReceiverID: d.Command.ReceiverID, WatchID: policy.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: class, Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
		var retained socialwatch.Receipt
		if err := json.Unmarshal(request("POST", "/v1/"+kind+"-recordings", capture, 201), &retained); err != nil {
			t.Fatal(err)
		}
		c := storage.DecisionReviewCommand{ID: "http-" + kind + "-source-review", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, Source: &storage.DecisionReviewSourceCommand{CaptureID: capture.ID, CaptureSHA256: retained.Entry.Capture.SHA256}, Claims: []storage.DecisionReviewClaim{{Field: "action", Value: "exit"}}}
		path := "/v1/paper-decision-reviews"
		body := any(c)
		if kind == "copy" {
			path = "/v1/supervision/calls"
			body = map[string]any{"tool": "review_decision_claims", "arguments": c}
		}
		var issued storage.DecisionReviewReceipt
		if err := json.Unmarshal(request("POST", path, body, 201), &issued); err != nil || issued.Record.Source == nil || issued.Record.Source.Kind != kind || issued.Record.Source.CaptureSHA256 != c.Source.CaptureSHA256 || issued.Record.Source.ProviderAccessVerified || issued.Record.Source.ClaimsExtractionVerified || issued.Record.ProseVerified || issued.Record.ModelUsed || issued.Record.AdmissionAvailable || issued.Record.ExecutionAvailable || issued.Record.QualificationCredit {
			t.Fatal(issued, err)
		}
		clock.panicRead = true
		var retry storage.DecisionReviewReceipt
		if err := json.Unmarshal(request("POST", path, body, 200), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, issued.Record) {
			t.Fatal(retry, err)
		}
		clock.panicRead = false
		changed := c
		changed.ID += "-duplicate"
		request("POST", "/v1/paper-decision-reviews", changed, 409)
		changed = c
		changed.Explanation = "Do not substitute original source text"
		request("POST", "/v1/paper-decision-reviews", changed, 422)
		raw, _ = json.Marshal(c)
		for _, field := range []string{`"capture_id":"duplicate",`, `"original":"caller source",`, `"policy_sha256":"caller policy",`, `"provider_access_verified":true,`} {
			request("POST", "/v1/paper-decision-reviews", strings.Replace(string(raw), `"capture_id":`, field+`"capture_id":`, 1), 422)
		}
		var actual storage.DecisionReviewRecord
		if err := json.Unmarshal(request("GET", "/v1/paper-decision-reviews/"+c.ID, nil, 200), &actual); err != nil || !reflect.DeepEqual(actual, issued.Record) {
			t.Fatal("readback changed source binding", err)
		}
	}
}
