// Package corporatecoverage reconciles one instrument's corporate actions for
// one ex-date window across Alpaca, Polygon and an optional issuer document,
// following ADR-0003. It derives a result from retained sources only; callers
// cannot supply rows, matches or coverage. A covered window is an evidence
// result, never an economic application or an admission.
package corporatecoverage

import (
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Method = "alpaca-polygon-issuer-window-reconciliation-v1"
const MaxWindowDays = 366
const MaxProcessLagDays = 120

var dateOnly = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)

// Window is an inclusive ex-date range for one instrument. ProcessLagDays
// bounds how far after the window end Alpaca may process an in-window
// action: Alpaca filters by process date, which for cash dividends is the
// payable date.
type Window struct {
	Symbol         string `json:"symbol"`
	Start          string `json:"start"`
	End            string `json:"end"`
	ProcessLagDays int    `json:"process_lag_days"`
}

func parseDate(s string) (time.Time, bool) {
	if !dateOnly.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

func (w Window) Validate() bool {
	a, ok1 := parseDate(w.Start)
	b, ok2 := parseDate(w.End)
	return symbolPattern.MatchString(w.Symbol) && ok1 && ok2 && !b.Before(a) && b.Sub(a) <= MaxWindowDays*24*time.Hour && w.ProcessLagDays >= 0 && w.ProcessLagDays <= MaxProcessLagDays
}

// closed is the first instant after the window's last ex-date (UTC day end).
func (w Window) closed() time.Time {
	end, _ := parseDate(w.End)
	return end.Add(24 * time.Hour)
}
func (w Window) processEnd() string {
	end, _ := parseDate(w.End)
	return end.AddDate(0, 0, w.ProcessLagDays).Format(time.DateOnly)
}

// AlpacaChain is one retained, owner-verified Alpaca query chain.
type AlpacaChain struct {
	CollectionID string                               `json:"collection_id"`
	Symbols      []string                             `json:"symbols"`
	Start        string                               `json:"start"`
	End          string                               `json:"end"`
	RequestedAt  time.Time                            `json:"requested_at"`
	Terminated   bool                                 `json:"terminated"`
	Records      []alpacarecord.CorporateActionRecord `json:"records"`
}

// Reference is one retained Polygon or issuer source. Snapshot or Document is
// nil when the retained bytes were unparseable.
type PolygonReference struct {
	SourceID    string               `json:"source_id"`
	RequestedAt time.Time            `json:"requested_at"`
	Snapshot    *polygonref.Snapshot `json:"snapshot"`
}
type IssuerReference struct {
	SourceID    string              `json:"source_id"`
	RequestedAt time.Time           `json:"requested_at"`
	Document    *issuerdoc.Document `json:"document"`
}

type Input struct {
	Window           Window
	Alpaca           []AlpacaChain
	PolygonDividends *PolygonReference
	PolygonSplits    *PolygonReference
	// IssuerRequired is set when a supported issuer method exists for the
	// instrument; a required document must then be present and agree.
	IssuerRequired bool
	IssuerCUSIP    string
	Issuer         *IssuerReference
}

type Dividend struct {
	ExDate          string `json:"ex_date"`
	Amount          string `json:"amount"`
	PayDate         string `json:"pay_date,omitempty"`
	AlpacaID        string `json:"alpaca_id"`
	PolygonID       string `json:"polygon_id"`
	IssuerConfirmed bool   `json:"issuer_confirmed"`
}
type Split struct {
	ExDate    string `json:"ex_date"`
	From      string `json:"from"`
	To        string `json:"to"`
	AlpacaID  string `json:"alpaca_id"`
	PolygonID string `json:"polygon_id"`
}

type Result struct {
	Schema                   string     `json:"schema"`
	Method                   string     `json:"method"`
	Window                   Window     `json:"window"`
	AlpacaProcessStart       string     `json:"alpaca_process_start"`
	AlpacaProcessEnd         string     `json:"alpaca_process_end"`
	AlpacaCollections        []string   `json:"alpaca_collections"`
	PolygonDividendsSource   string     `json:"polygon_dividends_source,omitempty"`
	PolygonSplitsSource      string     `json:"polygon_splits_source,omitempty"`
	IssuerRequired           bool       `json:"issuer_required"`
	IssuerSource             string     `json:"issuer_source,omitempty"`
	Status                   string     `json:"status"`
	Reasons                  []string   `json:"reasons"`
	Dividends                []Dividend `json:"dividends"`
	Splits                   []Split    `json:"splits"`
	Authority                string     `json:"authority"`
	EconomicApplicationReady bool       `json:"economic_application_ready"`
	AdmissionAvailable       bool       `json:"admission_available"`
	ExecutionAvailable       bool       `json:"execution_available"`
	QualificationCredit      bool       `json:"qualification_credit"`
	SHA256                   string     `json:"sha256"`
}

func (r Result) Covered() bool { return r.Status == "covered" }

type reasons map[string]bool

func (r reasons) add(code string) { r[code] = true }

// exact parses retained decimal text without floating point.
func exact(s string) (*big.Rat, bool) {
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return nil, false
	}
	v, ok := new(big.Rat).SetString(s)
	return v, ok && v.Sign() > 0
}

func sameAmount(a, b string) bool {
	x, ok1 := exact(a)
	y, ok2 := exact(b)
	return ok1 && ok2 && x.Cmp(y) == 0
}

// canonical renders an exact amount as a reduced fraction for matching keys.
func canonical(s string) string {
	v, ok := exact(s)
	if !ok {
		return ""
	}
	return v.RatString()
}

type side struct {
	id, exDate, amount, from, to, payDate string
}

// alpacaRecord decodes one Alpaca record. ok is false only when a supported
// kind is malformed; any other kind is reported as "unsupported".
func alpacaRecord(r alpacarecord.CorporateActionRecord, symbol string) (s side, kind string, ok bool) {
	var raw map[string]json.RawMessage
	d := json.NewDecoder(strings.NewReader(r.OriginalJSON))
	d.UseNumber()
	if d.Decode(&raw) != nil {
		return side{}, "", false
	}
	text := func(name string) string {
		var v string
		if json.Unmarshal(raw[name], &v) != nil {
			return ""
		}
		return v
	}
	number := func(name string) string {
		v := strings.TrimSpace(string(raw[name]))
		if v == "" || v[0] == '"' {
			return ""
		}
		var n json.Number
		if json.Unmarshal([]byte(v), &n) != nil {
			return ""
		}
		return n.String()
	}
	s = side{id: r.SourceID, exDate: text("ex_date"), payDate: text("payable_date")}
	if r.Kind != "cash_dividends" && r.Kind != "forward_splits" && r.Kind != "reverse_splits" {
		return s, "unsupported", true
	}
	if text("symbol") != symbol {
		return s, "other_symbol", true
	}
	if _, valid := parseDate(s.exDate); !valid {
		return s, r.Kind, false
	}
	switch r.Kind {
	case "cash_dividends":
		s.amount = number("rate")
		var special, foreign bool
		_ = json.Unmarshal(raw["special"], &special)
		_ = json.Unmarshal(raw["foreign"], &foreign)
		currency := text("currency")
		if _, has := raw["sub_type"]; has || foreign || currency != "" && currency != "USD" {
			return s, "unsupported_cash_dividend", true
		}
		if _, valid := exact(s.amount); !valid {
			return s, r.Kind, false
		}
		return s, r.Kind, true
	case "forward_splits", "reverse_splits":
		s.from, s.to = number("old_rate"), number("new_rate")
		if _, a := exact(s.from); !a {
			return s, r.Kind, false
		}
		if _, b := exact(s.to); !b {
			return s, r.Kind, false
		}
		return s, "splits", true
	}
	return s, r.Kind, false
}

func inWindow(w Window, date string) bool { return date >= w.Start && date <= w.End }

// Reconcile derives the window result. Every unmatched, ambiguous, extra or
// unsupported record, and every missing, late or incomplete source, is a
// reason; the window is covered only when no reason remains.
func Reconcile(in Input) Result {
	w := in.Window
	out := Result{Schema: "mantik-corporate-window-coverage-v1", Method: Method, Window: w, AlpacaCollections: []string{}, IssuerRequired: in.IssuerRequired, Reasons: []string{}, Dividends: []Dividend{}, Splits: []Split{}, Authority: "owner_derived_corporate_coverage_evidence_only"}
	why := reasons{}
	if !w.Validate() {
		why.add("window_invalid")
		return finish(out, why)
	}
	out.AlpacaProcessStart, out.AlpacaProcessEnd = w.Start, w.processEnd()
	closed := w.closed()

	alpacaDividends, alpacaSplits := alpacaActions(in.Alpaca, w, closed, why, &out)
	polygonDividends := polygonDividendSide(in.PolygonDividends, w, closed, why, &out)
	polygonSplits := polygonSplitSide(in.PolygonSplits, w, closed, why, &out)

	for _, p := range polygonDividends {
		if p.payDate != "" && p.payDate > out.AlpacaProcessEnd {
			why.add("polygon_pay_date_beyond_alpaca_process_span")
		}
	}
	out.Dividends = matchDividends(alpacaDividends, polygonDividends, why)
	out.Splits = matchSplits(alpacaSplits, polygonSplits, why)
	issuer(in, w, closed, why, &out)
	return finish(out, why)
}

func finish(out Result, why reasons) Result {
	for code := range why {
		out.Reasons = append(out.Reasons, code)
	}
	slices.Sort(out.Reasons)
	out.Status = "covered"
	if len(out.Reasons) > 0 {
		out.Status = "unreconciled"
	}
	out.SHA256 = workerplan.Hash(out)
	return out
}

// alpacaActions requires terminated single-symbol chains, acquired after the
// window closed, whose process-date ranges tile the whole span without a gap.
func alpacaActions(chains []AlpacaChain, w Window, closed time.Time, why reasons, out *Result) ([]side, []side) {
	if len(chains) == 0 {
		why.add("alpaca_source_missing")
		return nil, nil
	}
	sorted := slices.Clone(chains)
	slices.SortFunc(sorted, func(a, b AlpacaChain) int { return strings.Compare(a.Start+a.CollectionID, b.Start+b.CollectionID) })
	covered, _ := parseDate(w.Start)
	covered = covered.AddDate(0, 0, -1)
	records := map[string]alpacarecord.CorporateActionRecord{}
	for _, c := range sorted {
		out.AlpacaCollections = append(out.AlpacaCollections, c.CollectionID)
		start, ok1 := parseDate(c.Start)
		end, ok2 := parseDate(c.End)
		if !ok1 || !ok2 || !slices.Equal(c.Symbols, []string{w.Symbol}) {
			why.add("alpaca_query_scope_mismatch")
			continue
		}
		if !c.Terminated {
			why.add("alpaca_chain_unterminated")
		}
		if c.RequestedAt.Before(closed) {
			why.add("alpaca_acquired_before_window_close")
		}
		if start.After(covered.AddDate(0, 0, 1)) {
			why.add("alpaca_process_span_gap")
		}
		if end.After(covered) {
			covered = end
		}
		for _, r := range c.Records {
			if old, seen := records[r.SourceID]; seen && old.SHA256 != r.SHA256 {
				why.add("alpaca_conflicting_duplicate")
			}
			records[r.SourceID] = r
		}
	}
	if processEnd, _ := parseDate(w.processEnd()); covered.Before(processEnd) {
		why.add("alpaca_process_span_incomplete")
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var dividends, splits []side
	for _, id := range ids {
		s, kind, ok := alpacaRecord(records[id], w.Symbol)
		switch {
		case !ok:
			why.add("alpaca_record_unparseable")
		case kind == "unsupported":
			why.add("alpaca_unsupported_action_kind")
		case kind == "other_symbol":
			why.add("alpaca_record_other_symbol")
		case !inWindow(w, s.exDate):
		case kind == "unsupported_cash_dividend":
			why.add("alpaca_unsupported_cash_dividend")
		case kind == "cash_dividends":
			dividends = append(dividends, s)
		case kind == "splits":
			splits = append(splits, s)
		}
	}
	return dividends, splits
}

func polygonScope(ref *PolygonReference, method string, w Window, closed time.Time, prefix string, why reasons) *polygonref.Snapshot {
	if ref == nil {
		why.add(prefix + "_missing")
		return nil
	}
	if ref.Snapshot == nil {
		why.add(prefix + "_unparseable")
		return nil
	}
	s := ref.Snapshot
	if s.Method != method || s.Symbol != w.Symbol || s.Start > w.Start || s.End < w.End {
		why.add(prefix + "_scope_mismatch")
		return nil
	}
	if ref.RequestedAt.Before(closed) {
		why.add(prefix + "_acquired_before_window_close")
	}
	return s
}

func polygonDividendSide(ref *PolygonReference, w Window, closed time.Time, why reasons, out *Result) []side {
	s := polygonScope(ref, polygonref.MethodDividends, w, closed, "polygon_dividends", why)
	if s == nil {
		return nil
	}
	out.PolygonDividendsSource = ref.SourceID
	var sides []side
	for _, d := range s.Dividends {
		if !inWindow(w, d.ExDividendDate) {
			continue
		}
		if d.Currency != "USD" || d.DividendType != "CD" && d.DividendType != "SC" {
			why.add("polygon_unsupported_dividend")
			continue
		}
		sides = append(sides, side{id: d.ID, exDate: d.ExDividendDate, amount: d.CashAmount, payDate: d.PayDate})
	}
	return sides
}

func polygonSplitSide(ref *PolygonReference, w Window, closed time.Time, why reasons, out *Result) []side {
	s := polygonScope(ref, polygonref.MethodSplits, w, closed, "polygon_splits", why)
	if s == nil {
		return nil
	}
	out.PolygonSplitsSource = ref.SourceID
	var sides []side
	for _, p := range s.Splits {
		if inWindow(w, p.ExecutionDate) {
			sides = append(sides, side{id: p.ID, exDate: p.ExecutionDate, from: p.SplitFrom, to: p.SplitTo})
		}
	}
	return sides
}

// index groups sides by matching key; a key held more than once is ambiguous.
func index(sides []side, key func(side) string) map[string][]side {
	m := map[string][]side{}
	for _, s := range sides {
		m[key(s)] = append(m[key(s)], s)
	}
	return m
}

func keys(a, b map[string][]side) []string {
	all := map[string]bool{}
	for k := range a {
		all[k] = true
	}
	for k := range b {
		all[k] = true
	}
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func matchDividends(alpaca, polygon []side, why reasons) []Dividend {
	key := func(s side) string { return s.exDate + "|" + canonical(s.amount) }
	a, p := index(alpaca, key), index(polygon, key)
	out := []Dividend{}
	for _, k := range keys(a, p) {
		switch {
		case len(a[k]) > 1 || len(p[k]) > 1:
			why.add("dividend_match_ambiguous")
		case len(p[k]) == 0:
			why.add("alpaca_dividend_unmatched")
		case len(a[k]) == 0:
			why.add("polygon_dividend_unmatched")
		default:
			out = append(out, Dividend{ExDate: p[k][0].exDate, Amount: p[k][0].amount, PayDate: p[k][0].payDate, AlpacaID: a[k][0].id, PolygonID: p[k][0].id})
		}
	}
	return out
}

func matchSplits(alpaca, polygon []side, why reasons) []Split {
	key := func(s side) string { return s.exDate + "|" + canonical(s.from) + "|" + canonical(s.to) }
	a, p := index(alpaca, key), index(polygon, key)
	out := []Split{}
	for _, k := range keys(a, p) {
		switch {
		case len(a[k]) > 1 || len(p[k]) > 1:
			why.add("split_match_ambiguous")
		case len(p[k]) == 0:
			why.add("alpaca_split_unmatched")
		case len(a[k]) == 0:
			why.add("polygon_split_unmatched")
		default:
			out = append(out, Split{ExDate: p[k][0].exDate, From: p[k][0].from, To: p[k][0].to, AlpacaID: a[k][0].id, PolygonID: p[k][0].id})
		}
	}
	return out
}

func zeroOrEmpty(s string) bool {
	if s == "" {
		return true
	}
	v, ok := new(big.Rat).SetString(s)
	return ok && v.Sign() == 0
}

// issuer requires a document listing an ex-date beyond the window (the
// workbook states no as-of date) whose in-window rows match the reconciled
// dividends one-to-one. It never contributes split coverage.
func issuer(in Input, w Window, closed time.Time, why reasons, out *Result) {
	if !in.IssuerRequired {
		if in.Issuer != nil {
			why.add("issuer_document_without_supported_method")
		}
		return
	}
	if in.Issuer == nil {
		why.add("issuer_document_missing")
		return
	}
	out.IssuerSource = in.Issuer.SourceID
	doc := in.Issuer.Document
	if doc == nil {
		why.add("issuer_document_unparseable")
		return
	}
	if doc.Symbol != w.Symbol || doc.CUSIP != in.IssuerCUSIP {
		why.add("issuer_document_scope_mismatch")
		return
	}
	if in.Issuer.RequestedAt.Before(closed) {
		why.add("issuer_acquired_before_window_close")
	}
	if doc.LatestExDate <= w.End {
		why.add("issuer_document_not_beyond_window")
	}
	rows := []side{}
	for _, d := range doc.Distributions {
		if !inWindow(w, d.ExDate) {
			continue
		}
		if d.Status != "consistent_dates" {
			why.add("issuer_row_inconsistent_dates")
			continue
		}
		if !zeroOrEmpty(d.ShortTermCapitalGain) || !zeroOrEmpty(d.LongTermCapitalGain) {
			why.add("issuer_unsupported_capital_gain")
			continue
		}
		rows = append(rows, side{exDate: d.ExDate, amount: d.Dividend})
	}
	key := func(s side) string { return s.exDate + "|" + canonical(s.amount) }
	reconciled := map[string]int{}
	for i, d := range out.Dividends {
		reconciled[key(side{exDate: d.ExDate, amount: d.Amount})] = i + 1
	}
	listed := index(rows, key)
	for k, held := range listed {
		i := reconciled[k]
		switch {
		case len(held) > 1:
			why.add("issuer_row_ambiguous")
		case i == 0:
			why.add("issuer_dividend_unmatched")
		default:
			out.Dividends[i-1].IssuerConfirmed = true
		}
	}
	for _, d := range out.Dividends {
		if !d.IssuerConfirmed {
			why.add("reconciled_dividend_missing_from_issuer")
		}
	}
}
