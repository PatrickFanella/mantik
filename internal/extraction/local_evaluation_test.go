package extraction

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func localEvaluationFixture(t *testing.T) LocalCorpus {
	t.Helper()
	good := quoteFixture()
	wrong := copyJSON(good)
	wrong.Claims[0].Assertion = "The source reports that revenue fell."
	abstain := QuoteResponse{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []QuoteClaim{}}
	corpus := LocalCorpus{Schema: LocalCorpusSchema, Cases: []LocalEvaluationInput{}}
	for _, tc := range []struct {
		id       string
		status   int
		body     []byte
		expected Response
	}{
		{"good", 200, localReply("ollama/fixture", string(quoteJSON(good)), "stop"), FixtureResponse()},
		{"wrong", 200, localReply("ollama/fixture", string(quoteJSON(wrong)), "stop"), FixtureResponse()},
		{"http-failed", 503, []byte("private diagnostic"), FixtureResponse()},
		{"abstain", 200, localReply("ollama/fixture", string(quoteJSON(abstain)), "stop"), Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []Claim{}}},
	} {
		p, f := inspectionReceipts(t, tc.status, tc.body)
		corpus.Cases = append(corpus.Cases, LocalEvaluationInput{tc.id, p, &f, tc.expected})
	}
	corpus.Cases = append(corpus.Cases, LocalEvaluationInput{ID: "unknown", Prepared: corpus.Cases[0].Prepared, Expected: FixtureResponse()})
	return corpus
}
func TestLocalEvaluationPreservesFailuresUnknownAndSemanticDisagreement(t *testing.T) {
	c := localEvaluationFixture(t)
	r, err := EvaluateLocal(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := EvaluationCounts{Cases: 4, ExactCases: 2, ExpectedExtracted: 3, ExpectedAbstentions: 1, UnexpectedAbstentions: 1, MatchedClaims: 1, MissingClaims: 2, UnexpectedClaims: 1}
	if r.Counts.Cases != 5 || r.Counts.FinishedCases != 4 || r.Counts.UnknownCases != 1 || r.Counts.UnknownExpectedExtracted != 1 || r.Counts.UnknownExpectedClaims != 1 || r.Counts.DistinctPreparedReceipts != 4 || r.Counts.RepeatedPreparedCases != 1 || r.Counts.Compared != want {
		t.Fatal(r.Counts, want)
	}
	if r.Cases[1].Comparison.Actual.Outcome != "extracted" || r.Cases[1].Comparison.Exact || r.Cases[2].Comparison.Actual.ReasonCode != "extractor_unavailable" || r.Cases[4].Comparison != nil || r.Cases[4].Inspection.Disposition != "completion_unknown" {
		t.Fatal("failure or unknown rewritten")
	}
	if r.Counts.Dispositions["http_failed"] != 1 || r.Counts.Dispositions["completion_unknown"] != 1 || r.Authority != "supplied_annotation_agreement_only" || r.ExecutionAttested || r.QualificationCredit {
		t.Fatal("authority or disposition counts")
	}
	if !reflect.DeepEqual(r.Cases[2].Input.Finished, c.Cases[2].Finished) {
		t.Fatal("full failure bytes lost")
	}
	first := copyJSON(r)
	r.SHA256 = ""
	if first.SHA256 != hash(r) || first.CorpusSHA256 != hash(c) {
		t.Fatal("hash binding")
	}
	again, err := EvaluateLocal(context.Background(), c)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("unstable report", err)
	}
	c.Cases[0].Expected.Claims[0].Assertion = "Changed supplied annotation"
	changed, err := EvaluateLocal(context.Background(), c)
	if err != nil || changed.CorpusSHA256 == first.CorpusSHA256 || changed.SHA256 == first.SHA256 || !reflect.DeepEqual(changed.Cases[0].Comparison.Actual, first.Cases[0].Comparison.Actual) {
		t.Fatal("annotation not separated from actual", err)
	}
	c.Cases[2].Finished.Response.Base64 = "changed"
	if first.Cases[2].Input.Finished.Response.Base64 == "changed" {
		t.Fatal("input aliased report")
	}
}
func TestLocalEvaluationRejectsInvalidCohortWithoutPartialReport(t *testing.T) {
	baseline := localEvaluationFixture(t)
	for name, change := range map[string]func(*LocalCorpus){
		"schema": func(c *LocalCorpus) { c.Schema = "unknown" },
		"empty":  func(c *LocalCorpus) { c.Cases = nil },
		"capacity": func(c *LocalCorpus) {
			for len(c.Cases) <= MaxEvaluationCases {
				c.Cases = append(c.Cases, c.Cases[0])
			}
		},
		"duplicate-id": func(c *LocalCorpus) { c.Cases[1].ID = c.Cases[0].ID },
		"bad-id":       func(c *LocalCorpus) { c.Cases[1].ID = "private /" },
		"duplicate-snapshot": func(c *LocalCorpus) {
			c.Cases[1].Prepared = c.Cases[0].Prepared
			c.Cases[1].Finished = c.Cases[0].Finished
		},
		"source-pin": func(c *LocalCorpus) { c.Cases[1].Prepared.ArticleSHA256 = strings.Repeat("f", 64) },
		"resealed-result": func(c *LocalCorpus) {
			f := c.Cases[1].Finished
			f.Result.QualificationCredit = true
			f.Result.SHA256 = ""
			f.Result.SHA256 = hash(f.Result)
			*f = sealLocal(*f)
		},
		"legacy":                func(c *LocalCorpus) { c.Cases[1].Prepared.Schema = "mantik-local-news-extraction-v1" },
		"missing-finish-result": func(c *LocalCorpus) { c.Cases[1].Finished.Result = nil },
		"invalid-gold":          func(c *LocalCorpus) { c.Cases[4].Expected.Claims[0].Evidence[0].Quote = "wrong" },
		"null-gold-claims": func(c *LocalCorpus) {
			c.Cases[4].Expected = Response{Outcome: "abstain", ReasonCode: "ambiguous_source"}
		},
		"failure-as-gold": func(c *LocalCorpus) {
			c.Cases[4].Expected = Response{Outcome: "abstain", ReasonCode: "extractor_unavailable", Claims: []Claim{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := copyJSON(baseline)
			change(&c)
			r, err := EvaluateLocal(context.Background(), c)
			if err == nil || r.Schema != "" {
				t.Fatal("invalid cohort emitted report", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := EvaluateLocal(ctx, baseline); err != context.Canceled || r.Schema != "" {
		t.Fatal("cancelled cohort emitted report")
	}
}
func TestLocalEvaluationUnknownAbstentionIsNotScored(t *testing.T) {
	c := localEvaluationFixture(t)
	c.Cases = c.Cases[4:]
	c.Cases[0].Expected = Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []Claim{}}
	r, err := EvaluateLocal(context.Background(), c)
	if err != nil || r.Counts.Cases != 1 || r.Counts.UnknownCases != 1 || r.Counts.UnknownExpectedAbstentions != 1 || r.Counts.Compared != (EvaluationCounts{}) || r.Cases[0].Comparison != nil {
		t.Fatal(r, err)
	}
}
