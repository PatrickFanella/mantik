package investigation

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"strings"
	"testing"
	"time"
)

func methodStep(ref library.MethodRef) Step {
	return Step{Kind: "tool", Tool: &ToolCall{Name: "inspect_native_method", MethodID: ref.ID, MethodSHA256: ref.SHA256}}
}
func methodProposal(key string, refs ...library.MethodRef) Step {
	p := proposal(key)
	p.Candidate.Methods = refs
	return Step{Kind: "proposal", Proposal: p}
}
func TestMethodAwareReviewInspectsAndBindsCompiledMethods(t *testing.T) {
	j, r, _, g := subject(t)
	key := g.Claims[0].Claim.Key
	ref, _, _ := library.PinMethod(library.FixtureStrategy().ID)
	half, _, _ := library.PinMethod(library.HalfSizeID)
	steps := []Step{tool(key), methodStep(ref), methodStep(half), methodProposal(key, ref, half)}
	i := 0
	result, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, MethodTypeID, modelFunc(func(_ context.Context, f Frame) ([]byte, error) {
		if f.Schema != MethodSchema || f.Contract.TypeID != MethodTypeID || len(f.Methods) != 2 || strings.Contains(string(raw(f)), "UNSELECTED PRIVATE") {
			t.Fatal("profile or excerpt boundary lost")
		}
		if i == 0 && (len(f.Results) != 0 || strings.Contains(string(raw(f)), "max_notional_cents")) {
			t.Fatal("full method leaked before targeted inspection")
		}
		if i == 2 {
			if f.Results[1].Method == nil || f.Results[1].MethodRef == nil {
				t.Fatal("missing method result")
			}
			f.Results[1].Method.Strategy.Safety.MaxNotionalCents = 999999
			f.Results[1].Method.Entry.Evaluation.Required[0].Actions[0] = "tampered"
			f.Methods[0].SHA256 = strings.Repeat("f", 64)
			f.Contract.Tools = append(f.Contract.Tools, "execute_order")
		}
		if i == 3 && (f.Results[1].Method.Strategy.Safety.MaxNotionalCents != 100000 || f.Results[1].Method.Entry.Evaluation.Required[0].Actions[0] == "tampered" || len(f.Contract.Tools) != 2) {
			t.Fatal("model mutated application method/contract")
		}
		step := steps[i]
		i++
		return raw(step), nil
	}))
	if err != nil || result.Schema != MethodSchema || result.TypeID != MethodTypeID || result.Outcome != "propose_hypothesis" || result.Proposal == nil || len(result.Proposal.Candidate.Methods) != 2 || result.QualificationCredit || result.Authority != "research_only_unqualified" || len(result.Tools) != 3 {
		t.Fatal(result, err)
	}
	for _, c := range result.Checks {
		if !c.Passed {
			t.Fatal(c)
		}
	}
}
func TestMethodBindingFailuresAndTypedRejections(t *testing.T) {
	for _, test := range []string{"uninspected", "unknown", "wrong-pin", "missing", "duplicate", "wrong-schema", "tool-arguments", "authority"} {
		t.Run(test, func(t *testing.T) {
			j, r, _, g := subject(t)
			key := g.Claims[0].Claim.Key
			ref, _, _ := library.PinMethod(library.FixtureStrategy().ID)
			call := methodStep(ref)
			p := methodProposal(key, ref)
			reason := "method_not_eligible"
			switch test {
			case "unknown":
				call.Tool.MethodID = "invented"
			case "wrong-pin":
				call.Tool.MethodSHA256 = strings.Repeat("f", 64)
			case "missing":
				p.Proposal.Candidate.Methods = nil
			case "duplicate":
				p.Proposal.Candidate.Methods = append(p.Proposal.Candidate.Methods, ref)
			case "wrong-schema":
				p.Proposal.Candidate.Methods[0].Schema = "future"
			case "tool-arguments":
				call.Tool.ClaimKey = key
				reason = "invalid_tool_arguments"
			case "authority":
				call.Tool.Name = "change_risk_policy"
				reason = "tool_not_permitted"
			}
			steps := []Step{tool(key), call, p}
			if test == "uninspected" {
				steps = []Step{tool(key), p}
			}
			i := 0
			result, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, MethodTypeID, modelFunc(func(context.Context, Frame) ([]byte, error) { v := steps[i]; i++; return raw(v), nil }))
			if err != nil || result.Outcome != "abstain" || result.ReasonCode != reason || result.Proposal != nil || result.QualificationCredit {
				t.Fatal(result, err)
			}
			if test == "unknown" || test == "wrong-pin" {
				if len(result.Tools) != 2 || result.Tools[1].Status != "rejected" || result.Tools[1].Method != nil {
					t.Fatal("invalid method leaked definition")
				}
			}
		})
	}
}
func TestMethodProfileKeepsBoundedAbstentionAndLegacyRejection(t *testing.T) {
	j, r, _, g := subject(t)
	ref, _, _ := library.PinMethod(library.FixtureStrategy().ID)
	for _, test := range []string{"timeout", "budget", "explicit-abstain"} {
		ctx := context.Background()
		var cancel context.CancelFunc
		if test == "timeout" {
			ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
		}
		result, err := RunWithType(ctx, j, g.ID, r.ReceivedAt, MethodTypeID, modelFunc(func(ctx context.Context, _ Frame) ([]byte, error) {
			if test == "timeout" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if test == "explicit-abstain" {
				return raw(Step{Kind: "proposal", Proposal: &Proposal{Outcome: "abstain", ReasonCodes: []string{"insufficient_evidence"}, Uncertainty: "unverified"}}), nil
			}
			return raw(methodStep(ref)), nil
		}))
		want := map[string]string{"timeout": "model_timeout", "budget": "tool_budget_exhausted", "explicit-abstain": "model_proposal_checked"}[test]
		if err != nil || result.Outcome != "abstain" || result.ReasonCode != want || len(result.Tools) > 4 || len(result.Artifacts) > 8 {
			t.Fatal(result, err)
		}
		for _, c := range result.Checks {
			if c.ID == "method_binding" && test == "timeout" && (c.Passed || c.Status != "not_run") {
				t.Fatal("timeout invented method check")
			}
		}
	}
	for _, input := range []string{string(raw(methodStep(ref))), `{"kind":"tool","tool":{"name":"inspect_news_claim","claim_key":"x","method_id":""}}`, string(raw(methodProposal(g.Claims[0].Claim.Key, ref)))} {
		result, err := Run(context.Background(), j, g.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { return []byte(input), nil }))
		if err != nil || result.Schema != Schema || result.ReasonCode != "malformed_model_artifact" || result.Proposal != nil {
			t.Fatal("legacy v1 accepted new fields", result, err)
		}
	}
	called := false
	if _, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, "invented", modelFunc(func(context.Context, Frame) ([]byte, error) { called = true; return nil, nil })); err == nil || called {
		t.Fatal("unknown profile reached model")
	}
	// Models cannot supply method definitions or mandatory check outcomes.
	for _, input := range []string{`{"kind":"tool","tool":{"name":"inspect_native_method","method":{},"checks":[]}}`, `{"kind":"proposal","proposal":{"outcome":"abstain","qualification_credit":true}}`} {
		result, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, MethodTypeID, modelFunc(func(context.Context, Frame) ([]byte, error) { return []byte(input), nil }))
		if err != nil || result.ReasonCode != "malformed_model_artifact" {
			t.Fatal(result, err)
		}
	}
}
func TestMethodProfileDefinitionsAreDetached(t *testing.T) {
	c, err := DefinitionFor(MethodTypeID)
	if err != nil {
		t.Fatal(err)
	}
	c.RequiredChecks[0] = "tampered"
	c.Tools[0] = "execute"
	fresh, _ := DefinitionFor(MethodTypeID)
	legacy := Definition()
	if fresh.RequiredChecks[0] == "tampered" || legacy.Tools[0] != "inspect_news_claim" || len(legacy.RequiredChecks) != 5 || len(fresh.RequiredChecks) != 6 {
		t.Fatal("contract mutation escaped")
	}
	body, _ := json.Marshal(legacy)
	if strings.Contains(string(body), "method_binding") {
		t.Fatal("legacy definition changed")
	}
}
