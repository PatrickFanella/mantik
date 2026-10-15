package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

var code = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var claimKey = regexp.MustCompile(`^news-[a-f0-9]{64}$`)
var field = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Run pins one audited journal event view. Model implementations must honor ctx;
// provider adapters also need transport deadlines before use. No goroutine is
// detached to pretend an uncooperative model call has been terminated.
func Run(ctx context.Context, journal *news.Journal, event string, asOf time.Time, model Model) (Result, error) {
	if journal == nil || model == nil {
		return Result{}, errors.New("review requires a journal and model fixture")
	}
	contract := Definition()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(contract.TimeoutSeconds)*time.Second)
	defer cancel()
	view, err := journal.Events(asOf)
	if err != nil {
		return Result{}, err
	}
	var group *news.EventGroup
	for i := range view.Groups {
		if view.Groups[i].ID == event {
			group = &view.Groups[i]
			break
		}
	}
	if group == nil {
		return Result{}, errors.New("event group not found in audited receipt-cutoff snapshot")
	}
	raw, _ := json.Marshal(group)
	subject := Subject{JournalID: view.JournalID, EventGroupID: group.ID, EventKey: group.EventKey, EvidenceClass: group.EvidenceClass, AsOf: view.AsOf, SnapshotSHA256: news.SHA256(string(raw))}
	return run(ctx, subject, *group, model)
}

// RunPinned consumes a trusted application-built service snapshot. HTTP and model
// inputs must never supply Pin; the storage boundary owns source verification.
func RunPinned(ctx context.Context, pin Pin, model Model) (Result, error) {
	raw, _ := json.Marshal(pin.Group)
	if model == nil || pin.Subject.ReceiverID == "" || pin.Subject.CaseVersion < 1 || pin.Subject.CaseGeneration < 1 || pin.Subject.CaseID != pin.Group.ID || pin.Subject.EventGroupID != pin.Group.ID || pin.Subject.EventKey != pin.Group.EventKey || pin.Subject.EvidenceClass != pin.Group.EvidenceClass || pin.Subject.SnapshotSHA256 != news.SHA256(string(raw)) {
		return Result{}, errors.New("invalid application review pin")
	}
	pin = detached(pin)
	return run(ctx, pin.Subject, pin.Group, model)
}
func run(ctx context.Context, subject Subject, group news.EventGroup, model Model) (Result, error) {
	contract := Definition()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(contract.TimeoutSeconds)*time.Second)
	defer cancel()
	frame := Frame{Schema: Schema, Contract: contract, Subject: subject, Claims: []ClaimRef{}, Results: []ToolResult{}}
	claims := map[string]news.EventClaim{}
	for _, c := range group.Claims {
		claims[c.Claim.Key] = c
		frame.Claims = append(frame.Claims, ClaimRef{Key: c.Claim.Key, State: c.State, Code: c.Claim.Code})
	}
	result := Result{Schema: Schema, TypeID: TypeID, Status: "finalized", Subject: subject, Authority: "research_only_unqualified", Tools: []ToolResult{}, Artifacts: []Artifact{}, Checks: []Check{}}
	checks := map[string]string{}
	for _, id := range contract.RequiredChecks {
		checks[id] = "not_run"
	}
	checks["tool_budget"] = "passed"
	checks["authority_boundary"] = "passed"
	finish := func(outcome, reason string, proposal *Proposal) (Result, error) {
		result.Outcome, result.ReasonCode, result.Proposal = outcome, reason, detached(proposal)
		result.FinalizedAt = time.Now().UTC()
		for _, id := range contract.RequiredChecks {
			checkCode := checks[id]
			if checks[id] == "failed" {
				checkCode = reason
			}
			result.Checks = append(result.Checks, Check{ID: id, Version: "v1", Status: checks[id], Passed: checks[id] == "passed", Code: checkCode})
		}
		raw, _ := json.Marshal(result)
		result.ID = "review-" + news.SHA256(string(raw))
		return result, nil
	}
	fail := func(check, reason string) (Result, error) {
		checks[check] = "failed"
		return finish("abstain", reason, nil)
	}
	calls := 0
	for turn := 1; turn <= contract.MaxTurns; turn++ {
		if ctx.Err() != nil {
			return fail("completion", "model_timeout")
		}
		raw, err := model.Next(ctx, detached(frame))
		artifact := Artifact{Turn: turn, SHA256: news.SHA256(string(raw)), Bytes: len(raw)}
		if len(raw) <= MaxArtifactBytes && utf8.Valid(raw) {
			artifact.Raw = string(raw)
		}
		result.Artifacts = append(result.Artifacts, artifact)
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fail("completion", "model_timeout")
		}
		if errors.Is(err, io.EOF) {
			return fail("completion", "model_incomplete")
		}
		if err != nil {
			return fail("completion", "model_unavailable")
		}
		step, err := decodeStep(raw)
		if err != nil {
			return fail("model_schema", "malformed_model_artifact")
		}
		checks["model_schema"] = "passed"
		if step.Kind == "tool" {
			calls++
			if calls > contract.MaxToolCalls {
				return fail("tool_budget", "tool_budget_exhausted")
			}
			call := step.Tool
			if call.Name != "inspect_news_claim" {
				return fail("authority_boundary", "tool_not_permitted")
			}
			tool := ToolResult{Call: calls, Name: call.Name, Status: "rejected", Code: "claim_not_in_snapshot"}
			if claim, ok := claims[call.ClaimKey]; ok {
				c := detached(claim.Claim)
				tool.Status = "ok"
				tool.Code = "bound_excerpt"
				tool.State = claim.State
				tool.RecordKey = claim.RecordKey
				tool.Reference = claim.Reference
				tool.Claim = &c
			}
			result.Tools = append(result.Tools, tool)
			frame.Results = detached(result.Tools)
			continue
		}
		p := step.Proposal
		if !slices.Contains(contract.Outcomes, p.Outcome) {
			return fail("authority_boundary", "outcome_not_permitted")
		}
		if !validProposal(*p) {
			return fail("completion", "invalid_proposal")
		}
		inspected := map[string]ToolResult{}
		for _, tool := range result.Tools {
			if tool.Status == "ok" {
				inspected[tool.Claim.Key] = tool
			}
		}
		for _, key := range p.ClaimKeys {
			tool, ok := inspected[key]
			if !ok || p.Outcome == "propose_hypothesis" && (tool.State != "current" || tool.Claim.Assessment != "reported_unverified") {
				return fail("evidence_binding", "evidence_not_eligible")
			}
		}
		checks["evidence_binding"] = "passed"
		checks["completion"] = "passed"
		return finish(p.Outcome, "model_proposal_checked", p)
	}
	return fail("completion", "turn_budget_exhausted")
}
func validProposal(p Proposal) bool {
	if p.Uncertainty != "unverified" || len(p.ReasonCodes) < 1 || len(p.ReasonCodes) > 4 || len(p.ClaimKeys) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, reason := range p.ReasonCodes {
		if !code.MatchString(reason) || seen[reason] {
			return false
		}
		seen[reason] = true
	}
	seen = map[string]bool{}
	for _, key := range p.ClaimKeys {
		if seen[key] || !claimKey.MatchString(key) {
			return false
		}
		seen[key] = true
	}
	if p.Outcome != "abstain" && len(p.ClaimKeys) == 0 {
		return false
	}
	if p.Outcome != "propose_hypothesis" {
		return p.Candidate == nil
	}
	c := p.Candidate
	if c == nil || c.Kind != "research_hypothesis" || !code.MatchString(c.ClaimCode) || !code.MatchString(c.RefutationCode) || c.ClaimCode == c.RefutationCode || len(c.EvidenceKeys) != len(p.ClaimKeys) {
		return false
	}
	evidence := map[string]bool{}
	for _, key := range c.EvidenceKeys {
		if !seen[key] || evidence[key] {
			return false
		}
		evidence[key] = true
	}
	return true
}
func decodeStep(raw []byte) (Step, error) {
	var step Step
	if len(raw) == 0 || len(raw) > MaxArtifactBytes || !utf8.Valid(raw) {
		return step, errors.New("invalid model artifact size or encoding")
	}
	if err := unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return step, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&step); err != nil {
		return step, err
	}
	if d.Decode(new(any)) != io.EOF {
		return step, errors.New("expected one model artifact")
	}
	if step.Kind == "tool" && step.Tool != nil && step.Proposal == nil {
		return step, nil
	}
	if step.Kind == "proposal" && step.Proposal != nil && step.Tool == nil {
		return step, nil
	}
	return step, errors.New("invalid model artifact variant")
}
func unique(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("model nesting exceeds bound")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := k.(string)
				if !ok || !field.MatchString(name) || seen[name] {
					return errors.New("duplicate or noncanonical model field")
				}
				seen[name] = true
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid model delimiter")
		}
		_, err = d.Token()
		return err
	}
	return nil
}
