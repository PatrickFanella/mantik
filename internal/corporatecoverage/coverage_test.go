package corporatecoverage

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
)

var acquired = time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC)

func alpacaDividend(id, ex, rate, pay string) alpacarecord.CorporateActionRecord {
	raw := fmt.Sprintf(`{"cusip":"78462F103","ex_date":%q,"foreign":false,"id":%q,"payable_date":%q,"process_date":%q,"rate":%s,"record_date":%q,"special":false,"symbol":"SPY"}`, ex, id, pay, pay, rate, ex)
	return alpacarecord.CorporateActionRecord{Kind: "cash_dividends", SourceID: id, OriginalJSON: raw, SHA256: alpacarecord.Digest(raw)}
}

func record(kind, id, raw string) alpacarecord.CorporateActionRecord {
	return alpacarecord.CorporateActionRecord{Kind: kind, SourceID: id, OriginalJSON: raw, SHA256: alpacarecord.Digest(raw)}
}

// Window Q2 2026, ex-dates April through June, processed by mid-August.
func baseline() Input {
	w := Window{Symbol: "SPY", Start: "2026-04-01", End: "2026-06-30", ProcessLagDays: 45}
	tiles := [][2]string{{"2026-04-01", "2026-04-30"}, {"2026-05-01", "2026-05-31"}, {"2026-06-01", "2026-06-30"}, {"2026-07-01", "2026-07-31"}, {"2026-08-01", "2026-08-14"}}
	chains := []AlpacaChain{}
	for i, t := range tiles {
		c := AlpacaChain{CollectionID: fmt.Sprintf("alpaca-%d", i), Symbols: []string{"SPY"}, Start: t[0], End: t[1], RequestedAt: acquired, Terminated: true, Records: []alpacarecord.CorporateActionRecord{}}
		if i == 3 {
			// Processed (paid) in July although the ex-date is in June; the
			// March dividend paid in April is outside the ex-date window.
			c.Records = append(c.Records, alpacaDividend("a-june", "2026-06-18", "1.903516", "2026-07-31"))
		}
		if i == 0 {
			c.Records = append(c.Records, alpacaDividend("a-march", "2026-03-20", "1.796999", "2026-04-30"))
		}
		chains = append(chains, c)
	}
	dividends := &polygonref.Snapshot{Method: polygonref.MethodDividends, Symbol: "SPY", Start: "2026-01-01", End: "2026-10-30", Dividends: []polygonref.Dividend{
		{ID: "Ea1", ExDividendDate: "2026-03-20", CashAmount: "1.796999", Currency: "USD", DividendType: "CD", PayDate: "2026-04-30"},
		{ID: "Eb2", ExDividendDate: "2026-06-18", CashAmount: "1.903516", Currency: "USD", DividendType: "CD", PayDate: "2026-07-31"},
	}}
	splits := &polygonref.Snapshot{Method: polygonref.MethodSplits, Symbol: "SPY", Start: "2026-01-01", End: "2026-10-30"}
	doc := &issuerdoc.Document{Symbol: "SPY", CUSIP: "78462F103", LatestExDate: "2026-09-18", Distributions: []issuerdoc.Distribution{
		{Status: "consistent_dates", ExDate: "2026-03-20", Dividend: "1.796999"},
		{Status: "consistent_dates", ExDate: "2026-06-18", Dividend: "1.903516"},
		{Status: "consistent_dates", ExDate: "2026-09-18", Dividend: "1.888834"},
	}}
	return Input{Window: w, Alpaca: chains, PolygonDividends: &PolygonReference{SourceID: "polygon-dividends", RequestedAt: acquired, Snapshot: dividends}, PolygonSplits: &PolygonReference{SourceID: "polygon-splits", RequestedAt: acquired, Snapshot: splits}, IssuerRequired: true, IssuerCUSIP: "78462F103", Issuer: &IssuerReference{SourceID: "issuer", RequestedAt: acquired, Document: doc}}
}

func TestReconcileCoversAgreeingSources(t *testing.T) {
	r := Reconcile(baseline())
	if !r.Covered() || len(r.Reasons) != 0 || len(r.Dividends) != 1 || r.Dividends[0] != (Dividend{ExDate: "2026-06-18", Amount: "1.903516", PayDate: "2026-07-31", AlpacaID: "a-june", PolygonID: "Eb2", IssuerConfirmed: true}) || len(r.Splits) != 0 || r.AlpacaProcessEnd != "2026-08-14" || r.EconomicApplicationReady || r.QualificationCredit || r.SHA256 == "" {
		t.Fatalf("%+v", r)
	}
	if again := Reconcile(baseline()); again.SHA256 != r.SHA256 {
		t.Fatal("reconciliation is not deterministic")
	}
}

func TestReconcileMatchesExactValuesNotText(t *testing.T) {
	in := baseline()
	in.Alpaca[3].Records = []alpacarecord.CorporateActionRecord{alpacaDividend("a-june", "2026-06-18", "1.9035160", "2026-07-31")}
	in.Alpaca[2].Records = []alpacarecord.CorporateActionRecord{record("forward_splits", "a-split", `{"ex_date":"2026-06-01","id":"a-split","new_rate":4,"old_rate":1,"process_date":"2026-06-01","symbol":"SPY"}`)}
	in.PolygonSplits.Snapshot.Splits = []polygonref.Split{{ID: "P1", ExecutionDate: "2026-06-01", SplitFrom: "1.0", SplitTo: "4"}}
	r := Reconcile(in)
	if !r.Covered() || len(r.Splits) != 1 || r.Splits[0].AlpacaID != "a-split" {
		t.Fatalf("%+v", r)
	}
}

func TestReconcileFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Input)
		reason string
	}{
		"polygon missing":    {func(in *Input) { in.PolygonDividends = nil }, "polygon_dividends_missing"},
		"polygon unparsed":   {func(in *Input) { in.PolygonSplits.Snapshot = nil }, "polygon_splits_unparseable"},
		"polygon too narrow": {func(in *Input) { in.PolygonDividends.Snapshot.End = "2026-06-29" }, "polygon_dividends_scope_mismatch"},
		"polygon early":      {func(in *Input) { in.PolygonSplits.RequestedAt = time.Date(2026, 6, 30, 23, 0, 0, 0, time.UTC) }, "polygon_splits_acquired_before_window_close"},
		"alpaca missing":     {func(in *Input) { in.Alpaca = nil }, "alpaca_source_missing"},
		"alpaca gap":         {func(in *Input) { in.Alpaca = slices.Delete(in.Alpaca, 1, 2) }, "alpaca_process_span_gap"},
		"alpaca short":       {func(in *Input) { in.Alpaca = in.Alpaca[:4] }, "alpaca_process_span_incomplete"},
		"alpaca open chain":  {func(in *Input) { in.Alpaca[4].Terminated = false }, "alpaca_chain_unterminated"},
		"alpaca early":       {func(in *Input) { in.Alpaca[0].RequestedAt = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC) }, "alpaca_acquired_before_window_close"},
		"alpaca symbols":     {func(in *Input) { in.Alpaca[0].Symbols = []string{"SPY", "QQQ"} }, "alpaca_query_scope_mismatch"},
		"amount differs": {func(in *Input) {
			in.Alpaca[3].Records = []alpacarecord.CorporateActionRecord{alpacaDividend("a-june", "2026-06-18", "1.903517", "2026-07-31")}
		}, "alpaca_dividend_unmatched"},
		"alpaca lacks dividend": {func(in *Input) { in.Alpaca[3].Records = nil }, "polygon_dividend_unmatched"},
		"ambiguous": {func(in *Input) {
			in.Alpaca[4].Records = []alpacarecord.CorporateActionRecord{alpacaDividend("a-june-2", "2026-06-18", "1.903516", "2026-08-01")}
		}, "dividend_match_ambiguous"},
		"conflicting duplicate": {func(in *Input) {
			in.Alpaca[4].Records = []alpacarecord.CorporateActionRecord{alpacaDividend("a-june", "2026-06-18", "1.903516", "2026-08-01")}
		}, "alpaca_conflicting_duplicate"},
		"unsupported kind": {func(in *Input) {
			in.Alpaca[1].Records = []alpacarecord.CorporateActionRecord{record("spin_offs", "a-spin", `{"id":"a-spin","source_symbol":"SPY"}`)}
		}, "alpaca_unsupported_action_kind"},
		"return of capital": {func(in *Input) {
			in.Alpaca[1].Records = []alpacarecord.CorporateActionRecord{record("cash_dividends", "a-roc", `{"ex_date":"2026-05-01","id":"a-roc","rate":0.1,"sub_type":"return_of_capital","symbol":"SPY"}`)}
		}, "alpaca_unsupported_cash_dividend"},
		"malformed rate": {func(in *Input) {
			in.Alpaca[1].Records = []alpacarecord.CorporateActionRecord{record("cash_dividends", "a-bad", `{"ex_date":"2026-05-01","id":"a-bad","rate":"0.1","symbol":"SPY"}`)}
		}, "alpaca_record_unparseable"},
		"split one side": {func(in *Input) {
			in.PolygonSplits.Snapshot.Splits = []polygonref.Split{{ID: "P1", ExecutionDate: "2026-06-01", SplitFrom: "1", SplitTo: "4"}}
		}, "polygon_split_unmatched"},
		"capital gain type": {func(in *Input) {
			in.PolygonDividends.Snapshot.Dividends = append(in.PolygonDividends.Snapshot.Dividends, polygonref.Dividend{ID: "Ec3", ExDividendDate: "2026-06-18", CashAmount: "0.1", Currency: "USD", DividendType: "LT"})
		}, "polygon_unsupported_dividend"},
		"pay beyond span": {func(in *Input) { in.PolygonDividends.Snapshot.Dividends[1].PayDate = "2026-08-15" }, "polygon_pay_date_beyond_alpaca_process_span"},
		"issuer missing":  {func(in *Input) { in.Issuer = nil }, "issuer_document_missing"},
		"issuer unparsed": {func(in *Input) { in.Issuer.Document = nil }, "issuer_document_unparseable"},
		"issuer stale": {func(in *Input) {
			in.Issuer.Document.Distributions = in.Issuer.Document.Distributions[:2]
			in.Issuer.Document.LatestExDate = "2026-06-18"
		}, "issuer_document_not_beyond_window"},
		"issuer inconsistent": {func(in *Input) { in.Issuer.Document.Distributions[1].Status = "inconsistent_dates" }, "issuer_row_inconsistent_dates"},
		"issuer differs":      {func(in *Input) { in.Issuer.Document.Distributions[1].Dividend = "1.903515" }, "issuer_dividend_unmatched"},
		"issuer capital gain": {func(in *Input) { in.Issuer.Document.Distributions[1].LongTermCapitalGain = "0.01" }, "issuer_unsupported_capital_gain"},
		"issuer other fund":   {func(in *Input) { in.IssuerCUSIP = "78462F104" }, "issuer_document_scope_mismatch"},
		"issuer unrequired":   {func(in *Input) { in.IssuerRequired = false }, "issuer_document_without_supported_method"},
		"window invalid":      {func(in *Input) { in.Window.ProcessLagDays = MaxProcessLagDays + 1 }, "window_invalid"},
	} {
		in := baseline()
		tc.mutate(&in)
		r := Reconcile(in)
		if r.Covered() || !slices.Contains(r.Reasons, tc.reason) {
			t.Errorf("%s: %s %v", name, r.Status, r.Reasons)
		}
	}
}

func TestEmptyWindowNeedsBothProvidersToReportNone(t *testing.T) {
	in := baseline()
	in.Window.Start, in.Window.End = "2026-07-01", "2026-07-31"
	in.Window.ProcessLagDays = 14
	in.Alpaca = []AlpacaChain{{CollectionID: "alpaca-july", Symbols: []string{"SPY"}, Start: "2026-07-01", End: "2026-07-31", RequestedAt: acquired, Terminated: true, Records: []alpacarecord.CorporateActionRecord{}}, {CollectionID: "alpaca-august", Symbols: []string{"SPY"}, Start: "2026-08-01", End: "2026-08-14", RequestedAt: acquired, Terminated: true}}
	r := Reconcile(in)
	if !r.Covered() || len(r.Dividends) != 0 {
		t.Fatalf("%+v", r)
	}
	in.PolygonDividends = nil
	if Reconcile(in).Covered() {
		t.Fatal("a single provider's silence covered a window")
	}
}
