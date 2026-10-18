package extraction

import "context"

const SwitchyardCorpusSchema = "mantik-switchyard-extraction-corpus-v1"
const SwitchyardEvaluationSchema = "mantik-switchyard-extraction-evaluation-v1"
const MaxSwitchyardEvaluationBytes = MaxLocalEvaluationBytes

type SwitchyardEvaluationInput struct {
	ID       string             `json:"id"`
	Prepared SwitchyardReceipt  `json:"prepared"`
	Finished *SwitchyardReceipt `json:"finished,omitempty"`
	Expected Response           `json:"expected"`
}
type SwitchyardCorpus struct {
	Schema string                      `json:"schema"`
	Cases  []SwitchyardEvaluationInput `json:"cases"`
}
type SwitchyardCaseScore struct {
	Input      SwitchyardEvaluationInput `json:"input"`
	Inspection SwitchyardInspection      `json:"inspection"`
	Comparison *CaseScore                `json:"comparison,omitempty"`
}

// Counts share semantics across providers; receipt replay and report schemas do not.
type SwitchyardEvaluationCounts = LocalEvaluationCounts

type SwitchyardEvaluation struct {
	Schema              string                     `json:"schema"`
	CorpusSHA256        string                     `json:"corpus_sha256"`
	ContractSHA256      string                     `json:"contract_sha256"`
	Counts              SwitchyardEvaluationCounts `json:"counts"`
	Cases               []SwitchyardCaseScore      `json:"cases"`
	Authority           string                     `json:"authority"`
	ExecutionAttested   bool                       `json:"execution_attested"`
	QualificationCredit bool                       `json:"qualification_credit"`
	SHA256              string                     `json:"sha256"`
}

// EvaluateSwitchyard compares retained captures with supplied annotations.
// Unknown completion is not scored as abstention. No model or service is called.
func EvaluateSwitchyard(ctx context.Context, input SwitchyardCorpus) (SwitchyardEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return SwitchyardEvaluation{}, err
	}
	if input.Schema != SwitchyardCorpusSchema || len(input.Cases) < 1 || len(input.Cases) > MaxEvaluationCases {
		return SwitchyardEvaluation{}, ErrInvalid
	}
	// The shared core works on the common wire fields; it never relaxes the
	// Switchyard inspector's schema, configuration or authority checks.
	corpus := LocalCorpus{Schema: input.Schema, Cases: make([]LocalEvaluationInput, len(input.Cases))}
	for i, item := range input.Cases {
		var finished *LocalReceipt
		if item.Finished != nil {
			value := LocalReceipt(*item.Finished)
			finished = &value
		}
		corpus.Cases[i] = LocalEvaluationInput{item.ID, LocalReceipt(item.Prepared), finished, item.Expected}
	}
	inspect := func(ctx context.Context, prepared LocalReceipt, finished *LocalReceipt) (LocalInspection, error) {
		var f *SwitchyardReceipt
		if finished != nil {
			value := SwitchyardReceipt(*finished)
			f = &value
		}
		r, err := InspectSwitchyard(ctx, SwitchyardReceipt(prepared), f)
		return LocalInspection(r), err
	}
	r, err := evaluateCapturedCorpus(ctx, corpus, SwitchyardCorpusSchema, SwitchyardEvaluationSchema, "Switchyard", inspect)
	if err != nil {
		return SwitchyardEvaluation{}, err
	}
	result := SwitchyardEvaluation{Schema: r.Schema, CorpusSHA256: r.CorpusSHA256, ContractSHA256: r.ContractSHA256, Counts: r.Counts, Cases: make([]SwitchyardCaseScore, len(r.Cases)), Authority: r.Authority, ExecutionAttested: r.ExecutionAttested, QualificationCredit: r.QualificationCredit, SHA256: r.SHA256}
	for i, score := range r.Cases {
		var finished *SwitchyardReceipt
		if score.Input.Finished != nil {
			value := SwitchyardReceipt(*score.Input.Finished)
			finished = &value
		}
		result.Cases[i] = SwitchyardCaseScore{Input: SwitchyardEvaluationInput{score.Input.ID, SwitchyardReceipt(score.Input.Prepared), finished, score.Input.Expected}, Inspection: SwitchyardInspection(score.Inspection), Comparison: score.Comparison}
	}
	return result, nil
}
