package allocation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"slices"
	"time"
)

var sizingDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,12})?$`)
var sleeveName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func sizingRat(s string) (*big.Rat, error) {
	if !sizingDecimal.MatchString(s) {
		return nil, errors.New("invalid sizing decimal")
	}
	r, _ := new(big.Rat).SetString(s)
	return r, nil
}

// Sleeve is trusted lifecycle configuration, not part of a strategy's targets.
// Risk is daily return volatility, Minimum is the predeclared tradable balance.
type Sleeve struct {
	ID, StrategyID, Stage, GateID, Risk, Minimum string
	ModelDriven                                  bool
}

// SleeveTarget is a complete replacement target set for one declared sleeve.
type SleeveTarget struct {
	SleeveID string
	Decision TargetDecision
}

// TargetDecision is the distributor boundary: a complete target set or a hold.
// It intentionally has no quantities, control flags or broker access.
type TargetDecision struct {
	Targets       []WeightTarget
	KeepPositions bool
}

// WeightTarget expresses a sleeve-relative signed asset weight.
type WeightTarget struct{ Asset, Weight string }

// RebalanceRequest contains weights only: strategies cannot choose quantities,
// budgets, account buying power or the fixed risk limits.
type RebalanceRequest struct {
	ID           string
	At           time.Time
	Sleeves      []Sleeve
	Targets      []SleeveTarget
	Limits, Fees map[string]string
}

// SizingPosition is an authoritative filled holding, attributed to one sleeve.
type SizingPosition struct{ SleeveID, Symbol, Quantity string }

// SizingSnapshot comes from the local capped book. Pending sleeves wait for
// reconciliation instead of repeatedly issuing a replacement target.
type SizingSnapshot struct {
	PreviousTargets map[string]TargetDecision
	Equity          string
	Positions       []SizingPosition
	PendingSleeves  []string
	Marks           map[string]string
}

// SizedOrder is the distributor's immutable fractional day-limit order.
type SizedOrder struct {
	ReduceOnly                                                                 bool
	ID, AllocationID, SleeveID, StrategyID, Symbol, Quantity, Limit, Side, Fee string
	At                                                                         time.Time
}

// FlatTarget is a complete, explicit exit target. A hold may later resize from
// retained weights, so it cannot bypass an admission check as a no-op.
func FlatTarget(d TargetDecision) bool {
	if d.KeepPositions {
		return false
	}
	for _, t := range d.Targets {
		w, e := sizingRat(t.Weight)
		if e != nil || w.Sign() != 0 {
			return false
		}
	}
	return true
}

// SleeveAllocation retains a budget or an explicit wait reason.
type SleeveAllocation struct{ SleeveID, StrategyID, Budget, Status, Reason string }

// Distribution is retained before any order can be reserved or submitted.
type Distribution struct {
	ID, InputSHA256, Equity string
	At                      time.Time
	Sleeves                 []SleeveAllocation
	Orders                  []SizedOrder
}

// Distribute is the single quantity calculation for capped portfolio targets.
// Qualified sleeves share 65% by inverse volatility, capped at 20% each; the
// fixed 5% incubation pool shares equally. Model-driven incubation is capped at
// 1% per version. Unused capacity stays cash; 70% is a target, never a mandate.
func Distribute(req RebalanceRequest, snap SizingSnapshot) (Distribution, error) {
	out := Distribution{ID: req.ID, At: req.At.UTC(), Equity: snap.Equity, Sleeves: []SleeveAllocation{}, Orders: []SizedOrder{}}
	e, err := sizingRat(snap.Equity)
	if err != nil || !sleeveName.MatchString(req.ID) || req.At.IsZero() || len(req.Sleeves) == 0 || len(req.Sleeves) > 64 || len(req.Targets) != len(req.Sleeves) || len(snap.Positions) > 256 {
		return out, errors.New("invalid distribution")
	}
	req.Targets = slices.Clone(req.Targets)
	for i, t := range req.Targets {
		if t.Decision.KeepPositions && len(t.Decision.Targets) == 0 {
			if prior, ok := snap.PreviousTargets[t.SleeveID]; ok {
				req.Targets[i].Decision = prior
			}
		}
	}
	openingEquity := new(big.Rat).Set(e)
	if openingEquity.Sign() < 0 {
		openingEquity.SetInt64(0)
	}
	// Size from equity net of every possible fee in this rebalance. Otherwise a
	// full 5% position would exceed its own cap as soon as its fee is charged.
	for _, t := range req.Targets {
		assets := map[string]bool{}
		for _, v := range t.Decision.Targets {
			assets[v.Asset] = true
		}
		for _, p := range snap.Positions {
			if p.SleeveID == t.SleeveID {
				assets[p.Symbol] = true
			}
		}
		if t.Decision.KeepPositions {
			continue
		}
		for asset := range assets {
			fee, fe := sizingRat(req.Fees[asset])
			if fe != nil || fee.Sign() < 0 {
				return out, errors.New("missing fee bound")
			}
			e.Sub(e, fee)
			mark, me := sizingRat(snap.Marks[asset])
			limit, le := sizingRat(req.Limits[asset])
			if me != nil || le != nil || mark.Sign() <= 0 || limit.Sign() <= 0 {
				return out, errors.New("missing positive execution bound")
			}
			units := new(big.Rat).Quo(new(big.Rat).Mul(openingEquity, big.NewRat(5, 100)), mark)
			for _, p := range snap.Positions {
				if p.SleeveID == t.SleeveID && p.Symbol == asset {
					held, he := sizingRat(p.Quantity)
					if he != nil {
						return out, he
					}
					units.Add(units, new(big.Rat).Abs(held))
				}
			}
			gap := new(big.Rat).Abs(new(big.Rat).Sub(limit, mark))
			e.Sub(e, new(big.Rat).Mul(units, gap))
		}
	}
	if e.Sign() < 0 {
		e.SetInt64(0)
	}
	raw, err := json.Marshal(struct {
		Request  RebalanceRequest
		Snapshot SizingSnapshot
	}{req, snap})
	if err != nil {
		return out, err
	}
	h := sha256.Sum256(raw)
	out.InputSHA256 = hex.EncodeToString(h[:])
	ss := slices.Clone(req.Sleeves)
	slices.SortFunc(ss, func(a, b Sleeve) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	targets := map[string]TargetDecision{}
	for _, t := range req.Targets {
		if _, exists := targets[t.SleeveID]; exists {
			return out, errors.New("duplicate sleeve target")
		}
		targets[t.SleeveID] = t.Decision
	}
	weights := map[string]*big.Rat{}
	versions := map[string]bool{}
	total := new(big.Rat)
	incubation := 0
	for i, s := range ss {
		v, ve := sizingRat(s.Risk)
		m, me := sizingRat(s.Minimum)
		if !sleeveName.MatchString(s.ID) || i > 0 && ss[i-1].ID == s.ID || versions[s.StrategyID] || len(s.StrategyID) != 73 || !slices.Contains([]string{"qualified", "incubation", "forward_only"}, s.Stage) || ve != nil || v.Sign() <= 0 || me != nil || m.Sign() < 0 {
			return out, errors.New("invalid sleeve declaration")
		}
		versions[s.StrategyID] = true
		if _, ok := targets[s.ID]; !ok {
			return out, errors.New("missing complete target")
		}
		if s.Stage == "qualified" {
			if s.ModelDriven {
				return out, errors.New("model-driven version requires forward qualification")
			}
			weights[s.ID] = new(big.Rat).Inv(v)
			total.Add(total, weights[s.ID])
		} else {
			incubation++
		}
	}
	for _, s := range ss {
		budget := new(big.Rat)
		if s.Stage == "qualified" {
			budget.Mul(e, big.NewRat(65, 100))
			budget.Mul(budget, new(big.Rat).Quo(weights[s.ID], total))
		} else {
			budget.Mul(e, big.NewRat(5, int64(100*incubation)))
		}
		cap := new(big.Rat).Mul(e, big.NewRat(20, 100))
		if s.ModelDriven || s.Stage == "forward_only" {
			cap.Mul(e, big.NewRat(1, 100))
		}
		if budget.Cmp(cap) > 0 {
			budget.Set(cap)
		}
		a := SleeveAllocation{SleeveID: s.ID, StrategyID: s.StrategyID, Budget: budget.FloatString(9), Status: "ready"}
		minimum, _ := sizingRat(s.Minimum)
		if budget.Cmp(minimum) < 0 {
			a.Status, a.Reason = "wait", "minimum_balance"
		}
		if slices.Contains(snap.PendingSleeves, s.ID) {
			a.Status, a.Reason = "wait", "pending_orders"
		}
		if a.Reason == "pending_orders" {
			out.Sleeves = append(out.Sleeves, a)
			continue
		}
		d := targets[s.ID]
		if a.Reason == "minimum_balance" || budget.Sign() == 0 {
			d = TargetDecision{}
		}
		if d.KeepPositions {
			if len(d.Targets) > 0 {
				return out, errors.New("hold cannot include targets")
			}
			out.Sleeves = append(out.Sleeves, a)
			continue
		}
		want := map[string]*big.Rat{}
		gross := new(big.Rat)
		for _, t := range d.Targets {
			w, we := sizingRat(t.Weight)
			if _, ok := want[t.Asset]; ok || we != nil || !symbolPattern.MatchString(t.Asset) || new(big.Rat).Abs(w).Cmp(big.NewRat(1, 1)) > 0 {
				return out, errors.New("invalid target weight")
			}
			want[t.Asset] = new(big.Rat).Mul(budget, w)
			gross.Add(gross, new(big.Rat).Abs(w))
			positionCap := new(big.Rat).Mul(e, big.NewRat(5, 100))
			if new(big.Rat).Abs(want[t.Asset]).Cmp(positionCap) > 0 {
				want[t.Asset].Set(positionCap)
				if w.Sign() < 0 {
					want[t.Asset].Neg(want[t.Asset])
				}
			}
		}
		if gross.Cmp(big.NewRat(1, 1)) > 0 {
			return out, errors.New("target gross exceeds sleeve")
		}
		held := map[string]*big.Rat{}
		for _, p := range snap.Positions {
			if p.SleeveID != s.ID {
				continue
			}
			q, qe := sizingRat(p.Quantity)
			if qe != nil || held[p.Symbol] != nil {
				return out, errors.New("invalid sleeve holdings")
			}
			held[p.Symbol] = q
			if want[p.Symbol] == nil {
				want[p.Symbol] = new(big.Rat)
			}
		}
		assets := []string{}
		for asset := range want {
			assets = append(assets, asset)
		}
		slices.Sort(assets)
		for _, asset := range assets {
			mark, me := sizingRat(snap.Marks[asset])
			limit, le := sizingRat(req.Limits[asset])
			fee, fe := sizingRat(req.Fees[asset])
			if me != nil || le != nil || fe != nil || mark.Sign() <= 0 || limit.Sign() <= 0 || fee.Sign() < 0 {
				return out, errors.New("missing positive mark/limit or fee bound")
			}
			priceBound := new(big.Rat).Set(mark)
			if limit.Cmp(priceBound) > 0 {
				priceBound.Set(limit)
			}
			qty := new(big.Rat).Quo(want[asset], priceBound)
			if held[asset] != nil {
				qty.Sub(qty, held[asset])
			}
			// Truncate toward zero at nine decimal places; rounding must not grow exposure.
			units := new(big.Int).Quo(new(big.Int).Mul(qty.Num(), big.NewInt(1_000_000_000)), qty.Denom())
			qty.SetFrac(units, big.NewInt(1_000_000_000))
			if qty.Sign() == 0 {
				continue
			}
			if new(big.Rat).Mul(new(big.Rat).Abs(qty), limit).Cmp(big.NewRat(1, 1)) < 0 && want[asset].Sign() != 0 {
				a.Status, a.Reason = "wait", "minimum_order"
				continue
			}
			side := "buy"
			if qty.Sign() < 0 {
				side = "sell"
			}
			identity := sha256.Sum256([]byte(req.ID + "/" + s.ID + "/" + asset))
			reducing := want[asset].Sign() == 0
			if h := held[asset]; h != nil && h.Sign()*qty.Sign() < 0 && new(big.Rat).Abs(qty).Cmp(new(big.Rat).Abs(h)) <= 0 {
				reducing = true
			}
			out.Orders = append(out.Orders, SizedOrder{ReduceOnly: reducing, ID: "sized-" + hex.EncodeToString(identity[:16]), AllocationID: req.ID, SleeveID: s.ID, StrategyID: s.StrategyID, Symbol: asset, Quantity: new(big.Rat).Abs(qty).FloatString(9), Limit: req.Limits[asset], Side: side, Fee: req.Fees[asset], At: req.At.UTC()})
		}
		out.Sleeves = append(out.Sleeves, a)
	}
	return out, nil
}
