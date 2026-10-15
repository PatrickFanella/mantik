// Package investigation interprets bounded, typed model artifacts against an
// audited local news snapshot. It cannot issue decisions for operations.
package investigation

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const Schema = "mantik-news-review-v1"
const TypeID = "news-review-v1"
const MaxArtifactBytes = 16 << 10

type Contract struct {
	RequiredEvidence []string `json:"required_evidence"`
	CompletionRule   string   `json:"completion_rule"`
	TimeoutOutcome   string   `json:"timeout_outcome"`
	TypeID           string   `json:"type_id"`
	EvidenceSchema   string   `json:"evidence_schema"`
	Tools            []string `json:"tools"`
	Outcomes         []string `json:"outcomes"`
	RequiredChecks   []string `json:"required_checks"`
	MaxTurns         int      `json:"max_turns"`
	MaxToolCalls     int      `json:"max_tool_calls"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	Authority        string   `json:"authority"`
}

func Definition() Contract {
	return Contract{RequiredEvidence: []string{"audited_event_snapshot", "inspected_claims_for_non_abstention", "current_reported_claims_for_hypothesis"}, CompletionRule: "checked_proposal_or_finalized_abstention", TimeoutOutcome: "abstain", TypeID: TypeID, EvidenceSchema: news.EventSchema, Tools: []string{"inspect_news_claim"}, Outcomes: []string{"abstain", "request_evidence", "propose_hypothesis"}, RequiredChecks: []string{"model_schema", "completion", "tool_budget", "evidence_binding", "authority_boundary"}, MaxTurns: 8, MaxToolCalls: 4, TimeoutSeconds: 30, Authority: "research_only_unqualified"}
}

type Subject struct {
	JournalID      string    `json:"journal_id"`
	EventGroupID   string    `json:"event_group_id"`
	EventKey       string    `json:"event_key"`
	EvidenceClass  string    `json:"evidence_class"`
	AsOf           time.Time `json:"as_of"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
}
type ClaimRef struct {
	Key   string `json:"key"`
	State string `json:"state"`
	Code  string `json:"code"`
}
type ToolCall struct {
	Name     string `json:"name"`
	ClaimKey string `json:"claim_key"`
}
type ToolResult struct {
	Call      int              `json:"call"`
	Name      string           `json:"name"`
	Status    string           `json:"status"`
	Code      string           `json:"code"`
	State     string           `json:"state,omitempty"`
	RecordKey string           `json:"record_key,omitempty"`
	Reference string           `json:"reference,omitempty"`
	Claim     *news.BoundClaim `json:"claim,omitempty"`
}
type Candidate struct {
	Kind           string   `json:"kind"`
	ClaimCode      string   `json:"claim_code"`
	RefutationCode string   `json:"refutation_code"`
	EvidenceKeys   []string `json:"evidence_keys"`
}
type Proposal struct {
	Outcome     string     `json:"outcome"`
	ReasonCodes []string   `json:"reason_codes"`
	ClaimKeys   []string   `json:"claim_keys"`
	Uncertainty string     `json:"uncertainty"`
	Candidate   *Candidate `json:"candidate,omitempty"`
}
type Step struct {
	Kind     string    `json:"kind"`
	Tool     *ToolCall `json:"tool,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
}

// Frame never carries a full article, raw model transcript or narrative report.
// Excerpts appear only in successful, specifically requested ToolResults.
type Frame struct {
	Schema   string       `json:"schema"`
	Contract Contract     `json:"contract"`
	Subject  Subject      `json:"subject"`
	Claims   []ClaimRef   `json:"claims"`
	Results  []ToolResult `json:"results"`
}
type Model interface {
	Next(context.Context, Frame) ([]byte, error)
}
type Check struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Passed  bool   `json:"passed"`
	Code    string `json:"code"`
}
type Artifact struct {
	Turn   int    `json:"turn"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Raw    string `json:"raw,omitempty"`
}
type Result struct {
	Schema              string       `json:"schema"`
	ID                  string       `json:"id"`
	TypeID              string       `json:"type_id"`
	Status              string       `json:"status"`
	Subject             Subject      `json:"subject"`
	Outcome             string       `json:"outcome"`
	ReasonCode          string       `json:"reason_code"`
	Authority           string       `json:"authority"`
	QualificationCredit bool         `json:"qualification_credit"`
	FinalizedAt         time.Time    `json:"finalized_at"`
	Proposal            *Proposal    `json:"proposal,omitempty"`
	Tools               []ToolResult `json:"tools"`
	Artifacts           []Artifact   `json:"artifacts"`
	Checks              []Check      `json:"checks"`
}

func detached[T any](value T) T {
	raw, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}
