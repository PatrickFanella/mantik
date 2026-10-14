// Package alpaca normalizes bounded, attributed market recordings for research.
// It supplies no execution authority and never installs calendar obligations.
package alpaca

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

const RecordingSchema = "mantik-alpaca-recording-v1"
const MaxBytes = 1 << 20

var capturePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)
var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var pricePattern = regexp.MustCompile(`^[0-9]{1,7}(\.[0-9]{1,9})?$`)

type Recording struct {
	Currency           string    `json:"currency"`
	Schema             string    `json:"schema"`
	CaptureID          string    `json:"capture_id"`
	Capability         string    `json:"capability"`
	Feed               string    `json:"feed"`
	Symbols            []string  `json:"symbols"`
	Start              string    `json:"start,omitempty"`
	End                string    `json:"end,omitempty"`
	Revision           int       `json:"revision"`
	ReceivedAt         time.Time `json:"received_at"`
	EvidenceClass      string    `json:"evidence_class"`
	AccessRef          string    `json:"access_ref"`
	RawRef             string    `json:"raw_ref"`
	Original           string    `json:"original"`
	SHA256             string    `json:"sha256"`
	MaxQuoteAgeSeconds int64     `json:"max_quote_age_seconds"`
}

type Coverage struct {
	Capability string `json:"capability"`
	Symbol     string `json:"symbol,omitempty"`
	Status     string `json:"status"`
	Code       string `json:"code"`
}

type Session struct {
	Date     string    `json:"date"`
	Revision int       `json:"revision"`
	InputID  string    `json:"input_id"`
	OpenAt   time.Time `json:"open_at"`
	CloseAt  time.Time `json:"close_at"`
}

type Result struct {
	Schema      string                `json:"schema"`
	Inputs      []domain.RoutingInput `json:"inputs"`
	Coverage    []Coverage            `json:"coverage"`
	Sessions    []Session             `json:"sessions"`
	Unsupported []string              `json:"unsupported"`
}

func Digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }

func Read(r io.Reader) (Recording, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 2*MaxBytes+1))
	if err != nil {
		return Recording{}, err
	}
	if len(raw) > 2*MaxBytes {
		return Recording{}, errors.New("recording exceeds 2 MiB")
	}
	var record Recording
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, errors.New("invalid recording JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return record, errors.New("expected one recording")
	}
	return record, nil
}

func validTime(at time.Time) bool { return !at.IsZero() && at.Year() > 0 && at.Year() <= 9999 }

func (r Recording) validate() error {
	if r.Currency != "USD" || r.Schema != RecordingSchema || !capturePattern.MatchString(r.CaptureID) || r.Revision < 1 || r.Revision > 1000000 || !validTime(r.ReceivedAt) || len(r.Original) == 0 || len(r.Original) > MaxBytes || r.SHA256 != Digest(r.Original) || !json.Valid([]byte(r.Original)) || len(r.Symbols) < 1 || len(r.Symbols) > 10 || r.MaxQuoteAgeSeconds < 1 || r.MaxQuoteAgeSeconds > 86400 || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, r.EvidenceClass) {
		return errors.New("incomplete recording or mismatched raw digest")
	}
	seen := map[string]bool{}
	for _, s := range r.Symbols {
		if !symbolPattern.MatchString(s) || seen[s] {
			return errors.New("invalid or duplicate recording symbol")
		}
		seen[s] = true
	}
	switch r.Capability {
	case "latest_quotes":
		if !slices.Contains([]string{"iex", "sip", "delayed_sip"}, r.Feed) || r.Start != "" || r.End != "" {
			return errors.New("quotes require an explicit supported feed and no calendar range")
		}
	case "market_calendar":
		if r.Feed != "regular_session" {
			return errors.New("calendar requires regular_session coverage")
		}
		if _, _, err := calendarRange(r.Start, r.End); err != nil {
			return err
		}
	default:
		return errors.New("unsupported Alpaca capability")
	}
	return nil
}

func calendarRange(start, end string) (time.Time, time.Time, error) {
	a, e1 := time.Parse("2006-01-02", start)
	b, e2 := time.Parse("2006-01-02", end)
	if e1 != nil || e2 != nil || !validTime(a) || !validTime(b) || b.Before(a) || b.Sub(a) > 30*24*time.Hour {
		return a, b, errors.New("calendar requires an inclusive range of at most 31 dates")
	}
	return a, b, nil
}

func (r Recording) input(symbol, key string, at time.Time) domain.RoutingInput {
	source := "alpaca-" + strings.ReplaceAll(r.Feed, "_", "-")
	i := domain.RoutingInput{Source: source, Symbol: symbol, EvidenceClass: r.EvidenceClass, OccurredAt: at.UTC(), ReceivedAt: r.ReceivedAt.UTC(), Provenance: &domain.InputProvenance{RecordKey: r.CaptureID + "-" + key, Revision: r.Revision, RawRef: r.RawRef, SHA256: r.SHA256, AvailableAt: r.ReceivedAt.UTC(), SourceSymbol: symbol, MappingVersion: "alpaca-us-equities-v1", AccessRef: r.AccessRef, Quality: "usable"}}
	i.ID = routing.MarketInputID(i)
	return i
}

// Normalize is deterministic for a retained capture. Repeated retrieval is a
// new capture, not a rewrite of receipt times or raw bytes in the original.
func Normalize(r Recording) (Result, error) {
	out := Result{Schema: "mantik-alpaca-normalized-v1", Inputs: []domain.RoutingInput{}, Coverage: []Coverage{}, Sessions: []Session{}, Unsupported: []string{"corporate_actions", "expiry", "settlement", "order_execution"}}
	if err := r.validate(); err != nil {
		return out, err
	}
	probe := r.input(r.Symbols[0], "metadata", r.ReceivedAt)
	probe.Kind, probe.StateKey, probe.StateValue = "state", "capture", "received"
	if err := routing.ValidateInput(probe); err != nil {
		return out, err
	}
	var err error
	if r.Capability == "latest_quotes" {
		err = quotes(r, &out)
	} else {
		err = calendar(r, &out)
	}
	if err != nil {
		return out, err
	}
	for _, i := range out.Inputs {
		if err := routing.ValidateInput(i); err != nil {
			return Result{}, fmt.Errorf("normalized attribution: %w", err)
		}
	}
	return out, nil
}

type quote struct {
	Timestamp time.Time   `json:"t"`
	Ask       json.Number `json:"ap"`
	Bid       json.Number `json:"bp"`
	AskSize   int64       `json:"as"`
	BidSize   int64       `json:"bs"`
}

func cents(n json.Number) (int64, bool) {
	if !pricePattern.MatchString(n.String()) {
		return 0, false
	}
	value, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return 0, false
	}
	value.Mul(value, big.NewRat(100, 1))
	if !value.IsInt() || !value.Num().IsInt64() {
		return 0, false
	}
	v := value.Num().Int64()
	return v, v >= 1 && v <= 100000000
}

func quotes(r Recording, out *Result) error {
	var response struct {
		Quotes map[string]json.RawMessage `json:"quotes"`
	}
	if err := json.Unmarshal([]byte(r.Original), &response); err != nil || response.Quotes == nil {
		return errors.New("invalid Alpaca quotes envelope")
	}
	for s := range response.Quotes {
		if !slices.Contains(r.Symbols, s) {
			return errors.New("quote response includes unrequested instrument")
		}
	}
	symbols := slices.Clone(r.Symbols)
	slices.Sort(symbols)
	for _, symbol := range symbols {
		raw, present := response.Quotes[symbol]
		i := r.input(symbol, symbol+"-quote", r.ReceivedAt)
		coverage := Coverage{Capability: r.Capability, Symbol: symbol, Status: "ready", Code: "whole_cent_ask_v1"}
		var q quote
		if !present || string(raw) == "null" {
			coverage.Status, coverage.Code = "missing", "symbol_missing"
		} else if json.Unmarshal(raw, &q) != nil || !validTime(q.Timestamp) || q.Timestamp.After(r.ReceivedAt) {
			coverage.Status, coverage.Code = "degraded", "invalid_quote"
		} else {
			ask, askOK := cents(q.Ask)
			bid, bidOK := cents(q.Bid)
			if !askOK || !bidOK {
				coverage.Status, coverage.Code = "degraded", "unsupported_price_precision_or_range"
			} else if bid > ask || q.AskSize <= 0 || q.BidSize <= 0 {
				coverage.Status, coverage.Code = "degraded", "unusable_quote"
			} else if r.ReceivedAt.Sub(q.Timestamp) > time.Duration(r.MaxQuoteAgeSeconds)*time.Second {
				coverage.Status, coverage.Code = "degraded", "stale_quote"
			} else {
				i.Kind, i.PriceCents, i.OccurredAt = "quote", ask, q.Timestamp.UTC()
			}
		}
		if coverage.Status != "ready" {
			i.Kind, i.StateKey, i.StateValue = "state", "price_coverage", coverage.Status
			i.Provenance.Quality = "quarantined"
			if coverage.Status == "missing" {
				i.Provenance.Quality = "missing"
			}
			i.Provenance.Reason = coverage.Code
		}
		out.Inputs = append(out.Inputs, i)
		out.Coverage = append(out.Coverage, coverage)
	}
	return nil
}

func calendar(r Recording, out *Result) error {
	var days []struct {
		Date  string `json:"date"`
		Open  string `json:"open"`
		Close string `json:"close"`
	}
	if err := json.Unmarshal([]byte(r.Original), &days); err != nil || days == nil || len(days) > 31 {
		return errors.New("invalid Alpaca calendar envelope")
	}
	start, end, _ := calendarRange(r.Start, r.End)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, day := range days {
		date, err := time.Parse("2006-01-02", day.Date)
		if err != nil || date.Before(start) || date.After(end) || seen[day.Date] {
			return errors.New("calendar contains invalid, duplicate or out-of-range date")
		}
		seen[day.Date] = true
		open, e1 := time.ParseInLocation("2006-01-02 15:04", day.Date+" "+day.Open, location)
		close, e2 := time.ParseInLocation("2006-01-02 15:04", day.Date+" "+day.Close, location)
		if e1 != nil || e2 != nil || !close.After(open) {
			return errors.New("invalid regular-session times")
		}
		for _, symbol := range r.Symbols {
			i := r.input(symbol, symbol+"-session-"+day.Date, r.ReceivedAt)
			i.Kind, i.EventName = "event", "market_session_schedule"
			out.Inputs = append(out.Inputs, i)
			out.Sessions = append(out.Sessions, Session{Date: day.Date, Revision: r.Revision, InputID: i.ID, OpenAt: open.UTC(), CloseAt: close.UTC()})
		}
	}
	slices.SortFunc(out.Sessions, func(a, b Session) int { return strings.Compare(a.Date+a.InputID, b.Date+b.InputID) })
	slices.SortFunc(out.Inputs, func(a, b domain.RoutingInput) int { return strings.Compare(a.ID, b.ID) })
	out.Coverage = append(out.Coverage, Coverage{Capability: r.Capability, Status: "ready", Code: "regular_sessions_only"})
	return nil
}
