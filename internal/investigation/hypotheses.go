package investigation

import (
	"encoding/json"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

const ReuseTypeID = "news-reuse-review-v1"
const ReuseSchema = "mantik-news-reuse-review-v1"

func inspectHypothesis(call ToolCall, n int, inventory question.Inventory) ToolResult {
	out := ToolResult{Call: n, Name: call.Name, Status: "rejected", Code: "hypothesis_not_in_inventory"}
	for _, d := range inventory.Declarations {
		if d.ID != call.HypothesisID {
			continue
		}
		out.Code = "hypothesis_pin_mismatch"
		if d.DeclarationSHA256 != call.DeclarationSHA256 {
			continue
		}
		copy := detached(d)
		out.Status, out.Code = "ok", "retained_hypothesis_declaration"
		out.HypothesisRef = &question.Ref{ID: d.ID, DeclarationSHA256: d.DeclarationSHA256}
		out.Hypothesis = &copy
		return out
	}
	return out
}

func boundHypothesis(p Proposal, tools []ToolResult, subject Subject) bool {
	if p.Outcome != "propose_hypothesis" {
		return p.Candidate == nil
	}
	if p.Candidate == nil || p.Candidate.Reuse == nil {
		return false
	}
	c := p.Candidate
	for _, tool := range tools {
		if tool.Name != "inspect_hypothesis" || tool.Status != "ok" || tool.HypothesisRef == nil || *tool.HypothesisRef != *c.Reuse || tool.Hypothesis == nil {
			continue
		}
		d := tool.Hypothesis.Declaration
		if subject.EvidenceClass != d.EvidenceClass || c.ClaimCode != d.Mechanism.Code || c.RefutationCode != d.Refutation.Code || len(c.Methods) != len(d.Methods) {
			return false
		}
		for _, ref := range c.Methods {
			if !slices.Contains(d.Methods, ref) {
				return false
			}
		}
		for _, key := range p.ClaimKeys {
			matched := false
			for _, evidence := range tools {
				if evidence.Name == "inspect_news_claim" && evidence.Status == "ok" && evidence.Claim != nil && evidence.Claim.Key == key && evidence.Claim.EvidenceClass == d.EvidenceClass && slices.Contains(evidence.Claim.Symbols, d.Instrument) {
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
		return true
	}
	return false
}

// Existing profiles reject newly typed fields even when empty or null. Their
// artifact vocabulary and result hashes must not expand implicitly.
func noReuseFields(raw []byte) bool {
	var step map[string]json.RawMessage
	if json.Unmarshal(raw, &step) != nil {
		return false
	}
	var tool map[string]json.RawMessage
	_ = json.Unmarshal(step["tool"], &tool)
	for _, key := range []string{"hypothesis_id", "declaration_sha256"} {
		if _, ok := tool[key]; ok {
			return false
		}
	}
	var proposal, candidate map[string]json.RawMessage
	_ = json.Unmarshal(step["proposal"], &proposal)
	_ = json.Unmarshal(proposal["candidate"], &candidate)
	_, has := candidate["reuse"]
	return !has
}
