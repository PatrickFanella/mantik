package news

import (
	"encoding/json"
	"regexp"
)

const ReviewedExtractionMethod = "reviewed_recorded_extraction_v1"
const ExtractionProvenanceSchema = "mantik-reviewed-extraction-provenance-v1"

var provenanceReceiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var provenanceAttempt = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// ExtractionProvenance contains immutable research links, not qualification or
// permission to execute. Only the service-owned admission path verifies these
// links against retained attempts; normalization checks their structure/binding.
type ExtractionProvenance struct {
	Schema         string `json:"schema"`
	ReceiverID     string `json:"receiver_id"`
	AttemptID      string `json:"attempt_id"`
	AttemptSHA256  string `json:"attempt_sha256"`
	ResultSHA256   string `json:"result_sha256"`
	ArticleSHA256  string `json:"article_sha256"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ContractSHA256 string `json:"contract_sha256"`
}

func (p ExtractionProvenance) valid(r Recording) bool {
	if p.Schema != ExtractionProvenanceSchema || !provenanceReceiver.MatchString(p.ReceiverID) || !provenanceAttempt.MatchString(p.AttemptID) {
		return false
	}
	for _, s := range []string{p.AttemptSHA256, p.ResultSHA256, p.ArticleSHA256, p.ArtifactSHA256, p.ContractSHA256} {
		if !digest.MatchString(s) {
			return false
		}
	}
	a, err := ArticleFromRecording(r)
	if err != nil {
		return false
	}
	raw, _ := json.Marshal(a)
	return p.ArticleSHA256 == SHA256(string(raw))
}

// OperatorRecording keeps the original unaffiliated submission/journal path
// distinct from reviewed extraction admission. No caller-supplied provenance
// can turn an ordinary recording submission into a verified extraction link.
func OperatorRecording(r Recording) bool {
	return r.ExtractionMethod == "operator_recorded_v1" && r.Extraction == nil && r.LocalExtraction == nil
}
