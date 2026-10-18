package news

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSwitchyardProvenanceCannotClaimOperatorOrRecordedIdentity(t *testing.T) {
	r := Fixture()
	article, _ := ArticleFromRecording(r)
	raw, _ := json.Marshal(article)
	h := strings.Repeat("a", 64)
	r.SwitchyardExtraction = &SwitchyardExtractionProvenance{Schema: SwitchyardExtractionProvenanceSchema, ReceiverID: "receiver-abcdefghijklmnop", CaptureID: "local", CaptureSHA256: h, PreparedSHA256: h, FinishedSHA256: h, ResultSHA256: h, ArticleSHA256: SHA256(string(raw)), ModelArtifactSHA256: h, CandidateArtifactSHA256: h, ContractSHA256: h, PromptVersion: "local-news-quotes-v2", Authority: "caller_reported_switchyard_capture_only"}
	if OperatorRecording(r) || r.Validate() == nil {
		t.Fatal("operator label accepted local provenance")
	}
	r.ExtractionMethod = ReviewedSwitchyardExtractionMethod
	for _, method := range []string{"operator_recorded_v1", ReviewedExtractionMethod, ReviewedLocalExtractionMethod} {
		bad := r
		bad.ExtractionMethod = method
		if bad.Validate() == nil {
			t.Fatal("Switchyard provenance accepted another method", method)
		}
	}
	mixed := r
	mixed.LocalExtraction = &LocalExtractionProvenance{}
	if mixed.Validate() == nil {
		t.Fatal("mixed local and Switchyard provenance accepted")
	}
	result, err := Normalize(r)
	if err != nil || result.SwitchyardExtraction == nil || result.Extraction != nil {
		t.Fatal(err)
	}
	r.SwitchyardExtraction.CaptureID = "changed"
	if result.SwitchyardExtraction.CaptureID == "changed" {
		t.Fatal("normalization retained aliases")
	}
	for _, change := range []func(*Recording){func(r *Recording) { r.SwitchyardExtraction.ExecutionAttested = true }, func(r *Recording) { r.SwitchyardExtraction.QualificationCredit = true }, func(r *Recording) { r.SwitchyardExtraction.ArticleSHA256 = strings.Repeat("f", 64) }, func(r *Recording) { r.Extraction = &ExtractionProvenance{} }, func(r *Recording) { r.ExtractionMethod = ReviewedExtractionMethod }, func(r *Recording) { r.SwitchyardExtraction = nil }} {
		b, _ := json.Marshal(r)
		var bad Recording
		json.Unmarshal(b, &bad)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid mixed provenance accepted")
		}
	}
	old, _ := json.Marshal(Fixture())
	if strings.Contains(string(old), "switchyard_extraction") {
		t.Fatal("old operator wire bytes changed")
	}
}
