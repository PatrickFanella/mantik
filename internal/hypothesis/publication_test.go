package hypothesis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

func publicationFixture(t *testing.T) (newscase.State, review.Attempt, Declaration, time.Time) {
	t.Helper()
	r := news.Fixture()
	return publicationFixtureFor(t, r)
}
func publicationFixtureFor(t *testing.T, r news.Recording) (newscase.State, review.Attempt, Declaration, time.Time) {
	t.Helper()
	at := r.ReceivedAt.Add(time.Hour)
	s, receipt, err := newscase.Admit(newscase.InitialState(), r, at)
	if err != nil {
		t.Fatal(err)
	}
	a, err := review.Start(s, "receiver-abcdefghijklmnop", "review", receipt.Matches[0].CaseID, 1, at)
	if err != nil {
		t.Fatal(err)
	}
	d := Fixture()
	c := a.Pin.Group.Claims[0]
	d.Origins = []Origin{{Reference: c.Reference, ClaimKey: c.Claim.Key, RecordingSHA256: c.Claim.RecordingSHA256}}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: c.Claim.Key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{c.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{c.Claim.Key}}}})
	steps, digest, err := review.CanonicalSteps([]json.RawMessage{tool, proposal})
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
	return s, a, d, at
}
func TestPublicationEvidenceBoundary(t *testing.T) {
	s, a, d, at := publicationFixture(t)
	p, err := Publish(s, a, a.Pin.Subject.ReceiverID, "first", d, at)
	if err != nil || !p.OriginBindingVerified || p.Sealed.OriginsVerified || p.Sealed.QualificationCredit || p.Sealed.Authority != "research_declaration_only" {
		t.Fatal(p, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*newscase.State, *review.Attempt, *Declaration, *time.Time)
	}{
		{"pending", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) { a.Status = "pending" }},
		{"expired", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) { a.Status = "expired" }},
		{"late", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) { a.CompletedAt = a.Deadline }},
		{"backdated", func(_ *newscase.State, _ *review.Attempt, _ *Declaration, at *time.Time) { *at = at.Add(-time.Second) }},
		{"closed", func(s *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			c := s.Cases[a.Pin.Subject.CaseID]
			c.Status = "closed"
			s.Cases[c.ID] = c
		}},
		{"stale", func(s *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			c := s.Cases[a.Pin.Subject.CaseID]
			c.Version++
			s.Cases[c.ID] = c
		}},
		{"generation", func(s *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			c := s.Cases[a.Pin.Subject.CaseID]
			c.Generation++
			s.Cases[c.ID] = c
		}},
		{"origin-reference", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) {
			d.Origins[0].Reference = "invented"
		}},
		{"origin-digest", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) {
			d.Origins[0].RecordingSHA256 = strings.Repeat("a", 64)
		}},
		{"unreviewed-claim", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) {
			d.Origins[0].ClaimKey = "news-" + strings.Repeat("a", 64)
		}},
		{"mechanism", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) { d.Mechanism.Code = "changed" }},
		{"refutation", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) {
			d.Refutation.Code = "changed"
		}},
		{"method", func(_ *newscase.State, _ *review.Attempt, d *Declaration, _ *time.Time) {
			d.Methods[0].SHA256 = strings.Repeat("a", 64)
		}},
		{"authority", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.Authority = "execute"
		}},
		{"qualification", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.QualificationCredit = true
		}},
		{"abstain", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) { a.Result.Outcome = "abstain" }},
		{"missing-check", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.Checks = a.Result.Checks[1:]
		}},
		{"failed-check", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.Checks[0].Passed = false
		}},
		{"uninspected", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) { a.Result.Tools = nil }},
		{"candidate-evidence", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.Proposal.Candidate.EvidenceKeys = []string{"news-" + strings.Repeat("a", 64)}
		}},
		{"result-identity", func(_ *newscase.State, a *review.Attempt, _ *Declaration, _ *time.Time) {
			a.Result.ID = "review-" + strings.Repeat("a", 64)
		}},
		{"source-substitution", func(s *newscase.State, _ *review.Attempt, _ *Declaration, _ *time.Time) {
			for key, r := range s.Records {
				r.Recording.Original += "tampered"
				s.Records[key] = r
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, a, d, at := publicationFixture(t)
			test.mutate(&s, &a, &d, &at)
			// Retain valid result identity so each guard is exercised directly.
			if test.name != "result-identity" {
				a.Result.ID = ""
				raw, _ := json.Marshal(a.Result)
				a.Result.ID = "review-" + news.SHA256(string(raw))
			}
			if _, err := Publish(s, a, a.Pin.Subject.ReceiverID, "rejected", d, at); err == nil {
				t.Fatal("accepted invalid publication")
			}
		})
	}
}
func TestPublicationStrictInput(t *testing.T) {
	_, a, d, _ := publicationFixture(t)
	raw, _ := json.Marshal(PublicationCommand{ReceiverID: a.Pin.Subject.ReceiverID, RequestID: "first", ReviewID: a.ID, Declaration: d})
	if _, err := ReadPublicationCommand(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(raw) + "{}", strings.Replace(string(raw), `"request_id":`, `"request_id":"changed","request_id":`, 1), strings.Replace(string(raw), `"request_id":`, `"Request_ID":`, 1), strings.Replace(string(raw), `"declaration":`, `"authority":"execute","declaration":`, 1), strings.Replace(string(raw), `"label":`, `"label":"changed","label":`, 1)} {
		if _, err := ReadPublicationCommand(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted malformed publication command")
		}
	}
}

func TestPublicationSourceScope(t *testing.T) {
	for _, name := range []string{"other-symbol", "no-symbol", "market-class", "disputed"} {
		t.Run(name, func(t *testing.T) {
			r := news.Fixture()
			switch name {
			case "other-symbol":
				r.Claims[0].Symbols = []string{"SPY"}
			case "no-symbol":
				r.Claims[0].Symbols = nil
			case "market-class":
				r.EvidenceClass = "recorded_market_unqualified"
			case "disputed":
				r.Claims[0].Assessment = "disputed"
			}
			s, a, d, at := publicationFixtureFor(t, r)
			if _, err := Publish(s, a, a.Pin.Subject.ReceiverID, "rejected", d, at); err == nil {
				t.Fatal("unsupported source admitted")
			}
		})
	}
}
