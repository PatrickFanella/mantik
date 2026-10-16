package news

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorRecordingsRemainUnchangedAndProvenanceRequiresBinding(t *testing.T) {
	r := Fixture()
	raw, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if fields["extraction"] != nil || !OperatorRecording(r) {
		t.Fatal("legacy encoding acquired provenance")
	}
	n, _ := Normalize(r)
	raw, _ = json.Marshal(n)
	_ = json.Unmarshal(raw, &fields)
	if fields["extraction"] != nil {
		t.Fatal("legacy normalized result changed")
	}
	a, _ := ArticleFromRecording(r)
	encoded, _ := json.Marshal(a)
	p := &ExtractionProvenance{Schema: ExtractionProvenanceSchema, ReceiverID: "receiver-abcdefghijklmnop", AttemptID: "one", AttemptSHA256: strings.Repeat("a", 64), ResultSHA256: strings.Repeat("b", 64), ArticleSHA256: SHA256(string(encoded)), ArtifactSHA256: strings.Repeat("c", 64), ContractSHA256: strings.Repeat("d", 64)}
	r.ExtractionMethod = ReviewedExtractionMethod
	r.Extraction = p
	if !p.valid(r) || OperatorRecording(r) {
		t.Fatal("reviewed recording classified as ordinary")
	}
	result, err := Normalize(r)
	if err != nil || result.Extraction == nil {
		t.Fatal(result, err)
	}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.Retain(r); err == nil {
		t.Fatal("ordinary journal accepted unverifiable provenance")
	}
	r.Extraction.ArticleSHA256 = strings.Repeat("f", 64)
	if _, err = Normalize(r); err == nil {
		t.Fatal("provenance did not bind original metadata")
	}
	if result.Extraction.ArticleSHA256 == r.Extraction.ArticleSHA256 {
		t.Fatal("normalization retained mutable provenance alias")
	}
}
