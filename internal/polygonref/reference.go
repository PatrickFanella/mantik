// Package polygonref acquires and parses Polygon reference corporate actions.
// A parsed snapshot is provider-reported evidence only: it does not establish
// coverage, reconcile another source or apply any economic effect.
package polygonref

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Origin = "https://api.polygon.io"
const MethodDividends = "polygon-reference-dividends-v3"
const MethodSplits = "polygon-reference-splits-v3"
const MaxPageBytes = 4 << 20
const MaxPages = 32
const MaxRangeDays = 366

var ErrUnparseable = errors.New("polygon reference snapshot is unparseable")
var collectionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var symbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)
var dateOnly = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
var positiveDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var recordID = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)

// Spec selects a method, one instrument and an inclusive date range. It cannot
// choose a URL, supply a response or assert coverage.
type Spec struct {
	ID        string `json:"id"`
	Method    string `json:"method"`
	Symbol    string `json:"symbol"`
	Start     string `json:"start"`
	End       string `json:"end"`
	AccessRef string `json:"access_ref"`
}

func parseDate(s string) (time.Time, bool) {
	if !dateOnly.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

func (s Spec) Validate() error {
	start, ok1 := parseDate(s.Start)
	end, ok2 := parseDate(s.End)
	if !collectionID.MatchString(s.ID) || (s.Method != MethodDividends && s.Method != MethodSplits) || !symbol.MatchString(s.Symbol) || !ok1 || !ok2 || end.Before(start) || end.Sub(start) > MaxRangeDays*24*time.Hour || len(s.AccessRef) == 0 || len(s.AccessRef) > 256 || !utf8.ValidString(s.AccessRef) || strings.ContainsFunc(s.AccessRef, unicode.IsControl) {
		return errors.New("invalid polygon reference spec")
	}
	return nil
}

// FirstURL is the keyless first request. The key is added only when sending.
func (s Spec) FirstURL() string {
	path, field := "/v3/reference/dividends", "ex_dividend_date"
	if s.Method == MethodSplits {
		path, field = "/v3/reference/splits", "execution_date"
	}
	q := url.Values{"ticker": {s.Symbol}, field + ".gte": {s.Start}, field + ".lte": {s.End}, "order": {"asc"}, "sort": {field}, "limit": {"1000"}}
	return Origin + path + "?" + q.Encode()
}

// NextURL accepts only a cursor continuation on the same path. Any other host,
// scheme, path or parameter, including a returned credential, is rejected.
func NextURL(first, next string) (string, error) {
	f, err1 := url.Parse(first)
	n, err2 := url.Parse(next)
	if err1 != nil || err2 != nil || n.Scheme != "https" || n.Host != "api.polygon.io" || n.Path != f.Path || n.User != nil || n.Fragment != "" {
		return "", errors.New("polygon continuation leaves its query")
	}
	q := n.Query()
	cursor := q.Get("cursor")
	if len(q) != 1 || len(q["cursor"]) != 1 || cursor == "" || len(cursor) > 4096 {
		return "", errors.New("polygon continuation must carry only a cursor")
	}
	return Origin + n.Path + "?" + url.Values{"cursor": {cursor}}.Encode(), nil
}

type pageEnvelope struct {
	Status    string            `json:"status"`
	RequestID string            `json:"request_id"`
	Results   []json.RawMessage `json:"results"`
	NextURL   string            `json:"next_url"`
}

func decodePage(body []byte) (pageEnvelope, error) {
	var p pageEnvelope
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&p); err != nil || d.More() {
		return p, fmt.Errorf("%w: invalid page JSON", ErrUnparseable)
	}
	if p.Status != "OK" || p.Results == nil {
		return p, fmt.Errorf("%w: page status %q", ErrUnparseable, p.Status)
	}
	return p, nil
}

// Exact numeric text from the response; never converted through float64.
func exactNumber(n json.Number) (string, bool) {
	s := n.String()
	return s, positiveDecimal.MatchString(s) && strings.Trim(s, "0.") != ""
}

type Dividend struct {
	ID              string `json:"id"`
	ExDividendDate  string `json:"ex_dividend_date"`
	CashAmount      string `json:"cash_amount"`
	Currency        string `json:"currency"`
	DividendType    string `json:"dividend_type"`
	DeclarationDate string `json:"declaration_date,omitempty"`
	RecordDate      string `json:"record_date,omitempty"`
	PayDate         string `json:"pay_date,omitempty"`
}
type Split struct {
	ID            string `json:"id"`
	ExecutionDate string `json:"execution_date"`
	SplitFrom     string `json:"split_from"`
	SplitTo       string `json:"split_to"`
}

type Snapshot struct {
	Method                   string     `json:"method"`
	Symbol                   string     `json:"symbol"`
	Start                    string     `json:"start"`
	End                      string     `json:"end"`
	Pages                    int        `json:"pages"`
	PageSHA256               []string   `json:"page_sha256"`
	Dividends                []Dividend `json:"dividends,omitempty"`
	Splits                   []Split    `json:"splits,omitempty"`
	Authority                string     `json:"authority"`
	ProviderAttested         bool       `json:"provider_attested"`
	CoverageComplete         bool       `json:"coverage_complete"`
	EconomicApplicationReady bool       `json:"economic_application_ready"`
	SHA256                   string     `json:"sha256"`
}

func unparseable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnparseable, fmt.Sprintf(format, args...))
}

func optionalDate(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", unparseable("%s is not a string", field)
	}
	if _, ok := parseDate(s); !ok {
		return "", unparseable("%s is not a date", field)
	}
	return s, nil
}

func fields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, unparseable("result is not an object")
	}
	return m, nil
}

func stringField(m map[string]json.RawMessage, name string) (string, error) {
	var s string
	if json.Unmarshal(m[name], &s) != nil || s == "" {
		return "", unparseable("missing %s", name)
	}
	return s, nil
}

// A quoted numeric string also decodes into json.Number; require a number token.
func numberField(m map[string]json.RawMessage, name string) (string, error) {
	raw := bytes.TrimSpace(m[name])
	if len(raw) == 0 || raw[0] == '"' {
		return "", unparseable("%s is not a JSON number", name)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var n json.Number
	if d.Decode(&n) != nil {
		return "", unparseable("%s is not a number", name)
	}
	s, ok := exactNumber(n)
	if !ok {
		return "", unparseable("%s is not a positive plain decimal", name)
	}
	return s, nil
}

// Parse rederives a snapshot from exact retained page bodies. Every page must
// be complete JSON with status OK. A record for another ticker, outside the
// requested range, or with a duplicate ID makes the whole snapshot unparseable.
func Parse(s Spec, bodies [][]byte) (Snapshot, error) {
	if s.Validate() != nil || len(bodies) == 0 || len(bodies) > MaxPages {
		return Snapshot{}, unparseable("spec or page count outside bounds")
	}
	out := Snapshot{Method: s.Method, Symbol: s.Symbol, Start: s.Start, End: s.End, Pages: len(bodies), PageSHA256: []string{}, Authority: "provider_reported_reference_data_only"}
	seen := map[string]bool{}
	for i, body := range bodies {
		out.PageSHA256 = append(out.PageSHA256, workerplan.Digest(body))
		page, err := decodePage(body)
		if err != nil {
			return Snapshot{}, err
		}
		if (page.NextURL == "") != (i == len(bodies)-1) {
			return Snapshot{}, unparseable("page %d continuation does not match retained page count", i+1)
		}
		for _, raw := range page.Results {
			m, err := fields(raw)
			if err != nil {
				return Snapshot{}, err
			}
			id, err := stringField(m, "id")
			if err != nil || !recordID.MatchString(id) || seen[id] {
				return Snapshot{}, unparseable("missing, invalid or duplicate record id")
			}
			seen[id] = true
			ticker, err := stringField(m, "ticker")
			if err != nil || ticker != s.Symbol {
				return Snapshot{}, unparseable("record %s is for another ticker", id)
			}
			dateField := "ex_dividend_date"
			if s.Method == MethodSplits {
				dateField = "execution_date"
			}
			date, err := stringField(m, dateField)
			if _, ok := parseDate(date); err != nil || !ok || date < s.Start || date > s.End {
				return Snapshot{}, unparseable("record %s %s is missing or outside the requested range", id, dateField)
			}
			if s.Method == MethodSplits {
				from, err := numberField(m, "split_from")
				if err != nil {
					return Snapshot{}, err
				}
				to, err := numberField(m, "split_to")
				if err != nil {
					return Snapshot{}, err
				}
				out.Splits = append(out.Splits, Split{ID: id, ExecutionDate: date, SplitFrom: from, SplitTo: to})
				continue
			}
			d := Dividend{ID: id, ExDividendDate: date}
			if d.CashAmount, err = numberField(m, "cash_amount"); err != nil {
				return Snapshot{}, err
			}
			if d.Currency, err = stringField(m, "currency"); err != nil {
				return Snapshot{}, err
			}
			if d.DividendType, err = stringField(m, "dividend_type"); err != nil {
				return Snapshot{}, err
			}
			for name, target := range map[string]*string{"declaration_date": &d.DeclarationDate, "record_date": &d.RecordDate, "pay_date": &d.PayDate} {
				if *target, err = optionalDate(m[name], name); err != nil {
					return Snapshot{}, err
				}
			}
			out.Dividends = append(out.Dividends, d)
		}
	}
	slices.SortFunc(out.Dividends, func(a, b Dividend) int {
		return strings.Compare(a.ExDividendDate+a.ID, b.ExDividendDate+b.ID)
	})
	slices.SortFunc(out.Splits, func(a, b Split) int {
		return strings.Compare(a.ExecutionDate+a.ID, b.ExecutionDate+b.ID)
	})
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
