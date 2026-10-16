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

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

var code = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var claimKey = regexp.MustCompile(`^news-[a-f0-9]{64}$`)
var field = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Run pins one audited journal event view. Model implementations must honor ctx;
// provider adapters also need transport deadlines before use. No goroutine is
// detached to pretend an uncooperative model call has been terminated.
func Run(ctx context.Context, journal *news.Journal, event string, asOf time.Time, model Model) (Result, error) {
	return RunWithType(ctx, journal, event, asOf, TypeID, model)
}

// RunWithType supports explicitly selected local recorded-review contracts.
// Catalogue-aware reviews require RunWithInventory instead.
func RunWithType(ctx context.Context, journal *news.Journal, event string, asOf time.Time, typeID string, model Model) (Result, error) {
	if typeID == ReuseTypeID {
		return Result{}, errors.New("hypothesis reuse review requires an application-built inventory")
	}
	return runLocal(ctx, journal, event, asOf, typeID, nil, model)
}

// RunWithInventory consumes an application-built, validated catalogue projection.
// The model receives compact summaries until it requests a targeted declaration.
func RunWithInventory(ctx context.Context, journal *news.Journal, event string, asOf time.Time, inventory question.Inventory, model Model) (Result, error) {
	if inventory.Validate() != nil {
		return Result{}, question.ErrInvalid
	}
	inventory = detached(inventory)
	return runLocal(ctx, journal, event, asOf, ReuseTypeID, &inventory, model)
}

func runLocal(ctx context.Context, journal *news.Journal, event string, asOf time.Time, typeID string, inventory *question.Inventory, model Model) (Result, error) {
	contract, err := DefinitionFor(typeID)
	if err != nil {
		return Result{}, err
	}

	if journal == nil || model == nil {
		return Result{}, errors.New("review requires a journal and model fixture")
	}
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
	return run(ctx, subject, *group, model, contract, "", inventory)
}

// RunPinned consumes a trusted application-built service snapshot. HTTP and model
// inputs must never supply Pin; the storage boundary owns source verification.
func RunPinned(ctx context.Context, pin Pin, model Model) (Result, error) {
	raw, _ := json.Marshal(pin.Group)
	if model == nil || pin.Subject.ReceiverID == "" || pin.Subject.CaseVersion < 1 || pin.Subject.CaseGeneration < 1 || pin.Subject.CaseID != pin.Group.ID || pin.Subject.EventGroupID != pin.Group.ID || pin.Subject.EventKey != pin.Group.EventKey || pin.Subject.EvidenceClass != pin.Group.EvidenceClass || pin.Subject.SnapshotSHA256 != news.SHA256(string(raw)) {
		return Result{}, errors.New("invalid application review pin")
	}
	pin = detached(pin)
	return run(ctx, pin.Subject, pin.Group, model, Definition(), "", nil)
}

// RunPinnedProfile accepts only an exact currently supported application profile.
// A retained profile mismatch fails before any model invocation.
func RunPinnedProfile(ctx context.Context, pin Pin, p Profile, model Model) (Result, error) {
	if p.Contract.TypeID == ReuseTypeID {
		return Result{}, ErrProfile
	}
	return runPinnedProfile(ctx, pin, p, nil, model)
}

// RunPinnedProfileInventory requires a trusted server-captured inventory from
// the same receiver as the retained news pin. Neither is model/HTTP input.
func RunPinnedProfileInventory(ctx context.Context, pin Pin, p Profile, inventory question.Inventory, model Model) (Result, error) {
	if p.Contract.TypeID != ReuseTypeID || inventory.Validate() != nil || inventory.ReceiverID != pin.Subject.ReceiverID {
		return Result{}, ErrProfile
	}
	inventory = detached(inventory)
	return runPinnedProfile(ctx, pin, p, &inventory, model)
}

func runPinnedProfile(ctx context.Context, pin Pin, p Profile, inventory *question.Inventory, model Model) (Result, error) {
	if p.Validate() != nil {
		return Result{}, ErrProfile
	}
	raw, _ := json.Marshal(pin.Group)
	if model == nil || pin.Subject.ReceiverID == "" || pin.Subject.CaseVersion < 1 || pin.Subject.CaseGeneration < 1 || pin.Subject.CaseID != pin.Group.ID || pin.Subject.EventGroupID != pin.Group.ID || pin.Subject.EventKey != pin.Group.EventKey || pin.Subject.EvidenceClass != pin.Group.EvidenceClass || pin.Subject.SnapshotSHA256 != news.SHA256(string(raw)) {
		return Result{}, errors.New("invalid application review pin")
	}
	pin = detached(pin)
	p = detached(p)
	return run(ctx, pin.Subject, pin.Group, model, p.Contract, p.SHA256, inventory)
}
func run(ctx context.Context, subject Subject, group news.EventGroup, model Model, contract Contract, profileSHA string, inventory *question.Inventory) (Result, error) {
	schema := Schema
	var methods []library.MethodRef
	if contract.TypeID == MethodTypeID || contract.TypeID == ReuseTypeID {
		schema = MethodSchema
		if contract.TypeID == ReuseTypeID {
			schema = ReuseSchema
		}
		var err error
		methods, err = methodInventory()
		if err != nil {
			return Result{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(contract.TimeoutSeconds)*time.Second)
	defer cancel()
	frame := Frame{Schema: schema, Methods: methods, Contract: contract, Subject: subject, Claims: []ClaimRef{}, Results: []ToolResult{}}
	claims := map[string]news.EventClaim{}
	for _, c := range group.Claims {
		claims[c.Claim.Key] = c
		frame.Claims = append(frame.Claims, ClaimRef{Key: c.Claim.Key, State: c.State, Code: c.Claim.Code})
	}
	result := Result{ProfileSHA256: profileSHA, Schema: schema, TypeID: contract.TypeID, Status: "finalized", Subject: subject, Authority: "research_only_unqualified", Tools: []ToolResult{}, Artifacts: []Artifact{}, Checks: []Check{}}
	if inventory != nil {
		frame.Hypotheses = inventory.Summaries()
		frame.HypothesisInventorySHA256, frame.HypothesisCatalogueSHA256, frame.HypothesisReceiverID = inventory.SHA256, inventory.CatalogueSHA256, inventory.ReceiverID
		result.HypothesisInventorySHA256, result.HypothesisCatalogueSHA256, result.HypothesisReceiverID = inventory.SHA256, inventory.CatalogueSHA256, inventory.ReceiverID
	}
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
		if err != nil || contract.TypeID == TypeID && !legacyFields(raw) || contract.TypeID != ReuseTypeID && !noReuseFields(raw) {
			return fail("model_schema", "malformed_model_artifact")
		}
		checks["model_schema"] = "passed"
		if step.Kind == "tool" {
			calls++
			if calls > contract.MaxToolCalls {
				return fail("tool_budget", "tool_budget_exhausted")
			}
			call := step.Tool
			var tool ToolResult
			switch call.Name {
			case "inspect_news_claim":
				if call.MethodID != "" || call.MethodSHA256 != "" || call.HypothesisID != "" || call.DeclarationSHA256 != "" {
					return fail("model_schema", "invalid_tool_arguments")
				}
				tool = ToolResult{Call: calls, Name: call.Name, Status: "rejected", Code: "claim_not_in_snapshot"}
				if claim, ok := claims[call.ClaimKey]; ok {
					c := detached(claim.Claim)
					tool.Status = "ok"
					tool.Code = "bound_excerpt"
					tool.State = claim.State
					tool.RecordKey = claim.RecordKey
					tool.Reference = claim.Reference
					tool.Claim = &c
				}
			case "inspect_native_method":
				if contract.TypeID != MethodTypeID && contract.TypeID != ReuseTypeID {
					return fail("authority_boundary", "tool_not_permitted")
				}
				if call.ClaimKey != "" || call.MethodID == "" || call.MethodSHA256 == "" || call.HypothesisID != "" || call.DeclarationSHA256 != "" {
					return fail("model_schema", "invalid_tool_arguments")
				}
				tool = inspectMethod(*call, calls)
			case "inspect_hypothesis":
				if contract.TypeID != ReuseTypeID || inventory == nil {
					return fail("authority_boundary", "tool_not_permitted")
				}
				if call.ClaimKey != "" || call.MethodID != "" || call.MethodSHA256 != "" || call.HypothesisID == "" || call.DeclarationSHA256 == "" {
					return fail("model_schema", "invalid_tool_arguments")
				}
				tool = inspectHypothesis(*call, calls, *inventory)
			default:
				return fail("authority_boundary", "tool_not_permitted")
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
			if tool.Status == "ok" && tool.Claim != nil {
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
		if contract.TypeID == MethodTypeID || contract.TypeID == ReuseTypeID {
			if !boundMethods(*p, result.Tools) {
				return fail("method_binding", "method_not_eligible")
			}
			checks["method_binding"] = "passed"
		}
		if contract.TypeID == ReuseTypeID {
			if !boundHypothesis(*p, result.Tools, subject) {
				return fail("hypothesis_binding", "hypothesis_not_eligible")
			}
			checks["hypothesis_binding"] = "passed"
		}
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
