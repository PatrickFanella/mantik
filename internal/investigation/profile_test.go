package investigation

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"testing"
)

func TestRetainedProfileRefusesChangedContractBeforeModel(t *testing.T) {
	_, record, _, group := subject(t)
	pin := Pin{Subject: Subject{ReceiverID: "receiver-abcdefghijklmnop", CaseID: group.ID, CaseVersion: 1, CaseGeneration: 1, EventGroupID: group.ID, EventKey: group.EventKey, EvidenceClass: group.EvidenceClass, AsOf: record.ReceivedAt, SnapshotSHA256: news.SHA256(string(raw(group)))}, Group: group}

	for _, id := range []string{TypeID, MethodTypeID} {
		p, err := ProfileFor(id)
		if err != nil || p.Validate() != nil {
			t.Fatal(err)
		}
		called := false
		if result, err := RunPinnedProfile(context.Background(), pin, p, modelFunc(func(context.Context, Frame) ([]byte, error) {
			called = true
			return raw(Step{Kind: "proposal", Proposal: &Proposal{Outcome: "abstain", ReasonCodes: []string{"insufficient_evidence"}, Uncertainty: "unverified"}}), nil
		})); err != nil || !called || result.ProfileSHA256 != p.SHA256 {
			t.Fatal("valid pin did not reach model", err)
		}

		for _, change := range []func(*Profile){func(p *Profile) { p.Contract.MaxToolCalls++ }, func(p *Profile) { p.Contract.TypeID = "unknown" }, func(p *Profile) { p.Contract.RequiredChecks[0] = "invented" }, func(p *Profile) { p.SHA256 = "changed" }} {
			bad := detached(p)
			change(&bad)
			if bad.SHA256 != "changed" {
				bad.SHA256 = profileHash(bad)
			}
			called := false
			if _, err := RunPinnedProfile(context.Background(), pin, bad, modelFunc(func(context.Context, Frame) ([]byte, error) { called = true; return nil, nil })); err == nil || called {
				t.Fatal("changed profile reached model")
			}
		}
	}
}
