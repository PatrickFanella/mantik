package extraction

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func switchyardEvaluationFixture(t *testing.T) SwitchyardCorpus {
	t.Helper()
	good := quoteFixture()
	wrong := copyJSON(good)
	wrong.Claims[0].Assertion = "The source reports that revenue fell."
	abstain := QuoteResponse{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []QuoteClaim{}}
	corpus := SwitchyardCorpus{Schema: SwitchyardCorpusSchema, Cases: []SwitchyardEvaluationInput{}}
	for _, tc := range []struct {
		id       string
		status   int
		body     []byte
		expected Response
	}{
		{"good", 200, localReply(SwitchyardModel, string(quoteJSON(good)), "stop"), FixtureResponse()},
		{"wrong", 200, localReply(SwitchyardModel, string(quoteJSON(wrong)), "stop"), FixtureResponse()},
		{"http-failed", 503, []byte("private diagnostic"), FixtureResponse()},
		{"abstain", 200, localReply(SwitchyardModel, string(quoteJSON(abstain)), "stop"), Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []Claim{}}},
	} {
		p, f := switchyardAdmissionReceipts(t, tc.status, tc.body)
		corpus.Cases = append(corpus.Cases, SwitchyardEvaluationInput{tc.id, p, &f, tc.expected})
	}
	corpus.Cases = append(corpus.Cases, SwitchyardEvaluationInput{ID: "unknown", Prepared: corpus.Cases[0].Prepared, Expected: FixtureResponse()})
	return corpus
}
func TestSwitchyardEvaluationPreservesFailuresUnknownAndSemanticDisagreement(t *testing.T) {
	c := switchyardEvaluationFixture(t)
	r, err := EvaluateSwitchyard(context.Background(), c)
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
	again, err := EvaluateSwitchyard(context.Background(), c)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("unstable report", err)
	}
	c.Cases[0].Expected.Claims[0].Assertion = "Changed supplied annotation"
	changed, err := EvaluateSwitchyard(context.Background(), c)
	if err != nil || changed.CorpusSHA256 == first.CorpusSHA256 || changed.SHA256 == first.SHA256 || !reflect.DeepEqual(changed.Cases[0].Comparison.Actual, first.Cases[0].Comparison.Actual) {
		t.Fatal("annotation not separated from actual", err)
	}
	c.Cases[2].Finished.Response.Base64 = "changed"
	if first.Cases[2].Input.Finished.Response.Base64 == "changed" {
		t.Fatal("input aliased report")
	}
}
func TestSwitchyardEvaluationRejectsInvalidCohortWithoutPartialReport(t *testing.T) {
	baseline := switchyardEvaluationFixture(t)
	for name, change := range map[string]func(*SwitchyardCorpus){
		"schema": func(c *SwitchyardCorpus) { c.Schema = "unknown" },
		"empty":  func(c *SwitchyardCorpus) { c.Cases = nil },
		"capacity": func(c *SwitchyardCorpus) {
			for len(c.Cases) <= MaxEvaluationCases {
				c.Cases = append(c.Cases, c.Cases[0])
			}
		},
		"duplicate-id": func(c *SwitchyardCorpus) { c.Cases[1].ID = c.Cases[0].ID },
		"bad-id":       func(c *SwitchyardCorpus) { c.Cases[1].ID = "private /" },
		"duplicate-snapshot": func(c *SwitchyardCorpus) {
			c.Cases[1].Prepared = c.Cases[0].Prepared
			c.Cases[1].Finished = c.Cases[0].Finished
		},
		"source-pin": func(c *SwitchyardCorpus) { c.Cases[1].Prepared.ArticleSHA256 = strings.Repeat("f", 64) },
		"resealed-result": func(c *SwitchyardCorpus) {
			f := c.Cases[1].Finished
			f.Result.QualificationCredit = true
			f.Result.SHA256 = ""
			f.Result.SHA256 = hash(f.Result)
			*f = SwitchyardReceipt(sealLocal(LocalReceipt(*f)))
		},
		"legacy":                func(c *SwitchyardCorpus) { c.Cases[1].Prepared.Schema = "mantik-local-news-extraction-v1" },
		"missing-finish-result": func(c *SwitchyardCorpus) { c.Cases[1].Finished.Result = nil },
		"invalid-gold":          func(c *SwitchyardCorpus) { c.Cases[4].Expected.Claims[0].Evidence[0].Quote = "wrong" },
		"null-gold-claims": func(c *SwitchyardCorpus) {
			c.Cases[4].Expected = Response{Outcome: "abstain", ReasonCode: "ambiguous_source"}
		},
		"failure-as-gold": func(c *SwitchyardCorpus) {
			c.Cases[4].Expected = Response{Outcome: "abstain", ReasonCode: "extractor_unavailable", Claims: []Claim{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := copyJSON(baseline)
			change(&c)
			r, err := EvaluateSwitchyard(context.Background(), c)
			if err == nil || r.Schema != "" {
				t.Fatal("invalid cohort emitted report", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := EvaluateSwitchyard(ctx, baseline); err != context.Canceled || r.Schema != "" {
		t.Fatal("cancelled cohort emitted report")
	}
}
func TestSwitchyardEvaluationUnknownAbstentionIsNotScored(t *testing.T) {
	c := switchyardEvaluationFixture(t)
	c.Cases = c.Cases[4:]
	c.Cases[0].Expected = Response{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []Claim{}}
	r, err := EvaluateSwitchyard(context.Background(), c)
	if err != nil || r.Counts.Cases != 1 || r.Counts.UnknownCases != 1 || r.Counts.UnknownExpectedAbstentions != 1 || r.Counts.Compared != (EvaluationCounts{}) || r.Cases[0].Comparison != nil {
		t.Fatal(r, err)
	}
}

func TestEvaluationProviderSchemasCannotBeRelabeled(t *testing.T) {
	local := localEvaluationFixture(t)
	switchyard := switchyardEvaluationFixture(t)
	// Even changing the outer corpus schema cannot admit another provider's
	// otherwise valid receipts through the shared comparison core.
	badLocal := LocalCorpus{Schema: LocalCorpusSchema, Cases: []LocalEvaluationInput{{ID: "wrong-provider", Prepared: LocalReceipt(switchyard.Cases[0].Prepared), Expected: FixtureResponse()}}}
	if r, err := EvaluateLocal(context.Background(), badLocal); err == nil || r.Schema != "" {
		t.Fatal("Switchyard receipt entered local evaluator")
	}
	badSwitchyard := SwitchyardCorpus{Schema: SwitchyardCorpusSchema, Cases: []SwitchyardEvaluationInput{{ID: "wrong-provider", Prepared: SwitchyardReceipt(local.Cases[0].Prepared), Expected: FixtureResponse()}}}
	if r, err := EvaluateSwitchyard(context.Background(), badSwitchyard); err == nil || r.Schema != "" {
		t.Fatal("local receipt entered Switchyard evaluator")
	}
}
