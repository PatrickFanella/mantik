package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestResearchReviewCLI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal")
	j, err := news.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	r := news.Fixture()
	if _, err = j.Retain(r); err != nil {
		t.Fatal(err)
	}
	groups, err := j.Events(r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	group := groups.Groups[0]
	key := group.Claims[0].Claim.Key
	steps := []investigation.Step{{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: key}}, {Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "request_evidence", ReasonCodes: []string{"independence_not_assessed"}, ClaimKeys: []string{key}, Uncertainty: "unverified"}}}
	file := filepath.Join(root, "model.json")
	raw, _ := json.Marshal(steps)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out bytes.Buffer
	args := []string{"research-review", "--journal", path, "--event", group.ID, "--as-of", r.ReceivedAt.Format("2006-01-02T15:04:05Z07:00"), "--model-fixture", file}
	if err = Run(ctx, args, &out, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Mode   string               `json:"mode"`
		Result investigation.Result `json:"result"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "recorded_model_fixture" || result.Result.Outcome != "request_evidence" || result.Result.QualificationCredit {
		t.Fatal(result)
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, raw) {
		t.Fatal("model fixture changed")
	}
	out.Reset()
	if err = Run(ctx, []string{"research-review", "--export-contract"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var contract investigation.Contract
	_ = json.Unmarshal(out.Bytes(), &contract)
	if contract.TypeID != investigation.TypeID || contract.MaxToolCalls != 4 {
		t.Fatal(contract)
	}
	for _, bad := range [][]string{{"research-review", "--export-contract", "--journal", path}, {"research-review", "--journal", path}, {"research-review", "--event", group.ID, "--model-fixture", file, "--as-of", "invalid"}} {
		out.Reset()
		if err = Run(ctx, bad, &out, &out); err == nil {
			t.Fatal("incomplete review accepted")
		}
	}
	// The opt-in profile can inspect and bind a compiled method, without changing
	// the default profile or writing the journal/model fixture.
	ref, _, err := library.PinMethod(library.FixtureStrategy().ID)
	if err != nil {
		t.Fatal(err)
	}
	methodSteps := []investigation.Step{steps[0], {Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_native_method", MethodID: ref.ID, MethodSHA256: ref.SHA256}}, {Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", ReasonCodes: []string{"synthetic_method_candidate"}, ClaimKeys: []string{key}, Uncertainty: "unverified", Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: "synthetic_relationship", RefutationCode: "contrary_observation", EvidenceKeys: []string{key}, Methods: []library.MethodRef{ref}}}}}
	methodRaw, _ := json.Marshal(methodSteps)
	if err = os.WriteFile(file, methodRaw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = Run(ctx, append(append([]string{}, args...), "--type", investigation.MethodTypeID), &out, &out); err != nil {
		t.Fatal(err)
	}
	result.Result = investigation.Result{}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.Result.Schema != investigation.MethodSchema || result.Result.TypeID != investigation.MethodTypeID || result.Result.Outcome != "propose_hypothesis" || len(result.Result.Tools) != 2 || result.Result.Tools[1].MethodRef.SHA256 != ref.SHA256 {
		t.Fatal(result, err)
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, methodRaw) {
		t.Fatal("profile changed recorded fixture", err)
	}
	out.Reset()
	if err = Run(ctx, []string{"research-review", "--export-contract", "--type", investigation.MethodTypeID}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out.Bytes(), &contract); err != nil || contract.TypeID != investigation.MethodTypeID || len(contract.Tools) != 2 || len(contract.RequiredChecks) != 6 {
		t.Fatal(contract, err)
	}
	out.Reset()
	if err = Run(ctx, []string{"research-review", "--type", "unsupported", "--journal", path}, &out, &out); err == nil {
		t.Fatal("unknown profile accepted")
	}

	// A malformed artifact is a retained finalized abstention, not a successful
	// proposal or an unstructured command error.
	if err = os.WriteFile(file, []byte(`[{"kind":"proposal","narrative":"untrusted explanation"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = Run(ctx, args, &out, &out); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out.Bytes(), &result)
	if result.Result.Outcome != "abstain" || result.Result.ReasonCode != "malformed_model_artifact" {
		t.Fatal(result)
	}
}
