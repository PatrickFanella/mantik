package news

import "encoding/json"

const ReviewedLocalExtractionMethod = "reviewed_local_capture_v1"
const LocalExtractionProvenanceSchema = "mantik-reviewed-local-capture-provenance-v1"

// LocalExtractionProvenance identifies a reviewed caller report. These links
// are not provider execution attestation, source truth or trading authority.
type LocalExtractionProvenance struct {
	Schema                  string `json:"schema"`
	ReceiverID              string `json:"receiver_id"`
	CaptureID               string `json:"capture_id"`
	CaptureSHA256           string `json:"capture_sha256"`
	PreparedSHA256          string `json:"prepared_sha256"`
	FinishedSHA256          string `json:"finished_sha256"`
	ResultSHA256            string `json:"result_sha256"`
	ArticleSHA256           string `json:"article_sha256"`
	ModelArtifactSHA256     string `json:"model_artifact_sha256"`
	CandidateArtifactSHA256 string `json:"candidate_artifact_sha256"`
	ContractSHA256          string `json:"contract_sha256"`
	PromptVersion           string `json:"prompt_version"`
	Authority               string `json:"authority"`
	ExecutionAttested       bool   `json:"execution_attested"`
	QualificationCredit     bool   `json:"qualification_credit"`
}

func (p LocalExtractionProvenance) valid(r Recording) bool {
	if p.Schema != LocalExtractionProvenanceSchema || !provenanceReceiver.MatchString(p.ReceiverID) || !provenanceAttempt.MatchString(p.CaptureID) || p.Authority != "caller_reported_local_capture_only" || p.ExecutionAttested || p.QualificationCredit || !text(p.PromptVersion, 128) {
		return false
	}
	for _, s := range []string{p.CaptureSHA256, p.PreparedSHA256, p.FinishedSHA256, p.ResultSHA256, p.ArticleSHA256, p.ModelArtifactSHA256, p.CandidateArtifactSHA256, p.ContractSHA256} {
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
