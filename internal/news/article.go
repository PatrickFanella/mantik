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
const DiagnosticArticleSchema = "mantik-news-diagnostic-article-v1"

// PublicationPrecision records a calendar date without inventing an instant.
// Diagnostic inputs cannot be admitted as service news recordings.
type PublicationPrecision struct {
	Precision string `json:"precision"`
	Date      string `json:"date,omitempty"`
}

// Article retains a source before extraction. It has no claim or extraction
// fields, and is not accepted by the existing recording admission API.
type Article struct {
	Schema         string                `json:"schema"`
	Publication    *PublicationPrecision `json:"publication,omitempty"`
	SourceID       string                `json:"source_id"`
	ArticleID      string                `json:"article_id"`
	Revision       int                   `json:"revision"`
	Kind           string                `json:"kind"`
	Supersedes     *RevisionRef          `json:"supersedes,omitempty"`
	Publisher      string                `json:"publisher"`
	URL            string                `json:"url"`
	PublishedAt    time.Time             `json:"published_at"`
	AvailableAt    time.Time             `json:"available_at"`
	ReceivedAt     time.Time             `json:"received_at"`
	EvidenceClass  string                `json:"evidence_class"`
	AccessRef      string                `json:"access_ref"`
	RawRef         string                `json:"raw_ref"`
	Original       string                `json:"original"`
	OriginalSHA256 string                `json:"original_sha256"`
}

// MarshalJSON preserves exact-time v1 encodings and omits the nonexistent
// publication instant for diagnostics rather than emitting a year-one sentinel.
func (a Article) MarshalJSON() ([]byte, error) {
	type plain Article
	if a.Schema != DiagnosticArticleSchema {
		return json.Marshal(plain(a))
	}
	if !a.PublishedAt.IsZero() {
		return nil, errors.New("diagnostic article cannot encode an exact publication instant")
	}
	return json.Marshal(struct {
		plain
		PublishedAt *time.Time `json:"published_at,omitempty"`
	}{plain: plain(a)})
}

func (a Article) source() Recording {
	return Recording{Schema: RecordingSchema, SourceID: a.SourceID, ArticleID: a.ArticleID,
		Revision: a.Revision, Kind: a.Kind, Supersedes: a.Supersedes, Publisher: a.Publisher,
		URL: a.URL, PublishedAt: a.PublishedAt, AvailableAt: a.AvailableAt, ReceivedAt: a.ReceivedAt,
		EvidenceClass: a.EvidenceClass, AccessRef: a.AccessRef, RawRef: a.RawRef,
		Original: a.Original, OriginalSHA256: a.OriginalSHA256}
}

func ArticleFromRecording(r Recording) (Article, error) {
	return (Article{Schema: ArticleSchema, SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: r.Revision, Kind: r.Kind, Supersedes: r.Supersedes, Publisher: r.Publisher, URL: r.URL, PublishedAt: r.PublishedAt, AvailableAt: r.AvailableAt, ReceivedAt: r.ReceivedAt, EvidenceClass: r.EvidenceClass, AccessRef: r.AccessRef, RawRef: r.RawRef, Original: r.Original, OriginalSHA256: r.OriginalSHA256}).Canonical()
}

func (a Article) Validate() error {
	if a.Schema == ArticleSchema {
		if a.Publication != nil {
			return errors.New("exact-time articles cannot carry diagnostic publication metadata")
		}
		return a.source().validateSource()
	}
	if a.Schema != DiagnosticArticleSchema || a.Publication == nil || !a.PublishedAt.IsZero() {
		return errors.New("unsupported or conflicting news article publication precision")
	}
	if err := a.source().validateSourceMetadata(); err != nil {
		return err
	}
	if a.EvidenceClass != "recorded_market_unqualified" || a.Kind != "initial" || !validTime(a.AvailableAt) || !validTime(a.ReceivedAt) || a.ReceivedAt.Before(a.AvailableAt) {
		return errors.New("diagnostic articles require historical initial source and causal observation/receipt instants")
	}
	switch a.Publication.Precision {
	case "date_only":
		date, err := time.Parse("2006-01-02", a.Publication.Date)
		// Without a source zone, the earliest possible instant is UTC minus 14h.
		// This is only an impossible-future guard; no instant is assigned to the date.
		if err != nil || date.Format("2006-01-02") != a.Publication.Date || date.Year() < 1 || date.Add(-14*time.Hour).After(a.AvailableAt) {
			return errors.New("invalid or future diagnostic publication date")
		}
	case "unknown":
		if a.Publication.Date != "" {
			return errors.New("unknown publication cannot supply a date")
		}
	default:
		return errors.New("unsupported diagnostic publication precision")
	}
	return nil
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
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Article{}, errors.New("invalid news article object")
	}
	if _, present := fields["published_at"]; present && a.Schema == DiagnosticArticleSchema {
		return Article{}, errors.New("diagnostic publication cannot include an exact instant")
	}
	if _, present := fields["publication"]; present && a.Schema == ArticleSchema {
		return Article{}, errors.New("exact-time article cannot include diagnostic publication")
	}
	return a.Canonical()
}
