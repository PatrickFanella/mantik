// Package news validates recorded claims and their exact source bindings. It
// also retains private originals and derives event groups; it has no provider,
// model, service routing or execution access.
package news

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const RecordingSchema = "mantik-news-recording-v1"
const ResultSchema = "mantik-news-claims-v1"
const MaxRecordingBytes = 2 << 20

var jsonField = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var key = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var symbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type RevisionRef struct {
	SourceID        string `json:"source_id"`
	ArticleID       string `json:"article_id"`
	Revision        int    `json:"revision"`
	RecordingSHA256 string `json:"recording_sha256"`
}

// Span offsets are UTF-8 byte offsets into Original; Quote must match exactly.
type Span struct {
	Start int    `json:"start_byte"`
	End   int    `json:"end_byte"`
	Quote string `json:"quote"`
}
type Entity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Claim struct {
	ID         string   `json:"id"`
	EventKey   string   `json:"event_key"`
	Code       string   `json:"code"`
	Assertion  string   `json:"assertion"`
	Assessment string   `json:"assessment"`
	Entities   []Entity `json:"entities"`
	Symbols    []string `json:"symbols"`
	Evidence   []Span   `json:"evidence"`
}

type Recording struct {
	Schema           string       `json:"schema"`
	SourceID         string       `json:"source_id"`
	ArticleID        string       `json:"article_id"`
	Revision         int          `json:"revision"`
	Kind             string       `json:"kind"`
	Supersedes       *RevisionRef `json:"supersedes,omitempty"`
	Publisher        string       `json:"publisher"`
	URL              string       `json:"url"`
	PublishedAt      time.Time    `json:"published_at"`
	AvailableAt      time.Time    `json:"available_at"`
	ReceivedAt       time.Time    `json:"received_at"`
	EvidenceClass    string       `json:"evidence_class"`
	AccessRef        string       `json:"access_ref"`
	RawRef           string       `json:"raw_ref"`
	Original         string       `json:"original"`
	OriginalSHA256   string       `json:"original_sha256"`
	ExtractionMethod string       `json:"extraction_method"`
	Claims           []Claim      `json:"claims"`
}

type BoundClaim struct {
	Claim
	RecordingSHA256 string    `json:"recording_sha256"`
	Key             string    `json:"key"`
	SourceID        string    `json:"source_id"`
	ArticleID       string    `json:"article_id"`
	Revision        int       `json:"revision"`
	PublishedAt     time.Time `json:"published_at"`
	AvailableAt     time.Time `json:"available_at"`
	ReceivedAt      time.Time `json:"received_at"`
	EvidenceClass   string    `json:"evidence_class"`
	RawRef          string    `json:"raw_ref"`
	OriginalSHA256  string    `json:"original_sha256"`
	Independence    string    `json:"independence"`
}

type Result struct {
	Schema           string       `json:"schema"`
	RecordingSHA256  string       `json:"recording_sha256"`
	Publisher        string       `json:"publisher"`
	URL              string       `json:"url"`
	AccessRef        string       `json:"access_ref"`
	ExtractionMethod string       `json:"extraction_method"`
	Kind             string       `json:"kind"`
	Supersedes       *RevisionRef `json:"supersedes,omitempty"`
	Claims           []BoundClaim `json:"claims"`
}

func SHA256(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }
func text(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}
func validTime(t time.Time) bool { return !t.IsZero() && t.UTC().Year() > 0 && t.UTC().Year() <= 9999 }

func (r Recording) Validate() error {
	if r.ExtractionMethod != "operator_recorded_v1" {
		return errors.New("unsupported news extraction method")
	}
	if err := r.validateSource(); err != nil {
		return err
	}
	return r.validateClaims()
}

// Source validation is also used before any claims have been extracted. Keep
// source metadata independent of model artifacts and the operator-recorded API.
func (r Recording) validateSource() error {
	if r.Schema != RecordingSchema || !key.MatchString(r.SourceID) || !key.MatchString(r.ArticleID) || r.Revision < 1 || r.Revision > 1000000 || !text(r.Publisher, 256) || !text(r.AccessRef, 256) || !text(r.RawRef, 256) {
		return errors.New("incomplete news source identity or recording contract")
	}
	if !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, r.EvidenceClass) {
		return errors.New("news evidence must remain explicitly unqualified")
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(r.URL) > 2048 || !text(r.URL, 2048) {
		return errors.New("news source URL must be bounded HTTPS without credentials or fragment")
	}
	if !validTime(r.PublishedAt) || !validTime(r.AvailableAt) || !validTime(r.ReceivedAt) || r.AvailableAt.Before(r.PublishedAt) || r.ReceivedAt.Before(r.AvailableAt) {
		return errors.New("news requires causal publication, availability and receipt times")
	}
	if len(r.Original) == 0 || len(r.Original) > 128<<10 || !utf8.ValidString(r.Original) || !digest.MatchString(r.OriginalSHA256) || SHA256(r.Original) != r.OriginalSHA256 {
		return errors.New("news source bytes are missing, oversized, invalid UTF-8 or do not match the digest")
	}
	switch r.Kind {
	case "initial":
		if r.Revision != 1 || r.Supersedes != nil {
			return errors.New("initial news recording requires revision one and no predecessor")
		}
	case "correction", "retraction":
		p := r.Supersedes
		if r.Revision < 2 || p == nil || p.SourceID != r.SourceID || p.ArticleID != r.ArticleID || p.Revision != r.Revision-1 || !digest.MatchString(p.RecordingSHA256) {
			return errors.New("news revision requires its immediately preceding source/article recording identity")
		}
	default:
		return errors.New("unsupported news recording kind")
	}
	return nil
}

func (r Recording) validateClaims() error {
	if len(r.Claims) < 1 || len(r.Claims) > 16 {
		return errors.New("news recording requires 1..16 structured claims")
	}
	seen := map[string]bool{}
	for _, c := range r.Claims {
		if !key.MatchString(c.ID) || seen[c.ID] || !key.MatchString(c.EventKey) || !key.MatchString(c.Code) || !text(c.Assertion, 512) || !slices.Contains([]string{"reported_unverified", "disputed", "retracted"}, c.Assessment) {
			return errors.New("news claim requires unique identity, reviewed event key, assertion and explicit assessment")
		}
		seen[c.ID] = true
		if (r.Kind == "retraction") != (c.Assessment == "retracted") {
			return errors.New("retracted claims require a retraction recording")
		}
		if len(c.Entities) < 1 || len(c.Entities) > 16 || len(c.Symbols) > 16 || len(c.Evidence) < 1 || len(c.Evidence) > 4 {
			return errors.New("news claim entity, symbol or excerpt count is outside bounds")
		}
		entities := map[string]bool{}
		symbols := map[string]bool{}
		for _, e := range c.Entities {
			if !key.MatchString(e.ID) || !text(e.Name, 128) || entities[e.ID] {
				return errors.New("news claim entities must have unique attributed identities")
			}
			entities[e.ID] = true
		}
		for _, s := range c.Symbols {
			if !symbol.MatchString(s) || symbols[s] {
				return errors.New("news claim symbols must be unique explicit instruments")
			}
			symbols[s] = true
		}
		spans := map[[2]int]bool{}
		for _, s := range c.Evidence {
			if s.Start < 0 || s.End <= s.Start || s.End > len(r.Original) || s.End-s.Start > 2048 || !utf8.ValidString(r.Original[s.Start:s.End]) || s.Quote != r.Original[s.Start:s.End] || spans[[2]int{s.Start, s.End}] {
				return errors.New("news claim excerpt must match a distinct bounded UTF-8 source span exactly")
			}
			spans[[2]int{s.Start, s.End}] = true
		}
	}
	return nil
}

func Normalize(r Recording) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	r.PublishedAt, r.AvailableAt, r.ReceivedAt = r.PublishedAt.UTC(), r.AvailableAt.UTC(), r.ReceivedAt.UTC()
	// Round-trip copies caller-owned slices/pointers and defines the canonical
	// record identity without altering original source bytes or declaration order.
	raw, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	var copy Recording
	if err := json.Unmarshal(raw, &copy); err != nil {
		return Result{}, err
	}
	r = copy
	out := Result{Schema: ResultSchema, RecordingSHA256: SHA256(string(raw)), Publisher: r.Publisher, URL: r.URL, AccessRef: r.AccessRef, ExtractionMethod: r.ExtractionMethod, Kind: r.Kind, Supersedes: r.Supersedes, Claims: []BoundClaim{}}
	for _, c := range r.Claims {
		identity, _ := json.Marshal([]any{r.SourceID, r.ArticleID, r.Revision, c.ID, r.EvidenceClass})
		out.Claims = append(out.Claims, BoundClaim{Claim: c, RecordingSHA256: out.RecordingSHA256, Key: "news-" + SHA256(string(identity)), SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: r.Revision, PublishedAt: r.PublishedAt, AvailableAt: r.AvailableAt, ReceivedAt: r.ReceivedAt, EvidenceClass: r.EvidenceClass, RawRef: r.RawRef, OriginalSHA256: r.OriginalSHA256, Independence: "not_assessed"})
	}
	return out, nil
}

func Read(reader io.Reader) (Recording, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxRecordingBytes+1))
	if err != nil {
		return Recording{}, errors.New("cannot read news recording")
	}
	if len(raw) > MaxRecordingBytes {
		return Recording{}, errors.New("news recording exceeds 2 MiB input bound")
	}
	if !utf8.Valid(raw) {
		return Recording{}, errors.New("news recording JSON must be UTF-8")
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSON(tokens, 0); err != nil {
		return Recording{}, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var r Recording
	if err := d.Decode(&r); err != nil {
		return r, errors.New("invalid news recording JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return r, errors.New("expected one news recording")
	}
	return r, r.Validate()
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("news JSON nesting exceeds bound")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("invalid news recording JSON")
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return errors.New("invalid news JSON object")
				}
				name, ok := k.(string)
				if !ok || !jsonField.MatchString(name) || seen[name] {
					return errors.New("duplicate or noncanonical news JSON field")
				}
				seen[name] = true
				if err := uniqueJSON(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := uniqueJSON(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid news JSON delimiter")
		}
		if _, err := d.Token(); err != nil {
			return errors.New("invalid news recording JSON")
		}
	}
	return nil
}
