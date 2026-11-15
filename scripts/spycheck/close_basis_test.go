package main

import (
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func closeBasisInput() comparisonInput {
	closeAt := time.Date(2020, 3, 12, 20, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 11, 15, 19, 0, 0, 0, time.UTC)
	return comparisonInput{AssetID: "spy", AsOf: observed, Calendar: []string{"2020-03-12"}, Raw: []storage.DailyBar{{AssetID: "spy", Symbol: "SPY", Date: "2020-03-12", Open: "256", High: "266.66", Low: "247.68", Close: "255.24", VWAP: "254.82", AvailableAt: observed}}, SessionCloses: []sessionClose{{SourceBasis: "primary_exchange_close", SourceCondition: "6", Date: "2020-03-12", CloseAt: closeAt, OccurredAt: closeAt.Add(120 * time.Millisecond), ObservedAt: observed, Price: "248.11", TradeID: "123", SourceSHA256: strings.Repeat("a", 64)}}}
}
func TestSessionCloseProjectionPreservesOriginalObservation(t *testing.T) {
	in := closeBasisInput()
	got, err := sessionCloseSeries(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bars[0].Close != "248.11" || got.Bars[0].Raw.Close != "255.24" || in.Raw[0].Close != "255.24" {
		t.Fatal("projection rewrote original", got, in.Raw)
	}
}
func TestSessionCloseProjectionRequiresCompleteAvailableEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*comparisonInput)
	}{
		{"missing", func(i *comparisonInput) { i.SessionCloses = nil }},
		{"extra", func(i *comparisonInput) { i.SessionCloses = append(i.SessionCloses, i.SessionCloses[0]) }},
		{"different_date", func(i *comparisonInput) { i.SessionCloses[0].Date = "2020-03-13" }},
		{"future_raw", func(i *comparisonInput) { i.Raw[0].AvailableAt = i.AsOf.Add(time.Second) }},
		{"future_source", func(i *comparisonInput) { i.SessionCloses[0].ObservedAt = i.AsOf.Add(time.Second) }},
		{"before_close", func(i *comparisonInput) {
			i.SessionCloses[0].OccurredAt = i.SessionCloses[0].CloseAt.Add(-time.Nanosecond)
		}},
		{"late", func(i *comparisonInput) {
			i.SessionCloses[0].OccurredAt = i.SessionCloses[0].CloseAt.Add(time.Second + time.Nanosecond)
		}},
		{"digest", func(i *comparisonInput) { i.SessionCloses[0].SourceSHA256 = "invalid" }},
		{"trade_id", func(i *comparisonInput) { i.SessionCloses[0].TradeID = "" }},
		{"wrong_symbol", func(i *comparisonInput) { i.Raw[0].Symbol = "QQQ" }},
		{"invalid_price", func(i *comparisonInput) { i.SessionCloses[0].Price = "NaN" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := closeBasisInput()
			tc.change(&in)
			if _, err := sessionCloseSeries(in); err == nil {
				t.Fatal("invalid auction evidence accepted")
			}
		})
	}
}

func TestExplainedTradeGapRequiresIndependentSourceAndCannotExpandDates(t *testing.T) {
	in := closeBasisInput()
	in.Calendar[0] = outageDate
	in.Raw[0].Date = outageDate
	c := in.SessionCloses[0]
	c.Date = outageDate
	c.SourceBasis = "nasdaq_sip_gap"
	c.GapSourceSHA256 = strings.Repeat("b", 64)
	c.ExplanationURL = outageNotice
	c.CloseAt = time.Date(2019, 8, 12, 20, 0, 0, 0, time.UTC)
	c.OccurredAt = time.Time{}
	c.TradeID = ""
	c.SourceCondition = ""
	c.Price = in.Raw[0].Close
	in.SessionCloses[0] = c
	if _, err := sessionCloseSeries(in); err != nil {
		t.Fatal(err)
	}
	in.SessionCloses[0].GapSourceSHA256 = ""
	if _, err := sessionCloseSeries(in); err == nil {
		t.Fatal("gap lacks original empty SIP evidence")
	}
	in.SessionCloses[0] = c
	in.SessionCloses[0].Date = "2020-03-12"
	in.Raw[0].Date = "2020-03-12"
	in.Calendar[0] = "2020-03-12"
	if _, err := sessionCloseSeries(in); err == nil {
		t.Fatal("unsupported gap accepted")
	}
}
