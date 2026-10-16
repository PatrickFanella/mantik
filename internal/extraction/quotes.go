package extraction

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const QuoteAnchoringSchema = "mantik-exact-quote-anchoring-v1"

// QuoteResponse is a separate local-model protocol. Offsets and routing keys
// cannot be supplied by the model. All other claim fields are preserved.
type QuoteResponse struct {
	Outcome    string       `json:"outcome"`
	ReasonCode string       `json:"reason_code"`
	Claims     []QuoteClaim `json:"claims"`
}

type QuoteClaim struct {
	ID         string        `json:"id"`
	Code       string        `json:"code"`
	Assertion  string        `json:"assertion"`
	Assessment string        `json:"assessment"`
	Entities   []news.Entity `json:"entities"`
	Symbols    []string      `json:"symbols"`
	Evidence   []Quote       `json:"evidence"`
}

type Quote struct {
	Quote string `json:"quote"`
}

type QuoteAnchoring struct {
	Schema                  string `json:"schema"`
	ArticleSHA256           string `json:"article_sha256"`
	EventKey                string `json:"event_key"`
	ModelArtifactSHA256     string `json:"model_artifact_sha256"`
	CandidateArtifactSHA256 string `json:"candidate_artifact_sha256"`
	Status                  string `json:"status"`
	ReasonCode              string `json:"reason_code"`
	SHA256                  string `json:"sha256"`
}

func sealAnchoring(a QuoteAnchoring) QuoteAnchoring { a.SHA256 = ""; a.SHA256 = hash(a); return a }

// AnchorQuotes derives byte coordinates only for unique, exact quotes. It does
// not trim, normalize, paraphrase, pick among repeated matches or repair claims.
// A single invalid claim rejects the entire derivation with no partial bytes.
func AnchorQuotes(article news.Article, key string, raw []byte) (QuoteAnchoring, []byte) {
	r := QuoteAnchoring{Schema: QuoteAnchoringSchema, EventKey: key, ModelArtifactSHA256: news.SHA256(string(raw)), Status: "rejected"}
	fail := func(reason string) (QuoteAnchoring, []byte) {
		r.ReasonCode = reason
		return sealAnchoring(r), nil
	}
	a, err := article.Canonical()
	if err != nil || !eventKey.MatchString(key) {
		return fail("invalid_source_or_event")
	}
	r.ArticleSHA256 = hash(a)
	var input QuoteResponse
	if workerplan.ReadBoundedShape(raw, &input, MaxArtifactBytes) != nil || input.Claims == nil {
		return fail("invalid_quote_response")
	}
	response := Response{Outcome: input.Outcome, ReasonCode: input.ReasonCode, Claims: []Claim{}}
	if input.Outcome == "abstain" {
		if len(input.Claims) != 0 || !slices.Contains([]string{"insufficient_source", "no_relevant_claims", "ambiguous_source"}, input.ReasonCode) {
			return fail("invalid_quote_response")
		}
	} else if input.Outcome != "extracted" || input.ReasonCode != "claims_extracted" || len(input.Claims) < 1 || len(input.Claims) > Definition().MaxClaims {
		return fail("invalid_quote_response")
	}
	bound := []news.Claim{}
	for _, c := range input.Claims {
		if len(c.Evidence) < 1 || len(c.Evidence) > 4 {
			return fail("invalid_quote_count")
		}
		claim := Claim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []news.Span{}}
		for _, q := range c.Evidence {
			if len(q.Quote) == 0 || len(q.Quote) > 2048 || !utf8.ValidString(q.Quote) {
				return fail("invalid_quote")
			}
			start := strings.Index(a.Original, q.Quote)
			if start < 0 {
				return fail("quote_not_found")
			}
			// Advance one byte, not len(quote), so overlapping occurrences count.
			if strings.Contains(a.Original[start+1:], q.Quote) {
				return fail("quote_ambiguous")
			}
			claim.Evidence = append(claim.Evidence, news.Span{Start: start, End: start + len(q.Quote), Quote: q.Quote})
		}
		response.Claims = append(response.Claims, claim)
		bound = append(bound, news.Claim{ID: claim.ID, EventKey: key, Code: claim.Code, Assertion: claim.Assertion, Assessment: claim.Assessment, Entities: claim.Entities, Symbols: claim.Symbols, Evidence: claim.Evidence})
	}
	if input.Outcome == "extracted" && a.ValidateClaims(bound) != nil {
		return fail("invalid_anchored_claims")
	}
	derived, _ := json.Marshal(response)
	if len(derived) > MaxArtifactBytes {
		return fail("derived_artifact_too_large")
	}
	r.Status, r.ReasonCode, r.CandidateArtifactSHA256 = "derived", "exact_quotes_anchored", news.SHA256(string(derived))
	return sealAnchoring(r), derived
}
