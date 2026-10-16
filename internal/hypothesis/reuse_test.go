package hypothesis

import (
	"context"
	"encoding/json"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

func TestPublicationIndependentlyChecksHypothesisReuse(t *testing.T) {
	s, old, d, at := publicationFixture(t)
	seed, err := Publish(s, old, old.Pin.Subject.ReceiverID, "seed", d, at)
	if err != nil {
		t.Fatal(err)
	}
	c, err := MakeCatalogue(seed.ReceiverID, []Publication{seed})
	if err != nil {
		t.Fatal(err)
	}
	i, err := c.ReviewInventory()
	if err != nil {
		t.Fatal(err)
	}
	a, err := review.StartProfileInventory(s, seed.ReceiverID, "reuse", old.Pin.Subject.CaseID, 1, investigation.ReuseTypeID, &i, at)
	if err != nil {
		t.Fatal(err)
	}
	ref := d.Methods[0]
	key := d.Origins[0].ClaimKey
	inputs := []investigation.Step{
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: key}},
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_native_method", MethodID: ref.ID, MethodSHA256: ref.SHA256}},
		{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_hypothesis", HypothesisID: seed.Sealed.ID, DeclarationSHA256: seed.Sealed.DeclarationSHA256}},
		{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"reuse_existing"}, ClaimKeys: []string{key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{key}, Methods: d.Methods, Reuse: &question.Ref{ID: seed.Sealed.ID, DeclarationSHA256: seed.Sealed.DeclarationSHA256}}}},
	}
	steps := []json.RawMessage{}
	for _, input := range inputs {
		raw, _ := json.Marshal(input)
		steps = append(steps, raw)
	}
	steps, digest, err := review.CanonicalSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	result, err := review.Evaluate(context.Background(), a, steps)
	if err != nil {
		t.Fatal(err)
	}
	a, err = review.Commit(a, s, steps, digest, result, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Publish(s, a, seed.ReceiverID, "valid", d, at); err != nil || !ReuseSourceExists(a, []Publication{seed}) {
		t.Fatal("valid reuse rejected", err)
	}
	for name, mutate := range map[string]func(*review.Attempt, *Declaration){
		"inventory-result-pin": func(a *review.Attempt, _ *Declaration) { a.Result.HypothesisInventorySHA256 = "changed" },
		"catalogue-result-pin": func(a *review.Attempt, _ *Declaration) { a.Result.HypothesisCatalogueSHA256 = "changed" },
		"inventory-pin":        func(a *review.Attempt, _ *Declaration) { a.HypothesisInventory.SHA256 = "changed" },
		"missing-tool":         func(a *review.Attempt, _ *Declaration) { a.Result.Tools = a.Result.Tools[:2] },
		"changed-tool": func(a *review.Attempt, _ *Declaration) {
			a.Result.Tools[2].Hypothesis.Declaration.Mechanism.Statement = "changed"
		},
		"changed-tool-ref": func(a *review.Attempt, _ *Declaration) { a.Result.Tools[2].HypothesisRef.DeclarationSHA256 = "changed" },
		"changed-candidate": func(a *review.Attempt, _ *Declaration) {
			a.Result.Proposal.Candidate.Reuse.DeclarationSHA256 = "changed"
		},
		"missing-reuse":   func(a *review.Attempt, _ *Declaration) { a.Result.Proposal.Candidate.Reuse = nil },
		"changed-horizon": func(_ *review.Attempt, d *Declaration) { d.HorizonSeconds++ },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(a)
			var bad review.Attempt
			_ = json.Unmarshal(raw, &bad)
			raw, _ = json.Marshal(d)
			var changed Declaration
			_ = json.Unmarshal(raw, &changed)
			mutate(&bad, &changed)
			// Preserve valid result hashing and all claimed passing checks. The
			// publication boundary must independently reject the changed evidence.
			bad.Result.ID = ""
			raw, _ = json.Marshal(bad.Result)
			bad.Result.ID = "review-" + news.SHA256(string(raw))
			if _, err := Publish(s, bad, seed.ReceiverID, "forged", changed, at); err == nil {
				t.Fatal("forged reuse published")
			}
		})
	}
}
