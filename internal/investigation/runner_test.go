package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/operations"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type modelFunc func(context.Context, Frame) ([]byte, error)

func (f modelFunc) Next(ctx context.Context, frame Frame) ([]byte, error) { return f(ctx, frame) }
func raw(v any) []byte                                                    { b, _ := json.Marshal(v); return b }
func subject(t *testing.T) (*news.Journal, news.Recording, news.RetentionReceipt, news.EventGroup) {
	t.Helper()
	j, err := news.OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	r := news.Fixture()
	r.Original += "\nUNSELECTED PRIVATE SOURCE PARAGRAPH"
	r.OriginalSHA256 = news.SHA256(r.Original)
	receipt, err := j.Retain(r)
	if err != nil {
		t.Fatal(err)
	}
	view, err := j.Events(r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	return j, r, receipt, view.Groups[0]
}
func proposal(key string) *Proposal {
	return &Proposal{Outcome: "propose_hypothesis", ReasonCodes: []string{"investigate_reported_claim"}, ClaimKeys: []string{key}, Uncertainty: "unverified", Candidate: &Candidate{Kind: "research_hypothesis", ClaimCode: "reported_relationship", RefutationCode: "contrary_observation", EvidenceKeys: []string{key}}}
}
func tool(key string) Step {
	return Step{Kind: "tool", Tool: &ToolCall{Name: "inspect_news_claim", ClaimKey: key}}
}
func requireAbstention(t *testing.T, r Result, reason string) {
	t.Helper()
	if r.Status != "finalized" || r.Outcome != "abstain" || r.ReasonCode != reason || r.Proposal != nil || r.Authority != "research_only_unqualified" || r.QualificationCredit || len(r.Checks) != len(Definition().RequiredChecks) {
		t.Fatal(r)
	}
	failed := false
	for _, c := range r.Checks {
		failed = failed || !c.Passed
	}
	if !failed {
		t.Fatal("automatic abstention lost failed check")
	}
}
func TestTargetedReviewAndNoExecutionAuthority(t *testing.T) {
	j, r, _, group := subject(t)
	key := group.Claims[0].Claim.Key
	calls := 0
	model := modelFunc(func(_ context.Context, frame Frame) ([]byte, error) {
		calls++
		if strings.Contains(string(raw(frame)), "UNSELECTED PRIVATE") {
			t.Fatal("model received unselected article text")
		}
		if calls == 1 {
			if len(frame.Results) != 0 || frame.Subject.SnapshotSHA256 == "" {
				t.Fatal(frame)
			}
			frame.Contract.Tools[0] = "execute_order"
			frame.Claims[0].State = "qualified"
			return raw(tool(key)), nil
		}
		if frame.Contract.Tools[0] != "inspect_news_claim" || frame.Claims[0].State != "current" || len(frame.Results) != 1 || frame.Results[0].Claim == nil {
			t.Fatal("frame mutation escaped copy boundary")
		}
		frame.Results[0].Claim.Assertion = "tampered"
		return raw(Step{Kind: "proposal", Proposal: proposal(key)}), nil
	})
	result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, model)
	if err != nil || result.Outcome != "propose_hypothesis" || result.Proposal == nil || calls != 2 || result.Tools[0].Claim.Assertion == "tampered" {
		t.Fatal(result, err)
	}
	for _, c := range result.Checks {
		if !c.Passed {
			t.Fatal(c)
		}
	}
	if result.QualificationCredit || result.Authority != "research_only_unqualified" || len(result.Artifacts) != 2 {
		t.Fatal("proposal gained authority")
	}
	// Even an attempted conversion cannot satisfy the existing operations contract.
	var executable domain.Decision
	if err = json.Unmarshal(raw(result), &executable); err != nil {
		t.Fatal(err)
	}
	if _, err = operations.ExecutePaper(workflow.InitialState(), workflow.Fixtures()[1], executable); err == nil {
		t.Fatal("research review acquired fixture execution authority")
	}
}
func TestMalformedArtifactsAndAuthorityAttempts(t *testing.T) {
	j, r, _, group := subject(t)
	inputs := []string{`{"kind":"tool","kind":"proposal"}`, `{"Kind":"tool"}`, `{"kind":"tool","tool":{"name":"inspect_news_claim","claim_key":"x","checks":[]}}`, `{"kind":"proposal","proposal":{"outcome":"abstain","risk_policy":"widen"}}`, `{"kind":"proposal","narrative":"explain before finalization"}`, `{"kind":"tool","tool":{}} {}`, `{"kind":"proposal","proposal":{},"tool":{}}`, strings.Repeat("x", MaxArtifactBytes+1), strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18)}
	for _, input := range inputs {
		t.Run(news.SHA256(input)[:8], func(t *testing.T) {
			result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { return []byte(input), nil }))
			if err != nil {
				t.Fatal(err)
			}
			requireAbstention(t, result, "malformed_model_artifact")
			if result.Artifacts[0].SHA256 != news.SHA256(input) {
				t.Fatal("lost artifact identity")
			}
		})
	}
	for _, test := range []string{"tool", "outcome"} {
		step := tool(group.Claims[0].Claim.Key)
		reason := "tool_not_permitted"
		if test == "tool" {
			step.Tool.Name = "change_risk_policy"
		} else {
			step = Step{Kind: "proposal", Proposal: &Proposal{Outcome: "execute_order"}}
			reason = "outcome_not_permitted"
		}
		result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { return raw(step), nil }))
		if err != nil {
			t.Fatal(err)
		}
		requireAbstention(t, result, reason)
	}
}
func TestEvidenceRequirementsAndProposalShape(t *testing.T) {
	j, r, _, group := subject(t)
	key := group.Claims[0].Claim.Key
	for _, test := range []string{"uninspected", "unknown", "duplicate", "qualified", "missing_candidate", "candidate_evidence", "candidate_on_abstain"} {
		t.Run(test, func(t *testing.T) {
			p := proposal(key)
			switch test {
			case "unknown":
				p.ClaimKeys = []string{"news-" + news.SHA256("unknown")}
				p.Candidate.EvidenceKeys = p.ClaimKeys
			case "duplicate":
				p.ClaimKeys = append(p.ClaimKeys, key)
			case "qualified":
				p.Uncertainty = "qualified"
			case "missing_candidate":
				p.Candidate = nil
			case "candidate_evidence":
				p.Candidate.EvidenceKeys = []string{"news-" + news.SHA256("other")}
			case "candidate_on_abstain":
				p.Outcome = "abstain"
			}
			calls := 0
			model := modelFunc(func(context.Context, Frame) ([]byte, error) {
				calls++
				if calls == 1 && test != "uninspected" {
					return raw(tool(key)), nil
				}
				return raw(Step{Kind: "proposal", Proposal: p}), nil
			})
			result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, model)
			if err != nil {
				t.Fatal(err)
			}
			reason := "invalid_proposal"
			if test == "uninspected" || test == "unknown" {
				reason = "evidence_not_eligible"
			}
			requireAbstention(t, result, reason)
		})
	}
	// An explicit model abstention needs no evidence or candidate, but still has
	// app-generated checks and unqualified authority.
	result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) {
		return raw(Step{Kind: "proposal", Proposal: &Proposal{Outcome: "abstain", ReasonCodes: []string{"insufficient_evidence"}, Uncertainty: "unverified"}}), nil
	}))
	if err != nil || result.Outcome != "abstain" || result.Proposal == nil {
		t.Fatal(result, err)
	}
}
func TestBoundedCompletionAndCancellation(t *testing.T) {
	j, r, _, group := subject(t)
	key := group.Claims[0].Claim.Key
	result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { return raw(tool(key)), nil }))
	if err != nil {
		t.Fatal(err)
	}
	requireAbstention(t, result, "tool_budget_exhausted")
	if len(result.Tools) != Definition().MaxToolCalls || len(result.Artifacts) > Definition().MaxTurns {
		t.Fatal("budget exceeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err = Run(ctx, j, group.ID, r.ReceivedAt, modelFunc(func(ctx context.Context, _ Frame) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }))
	if err != nil {
		t.Fatal(err)
	}
	requireAbstention(t, result, "model_timeout")
	for _, check := range result.Checks {
		if check.ID == "evidence_binding" && (check.Passed || check.Status != "not_run") {
			t.Fatal("timeout invented a completed evidence check")
		}
	}
	result, err = Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { return nil, errors.New("model unavailable") }))
	if err != nil {
		t.Fatal(err)
	}
	requireAbstention(t, result, "model_unavailable")
}
func TestRevisionCutoffsAndIneligibleEvidence(t *testing.T) {
	j, r, first, group := subject(t)
	oldKey := group.Claims[0].Claim.Key
	correction := detached(r)
	correction.Revision = 2
	correction.Kind = "correction"
	correction.AvailableAt = r.AvailableAt.Add(time.Second)
	correction.ReceivedAt = r.ReceivedAt.Add(time.Second)
	correction.Supersedes = &news.RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: 1, RecordingSHA256: first.RecordingSHA256}
	if _, err := j.Retain(correction); err != nil {
		t.Fatal(err)
	}
	evaluate := func(cutoff time.Time) Result {
		calls := 0
		result, err := Run(context.Background(), j, group.ID, cutoff, modelFunc(func(context.Context, Frame) ([]byte, error) {
			calls++
			if calls == 1 {
				return raw(tool(oldKey)), nil
			}
			return raw(Step{Kind: "proposal", Proposal: proposal(oldKey)}), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := evaluate(r.ReceivedAt)
	after := evaluate(correction.ReceivedAt)
	if before.Outcome != "propose_hypothesis" {
		t.Fatal(before)
	}
	requireAbstention(t, after, "evidence_not_eligible")
	if before.Subject.SnapshotSHA256 == after.Subject.SnapshotSHA256 {
		t.Fatal("changed evidence retained same snapshot pin")
	}
	view, _ := j.Events(r.ReceivedAt)
	if !reflect.DeepEqual(view.Groups[0], group) {
		t.Fatal("later evidence changed earlier view")
	}
}

func TestDisputedAndRetractedClaimsOnlySupportEvidenceRequests(t *testing.T) {
	for _, assessment := range []string{"disputed", "retracted"} {
		t.Run(assessment, func(t *testing.T) {
			j, r, receipt, group := subject(t)
			if assessment == "disputed" {
				correction := detached(r)
				correction.Revision = 2
				correction.Kind = "correction"
				correction.Supersedes = &news.RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: 1, RecordingSHA256: receipt.RecordingSHA256}
				correction.ReceivedAt = correction.ReceivedAt.Add(time.Second)
				correction.Claims[0].Assessment = assessment
				r = correction
			} else {
				retraction := detached(r)
				retraction.Revision = 2
				retraction.Kind = "retraction"
				retraction.Supersedes = &news.RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: 1, RecordingSHA256: receipt.RecordingSHA256}
				retraction.ReceivedAt = retraction.ReceivedAt.Add(time.Second)
				retraction.Claims[0].Assessment = assessment
				r = retraction
			}
			if _, err := j.Retain(r); err != nil {
				t.Fatal(err)
			}
			view, _ := j.Events(r.ReceivedAt)
			var selected string
			for _, c := range view.Groups[0].Claims {
				if c.Claim.Revision == 2 {
					selected = c.Claim.Key
				}
			}
			for _, outcome := range []string{"propose_hypothesis", "request_evidence"} {
				calls := 0
				p := proposal(selected)
				p.Outcome = outcome
				if outcome == "request_evidence" {
					p.Candidate = nil
				}
				result, err := Run(context.Background(), j, group.ID, r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) {
					calls++
					if calls == 1 {
						return raw(tool(selected)), nil
					}
					return raw(Step{Kind: "proposal", Proposal: p}), nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "propose_hypothesis" {
					requireAbstention(t, result, "evidence_not_eligible")
				} else if result.Outcome != "request_evidence" {
					t.Fatal("could not request evidence about withdrawn/disputed claim", result)
				}
			}
		})
	}
}

func TestInvalidSubjectNeverCallsModel(t *testing.T) {
	j, r, _, _ := subject(t)
	called := false
	if _, err := Run(context.Background(), j, "unknown", r.ReceivedAt, modelFunc(func(context.Context, Frame) ([]byte, error) { called = true; return nil, nil })); err == nil || called {
		t.Fatal("invalid subject reached model")
	}
}
