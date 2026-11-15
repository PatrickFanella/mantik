package main

import (
	"errors"
	"math/big"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

// sessionClose identifies a primary-exchange closing record validated from
// original SIP pages by prepare.py; source hashes remain in the input manifest.
var auctionDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var auctionTradeID = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)

const outageDate = "2019-08-12"
const outageNotice = "https://www.nasdaqtrader.com/TraderNews.aspx?id=ETA2019-68"

type sessionClose struct {
	GapSourceSHA256 string    `json:"gap_source_sha256,omitempty"`
	SourceCondition string    `json:"source_condition,omitempty"`
	SourceBasis     string    `json:"source_basis"`
	ExplanationURL  string    `json:"explanation_url,omitempty"`
	Date            string    `json:"date"`
	CloseAt         time.Time `json:"close_at"`
	OccurredAt      time.Time `json:"occurred_at,omitzero"`
	ObservedAt      time.Time `json:"observed_at"`
	Price           string    `json:"price"`
	TradeID         string    `json:"trade_id,omitempty"`
	SourceSHA256    string    `json:"source_sha256"`
}
type closeReconciliation struct {
	Date         string `json:"date"`
	RawClose     string `json:"raw_close"`
	SessionClose string `json:"session_close"`
	SourceSHA256 string `json:"source_sha256"`
}
type basisComparison struct {
	YahooPrimary       marketdata.Comparison `json:"yahoo_primary_diagnostic"`
	ReferenceSampleEnd string                `json:"reference_sample_end"`
	Passed             bool                  `json:"passed"`
	CloseBasis         string                `json:"close_basis"`
	Sessions           int                   `json:"primary_close_sessions"`
	OfficialNotices    int                   `json:"official_notice_sessions"`
	RawSIP             marketdata.Comparison `json:"raw_sip"`
	SessionClose       marketdata.Comparison `json:"session_close"`
	ExplainedSessions  []sessionClose        `json:"explained_sessions"`
	Reconciliations    []closeReconciliation `json:"close_reconciliations"`
}

// sessionCloseSeries uses a separate projection for comparison. It never
// corrects stored observations or rewrites the original raw-SIP comparison.
// Every session needs evidence, including sessions that already agreed.
func sessionCloseSeries(in comparisonInput) (marketdata.Series, error) {
	if len(in.Raw) == 0 || len(in.Raw) != len(in.SessionCloses) || len(in.Raw) != len(in.Calendar) {
		return marketdata.Series{}, errors.New("session-close projection requires every calendar session")
	}
	raw := slices.Clone(in.Raw)
	for i, c := range in.SessionCloses {
		if raw[i].AvailableAt.After(in.AsOf) || raw[i].Symbol != "SPY" || raw[i].Date != c.Date || c.Date != in.Calendar[i] || c.CloseAt.IsZero() || c.CloseAt.UTC().Format(time.DateOnly) != c.Date || c.ObservedAt.After(in.AsOf) || c.ObservedAt.Before(c.CloseAt) || !auctionDigest.MatchString(c.SourceSHA256) {
			return marketdata.Series{}, errors.New("invalid or unavailable SPY close evidence")
		}
		switch c.SourceBasis {
		case "primary_exchange_close":
			if (c.SourceCondition != "6" && c.SourceCondition != "M") || c.OccurredAt.Before(c.CloseAt) || c.OccurredAt.After(c.CloseAt.Add(time.Second)) || c.ObservedAt.Before(c.OccurredAt) || !auctionTradeID.MatchString(c.TradeID) || c.ExplanationURL != "" {
				return marketdata.Series{}, errors.New("invalid SPY auction print")
			}
		case "nasdaq_sip_gap":
			if c.Date != outageDate || c.ExplanationURL != outageNotice || !auctionDigest.MatchString(c.GapSourceSHA256) || !c.OccurredAt.IsZero() || c.TradeID != "" || c.SourceCondition != "" {
				return marketdata.Series{}, errors.New("unexplained SPY trade gap")
			}
		default:
			return marketdata.Series{}, errors.New("unknown SPY close basis")
		}

		raw[i].Close = c.Price
		// This is a present-day read projection. The auction's historical event
		// time does not make its newly acquired source available in that past.
		if raw[i].AvailableAt.Before(c.ObservedAt) {
			raw[i].AvailableAt = c.ObservedAt
		}
	}
	series, err := marketdata.Adjust(in.AssetID, raw, in.Actions, in.AsOf)
	if err != nil {
		return marketdata.Series{}, err
	}
	// Raw always remains the unmodified provider observation in an adjusted bar.
	for i := range series.Bars {
		series.Bars[i].Raw = in.Raw[i]
	}
	return series, nil
}

func samePrice(a, b string) bool {
	x, xok := new(big.Rat).SetString(a)
	y, yok := new(big.Rat).SetString(b)
	return xok && yok && x.Cmp(y) == 0
}
