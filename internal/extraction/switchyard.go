package extraction

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const SwitchyardSchema = "mantik-switchyard-news-extraction-v1"
const SwitchyardInspectionSchema = "mantik-switchyard-extraction-inspection-v1"
const SwitchyardDiagnosticSchema = "mantik-switchyard-news-diagnostic-v1"
const SwitchyardDiagnosticInspectionSchema = "mantik-switchyard-diagnostic-inspection-v1"
const SwitchyardModel = "openai/gpt-6.1-sol"

// SwitchyardConfig pins the selected provider route. The gateway owns provider
// credentials. Requested generation settings are not an upstream attestation.
type SwitchyardConfig LocalConfig

func (c SwitchyardConfig) Validate() error {
	if c.Model != SwitchyardModel || c.ReasoningEffort != "low" {
		return errors.New("Switchyard extraction requires openai/gpt-6.1-sol and low reasoning")
	}
	return validateCaptureEndpoint(LocalConfig(c))
}

// Shared receipt fields have a separate schema and authority. Local import,
// admission and evaluation continue to reject Switchyard receipts.
type SwitchyardReceipt LocalReceipt
type SwitchyardInspection LocalInspection

func captureRequestBytes(article news.Article, config LocalConfig, schema string) []byte {
	if schema == LocalSchema {
		return localRequestBytes(article, config)
	}
	source, _ := json.Marshal(article)
	request := struct {
		Model           string         `json:"model"`
		Messages        []localMessage `json:"messages"`
		Stream          bool           `json:"stream"`
		MaxTokens       int            `json:"max_tokens"`
		ReasoningEffort string         `json:"reasoning_effort"`
		ResponseFormat  struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}{Model: config.Model, Messages: []localMessage{{Role: "system", Content: localInstructions}, {Role: "user", Content: string(source)}}, MaxTokens: config.MaxTokens, ReasoningEffort: config.ReasoningEffort}
	request.ResponseFormat.Type = "json_object"
	raw, _ := json.Marshal(request)
	return raw
}

// RunSwitchyard retains intent before one chat request. A client deadline does
// not prove the gateway stopped upstream execution or bounded provider spend.
func RunSwitchyard(ctx context.Context, article news.Article, key string, config SwitchyardConfig, token string, retain func(SwitchyardReceipt) error) (SwitchyardReceipt, error) {
	if err := config.Validate(); err != nil {
		return SwitchyardReceipt{}, err
	}
	if retain == nil {
		return SwitchyardReceipt{}, errors.New("Switchyard extraction requires a receipt sink")
	}
	schema, authority := SwitchyardSchema, "switchyard_model_claim_candidates_only"
	if article.Schema == news.DiagnosticArticleSchema {
		schema, authority = SwitchyardDiagnosticSchema, "switchyard_diagnostic_claim_candidates_only"
	}
	r, err := runCaptured(ctx, article, key, LocalConfig(config), token, func(r LocalReceipt) error { return retain(SwitchyardReceipt(r)) }, schema, authority)
	return SwitchyardReceipt(r), err
}

// InspectSwitchyard replays retained request/response consistency offline. It
// does not prove the gateway used this provider, reasoning effort or token cap.
func InspectSwitchyard(ctx context.Context, prepared SwitchyardReceipt, finished *SwitchyardReceipt) (SwitchyardInspection, error) {
	var f *LocalReceipt
	if finished != nil {
		copy := LocalReceipt(*finished)
		f = &copy
	}
	schema, inspectionSchema, authority := SwitchyardSchema, SwitchyardInspectionSchema, "switchyard_model_claim_candidates_only"
	if prepared.Schema == SwitchyardDiagnosticSchema {
		schema, inspectionSchema, authority = SwitchyardDiagnosticSchema, SwitchyardDiagnosticInspectionSchema, "switchyard_diagnostic_claim_candidates_only"
	}
	r, err := inspectCaptured(ctx, LocalReceipt(prepared), f, schema, inspectionSchema, authority, SwitchyardConfig(prepared.Config).Validate)
	if err == nil {
		r.Authority = "retained_switchyard_bytes_consistency_only"
		r.SHA256 = ""
		r.SHA256 = hash(r)
	}
	return SwitchyardInspection(r), err
}
