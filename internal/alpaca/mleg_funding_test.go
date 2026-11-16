package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

type mlegFundingTransport struct {
	base           http.RoundTripper
	v              *fakeMlegVenue
	wholeContracts int
}

func (x mlegFundingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := x.base.RoundTrip(r)
	if err != nil {
		return res, err
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v2/orders" {
		raw, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		var o OptionPaperOrder
		_ = json.Unmarshal(raw, &o)
		if strings.HasPrefix(o.ClientOrderID, "mleg-close-") {
			// The whole close terminates after buying back only one short side.
			// This deliberately contradicts atomic parent/child quantities and net.
			// It must quarantine immediately, before any fallback authority.
			price := "12.4"
			o.Legs[1].FilledQty = strconv.Itoa(x.wholeContracts)
			o.Legs[1].FilledAvgPrice = &price
		} else if strings.HasPrefix(o.ClientOrderID, "mleg-leg-") && o.Side == "buy" {
			price := "6.2"
			o.FilledAvgPrice = &price
		}
		x.v.orders[o.ClientOrderID] = o
		raw, _ = json.Marshal(o)
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
	}
	return res, nil
}
func TestMLegNonproportionalWholeCashReportQuarantines(t *testing.T) {
	for _, wholeContracts := range []int{1, 2} {
		t.Run(strconv.Itoa(wholeContracts), func(t *testing.T) {
			v, p, root := newFakeMlegVenue(t)
			opening := mlegFixtureOpen()
			// Preserve the narrow protected structure at $100 strikes; the buyback
			// prices below are individually possible, including the short put.
			symbols := []string{"TST261218P00099900", "TST261218P00099950", "TST261218C00100050", "TST261218C00100100"}
			for i, symbol := range symbols {
				opening.Order.Legs[i].Symbol = symbol
				opening.Structure.Legs[i].Symbol = symbol
			}
			opening.Funding, _ = account.NewCappedState(account.DefaultCappedConfig("dedicated-options", "account-1", account.Margin, opening.Chain.AsOf.AddDate(0, 0, -14)))
			deposits, _ := opening.Funding.Config.DueDeposits(opening.Chain.AsOf)
			for _, deposit := range deposits {
				var err error
				opening.Funding, err = account.ApplyCappedCommand(opening.Funding, deposit)
				if err != nil {
					t.Fatal(err)
				}
			}
			cash, err := opening.LocalFunding("account-1")
			if err != nil || cash != "2500.000000000" {
				t.Fatal(cash, err)
			}
			testOpenMleg(t, p, opening)
			v.whole = "canceled"
			base := p.http.Transport
			if base == nil {
				base = http.DefaultTransport
			}
			p.http.Transport = mlegFundingTransport{base: base, v: v, wholeContracts: wholeContracts}
			plan := mlegPlan()
			plan.LimitPrice = "12.4"
			plan.LegLimits[1] = "6.2"
			plan.LegLimits[2] = "6.2"
			result, err := p.CloseMLeg(context.Background(), plan)
			if !errors.Is(err, ErrOptionQuarantined) || result.State == "closed" {
				t.Fatalf("spent cash authorized fallback: %+v %v", result, err)
			}
			expectedPosts := 2
			if len(v.posts) != expectedPosts {
				t.Fatal("fallback posted after whole close consumed cash", v.posts)
			}
			debits := 0.0
			for _, o := range v.orders {
				legs := o.Legs
				if len(legs) == 0 {
					legs = []OptionPaperOrder{o}
				}
				for _, l := range legs {
					if l.Side != "buy" || l.FilledAvgPrice == nil {
						continue
					}
					n, _ := filledContracts(l)
					price, _ := strconv.ParseFloat(*l.FilledAvgPrice, 64)
					debits += float64(n) * 100 * price
				}
			}
			t.Logf("state=%s posts=%d dedicated_settled_cash=2500 gross_buy_debits=%.2f (before fees, no sale proceeds allowed)", result.State, len(v.posts), debits)
			if debits > 2500 {
				t.Fatal("buybacks exceeded dedicated cash", debits)
			}
			for i, l := range opening.Order.Legs {
				if l.Side == "buy" && v.positions()[l.Symbol] != 2 {
					t.Fatalf("protective leg %d was sold", i)
				}
			}
			if _, err = p.journal.Rollover(context.Background(), t.TempDir()); err != nil {
				t.Fatal(err)
			}
			if err = p.journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			p.journal = j
			if _, err = p.CloseMLeg(context.Background(), plan); err == nil || len(v.posts) != expectedPosts {
				t.Fatal("restart reused spent cash", err, v.posts)
			}
		})
	}
}
