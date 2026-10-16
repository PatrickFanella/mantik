package news

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"
)

const ArticleSchema = "mantik-news-article-v1"

// Article retains a source before extraction. It has no claim or extraction
// fields, and is not accepted by the existing recording admission API.
type Article struct {
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

func (a Article) source() Recording {
	return Recording{Schema: RecordingSchema, SourceID: a.SourceID, ArticleID: a.ArticleID,
		Revision: a.Revision, Kind: a.Kind, Supersedes: a.Supersedes, Publisher: a.Publisher,
		URL: a.URL, PublishedAt: a.PublishedAt, AvailableAt: a.AvailableAt, ReceivedAt: a.ReceivedAt,
		EvidenceClass: a.EvidenceClass, AccessRef: a.AccessRef, RawRef: a.RawRef,
		Original: a.Original, OriginalSHA256: a.OriginalSHA256}
}

func (a Article) Validate() error {
	if a.Schema != ArticleSchema {
		return errors.New("unsupported news article schema")
	}
	return a.source().validateSource()
}

// ValidateClaims reuses the recording's excerpt/uncertainty/revision checks.
// It does not label the extraction operator-recorded or produce an admissible
// recording. Source truth and semantic relevance are outside these checks.
func (a Article) ValidateClaims(claims []Claim) error {
	if err := a.Validate(); err != nil {
		return err
	}
	r := a.source()
	r.Claims = claims
	return r.validateClaims()
}

func (a Article) Canonical() (Article, error) {
	if err := a.Validate(); err != nil {
		return Article{}, err
	}
	a.PublishedAt, a.AvailableAt, a.ReceivedAt = a.PublishedAt.UTC(), a.AvailableAt.UTC(), a.ReceivedAt.UTC()
	raw, _ := json.Marshal(a)
	var copy Article
	if err := json.Unmarshal(raw, &copy); err != nil {
		return Article{}, err
	}
	return copy, nil
}

func FixtureArticle() Article {
	r := Fixture()
	return Article{Schema: ArticleSchema, SourceID: r.SourceID, ArticleID: r.ArticleID,
		Revision: r.Revision, Kind: r.Kind, Publisher: r.Publisher, URL: r.URL,
		PublishedAt: r.PublishedAt, AvailableAt: r.AvailableAt, ReceivedAt: r.ReceivedAt,
		EvidenceClass: r.EvidenceClass, AccessRef: r.AccessRef, RawRef: r.RawRef,
		Original: r.Original, OriginalSHA256: r.OriginalSHA256}
}

func ReadArticle(r io.Reader) (Article, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxRecordingBytes+1))
	if err != nil || len(raw) > MaxRecordingBytes || !utf8.Valid(raw) {
		return Article{}, errors.New("news article requires bounded UTF-8 JSON")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return Article{}, errors.New("invalid or ambiguous news article JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var a Article
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF {
		return Article{}, errors.New("invalid news article JSON")
	}
	return a.Canonical()
}
