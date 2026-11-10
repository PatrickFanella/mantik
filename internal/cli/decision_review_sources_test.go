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
		original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: policy.Policy.AccountID, PostID: "http-" + kind + "-post"}, Revision: 1, Kind: "post", OccurredAt: clock.at.Add(-2 * time.Second), ObservedAt: clock.at.Add(-time.Second), Text: "Opaque unverified source statement", Symbols: []string{"SPY"}}
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
		var row storage.PaperAttributionRow
		if err := json.Unmarshal(request("GET", "/v1/paper-attribution/"+d.Command.ID, nil, 200), &row); err != nil || validateAttributionRow(row, d.Command.ReceiverID) != nil || row.ClaimReviewHistory == nil {
			t.Fatal("missing actual claim-source attribution", row, err)
		}
		found := false
		for _, timing := range row.ClaimReviewHistory.SourceTimings {
			if timing.ReviewID == c.ID {
				if timing.ObservationDelaySeconds != 1 || timing.AvailabilityDelaySeconds != 2 || timing.ClockRelation != "retained_after_decision_clock" || timing.DecisionInputVerified {
					t.Fatal("source delay or decision role", timing)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("review timing omitted")
		}
		analysis, err := analyzePaperAttribution(d.Command.ReceiverID, []storage.PaperAttributionRow{row})
		if err != nil || analysis.Quality[0].Decisions != 1 || analysis.Quality[0].IndependentSamples != nil || analysis.DecisionTrace[0].DecisionSHA256 != d.SHA256 {
			t.Fatal("actual native decision analysis", analysis, err)
		}
		measured := false
		for _, group := range analysis.SourceDelays {
			if group.Kind == kind {
				if group.UniqueCaptures != 1 || group.ReviewLinks != 1 || group.Observation != (attributionDelayStats{1000, 1000, 1000, 1000}) || group.Ingestion != (attributionDelayStats{1000, 1000, 1000, 1000}) || group.Availability != (attributionDelayStats{2000, 2000, 2000, 2000}) {
					t.Fatal("native source clocks changed in analysis", group)
				}
				measured = true
			}
		}
		if !measured {
			t.Fatal("native source-delay analysis omitted source kind", kind)
		}
		var supervised storage.PaperAttributionRow
		if err := json.Unmarshal(request("POST", "/v1/supervision/calls", map[string]any{"tool": "inspect_record", "arguments": map[string]string{"kind": "decision_attribution", "id": d.Command.ID}}, 200), &supervised); err != nil || !reflect.DeepEqual(supervised, row) {
			t.Fatal("supervision changed claim-source attribution", err)
		}
		for name, mutate := range map[string]func(*storage.PaperClaimReviewHistory){
			"invented decision input": func(h *storage.PaperClaimReviewHistory) { h.SourceTimings[0].DecisionInputVerified = true },
			"clock relation": func(h *storage.PaperClaimReviewHistory) {
				h.SourceTimings[0].ClockRelation = "retained_before_decision_clock"
			},
			"delay":                func(h *storage.PaperClaimReviewHistory) { h.SourceTimings[0].AvailabilityDelaySeconds++ },
			"capture substitution": func(h *storage.PaperClaimReviewHistory) { h.SourceTimings[0].CaptureSHA256 = strings.Repeat("f", 64) },
			"missing timing":       func(h *storage.PaperClaimReviewHistory) { h.SourceTimings = nil },
			"grant":                func(h *storage.PaperClaimReviewHistory) { h.Records[0].AdmissionAvailable = true },
		} {
			raw, _ := json.Marshal(row)
			var forged storage.PaperAttributionRow
			if err := json.Unmarshal(raw, &forged); err != nil {
				t.Fatal(err)
			}
			mutate(forged.ClaimReviewHistory)
			forged.ClaimReviewHistory.SHA256 = ""
			forged.ClaimReviewHistory.SHA256 = workerplan.Hash(forged.ClaimReviewHistory)
			forged.SHA256 = ""
			forged.SHA256 = workerplan.Hash(forged)
			if validateAttributionRow(forged, d.Command.ReceiverID) == nil {
				t.Fatal("self-rehashed forged claim attribution accepted", name)
			}
		}
		var display strings.Builder
		if err := renderPaperAttribution(&display, d.Command.ReceiverID, []storage.PaperAttributionRow{row}); err != nil || !strings.Contains(display.String(), "decision_input_verified=false") || !strings.Contains(display.String(), c.ID) || !strings.Contains(display.String(), "ingestion_delay_seconds=1") || !strings.Contains(display.String(), `kind="`+kind+`"`) || !strings.Contains(display.String(), retained.Entry.Capture.SHA256) {
			t.Fatal("claim timing not presented", display.String(), err)
		}
	}
}
