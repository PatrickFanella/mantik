package extraction

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const LocalInspectionSchema = "mantik-local-extraction-inspection-v1"
const MaxLocalReceiptBytes = 8 << 20
const maxLocalRequestBytes = 2 << 20

var ErrLocalEvidence = errors.New("invalid or inconsistent retained local extraction evidence")
var ErrLocalVersion = errors.New("unsupported retained local extraction version")

type LocalInspection struct {
	Schema                  string      `json:"schema"`
	State                   string      `json:"state"`
	Config                  LocalConfig `json:"config"`
	PromptVersion           string      `json:"prompt_version"`
	ArticleSHA256           string      `json:"article_sha256"`
	ContractSHA256          string      `json:"contract_sha256"`
	EventKey                string      `json:"event_key"`
	PreparedSHA256          string      `json:"prepared_sha256"`
	FinishedSHA256          string      `json:"finished_sha256,omitempty"`
	Disposition             string      `json:"disposition"`
	Outcome                 string      `json:"outcome,omitempty"`
	ReasonCode              string      `json:"reason_code,omitempty"`
	ClaimCount              int         `json:"claim_count"`
	ModelArtifactSHA256     string      `json:"model_artifact_sha256,omitempty"`
	CandidateArtifactSHA256 string      `json:"candidate_artifact_sha256,omitempty"`
	Checks                  []string    `json:"checks"`
	Authority               string      `json:"authority"`
	ExecutionAttested       bool        `json:"execution_attested"`
	QualificationCredit     bool        `json:"qualification_credit"`
	SHA256                  string      `json:"sha256"`
}

func inspectBytes(b LocalBytes, limit int) ([]byte, error) {
	if b.Bytes < 0 || b.Bytes > limit || len(b.Base64) > base64.StdEncoding.EncodedLen(limit) {
		return nil, ErrLocalEvidence
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(b.Base64)
	if err != nil || len(raw) != b.Bytes || base64.StdEncoding.EncodeToString(raw) != b.Base64 || news.SHA256(string(raw)) != b.SHA256 {
		return nil, ErrLocalEvidence
	}
	return raw, nil
}

type localReplay struct {
	raw []byte
	err error
}

func (r localReplay) Extract(context.Context, Frame) ([]byte, error) { return r.raw, r.err }

// InspectLocal checks consistency of retained bytes only, without a clock,
// HTTP client or receipt writes. Reported transport/timing metadata cannot
// attest that a model ran, that a timeout occurred, or that the source is true.
func InspectLocal(ctx context.Context, prepared LocalReceipt, finished *LocalReceipt) (LocalInspection, error) {
	return inspectCaptured(ctx, prepared, finished, LocalSchema, LocalInspectionSchema, "local_model_claim_candidates_only", prepared.Config.Validate)
}

func inspectCaptured(ctx context.Context, prepared LocalReceipt, finished *LocalReceipt, schema, inspectionSchema, authority string, validate func() error) (LocalInspection, error) {
	if err := ctx.Err(); err != nil {
		return LocalInspection{}, err
	}
	if prepared.Schema != schema || finished != nil && finished.Schema != schema {
		return LocalInspection{}, ErrLocalVersion
	}
	if validate() != nil || prepared.PromptVersion != LocalPromptVersion || !validAt(prepared.StartedAt) || !eventKey.MatchString(prepared.EventKey) || !prepared.Request.Complete {
		return LocalInspection{}, ErrLocalEvidence
	}
	raw, err := inspectBytes(prepared.Request, maxLocalRequestBytes)
	if err != nil {
		return LocalInspection{}, err
	}
	var request localRequest
	if workerplan.ReadBoundedShape(raw, &request, maxLocalRequestBytes) != nil || len(request.Messages) != 2 {
		return LocalInspection{}, ErrLocalEvidence
	}
	article, err := news.ReadArticle(bytes.NewReader([]byte(request.Messages[1].Content)))
	if err != nil || (article.Schema == news.DiagnosticArticleSchema) != (schema == SwitchyardDiagnosticSchema) || !bytes.Equal(raw, captureRequestBytes(article, prepared.Config, schema)) {
		return LocalInspection{}, ErrLocalEvidence
	}
	expected := LocalReceipt{Schema: schema, Config: prepared.Config, PromptVersion: LocalPromptVersion,
		ArticleSHA256: hash(article), ContractSHA256: hash(definitionFor(article)), EventKey: prepared.EventKey,
		StartedAt: prepared.StartedAt, Status: "prepared", Disposition: "not_sent", Request: prepared.Request,
		Authority: authority}
	if !reflect.DeepEqual(sealLocal(expected), prepared) {
		return LocalInspection{}, ErrLocalEvidence
	}
	report := LocalInspection{Schema: inspectionSchema, State: "prepared_only", Config: prepared.Config,
		PromptVersion: prepared.PromptVersion, ArticleSHA256: prepared.ArticleSHA256, ContractSHA256: prepared.ContractSHA256,
		EventKey: prepared.EventKey, PreparedSHA256: prepared.SHA256, Disposition: "completion_unknown",
		Checks: []string{"prepared_hash", "request_source_contract"}, Authority: "retained_local_bytes_consistency_only"}
	if finished != nil {
		f := copyJSON(*finished)
		if !validAt(f.CompletedAt) || f.CompletedAt.Before(prepared.StartedAt) || f.Result == nil {
			return LocalInspection{}, ErrLocalEvidence
		}
		var candidate []byte
		var replayErr error
		if f.HTTPStatus == 0 {
			// These are caller-reported events with no response bytes to replay.
			if f.Response != (LocalBytes{}) || (f.Disposition != "transport_failed" && f.Disposition != "not_sent") || f.Disposition == "not_sent" && f.Result.ReasonCode != "extractor_timeout" {
				return LocalInspection{}, ErrLocalEvidence
			}
			expected.Disposition = f.Disposition
			replayErr = errors.New("reported local transport failure")
		} else {
			if f.HTTPStatus < 100 || f.HTTPStatus > 599 {
				return LocalInspection{}, ErrLocalEvidence
			}
			body, err := inspectBytes(f.Response, MaxLocalHTTPBytes)
			if err != nil {
				return LocalInspection{}, err
			}
			expected.HTTPStatus, expected.Response = f.HTTPStatus, f.Response
			candidate, replayErr = interpretLocalResponse(&expected, article, body)
		}
		// The live caller may cancel after any capture stage. We can reproduce
		// its claimed result but cannot prove when cancellation actually occurred.
		if f.Result.ReasonCode == "extractor_timeout" {
			replayErr = context.DeadlineExceeded
		}
		result, err := Run(ctx, article, prepared.EventKey, localReplay{raw: candidate, err: replayErr})
		if err != nil {
			return LocalInspection{}, err
		}
		if err := ctx.Err(); err != nil {
			return LocalInspection{}, err
		}
		expected.Status, expected.CompletedAt, expected.Result = "finished", f.CompletedAt, &result
		if !reflect.DeepEqual(sealLocal(expected), f) {
			return LocalInspection{}, ErrLocalEvidence
		}
		report.State, report.FinishedSHA256, report.Disposition = "finished", f.SHA256, f.Disposition
		report.Outcome, report.ReasonCode, report.ClaimCount = result.Outcome, result.ReasonCode, len(result.Claims)
		if expected.ModelArtifact != nil {
			report.ModelArtifactSHA256 = expected.ModelArtifact.SHA256
		}
		if expected.Anchoring != nil {
			report.CandidateArtifactSHA256 = expected.Anchoring.CandidateArtifactSHA256
		}
		if expected.HTTPStatus == 0 {
			report.Checks = append(report.Checks, "reported_transport_shape")
		} else {
			report.Checks = append(report.Checks, "response_capture_structure")
		}
		if expected.ModelArtifact != nil {
			report.Checks = append(report.Checks, "model_content_binding")
		}
		if expected.Anchoring != nil {
			report.Checks = append(report.Checks, "quote_anchoring_replay")
		}
		report.Checks = append(report.Checks, "result_replay", "finished_hash")
	}
	report.SHA256 = hash(report)
	return report, nil
}
