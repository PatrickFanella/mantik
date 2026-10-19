// Package extraction checks bounded extractor artifacts against one original
// article. Its candidates carry no publication, source-truth or trading authority.
package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-news-extraction-v1"
const ContractSchema = "mantik-news-extraction-contract-v1"
const MaxArtifactBytes = 128 << 10
const Authority = "claim_candidates_only"

var eventKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type Contract struct {
	Schema           string   `json:"schema"`
	ArticleSchema    string   `json:"article_schema"`
	Outcomes         []string `json:"outcomes"`
	RequiredChecks   []string `json:"required_checks"`
	MaxClaims        int      `json:"max_claims"`
	MaxArtifactBytes int      `json:"max_artifact_bytes"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	Authority        string   `json:"authority"`
}

func Definition() Contract {
	return Contract{Schema: ContractSchema, ArticleSchema: news.ArticleSchema,
		Outcomes:       []string{"extracted", "abstain"},
		RequiredChecks: []string{"source_binding", "completion", "model_schema", "claim_bindings", "authority_boundary"},
		MaxClaims:      16, MaxArtifactBytes: MaxArtifactBytes, TimeoutSeconds: 30, Authority: Authority}
}

// definitionFor preserves the existing contract hash for exact-time articles.
func definitionFor(a news.Article) Contract {
	c := Definition()
	if a.Schema == news.DiagnosticArticleSchema {
		c.Schema = "mantik-news-diagnostic-extraction-contract-v1"
		c.ArticleSchema = news.DiagnosticArticleSchema
		c.Authority = "diagnostic_claim_candidates_only"
	}
	return c
}

// Claim intentionally has no event key, source identity, times or authority.
// Source metadata and the operator-selected event key come from the caller's
// pinned article/context, never from an extractor artifact.
type Claim struct {
	ID         string        `json:"id"`
	Code       string        `json:"code"`
	Assertion  string        `json:"assertion"`
	Assessment string        `json:"assessment"`
	Entities   []news.Entity `json:"entities"`
	Symbols    []string      `json:"symbols"`
	Evidence   []news.Span   `json:"evidence"`
}

type Response struct {
	Outcome    string  `json:"outcome"`
	ReasonCode string  `json:"reason_code"`
	Claims     []Claim `json:"claims"`
}

type Frame struct {
	Contract      Contract     `json:"contract"`
	Article       news.Article `json:"article"`
	ArticleSHA256 string       `json:"article_sha256"`
}

type Model interface {
	Extract(context.Context, Frame) ([]byte, error)
}

type Artifact struct {
	Bytes           int    `json:"bytes"`
	SHA256          string `json:"sha256"`
	CaptureComplete bool   `json:"capture_complete"`
	RawBase64       string `json:"raw_base64"`
}

type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type Result struct {
	Schema              string       `json:"schema"`
	Article             news.Article `json:"article"`
	ArticleSHA256       string       `json:"article_sha256"`
	ContractSHA256      string       `json:"contract_sha256"`
	EventKey            string       `json:"event_key"`
	Outcome             string       `json:"outcome"`
	ReasonCode          string       `json:"reason_code"`
	Artifact            Artifact     `json:"artifact"`
	Claims              []news.Claim `json:"claims"`
	Checks              []Check      `json:"checks"`
	Authority           string       `json:"authority"`
	QualificationCredit bool         `json:"qualification_credit"`
	SHA256              string       `json:"sha256"`
}

func hash(v any) string {
	raw, _ := json.Marshal(v)
	return news.SHA256(string(raw))
}
func copyJSON[T any](v T) T {
	raw, _ := json.Marshal(v)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}

// Run makes one cooperative, deadline-bounded extraction call. Implementations
// must honor ctx and bound transport reads; no detached goroutine claims to kill
// an uncooperative provider. The CLI supplies a local recorded artifact only.
func Run(ctx context.Context, article news.Article, key string, model Model) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	a, err := article.Canonical()
	if err != nil || !eventKey.MatchString(key) || model == nil {
		return Result{}, errors.New("extraction requires a valid article, operator event key and model")
	}
	contract := definitionFor(a)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(contract.TimeoutSeconds)*time.Second)
	defer cancel()
	result := Result{Schema: Schema, Article: a, ArticleSHA256: hash(a), ContractSHA256: hash(contract),
		EventKey: key, Claims: []news.Claim{}, Checks: []Check{}, Authority: contract.Authority}
	statuses := map[string]string{"source_binding": "passed", "authority_boundary": "passed"}
	finish := func(outcome, reason, failedCheck string) (Result, error) {
		result.Outcome, result.ReasonCode = outcome, reason
		if failedCheck != "" {
			statuses[failedCheck] = "failed"
		}
		for _, id := range contract.RequiredChecks {
			status := statuses[id]
			if status == "" {
				status = "not_run"
			}
			code := status
			if id == failedCheck {
				code = reason
			}
			result.Checks = append(result.Checks, Check{ID: id, Status: status, Code: code})
		}
		result.SHA256 = hash(result)
		return result, nil
	}
	raw, callErr := model.Extract(ctx, Frame{Contract: copyJSON(contract), Article: copyJSON(a), ArticleSHA256: result.ArticleSHA256})
	result.Artifact = Artifact{Bytes: len(raw), SHA256: news.SHA256(string(raw)), CaptureComplete: len(raw) <= MaxArtifactBytes}
	if result.Artifact.CaptureComplete {
		result.Artifact.RawBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	if ctx.Err() != nil || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
		return finish("abstain", "extractor_timeout", "completion")
	}
	if errors.Is(callErr, io.EOF) {
		return finish("abstain", "extractor_incomplete", "completion")
	}
	if callErr != nil {
		return finish("abstain", "extractor_unavailable", "completion")
	}
	statuses["completion"] = "passed"
	var response Response
	if workerplan.ReadBoundedShape(raw, &response, MaxArtifactBytes) != nil || response.Claims == nil {
		return finish("abstain", "malformed_extractor_artifact", "model_schema")
	}
	if response.Outcome == "abstain" {
		if len(response.Claims) != 0 || !slices.Contains([]string{"insufficient_source", "no_relevant_claims", "ambiguous_source"}, response.ReasonCode) {
			return finish("abstain", "malformed_extractor_artifact", "model_schema")
		}
		statuses["model_schema"] = "passed"
		return finish("abstain", response.ReasonCode, "")
	}
	if response.Outcome != "extracted" || response.ReasonCode != "claims_extracted" {
		return finish("abstain", "malformed_extractor_artifact", "model_schema")
	}
	statuses["model_schema"] = "passed"
	claims := []news.Claim{}
	for _, c := range response.Claims {
		claims = append(claims, news.Claim{ID: c.ID, EventKey: key, Code: c.Code,
			Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: c.Evidence})
	}
	if a.ValidateClaims(claims) != nil {
		return finish("abstain", "invalid_claim_bindings", "claim_bindings")
	}
	statuses["claim_bindings"] = "passed"
	result.Claims = claims
	return finish("extracted", "claims_extracted", "")
}

// FixtureResponse is invented output for FixtureArticle, not a model evaluation.
func FixtureResponse() Response {
	c := news.Fixture().Claims[0]
	return Response{Outcome: "extracted", ReasonCode: "claims_extracted", Claims: []Claim{{
		ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment,
		Entities: c.Entities, Symbols: c.Symbols, Evidence: c.Evidence}}}
}
