package news

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalProvenanceCannotClaimOperatorOrRecordedIdentity(t *testing.T) {
	r := Fixture()
	article, _ := ArticleFromRecording(r)
	raw, _ := json.Marshal(article)
	h := strings.Repeat("a", 64)
	r.LocalExtraction = &LocalExtractionProvenance{Schema: LocalExtractionProvenanceSchema, ReceiverID: "receiver-abcdefghijklmnop", CaptureID: "local", CaptureSHA256: h, PreparedSHA256: h, FinishedSHA256: h, ResultSHA256: h, ArticleSHA256: SHA256(string(raw)), ModelArtifactSHA256: h, CandidateArtifactSHA256: h, ContractSHA256: h, PromptVersion: "local-news-quotes-v2", Authority: "caller_reported_local_capture_only"}
	if OperatorRecording(r) || r.Validate() == nil {
		t.Fatal("operator label accepted local provenance")
	}
	r.ExtractionMethod = ReviewedLocalExtractionMethod
	result, err := Normalize(r)
	if err != nil || result.LocalExtraction == nil || result.Extraction != nil {
		t.Fatal(err)
	}
	r.LocalExtraction.CaptureID = "changed"
	if result.LocalExtraction.CaptureID == "changed" {
		t.Fatal("normalization retained aliases")
	}
	for _, change := range []func(*Recording){func(r *Recording) { r.LocalExtraction.ExecutionAttested = true }, func(r *Recording) { r.LocalExtraction.QualificationCredit = true }, func(r *Recording) { r.LocalExtraction.ArticleSHA256 = strings.Repeat("f", 64) }, func(r *Recording) { r.Extraction = &ExtractionProvenance{} }, func(r *Recording) { r.ExtractionMethod = ReviewedExtractionMethod }, func(r *Recording) { r.LocalExtraction = nil }} {
		b, _ := json.Marshal(r)
		var bad Recording
		json.Unmarshal(b, &bad)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid mixed provenance accepted")
		}
	}
	old, _ := json.Marshal(Fixture())
	if strings.Contains(string(old), "local_extraction") {
		t.Fatal("old operator wire bytes changed")
	}
}
