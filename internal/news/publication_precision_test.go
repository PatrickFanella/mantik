package news

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func diagnosticArticle() Article {
	a := FixtureArticle()
	a.Schema, a.PublishedAt, a.EvidenceClass = DiagnosticArticleSchema, time.Time{}, "recorded_market_unqualified"
	a.Publication = &PublicationPrecision{Precision: "date_only", Date: "2025-05-01"}
	return a
}

func TestDiagnosticPublicationPrecision(t *testing.T) {
	for _, precision := range []PublicationPrecision{{Precision: "date_only", Date: "2025-05-01"}, {Precision: "unknown"}} {
		a := diagnosticArticle()
		a.Publication = &precision
		raw, err := json.Marshal(a)
		if err != nil || bytes.Contains(raw, []byte(`"published_at"`)) || bytes.Contains(raw, []byte("0001-01-01")) {
			t.Fatal("invented publication instant", string(raw), err)
		}
		got, err := ReadArticle(bytes.NewReader(raw))
		if err != nil || got.Publication == nil || *got.Publication != precision || !got.PublishedAt.IsZero() {
			t.Fatal(got, err)
		}
		got.Publication.Precision = "changed"
		if a.Publication.Precision == "changed" {
			t.Fatal("aliased precision")
		}
		if a.ValidateClaims(Fixture().Claims) != nil {
			t.Fatal("source-bound claims rejected")
		}
		if a.source().validateSource() == nil {
			t.Fatal("diagnostic admitted as exact recording")
		}
		for _, field := range []string{`"published_at":null,`, `"published_at":"0001-01-01T00:00:00Z",`, `"publication":null,`} {
			bad := strings.Replace(string(raw), "{", "{"+field, 1)
			if _, err := ReadArticle(strings.NewReader(bad)); err == nil {
				t.Fatal("conflicting precision field accepted", field)
			}
		}
	}
}

func TestDiagnosticPublicationRejectsAmbiguityAndBrokenCausality(t *testing.T) {
	for name, change := range map[string]func(*Article){
		"missing":             func(a *Article) { a.Publication = nil },
		"instant":             func(a *Article) { a.PublishedAt = FixtureArticle().PublishedAt },
		"invalid date":        func(a *Article) { a.Publication.Date = "2025-02-30" },
		"unbounded date":      func(a *Article) { a.Publication.Date = "0000-01-01" },
		"format":              func(a *Article) { a.Publication.Date = "2025-5-1" },
		"future":              func(a *Article) { a.Publication.Date = "9999-01-01" },
		"unknown with date":   func(a *Article) { a.Publication.Precision = "unknown" },
		"precision":           func(a *Article) { a.Publication.Precision = "exact" },
		"backward receipt":    func(a *Article) { a.ReceivedAt = a.AvailableAt.Add(-time.Second) },
		"missing observation": func(a *Article) { a.AvailableAt = time.Time{} },
		"prospective":         func(a *Article) { a.EvidenceClass = "prospective_market_unqualified" },
		"synthetic":           func(a *Article) { a.EvidenceClass = "synthetic_unqualified" },
		"v1 mixing":           func(a *Article) { a.Schema = ArticleSchema; a.PublishedAt = FixtureArticle().PublishedAt },
	} {
		t.Run(name, func(t *testing.T) {
			a := diagnosticArticle()
			change(&a)
			if a.Validate() == nil {
				t.Fatal("accepted invalid precision")
			}
		})
	}
	// Calendar dates without a source zone can be a day ahead of UTC observation.
	a := diagnosticArticle()
	a.Publication.Date = "2025-05-01"
	a.AvailableAt = time.Date(2025, 4, 30, 10, 0, 0, 0, time.UTC)
	a.ReceivedAt = a.AvailableAt
	if a.Validate() != nil {
		t.Fatal("earliest UTC+14 observation rejected")
	}
	a.AvailableAt = a.AvailableAt.Add(-time.Second)
	if a.Validate() == nil {
		t.Fatal("impossible future publication accepted")
	}
}

func TestExactArticleJSONCompatibility(t *testing.T) {
	a := FixtureArticle()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	type legacy struct {
		Schema         string       `json:"schema"`
		SourceID       string       `json:"source_id"`
		ArticleID      string       `json:"article_id"`
		Revision       int          `json:"revision"`
		Kind           string       `json:"kind"`
		Supersedes     *RevisionRef `json:"supersedes,omitempty"`
		Publisher      string       `json:"publisher"`
		URL            string       `json:"url"`
		PublishedAt    time.Time    `json:"published_at"`
		AvailableAt    time.Time    `json:"available_at"`
		ReceivedAt     time.Time    `json:"received_at"`
		EvidenceClass  string       `json:"evidence_class"`
		AccessRef      string       `json:"access_ref"`
		RawRef         string       `json:"raw_ref"`
		Original       string       `json:"original"`
		OriginalSHA256 string       `json:"original_sha256"`
	}
	var old legacy
	if json.Unmarshal(raw, &old) != nil {
		t.Fatal("legacy read")
	}
	want, _ := json.Marshal(old)
	if !bytes.Equal(raw, want) {
		t.Fatal("exact-time encoding changed")
	}
	bad := strings.Replace(string(raw), "{", `{"publication":null,`, 1)
	if _, err = ReadArticle(strings.NewReader(bad)); err == nil {
		t.Fatal("v1 publication field accepted")
	}
}
