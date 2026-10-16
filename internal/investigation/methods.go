package investigation

import (
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

const MethodTypeID = "news-method-review-v1"
const MethodSchema = "mantik-news-method-review-v1"

// DefinitionFor selects an application-owned profile; models cannot edit it.
func DefinitionFor(id string) (Contract, error) {
	c := Definition()
	if id == TypeID {
		return c, nil
	}
	if id != MethodTypeID && id != ReuseTypeID {
		return Contract{}, errors.New("unsupported research review type")
	}
	c.TypeID = MethodTypeID
	c.Tools = append(c.Tools, "inspect_native_method")
	c.RequiredEvidence = append(c.RequiredEvidence, "inspected_native_method_pins_for_hypothesis")
	c.RequiredChecks = append(c.RequiredChecks, "method_binding")
	if id == ReuseTypeID {
		c.TypeID = ReuseTypeID
		c.Tools = append(c.Tools, "inspect_hypothesis")
		c.RequiredEvidence = append(c.RequiredEvidence, "inspected_exact_hypothesis_declaration_for_reuse")
		c.RequiredChecks = append(c.RequiredChecks, "hypothesis_binding")
	}
	return c, nil
}
func methodInventory() ([]library.MethodRef, error) {
	refs := []library.MethodRef{}
	for _, id := range []string{library.FixtureStrategy().ID, library.HalfSizeID} {
		ref, _, err := library.PinMethod(id)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
func inspectMethod(call ToolCall, n int) ToolResult {
	out := ToolResult{Call: n, Name: call.Name, Status: "rejected", Code: "method_not_in_inventory"}
	ref, method, err := library.PinMethod(call.MethodID)
	if err != nil {
		return out
	}
	if call.MethodSHA256 != ref.SHA256 {
		out.Code = "method_pin_mismatch"
		return out
	}
	out.Status = "ok"
	out.Code = "compiled_method_contract"
	out.MethodRef = &ref
	out.Method = &method
	return out
}
func boundMethods(p Proposal, tools []ToolResult) bool {
	if p.Outcome != "propose_hypothesis" {
		return p.Candidate == nil
	}
	if p.Candidate == nil || len(p.Candidate.Methods) < 1 || len(p.Candidate.Methods) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range p.Candidate.Methods {
		if seen[ref.ID] {
			return false
		}
		seen[ref.ID] = true
		found := false
		for _, tool := range tools {
			if tool.Name == "inspect_native_method" && tool.Status == "ok" && tool.MethodRef != nil && *tool.MethodRef == ref {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Newly typed fields remain forbidden in the old v1 profile, including empty
// values that would disappear under omitempty during canonical reencoding.
func legacyFields(raw []byte) bool {
	var step map[string]json.RawMessage
	if json.Unmarshal(raw, &step) != nil {
		return false
	}
	var tool map[string]json.RawMessage
	_ = json.Unmarshal(step["tool"], &tool)
	for _, key := range []string{"method_id", "method_sha256"} {
		if _, ok := tool[key]; ok {
			return false
		}
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(step["proposal"], &p)
	var c map[string]json.RawMessage
	_ = json.Unmarshal(p["candidate"], &c)
	_, has := c["methods"]
	return !has
}
