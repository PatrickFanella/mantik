package investigation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

func reuseInputs(t *testing.T, key string) (question.Inventory, []Step) {
	t.Helper()
	d, err := question.Seal(question.Fixture())
	if err != nil {
		t.Fatal(err)
	}
	i, err := question.MakeInventory("receiver-abcdefghijklmnop", news.SHA256("retained catalogue"), []question.Result{d})
	if err != nil {
		t.Fatal(err)
	}
	ref := question.Ref{ID: d.ID, DeclarationSHA256: d.DeclarationSHA256}
	p := methodProposal(key, d.Declaration.Methods...)
	p.Proposal.Candidate.ClaimCode = d.Declaration.Mechanism.Code
	p.Proposal.Candidate.RefutationCode = d.Declaration.Refutation.Code
	p.Proposal.Candidate.Reuse = &ref
	return i, []Step{tool(key), methodStep(d.Declaration.Methods[0]), {Kind: "tool", Tool: &ToolCall{Name: "inspect_hypothesis", HypothesisID: ref.ID, DeclarationSHA256: ref.DeclarationSHA256}}, p}
}

func TestHypothesisReviewTargetsAndBindsInspectedDeclaration(t *testing.T) {
	j, r, _, g := subject(t)
	i, steps := reuseInputs(t, g.Claims[0].Claim.Key)
	n := 0
	result, err := RunWithInventory(context.Background(), j, g.ID, r.ReceivedAt, i, modelFunc(func(_ context.Context, f Frame) ([]byte, error) {
		if f.Schema != ReuseSchema || f.Contract.TypeID != ReuseTypeID || len(f.Contract.RequiredChecks) != 7 || len(f.Hypotheses) != 1 || f.HypothesisInventorySHA256 != i.SHA256 || f.HypothesisCatalogueSHA256 != i.CatalogueSHA256 || f.HypothesisReceiverID != i.ReceiverID {
			t.Fatal("missing bounded inventory pin", f)
		}
		if f.Hypotheses[0].Instrument != "DEMO" || f.Hypotheses[0].EvidenceClass != "synthetic_unqualified" || f.Hypotheses[0].MechanismCode != i.Declarations[0].Declaration.Mechanism.Code || f.Hypotheses[0].HorizonSeconds != 3600 {
			t.Fatal("missing compact discovery metadata")
		}
		if n < 3 && strings.Contains(string(raw(f)), i.Declarations[0].Declaration.Mechanism.Statement) {
			t.Fatal("unrequested declaration leaked into frame")
		}
		if n == 3 {
			if f.Results[2].Hypothesis == nil || f.Results[2].HypothesisRef == nil {
				t.Fatal("missing targeted declaration")
			}
			f.Results[2].Hypothesis.Declaration.Methods[0].SHA256 = "tampered"
			f.Hypotheses[0].DeclarationSHA256 = "tampered"
		}
		v := steps[n]
		n++
		return raw(v), nil
	}))
	if err != nil || result.Outcome != "propose_hypothesis" || result.Proposal == nil || result.Proposal.Candidate.Reuse == nil || result.HypothesisInventorySHA256 != i.SHA256 || result.HypothesisCatalogueSHA256 != i.CatalogueSHA256 || result.HypothesisReceiverID != i.ReceiverID || result.QualificationCredit || result.Tools[2].Hypothesis.Declaration.Methods[0].SHA256 == "tampered" || i.Validate() != nil {
		t.Fatal(result, err)
	}
	for _, check := range result.Checks {
		if !check.Passed {
			t.Fatal(check)
		}
	}
	id := result.ID
	result.ID = ""
	if id != "review-"+news.SHA256(string(raw(result))) {
		t.Fatal("result hash does not bind inventory pins")
	}
}

func TestHypothesisReviewRejectsUninspectedOrChangedReuse(t *testing.T) {
	for _, name := range []string{"uninspected", "wrong-pin", "unknown", "missing-ref", "changed-code", "changed-method", "wrong-ref", "mixed-arguments", "uninspected-news", "stale", "authority"} {
		t.Run(name, func(t *testing.T) {
			j, r, receipt, g := subject(t)
			i, steps := reuseInputs(t, g.Claims[0].Claim.Key)
			want := "hypothesis_not_eligible"
			switch name {
			case "uninspected":
				steps = append(steps[:2], steps[3])
			case "wrong-pin":
				steps[2].Tool.DeclarationSHA256 = strings.Repeat("f", 64)
			case "unknown":
				steps[2].Tool.HypothesisID = "hypothesis-" + strings.Repeat("f", 64)
			case "missing-ref":
				steps[3].Proposal.Candidate.Reuse = nil
			case "changed-code":
				steps[3].Proposal.Candidate.ClaimCode = "other_mechanism"
			case "changed-method":
				half, _, _ := library.PinMethod(library.HalfSizeID)
				steps[1] = methodStep(half)
				steps[3].Proposal.Candidate.Methods = []library.MethodRef{half}
			case "wrong-ref":
				steps[3].Proposal.Candidate.Reuse.DeclarationSHA256 = strings.Repeat("f", 64)
			case "mixed-arguments":
				steps[2].Tool.ClaimKey = g.Claims[0].Claim.Key
				want = "invalid_tool_arguments"
			case "uninspected-news":
				steps = steps[1:]
				want = "evidence_not_eligible"
			case "stale":
				correction := r
				correction.Revision = 2
				correction.Kind = "correction"
				correction.AvailableAt = r.ReceivedAt.Add(time.Second)
				correction.ReceivedAt = correction.AvailableAt
				correction.Supersedes = &news.RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: 1, RecordingSHA256: receipt.RecordingSHA256}
				if _, err := j.Retain(correction); err != nil {
					t.Fatal(err)
				}
				r.ReceivedAt = correction.ReceivedAt
				want = "evidence_not_eligible"
			case "authority":
				steps[2].Tool.Name = "execute_hypothesis"
				want = "tool_not_permitted"
			}
			n := 0
			result, err := RunWithInventory(context.Background(), j, g.ID, r.ReceivedAt, i, modelFunc(func(context.Context, Frame) ([]byte, error) { v := steps[n]; n++; return raw(v), nil }))
			if err != nil || result.Outcome != "abstain" || result.ReasonCode != want || result.Proposal != nil || result.QualificationCredit || result.HypothesisInventorySHA256 != i.SHA256 {
				t.Fatal(result, err)
			}
			if name == "wrong-pin" || name == "unknown" {
				if result.Tools[2].Status != "rejected" || result.Tools[2].Hypothesis != nil || result.Tools[2].HypothesisRef != nil {
					t.Fatal("rejected lookup leaked declaration")
				}
			}
		})
	}
}

func TestHypothesisReviewBoundsAndProfileIsolation(t *testing.T) {
	j, r, _, g := subject(t)
	i, steps := reuseInputs(t, g.Claims[0].Claim.Key)
	called := false
	m := modelFunc(func(context.Context, Frame) ([]byte, error) { called = true; return raw(steps[0]), nil })
	bad := detached(i)
	bad.Declarations[0].Declaration.HorizonSeconds++
	if _, err := RunWithInventory(context.Background(), j, g.ID, r.ReceivedAt, bad, m); err == nil || called {
		t.Fatal("invalid inventory reached model")
	}
	if _, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, ReuseTypeID, m); err == nil || called {
		t.Fatal("missing inventory reached model")
	}
	if _, err := ProfileFor(ReuseTypeID); err == nil {
		t.Fatal("local-only profile accepted for retained service review")
	}
	for _, typeID := range []string{TypeID, MethodTypeID} {
		for _, input := range []string{string(raw(steps[2])), `{"kind":"tool","tool":{"name":"inspect_news_claim","claim_key":"x","hypothesis_id":""}}`, `{"kind":"proposal","proposal":{"candidate":{"reuse":null}}}`} {
			result, err := RunWithType(context.Background(), j, g.ID, r.ReceivedAt, typeID, modelFunc(func(context.Context, Frame) ([]byte, error) { return []byte(input), nil }))
			if err != nil || result.ReasonCode != "malformed_model_artifact" || result.HypothesisInventorySHA256 != "" {
				t.Fatal("older profile vocabulary expanded", result, err)
			}
		}
	}
	for _, name := range []string{"budget", "timeout", "abstain", "forged", "empty"} {
		ctx := context.Background()
		var cancel context.CancelFunc
		if name == "timeout" {
			ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
		}
		inventory := i
		if name == "empty" {
			inventory, _ = question.MakeInventory(i.ReceiverID, i.CatalogueSHA256, nil)
		}
		result, err := RunWithInventory(ctx, j, g.ID, r.ReceivedAt, inventory, modelFunc(func(ctx context.Context, _ Frame) ([]byte, error) {
			switch name {
			case "timeout":
				<-ctx.Done()
				return nil, ctx.Err()
			case "abstain", "empty":
				return raw(Step{Kind: "proposal", Proposal: &Proposal{Outcome: "abstain", Uncertainty: "unverified", ReasonCodes: []string{"no_matching_question"}}}), nil
			case "forged":
				return []byte(`{"kind":"tool","tool":{"name":"inspect_hypothesis","hypothesis":{}}}`), nil
			default:
				return raw(steps[2]), nil
			}
		}))
		want := map[string]string{"budget": "tool_budget_exhausted", "timeout": "model_timeout", "abstain": "model_proposal_checked", "empty": "model_proposal_checked", "forged": "malformed_model_artifact"}[name]
		if err != nil || result.Outcome != "abstain" || result.ReasonCode != want || len(result.Tools) > 4 || len(result.Artifacts) > 8 || result.HypothesisInventorySHA256 != inventory.SHA256 {
			t.Fatal(result, err)
		}
	}
}

func TestHypothesisReviewDoesNotMixEvidenceClassOrInstrument(t *testing.T) {
	for _, name := range []string{"class", "instrument"} {
		t.Run(name, func(t *testing.T) {
			j, err := news.OpenJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			r := news.Fixture()
			if name == "class" {
				r.EvidenceClass = "recorded_market_unqualified"
			} else {
				r.Claims[0].Symbols = []string{"SPY"}
			}
			if _, err = j.Retain(r); err != nil {
				t.Fatal(err)
			}
			groups, err := j.Events(r.ReceivedAt)
			if err != nil {
				t.Fatal(err)
			}
			g := groups.Groups[0]
			i, steps := reuseInputs(t, g.Claims[0].Claim.Key)
			n := 0
			result, err := RunWithInventory(context.Background(), j, g.ID, r.ReceivedAt, i, modelFunc(func(context.Context, Frame) ([]byte, error) { v := steps[n]; n++; return raw(v), nil }))
			if err != nil || result.Outcome != "abstain" || result.ReasonCode != "hypothesis_not_eligible" || result.Proposal != nil {
				t.Fatal(result, err)
			}
		})
	}
}
