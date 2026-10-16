// Package hypothesis defines pre-test research declarations. Validation does
// not run a method, verify a source, publish a hypothesis or grant qualification.
package hypothesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const Schema = "mantik-hypothesis-declaration-v1"
const ResultSchema = "mantik-hypothesis-declaration-result-v1"
const MethodSchema = library.NativeMethodSchema
const MaxBytes = 64 << 10

var ErrInvalid = errors.New("invalid or unsupported hypothesis declaration")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var claimKey = regexp.MustCompile(`^news-[a-f0-9]{64}$`)
var field = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Mechanism struct {
	Code      string `json:"code"`
	Statement string `json:"statement"`
}
type Condition struct {
	Code       string `json:"code"`
	Metric     string `json:"metric"`
	Comparison string `json:"comparison"`
	Threshold  int64  `json:"threshold"`
	Unit       string `json:"unit"`
}
type MethodRef = library.MethodRef
type Origin struct {
	Reference       string `json:"reference"`
	ClaimKey        string `json:"claim_key"`
	RecordingSHA256 string `json:"recording_sha256"`
}
type Declaration struct {
	Schema         string      `json:"schema"`
	Label          string      `json:"label"`
	EvidenceClass  string      `json:"evidence_class"`
	Instrument     string      `json:"instrument"`
	Mechanism      Mechanism   `json:"mechanism"`
	Prediction     Condition   `json:"prediction"`
	Null           Condition   `json:"null"`
	Refutation     Condition   `json:"refutation"`
	HorizonSeconds int64       `json:"horizon_seconds"`
	Methods        []MethodRef `json:"methods"`
	Origins        []Origin    `json:"origins"`
}
type NativeMethod = library.NativeMethod
type Result struct {
	Schema              string         `json:"schema"`
	ID                  string         `json:"id"`
	DeclarationSHA256   string         `json:"declaration_sha256"`
	QuestionSHA256      string         `json:"question_sha256"`
	Status              string         `json:"status"`
	ValidationScope     string         `json:"validation_scope"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	OriginsVerified     bool           `json:"origins_verified"`
	Declaration         Declaration    `json:"declaration"`
	Methods             []NativeMethod `json:"methods"`
}

func PinMethod(id string) (MethodRef, NativeMethod, error) {
	ref, method, err := library.PinMethod(id)
	if err != nil {
		return MethodRef{}, NativeMethod{}, ErrInvalid
	}
	return ref, method, nil
}
func text(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

type interval struct{ low, high int64 }

func intervals(c Condition) ([]interval, bool) {
	if !identifier.MatchString(c.Code) || c.Threshold < -1000000000000 || c.Threshold > 1000000000000 {
		return nil, false
	}
	minimum := int64(math.MinInt64)
	switch c.Metric {
	case "realized_pnl_cents", "marked_pnl_cents":
		if c.Unit != "cents" {
			return nil, false
		}
	case "observations", "actions":
		if c.Unit != "count" || c.Threshold < 0 {
			return nil, false
		}
		minimum = 0
	default:
		return nil, false
	}
	x := c.Threshold
	var spans []interval
	switch c.Comparison {
	case "lt":
		spans = []interval{{minimum, x - 1}}
	case "le":
		spans = []interval{{minimum, x}}
	case "eq":
		spans = []interval{{x, x}}
	case "ne":
		spans = []interval{{minimum, x - 1}, {x + 1, math.MaxInt64}}
	case "ge":
		spans = []interval{{x, math.MaxInt64}}
	case "gt":
		spans = []interval{{x + 1, math.MaxInt64}}
	default:
		return nil, false
	}
	nonempty := []interval{}
	for _, span := range spans {
		if span.low <= span.high {
			nonempty = append(nonempty, span)
		}
	}
	return nonempty, len(nonempty) > 0
}
func overlap(a, b []interval) bool {
	for _, x := range a {
		for _, y := range b {
			if x.low <= y.high && y.low <= x.high {
				return true
			}
		}
	}
	return false
}
func Seal(d Declaration) (Result, error) {
	if d.Schema != Schema || !identifier.MatchString(d.Label) || d.EvidenceClass != "synthetic_unqualified" || d.Instrument != "DEMO" || !identifier.MatchString(d.Mechanism.Code) || !text(d.Mechanism.Statement, 1024) || d.HorizonSeconds < 1 || d.HorizonSeconds > 86400 || len(d.Methods) < 1 || len(d.Methods) > 2 || len(d.Origins) < 1 || len(d.Origins) > 8 {
		return Result{}, ErrInvalid
	}
	prediction, ok := intervals(d.Prediction)
	if !ok {
		return Result{}, ErrInvalid
	}
	null, nullOK := intervals(d.Null)
	refutation, refuteOK := intervals(d.Refutation)
	if !nullOK || !refuteOK || d.Null.Metric != d.Prediction.Metric || d.Refutation.Metric != d.Prediction.Metric || d.Null.Unit != d.Prediction.Unit || d.Refutation.Unit != d.Prediction.Unit || d.Prediction.Code == d.Null.Code || d.Prediction.Code == d.Refutation.Code || d.Null.Code == d.Refutation.Code || overlap(prediction, null) || overlap(prediction, refutation) {
		return Result{}, ErrInvalid
	}
	// Detach all caller-owned declarations before sorting canonical set fields.
	raw, _ := json.Marshal(d)
	var copy Declaration
	if err := json.Unmarshal(raw, &copy); err != nil {
		return Result{}, ErrInvalid
	}
	d = copy
	slices.SortFunc(d.Methods, func(a, b MethodRef) int { return strings.Compare(a.ID, b.ID) })
	methods := []NativeMethod{}
	seen := map[string]bool{}
	for _, ref := range d.Methods {
		expected, method, err := PinMethod(ref.ID)
		if err != nil || ref != expected || seen[ref.ID] || method.Strategy.Symbol != d.Instrument {
			return Result{}, ErrInvalid
		}
		seen[ref.ID] = true
		methods = append(methods, method)
	}
	slices.SortFunc(d.Origins, func(a, b Origin) int {
		if c := strings.Compare(a.Reference, b.Reference); c != 0 {
			return c
		}
		return strings.Compare(a.ClaimKey, b.ClaimKey)
	})
	seen = map[string]bool{}
	for _, origin := range d.Origins {
		if !text(origin.Reference, 256) || !claimKey.MatchString(origin.ClaimKey) || !digest.MatchString(origin.RecordingSHA256) || seen[origin.ClaimKey] {
			return Result{}, ErrInvalid
		}
		seen[origin.ClaimKey] = true
	}
	raw, _ = json.Marshal(d)
	declarationHash := news.SHA256(string(raw))
	// Labels and supporting origins vary between declarations of the same exact
	// research question. This is a matching key, not an automatic merge decision.
	question := d
	question.Label = ""
	question.Origins = nil
	raw, _ = json.Marshal(question)
	questionHash := news.SHA256(string(raw))
	return Result{Schema: ResultSchema, ID: "hypothesis-" + questionHash, DeclarationSHA256: declarationHash, QuestionSHA256: questionHash, Status: "declared", ValidationScope: "declaration_structure_and_native_method_pins", Authority: "research_declaration_only", Declaration: d, Methods: methods}, nil
}
func Fixture() Declaration {
	pin, _, _ := PinMethod(library.FixtureStrategy().ID)
	source, _ := news.Normalize(news.Fixture())
	claim := source.Claims[0]
	return Declaration{Schema: Schema, Label: "synthetic-threshold-question", EvidenceClass: "synthetic_unqualified", Instrument: "DEMO", Mechanism: Mechanism{Code: "synthetic_threshold_response", Statement: "The synthetic entry/exit threshold may produce a positive marked result within the declared fixture horizon."}, Prediction: Condition{Code: "positive_mark", Metric: "marked_pnl_cents", Comparison: "gt", Threshold: 0, Unit: "cents"}, Null: Condition{Code: "nonpositive_mark", Metric: "marked_pnl_cents", Comparison: "le", Threshold: 0, Unit: "cents"}, Refutation: Condition{Code: "negative_mark", Metric: "marked_pnl_cents", Comparison: "lt", Threshold: 0, Unit: "cents"}, HorizonSeconds: 3600, Methods: []MethodRef{pin}, Origins: []Origin{{Reference: claim.RawRef, ClaimKey: claim.Key, RecordingSHA256: source.RecordingSHA256}}}
}
func Read(reader io.Reader) (Declaration, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes || !utf8.Valid(raw) {
		return Declaration{}, ErrInvalid
	}
	if err := unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return Declaration{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var d Declaration
	if err = decoder.Decode(&d); err != nil {
		return Declaration{}, ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Declaration{}, ErrInvalid
	}
	if _, err = Seal(d); err != nil {
		return Declaration{}, err
	}
	return d, nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := k.(string)
				if !ok || !field.MatchString(name) || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
		if _, err = d.Token(); err != nil {
			return ErrInvalid
		}
	}
	return nil
}
