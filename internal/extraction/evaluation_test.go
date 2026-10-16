package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

func artifactResponse(r Response) string {
	b, _ := json.Marshal(r)
	return base64.StdEncoding.EncodeToString(b)
}

func TestEvaluationCountsEveryOutcomeAndSemanticMismatch(t *testing.T) {
	c := FixtureCorpus()
	abstain := Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []Claim{}}
	add := func(id string, expected Response, raw string) {
		item := copyJSON(c.Cases[0])
		item.ID = id
		item.Expected = expected
		item.ArtifactBase64 = raw
		c.Cases = append(c.Cases, item)
	}
	add("malformed", FixtureResponse(), base64.StdEncoding.EncodeToString([]byte{0xff}))
	add("abstain", abstain, artifactResponse(abstain))
	add("missed-abstention", abstain, artifactResponse(FixtureResponse()))
	changed := FixtureResponse()
	changed.Claims[0].ID = "different-local-id"
	add("renamed", FixtureResponse(), artifactResponse(changed))
	duplicate := copyJSON(changed)
	duplicate.Claims = append(duplicate.Claims, copyJSON(duplicate.Claims[0]))
	duplicate.Claims[1].ID = "duplicate-content"
	add("extra-duplicate", FixtureResponse(), artifactResponse(duplicate))
	reason := copyJSON(abstain)
	reason.ReasonCode = "no_relevant_claims"
	add("wrong-reason", abstain, artifactResponse(reason))
	r, err := Evaluate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := EvaluationCounts{Cases: 8, ExactCases: 3, ExpectedExtracted: 5, ExpectedAbstentions: 3, UnexpectedAbstentions: 1, MissedAbstentions: 1, MatchedClaims: 3, MissingClaims: 2, UnexpectedClaims: 3}
	if r.Counts != want {
		t.Fatalf("counts %+v want %+v", r.Counts, want)
	}
	if r.Cases[1].Actual.Outcome != "extracted" || r.Cases[1].Exact || r.Cases[2].Actual.Artifact.RawBase64 != base64.StdEncoding.EncodeToString([]byte{0xff}) {
		t.Fatal("mismatch or failed artifact hidden")
	}
	if r.Authority != "supplied_annotation_agreement_only" || r.QualificationCredit || len(r.Cases) != len(c.Cases) {
		t.Fatal("authority or filtered history")
	}
	first := copyJSON(r)
	r.SHA256 = ""
	if first.SHA256 != hash(r) {
		t.Fatal("report hash")
	}
	again, err := Evaluate(context.Background(), c)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("unstable evaluation", err)
	}
	c.Cases[0].Expected.Claims[0].Assertion = "changed after evaluation"
	if first.Cases[0].Expected.Claims[0].Assertion == c.Cases[0].Expected.Claims[0].Assertion {
		t.Fatal("aliased input")
	}
}

func TestEvaluationRejectsInvalidCorpusWithoutPartialReport(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Corpus)
	}{
		{"schema", func(c *Corpus) { c.Schema = "unknown" }},
		{"empty", func(c *Corpus) { c.Cases = nil }},
		{"capacity", func(c *Corpus) {
			for len(c.Cases) <= MaxEvaluationCases {
				c.Cases = append(c.Cases, c.Cases[0])
			}
		}},
		{"duplicate-id", func(c *Corpus) { c.Cases[1].ID = c.Cases[0].ID }},
		{"bad-id", func(c *Corpus) { c.Cases[1].ID = "private source text /" }},
		{"source", func(c *Corpus) { c.Cases[1].Article.Original = "changed" }},
		{"key", func(c *Corpus) { c.Cases[1].EventKey = "/" }},
		{"base64", func(c *Corpus) { c.Cases[1].ArtifactBase64 = "AA==\n" }},
		{"oversize-artifact", func(c *Corpus) {
			c.Cases[1].ArtifactBase64 = base64.StdEncoding.EncodeToString(make([]byte, MaxArtifactBytes+1))
		}},
		{"null-gold", func(c *Corpus) { c.Cases[1].Expected = Response{Outcome: "abstain", ReasonCode: "ambiguous_source"} }},
		{"forged-gold-failure", func(c *Corpus) {
			c.Cases[1].Expected = Response{Outcome: "abstain", ReasonCode: "malformed_extractor_artifact", Claims: []Claim{}}
		}},
		{"bad-gold-span", func(c *Corpus) { c.Cases[1].Expected.Claims[0].Evidence[0].Quote = "wrong" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := FixtureCorpus()
			tt.mutate(&c)
			r, err := Evaluate(context.Background(), c)
			if err == nil || r.Schema != "" {
				t.Fatal("invalid input emitted report", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Evaluate(ctx, FixtureCorpus())
	if err != context.Canceled || r.Schema != "" {
		t.Fatal("cancellation emitted report")
	}
}

func TestEvaluationClaimOrderAndMultiplicity(t *testing.T) {
	c := FixtureCorpus()
	c.Cases = c.Cases[:1]
	expected := FixtureResponse()
	second := copyJSON(expected.Claims[0])
	second.ID = "second"
	second.Assertion = "An alternative supplied annotation."
	expected.Claims = append(expected.Claims, second)
	actual := copyJSON(expected)
	actual.Claims[0], actual.Claims[1] = actual.Claims[1], actual.Claims[0]
	c.Cases[0].Expected = expected
	c.Cases[0].ArtifactBase64 = artifactResponse(actual)
	r, err := Evaluate(context.Background(), c)
	if err != nil || r.Counts.ExactCases != 1 || r.Counts.MatchedClaims != 2 {
		t.Fatal("claim order affected agreement", err, r.Counts)
	}
	c.Cases[0].Expected.Claims[1] = copyJSON(expected.Claims[0])
	c.Cases[0].Expected.Claims[1].ID = "second"
	r, err = Evaluate(context.Background(), c)
	if err != nil || r.Counts.MatchedClaims != 1 || r.Counts.MissingClaims != 1 || r.Counts.UnexpectedClaims != 1 {
		t.Fatal("multiset matching reused a claim", err, r.Counts)
	}
}
