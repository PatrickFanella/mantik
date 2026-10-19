package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const LocalCorpusSchema = "mantik-local-extraction-corpus-v1"
const LocalEvaluationSchema = "mantik-local-extraction-evaluation-v1"
const MaxLocalEvaluationBytes = 64 << 20

type LocalEvaluationInput struct {
	ID       string        `json:"id"`
	Prepared LocalReceipt  `json:"prepared"`
	Finished *LocalReceipt `json:"finished,omitempty"`
	Expected Response      `json:"expected"`
}
type LocalCorpus struct {
	Schema string                 `json:"schema"`
	Cases  []LocalEvaluationInput `json:"cases"`
}
type LocalCaseScore struct {
	Input      LocalEvaluationInput `json:"input"`
	Inspection LocalInspection      `json:"inspection"`
	Comparison *CaseScore           `json:"comparison,omitempty"`
}
type LocalEvaluationCounts struct {
	DistinctPreparedReceipts   int              `json:"distinct_prepared_receipts"`
	RepeatedPreparedCases      int              `json:"repeated_prepared_cases"`
	Cases                      int              `json:"cases"`
	FinishedCases              int              `json:"finished_cases"`
	UnknownCases               int              `json:"unknown_cases"`
	UnknownExpectedExtracted   int              `json:"unknown_expected_extracted"`
	UnknownExpectedAbstentions int              `json:"unknown_expected_abstentions"`
	UnknownExpectedClaims      int              `json:"unknown_expected_claims"`
	Compared                   EvaluationCounts `json:"compared"`
	Dispositions               map[string]int   `json:"dispositions"`
}
type LocalEvaluation struct {
	Schema              string                `json:"schema"`
	CorpusSHA256        string                `json:"corpus_sha256"`
	ContractSHA256      string                `json:"contract_sha256"`
	Counts              LocalEvaluationCounts `json:"counts"`
	Cases               []LocalCaseScore      `json:"cases"`
	Authority           string                `json:"authority"`
	ExecutionAttested   bool                  `json:"execution_attested"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}

// EvaluateLocal compares checked finished captures with supplied annotations.
// Prepared-only completion remains unknown, never a fabricated abstention.
// Full receipts and every input stay in the report; no model or API is called.
func EvaluateLocal(ctx context.Context, input LocalCorpus) (LocalEvaluation, error) {
	return evaluateCapturedCorpus(ctx, input, LocalCorpusSchema, LocalEvaluationSchema, "local", InspectLocal)
}

// evaluateCapturedCorpus shares comparison and counting only. Each entry point
// supplies its schema fence and provider-specific receipt replay.
func evaluateCapturedCorpus(ctx context.Context, input LocalCorpus, corpusSchema, evaluationSchema, label string, inspect func(context.Context, LocalReceipt, *LocalReceipt) (LocalInspection, error)) (LocalEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return LocalEvaluation{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > MaxLocalEvaluationBytes || input.Schema != corpusSchema || len(input.Cases) < 1 || len(input.Cases) > MaxEvaluationCases {
		return LocalEvaluation{}, ErrInvalid
	}
	corpus := copyJSON(input)
	seen := map[string]bool{}
	snapshots := map[string]bool{}
	inspections := make([]LocalInspection, len(corpus.Cases))
	gold := make([]Result, len(corpus.Cases))
	for i, item := range corpus.Cases {
		if !eventKey.MatchString(item.ID) || seen[item.ID] {
			return LocalEvaluation{}, ErrInvalid
		}
		seen[item.ID] = true
		for _, v := range []any{item.Prepared, item.Finished} {
			b, e := json.Marshal(v)
			if e != nil || len(b) > MaxLocalReceiptBytes {
				return LocalEvaluation{}, ErrInvalid
			}
		}
		report, e := inspect(ctx, item.Prepared, item.Finished)
		if e != nil {
			return LocalEvaluation{}, fmt.Errorf("%s evaluation case %d: %w", label, i+1, e)
		}
		snapshot := item.Prepared.SHA256 + ":" + report.FinishedSHA256
		if snapshots[snapshot] {
			return LocalEvaluation{}, ErrInvalid
		}
		snapshots[snapshot] = true
		if i > 0 && report.ContractSHA256 != inspections[0].ContractSHA256 {
			return LocalEvaluation{}, ErrInvalid
		}
		inspections[i] = report
		requestBytes, e := inspectBytes(item.Prepared.Request, maxLocalRequestBytes)
		if e != nil {
			return LocalEvaluation{}, e
		}
		var request localRequest
		if json.Unmarshal(requestBytes, &request) != nil || len(request.Messages) != 2 {
			return LocalEvaluation{}, ErrInvalid
		}
		article, e := news.ReadArticle(bytes.NewReader([]byte(request.Messages[1].Content)))
		if e != nil {
			return LocalEvaluation{}, ErrInvalid
		}
		expected, e := evaluateExpected(ctx, article, item.Prepared.EventKey, item.Expected)
		if e != nil {
			return LocalEvaluation{}, e
		}
		gold[i] = expected
	}
	r := LocalEvaluation{Schema: evaluationSchema, CorpusSHA256: hash(corpus), ContractSHA256: inspections[0].ContractSHA256, Cases: []LocalCaseScore{}, Counts: LocalEvaluationCounts{Dispositions: map[string]int{}}, Authority: "supplied_annotation_agreement_only"}
	prepared := map[string]bool{}
	for i, item := range corpus.Cases {
		if err := ctx.Err(); err != nil {
			return LocalEvaluation{}, err
		}
		score := LocalCaseScore{Input: item, Inspection: inspections[i]}
		r.Counts.Cases++
		if prepared[item.Prepared.SHA256] {
			r.Counts.RepeatedPreparedCases++
		} else {
			prepared[item.Prepared.SHA256] = true
			r.Counts.DistinctPreparedReceipts++
		}
		r.Counts.Dispositions[score.Inspection.Disposition]++
		if item.Finished == nil {
			r.Counts.UnknownCases++
			r.Counts.UnknownExpectedClaims += len(gold[i].Claims)
			if item.Expected.Outcome == "extracted" {
				r.Counts.UnknownExpectedExtracted++
			} else {
				r.Counts.UnknownExpectedAbstentions++
			}
		} else {
			r.Counts.FinishedCases++
			comparison := compareEvaluationCase(item.ID, item.Expected, gold[i], *item.Finished.Result)
			score.Comparison = &comparison
			addEvaluationCounts(&r.Counts.Compared, comparison)
		}
		r.Cases = append(r.Cases, score)
	}
	r.SHA256 = hash(r)
	if err := ctx.Err(); err != nil {
		return LocalEvaluation{}, err
	}
	return r, nil
}
