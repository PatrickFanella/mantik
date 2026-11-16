package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

func restartOptionJournal(t *testing.T, p *PaperTrader, root string) {
	t.Helper()
	if _, err := p.journal.Rollover(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := p.journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	p.journal = j
}

func TestMLegPolicyNeverDefaultsOrUpgradesRetainedWork(t *testing.T) {
	for _, policy := range []string{"", "legacy-gross", PaperAtomicNetV1, "paper-atomic-net-premium-fee065x3-v3"} {
		t.Run(policy, func(t *testing.T) {
			v, p, root := newFakeMlegVenue(t)
			o := mlegFixtureOpen()
			o.ExecutionPolicy = policy
			if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); !errors.Is(err, ErrOptionPolicy) || len(v.posts) != 0 {
				t.Fatal(err, v.posts)
			}
			raw, _ := json.Marshal(o)
			if err := p.journal.writeOnce(optionLifecycle, raw); err != nil {
				t.Fatal(err)
			}
			req := optionRequest{MLegOrder: o.Order}
			v.orders[o.Order.ClientOrderID] = mlegResponse(req, 1, "partially_filled")
			v.requests[o.Order.ClientOrderID] = req
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := p.OpenMLeg(context.Background(), o, nil); err != nil {
					t.Fatal("GET inspection failed", err)
				}
				out, err := p.CloseMLeg(context.Background(), mlegPlan())
				if !errors.Is(err, ErrOptionPolicy) || out.State == "closed" {
					t.Fatal(out, err)
				}
				upgraded := o
				upgraded.ExecutionPolicy = PaperAtomicNetV2
				if _, err := p.OpenMLeg(context.Background(), upgraded, nil); !errors.Is(err, ErrJournalConflict) {
					t.Fatal(err)
				}
				restartOptionJournal(t, p, root)
			}
			if len(v.posts) != 0 || v.deletes != 0 {
				t.Fatal("legacy acquired mutation authority", v.posts, v.deletes)
			}
		})
	}
	v, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	testOpenMleg(t, p, o)
	o.ExecutionPolicy = "changed"
	if _, err := p.OpenMLeg(context.Background(), o, nil); !errors.Is(err, ErrJournalConflict) || len(v.posts) != 1 {
		t.Fatal(err)
	}
}

func TestMLegPackageFundingIncludesFixedFeesBeforePOST(t *testing.T) {
	// Eight requested contracts * three rounds * $0.65 = $15.60.
	// The remaining $1984.40 permits exactly 2 * 100 * $9.922.
	for _, stage := range []string{"opening", "whole_close"} {
		for _, limit := range []string{"9.922", "9.922000001"} {
			t.Run(stage+limit, func(t *testing.T) {
				v, p, _ := newFakeMlegVenue(t)
				o := mlegFixtureOpen()
				if stage == "opening" {
					o.Order.LimitPrice = limit
					for i := range o.Structure.Legs {
						if o.Structure.Legs[i].Contracts > 0 {
							o.Structure.Legs[i].Limit = 5
						}
					}
				}
				if stage == "opening" {
					_, err := p.OpenMLeg(context.Background(), o, func() error { return nil })
					if (err == nil) != (limit == "9.922") {
						t.Fatal(err)
					}
					if len(v.posts) != map[bool]int{true: 1, false: 0}[limit == "9.922"] {
						t.Fatal(v.posts)
					}
				} else {
					testOpenMleg(t, p, o)
					plan := mlegPlan()
					plan.LimitPrice = limit
					out, err := p.CloseMLeg(context.Background(), plan)
					if (err == nil) != (limit == "9.922") {
						t.Fatal(out, err)
					}
					want := 1
					if limit == "9.922" {
						want = 2
					}
					if len(v.posts) != want {
						t.Fatal("funding refusal occurred after POST", v.posts)
					}
				}
			})
		}
	}
}

func TestMLegAtomicContradictionsStayQuarantined(t *testing.T) {
	for name, change := range map[string]func(*OptionPaperOrder){
		"nonproportional":  func(o *OptionPaperOrder) { o.Legs[0].FilledQty = "1" },
		"net_disagreement": func(o *OptionPaperOrder) { s := "-0.03"; o.FilledAvgPrice = &s },
		"signed_limit": func(o *OptionPaperOrder) {
			s := "0.01"
			o.FilledAvgPrice = &s
			for i := range o.Legs {
				x := "0.01"
				o.Legs[i].FilledAvgPrice = &x
			}
			x := "0.02"
			o.Legs[0].FilledAvgPrice = &x
		},
		"changed_parent": func(o *OptionPaperOrder) { o.ID = "another-parent" },
		"changed_child":  func(o *OptionPaperOrder) { o.Legs[0].ID = "another-child" },
		"regression": func(o *OptionPaperOrder) {
			o.FilledQty = "1"
			o.Status = "canceled"
			for i := range o.Legs {
				o.Legs[i].FilledQty = "1"
				o.Legs[i].Status = "canceled"
			}
		},
		"overfill":          func(o *OptionPaperOrder) { o.FilledQty = "3" },
		"missing_precision": func(o *OptionPaperOrder) { s := "-0.0200000001"; o.FilledAvgPrice = &s },
		"malformed_custody": nil,
	} {
		t.Run(name, func(t *testing.T) {
			v, p, root := newFakeMlegVenue(t)
			o := mlegFixtureOpen()
			testOpenMleg(t, p, o)
			good := v.orders[o.Order.ClientOrderID]
			if change != nil {
				bad := mlegResponse(optionRequest{MLegOrder: o.Order}, 2, "filled")
				change(&bad)
				v.orders[o.Order.ClientOrderID] = bad
			} else if err := os.WriteFile(filepath.Join(root, optionObservationName(1)), []byte(`{broken`), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(err, ErrOptionQuarantined) || out.State == "closed" {
				t.Fatal(out, err)
			}
			v.orders[o.Order.ClientOrderID] = good
			restartOptionJournal(t, p, root)
			out, err = p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(err, ErrOptionQuarantined) || out.State == "closed" {
				t.Fatal("convenient GET restored authority", out, err)
			}
			if _, err := p.OpenMLeg(context.Background(), o, nil); !errors.Is(err, ErrOptionQuarantined) {
				t.Fatal(err)
			}
			// Stock actions must not bypass a dedicated option journal's fences.
			if _, err := p.Submit(context.Background(), testStockOrder()); err == nil {
				t.Fatal("stock bypass")
			}
			if err := p.Cancel(context.Background(), o.Order.ClientOrderID); err == nil {
				t.Fatal("cancel bypass")
			}
			if len(v.posts) != 1 || v.deletes != 0 {
				t.Fatal(v.posts, v.deletes)
			}
		})
	}
}

func TestMLegUnchangedObservationDoesNotGrowCustody(t *testing.T) {
	_, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	testOpenMleg(t, p, o)
	for i := 0; i < 3; i++ {
		if _, err := p.OpenMLeg(context.Background(), o, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, n, err := p.optionObservations()
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestSimpleFallbackExecutionLimits(t *testing.T) {
	for _, side := range []string{"buy", "sell"} {
		req := closingLeg(optionRequest{MLegOrder: mlegFixtureOpen().Order}, "simple", mlegFixtureOpen().Order.Legs[0], 2, "0.02")
		req.Side = side
		req.PositionIntent = side + "_to_close"
		for _, price := range []string{"0.019999999", "0.02", "0.020000001"} {
			o := mlegResponse(req, 2, "filled")
			o.FilledAvgPrice = &price
			raw, _ := json.Marshal(o)
			_, err := decodeOptionOrder(raw, req)
			allowed := side == "buy" && price <= "0.02" || side == "sell" && price >= "0.02"
			if (err == nil) != allowed {
				t.Fatal(side, price, err)
			}
		}
	}
}

func TestMLegCreditsAndEarlierBuyNeverReplenishCash(t *testing.T) {
	_, p, _ := newFakeMlegVenue(t)
	for _, openingLimit := range []string{"1", "-10"} {
		for _, wholeLimit := range []string{"1", "-10"} {
			open := mlegFixtureOpen()
			open.Order.LimitPrice = openingLimit
			filled := mlegResponse(optionRequest{MLegOrder: open.Order}, 2, "filled")
			wholeReq := optionRequest{MLegOrder: open.Order}
			wholeReq.ClientOrderID = optionCloseID(open.Order.ClientOrderID)
			wholeReq.LimitPrice = wholeLimit
			wholeReq.Legs = append([]MLeg(nil), open.Order.Legs...)
			for i := range wholeReq.Legs {
				if wholeReq.Legs[i].Side == "buy" {
					wholeReq.Legs[i].Side = "sell"
				} else {
					wholeReq.Legs[i].Side = "buy"
				}
				wholeReq.Legs[i].PositionIntent = wholeReq.Legs[i].Side + "_to_close"
			}
			whole := mlegResponse(wholeReq, 2, "filled")
			cash, err := p.optionCloseCash(open, filled)
			expected := big.NewRat(198440, 100)
			if openingLimit == "1" {
				expected.Sub(expected, big.NewRat(200, 1))
			}
			if err != nil || cash.Cmp(expected) != 0 {
				t.Fatal(cash, err)
			}
			buy := closingLeg(optionRequest{MLegOrder: open.Order}, "first", open.Order.Legs[1], 2, "1")
			sale := closingLeg(optionRequest{MLegOrder: open.Order}, "sale", open.Order.Legs[0], 2, "1000")
			previous := map[int]OptionPaperOrder{1: mlegResponse(buy, 2, "filled"), 0: mlegResponse(sale, 2, "filled")}
			expected.Sub(expected, big.NewRat(200, 1))
			if wholeLimit == "1" {
				expected.Sub(expected, big.NewRat(200, 1))
			}
			next := buy
			next.ClientOrderID = "second"
			limit := new(big.Rat).Quo(expected, big.NewRat(200, 1))
			next.LimitPrice = limit.FloatString(9)
			// Equality after opener, whole and previous BUY limit allowances and fees.
			// The very large separate sale contributes nothing.
			if err := p.optionFallbackFunding(open, filled, whole, previous, next); err != nil {
				t.Fatal(err)
			}
			next.LimitPrice = limit.Add(limit, big.NewRat(1, 1000000000)).FloatString(9)
			if err := p.optionFallbackFunding(open, filled, whole, previous, next); err == nil {
				t.Fatal("credit or earlier buy funded another order", openingLimit, wholeLimit)
			}
		}
	}
}

func TestMLegCoherentPartialChargesLimitAcrossRestart(t *testing.T) {
	v, p, root := newFakeMlegVenue(t)
	open := fundedMlegFixture(t)
	testOpenMleg(t, p, open)
	plan := mlegPlan()
	plan.LimitPrice = "12.4"
	plan.LegLimits[1] = "6.2"
	plan.LegLimits[2] = "6.2"
	v.whole = "new"
	v.cancelFill = 1
	v.short = "partially_filled"
	out, err := p.CloseMLeg(context.Background(), plan)
	if !errors.Is(err, ErrOptionPending) || len(v.posts) != 3 || v.deletes != 1 || out.State == "closed" {
		t.Fatal(out, err, v.posts)
	}
	// Whole fills one coherent strategy unit ($1240 allowance). First short
	// fallback fills one contract ($620). $15.60 remains held for all fee rounds.
	next := closingLeg(v.posts[1], "next", open.Order.Legs[2], 1, "6.244")
	previous := map[int]OptionPaperOrder{1: out.Fallback[0]}
	first := previous[1]
	first.Status = "filled"
	previous[1] = first
	if err := p.optionFallbackFunding(open, out.Open, out.Whole, previous, next); err != nil {
		t.Fatal(err)
	}
	next.LimitPrice = "6.244000001"
	if err := p.optionFallbackFunding(open, out.Open, out.Whole, previous, next); err == nil {
		t.Fatal("partial allowance or previous buy was reused")
	}
	restartOptionJournal(t, p, root)
	if _, err := p.CloseMLeg(context.Background(), plan); !errors.Is(err, ErrOptionPending) || len(v.posts) != 3 {
		t.Fatal(err, v.posts)
	}
	id := v.posts[2].ClientOrderID
	o := v.orders[id]
	o.Status = "filled"
	v.orders[id] = o
	v.short = "filled"
	out, err = p.CloseMLeg(context.Background(), plan)
	if err != nil || out.State != "closed" || len(v.posts) != 6 {
		t.Fatal(out, err, v.posts)
	}
	// Two units opened, exactly one whole unit closed and all four remainders
	// separately closed. Restart may neither enlarge units nor recycle budget.
	for _, r := range v.posts[2:] {
		if r.Qty != "1" {
			t.Fatal(r)
		}
	}
}

func fundedMlegFixture(t *testing.T) MLegOpen {
	t.Helper()
	o := mlegFixtureOpen()
	var err error
	o.Funding, err = account.NewCappedState(account.DefaultCappedConfig("dedicated-options", "account-1", account.Margin, o.Chain.AsOf.AddDate(0, 0, -14)))
	if err != nil {
		t.Fatal(err)
	}
	due, err := o.Funding.Config.DueDeposits(o.Chain.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		o.Funding, err = account.ApplyCappedCommand(o.Funding, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	return o
}

func TestMLegHighGrossPricesChargeOnlyPackageLimitsAndFees(t *testing.T) {
	_, p, _ := newFakeMlegVenue(t)
	open := fundedMlegFixture(t)
	open.Order.Qty = "1"
	for i := range open.Structure.Legs {
		open.Structure.Legs[i].Contracts /= 2
	}
	opening := mlegResponse(optionRequest{MLegOrder: open.Order}, 1, "filled")
	for i := range opening.Legs {
		price := "15"
		if opening.Legs[i].Side == "sell" {
			price = "15.01"
		}
		opening.Legs[i].FilledAvgPrice = &price
	}
	raw, _ := json.Marshal(opening)
	lifecycle, _ := json.Marshal(open)
	if err := p.journal.writeOnce(optionLifecycle, lifecycle); err != nil {
		t.Fatal(err)
	}
	if _, err := p.optionObserve(raw, optionRequest{MLegOrder: open.Order}); err != nil {
		t.Fatal(err)
	}
	cash, err := p.optionCloseCash(open, opening)
	if err != nil || cash.Cmp(big.NewRat(249220, 100)) != 0 {
		t.Fatal("gross prices or opening credit altered cash", cash, err)
	}
	wholeReq := optionRequest{MLegOrder: open.Order}
	wholeReq.ClientOrderID = optionCloseID(open.Order.ClientOrderID)
	wholeReq.LimitPrice = "0.02"
	wholeReq.Legs = append([]MLeg(nil), open.Order.Legs...)
	for i := range wholeReq.Legs {
		if wholeReq.Legs[i].Side == "buy" {
			wholeReq.Legs[i].Side = "sell"
		} else {
			wholeReq.Legs[i].Side = "buy"
		}
		wholeReq.Legs[i].PositionIntent = wholeReq.Legs[i].Side + "_to_close"
	}
	whole := mlegResponse(wholeReq, 1, "filled")
	for i := range whole.Legs {
		price := "15"
		if whole.Legs[i].Side == "sell" {
			price = "14.99"
		}
		whole.Legs[i].FilledAvgPrice = &price
	}
	raw, _ = json.Marshal(whole)
	if _, err := decodeOptionOrder(raw, wholeReq); err != nil {
		t.Fatal(err)
	}
	next := closingLeg(wholeReq, "budget-check", open.Order.Legs[1], 1, "24.902")
	if err := p.optionFallbackFunding(open, opening, whole, nil, next); err != nil {
		t.Fatal("gross child prices charged instead of $2 package", err)
	}
	next.LimitPrice = "24.902000001"
	if err := p.optionFallbackFunding(open, opening, whole, nil, next); err == nil {
		t.Fatal("whole package consumption omitted")
	}
}

// contradictionTransport returns a contradictory successful POST but keeps the
// venue's coherent GET snapshot, proving the initial evidence cannot be erased.
type contradictionTransport struct {
	base  http.RoundTripper
	stage string
}

func (x contradictionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := x.base.RoundTrip(r)
	if err != nil {
		return res, err
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v2/orders" {
		return res, nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if err := res.Body.Close(); err != nil {
		return nil, err
	}
	var o OptionPaperOrder
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	matches := x.stage == "opening" && o.ClientOrderID == "condor-open" || x.stage == "whole" && strings.HasPrefix(o.ClientOrderID, "mleg-close-") || x.stage == "buy" && o.Side == "buy" || x.stage == "sell" && o.Side == "sell"
	if matches {
		if len(o.Legs) > 0 {
			o.Legs[0].FilledQty = "1"
		} else {
			price := "0.020000001"
			if o.Side == "sell" {
				price = "0.009999999"
			}
			o.FilledAvgPrice = &price
		}
		raw, err = json.Marshal(o)
		if err != nil {
			return nil, err
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}
func TestMLegSuccessfulPOSTContradictionsFenceAllLaterActions(t *testing.T) {
	for _, stage := range []string{"opening", "whole", "buy", "sell"} {
		t.Run(stage, func(t *testing.T) {
			v, p, root := newFakeMlegVenue(t)
			o := mlegFixtureOpen()
			base := p.http.Transport
			if base == nil {
				base = http.DefaultTransport
			}
			if stage == "opening" {
				p.http.Transport = contradictionTransport{base: base, stage: stage}
				if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); !errors.Is(err, ErrOptionQuarantined) {
					t.Fatal(err)
				}
			} else {
				testOpenMleg(t, p, o)
				if stage != "whole" {
					v.whole = "rejected"
				}
				p.http.Transport = contradictionTransport{base: base, stage: stage}
				out, err := p.CloseMLeg(context.Background(), mlegPlan())
				if !errors.Is(err, ErrOptionQuarantined) || out.State == "closed" {
					t.Fatal(out, err)
				}
			}
			posts, deletes := len(v.posts), v.deletes
			restartOptionJournal(t, p, root)
			out, err := p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(err, ErrOptionQuarantined) || out.State == "closed" || len(v.posts) != posts || v.deletes != deletes {
				t.Fatal(out, err, v.posts)
			}
		})
	}
}

func TestMLegOpeningComparisonRemainsSigned(t *testing.T) {
	for _, limit := range []string{"-0.03", "-0.02", "-0.019999999"} {
		t.Run(limit, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			o := mlegFixtureOpen()
			o.Order.LimitPrice = limit
			_, err := p.OpenMLeg(context.Background(), o, func() error { return nil })
			allowed := limit != "-0.019999999"
			if (err == nil) != allowed || len(v.posts) != map[bool]int{true: 1, false: 0}[allowed] {
				t.Fatal("credit admission comparison lost its sign", err, v.posts)
			}
		})
	}
}
