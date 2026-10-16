package extraction

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func quoteFixture() QuoteResponse {
	f := FixtureResponse()
	r := QuoteResponse{Outcome: f.Outcome, ReasonCode: f.ReasonCode, Claims: []QuoteClaim{}}
	for _, c := range f.Claims {
		q := QuoteClaim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []Quote{}}
		for _, s := range c.Evidence {
			q.Evidence = append(q.Evidence, Quote{Quote: s.Quote})
		}
		r.Claims = append(r.Claims, q)
	}
	return r
}
func quoteJSON(r QuoteResponse) []byte { b, _ := json.Marshal(r); return b }

func TestExactQuoteAnchoringPreservesClaimsAndBindsBothArtifacts(t *testing.T) {
	a := news.FixtureArticle()
	q := quoteFixture()
	q.Claims[0].Assertion = "An independently supplied disputed assertion."
	q.Claims[0].Assessment = "disputed"
	raw := quoteJSON(q)
	anchor, derived := AnchorQuotes(a, "event", raw)
	if anchor.Status != "derived" || anchor.ModelArtifactSHA256 != news.SHA256(string(raw)) || anchor.CandidateArtifactSHA256 != news.SHA256(string(derived)) || anchor.ArticleSHA256 != hash(a) || anchor.EventKey != "event" || sealAnchoring(anchor) != anchor {
		t.Fatal("artifact/source pins", anchor)
	}
	var response Response
	if json.Unmarshal(derived, &response) != nil {
		t.Fatal("invalid derived response")
	}
	got := response.Claims[0]
	want := q.Claims[0]
	if got.ID != want.ID || got.Code != want.Code || got.Assertion != want.Assertion || got.Assessment != want.Assessment || !reflect.DeepEqual(got.Entities, want.Entities) || !reflect.DeepEqual(got.Symbols, want.Symbols) {
		t.Fatal("claim content repaired")
	}
	s := got.Evidence[0]
	if a.Original[s.Start:s.End] != want.Evidence[0].Quote || s.End-s.Start != len(s.Quote) {
		t.Fatal("incorrect byte coordinates")
	}
	result, err := Run(context.Background(), a, "event", evaluationArtifact(derived))
	if err != nil || result.Outcome != "extracted" || result.QualificationCredit {
		t.Fatal("existing contract rejected correct derivation", err)
	}
	again, b := AnchorQuotes(a, "event", raw)
	if again != anchor || string(b) != string(derived) {
		t.Fatal("unstable derivation")
	}
	changed := append(append([]byte(nil), raw...), byte(' '))
	other, b := AnchorQuotes(a, "event", changed)
	if other.ModelArtifactSHA256 == anchor.ModelArtifactSHA256 || other.CandidateArtifactSHA256 != anchor.CandidateArtifactSHA256 || string(b) != string(derived) {
		t.Fatal("lost exact raw-byte provenance")
	}
	q.Claims[0].Assertion = "changed"
	if got.Assertion == q.Claims[0].Assertion {
		t.Fatal("aliased output")
	}
}

func TestExactQuoteAnchoringUTF8AndOverlappingAmbiguity(t *testing.T) {
	a := news.FixtureArticle()
	a.Original = "😀 intro — Café reported €100. tail"
	a.OriginalSHA256 = news.SHA256(a.Original)
	q := quoteFixture()
	q.Claims[0].Evidence = []Quote{{Quote: "Café reported €100."}}
	anchor, b := AnchorQuotes(a, "event", quoteJSON(q))
	if anchor.Status != "derived" {
		t.Fatal(anchor)
	}
	var response Response
	json.Unmarshal(b, &response)
	span := response.Claims[0].Evidence[0]
	if span.Start != len("😀 intro — ") || span.End != len("😀 intro — Café reported €100.") {
		t.Fatal("rune count used as byte offset", span)
	}
	a.Original = "aaa"
	a.OriginalSHA256 = news.SHA256(a.Original)
	q.Claims[0].Evidence = []Quote{{Quote: "aa"}}
	anchor, b = AnchorQuotes(a, "event", quoteJSON(q))
	if anchor.ReasonCode != "quote_ambiguous" || len(b) != 0 {
		t.Fatal("overlap ambiguity accepted", anchor)
	}
	a.Original = "Café"
	a.OriginalSHA256 = news.SHA256(a.Original)
	q.Claims[0].Evidence = []Quote{{Quote: "Cafe\u0301"}}
	anchor, b = AnchorQuotes(a, "event", quoteJSON(q))
	if anchor.ReasonCode != "quote_not_found" || len(b) != 0 {
		t.Fatal("Unicode normalized instead of exact matched")
	}
}

func TestExactQuoteAnchoringRejectsWholeSetWithoutRepair(t *testing.T) {
	tests := []struct {
		name, reason string
		change       func(*news.Article, *QuoteResponse)
	}{
		{"absent", "quote_not_found", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Evidence[0].Quote = "not in original" }},
		{"ambiguous", "quote_ambiguous", func(a *news.Article, q *QuoteResponse) {
			a.Original += " " + q.Claims[0].Evidence[0].Quote
			a.OriginalSHA256 = news.SHA256(a.Original)
		}},
		{"empty-quote", "invalid_quote", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Evidence[0].Quote = "" }},
		{"long-quote", "invalid_quote", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Evidence[0].Quote = strings.Repeat("x", 2049) }},
		{"no-quotes", "invalid_quote_count", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Evidence = nil }},
		{"duplicate-quote", "invalid_anchored_claims", func(a *news.Article, q *QuoteResponse) {
			q.Claims[0].Evidence = append(q.Claims[0].Evidence, q.Claims[0].Evidence[0])
		}},
		{"missing-entity", "invalid_anchored_claims", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Entities = nil }},
		{"empty-assertion", "invalid_anchored_claims", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Assertion = "" }},
		{"qualification-assessment", "invalid_anchored_claims", func(a *news.Article, q *QuoteResponse) { q.Claims[0].Assessment = "verified" }},
		{"null-claims", "invalid_quote_response", func(a *news.Article, q *QuoteResponse) { q.Claims = nil }},
		{"mixed-failure", "quote_not_found", func(a *news.Article, q *QuoteResponse) {
			second := copyJSON(q.Claims[0])
			second.ID = "second"
			second.Evidence[0].Quote = "missing"
			q.Claims = append(q.Claims, second)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := news.FixtureArticle()
			q := quoteFixture()
			tt.change(&a, &q)
			r, b := AnchorQuotes(a, "event", quoteJSON(q))
			if r.Status != "rejected" || r.ReasonCode != tt.reason || len(b) != 0 || r.CandidateArtifactSHA256 != "" {
				t.Fatal("invalid or partial candidate derived", r)
			}
		})
	}
	good := quoteJSON(quoteFixture())
	for _, raw := range [][]byte{[]byte(`{"outcome":"extracted","outcome":"abstain"}`), []byte(`{"Outcome":"extracted"}`), []byte(`{"outcome":"abstain","reason_code":"ambiguous_source","claims":[],"risk":true}`), append(append([]byte(nil), good...), []byte(`{}`)...), []byte{0xff}} {
		r, b := AnchorQuotes(news.FixtureArticle(), "event", raw)
		if r.ReasonCode != "invalid_quote_response" || len(b) != 0 {
			t.Fatal("malformed input accepted")
		}
	}
	// Legacy offsets must not bypass quote-only validation, even if correct.
	legacy, _ := json.Marshal(FixtureResponse())
	r, b := AnchorQuotes(news.FixtureArticle(), "event", legacy)
	if r.ReasonCode != "invalid_quote_response" || len(b) != 0 {
		t.Fatal("legacy coordinates accepted")
	}
	q := QuoteResponse{Outcome: "abstain", ReasonCode: "ambiguous_source", Claims: []QuoteClaim{}}
	r, b = AnchorQuotes(news.FixtureArticle(), "event", quoteJSON(q))
	if r.Status != "derived" {
		t.Fatal("explicit abstention lost")
	}
	result, err := Run(context.Background(), news.FixtureArticle(), "event", evaluationArtifact(b))
	if err != nil || result.Outcome != "abstain" || result.ReasonCode != "ambiguous_source" {
		t.Fatal("abstention changed", err)
	}
}
