package news

import "time"

// Fixture is invented source text with no market or qualification credit.
func Fixture() Recording {
	text := "Demo issuer reported an illustrative revenue estimate of 100 units."
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	return Recording{Schema: RecordingSchema, SourceID: "synthetic-news", ArticleID: "demo-article", Revision: 1, Kind: "initial", Publisher: "Synthetic fixture publisher", URL: "https://example.invalid/demo-article", PublishedAt: at, AvailableAt: at.Add(time.Minute), ReceivedAt: at.Add(time.Minute + time.Second), EvidenceClass: "synthetic_unqualified", AccessRef: "synthetic-news-fixture-v1", RawRef: "fixture:news-demo-article-r1", Original: text, OriginalSHA256: SHA256(text), ExtractionMethod: "operator_recorded_v1", Claims: []Claim{{ID: "demo-estimate", EventKey: "demo-revenue-event", Code: "revenue_estimate", Assertion: "Demo issuer reported an illustrative estimate", Assessment: "reported_unverified", Entities: []Entity{{ID: "demo-issuer", Name: "Demo issuer"}}, Symbols: []string{"DEMO"}, Evidence: []Span{{Start: 0, End: len(text), Quote: text}}}}}
}
