package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
)

// ErrOptionPending requires reconciliation of the original order, never a new ID.
var ErrOptionPending = errors.New("option lifecycle pending; repeat the exact command to reconcile")

// ErrOptionRefused is a durably retained, confirmed API validation refusal.
var ErrOptionRefused = errors.New("option order confirmed refused")

const optionLifecycle = "paper-intent-mleg-lifecycle.json"
const optionClosePlan = "paper-intent-mleg-close-plan.json"

// MLeg is a positive integer ratio and explicit opening/closing option intent.
type MLeg struct {
	Symbol         string `json:"symbol"`
	RatioQty       string `json:"ratio_qty"`
	Side           string `json:"side"`
	PositionIntent string `json:"position_intent"`
}

// MLegOrder is a two-to-four-leg, signed limit order. Parent qty is strategy units.
// Symbol and Side are deliberately absent from this wire contract.
type MLegOrder struct {
	ClientOrderID string `json:"client_order_id"`
	Qty           string `json:"qty"`
	Type          string `json:"type"`
	TimeInForce   string `json:"time_in_force"`
	LimitPrice    string `json:"limit_price"`
	OrderClass    string `json:"order_class"`
	Legs          []MLeg `json:"legs,omitempty"`
}

// PaperAtomicNetV1 identifies the legacy contract without reply-classification
// crash fencing. Retained v1 lifecycles are inspection only.
const PaperAtomicNetV1 = "paper-atomic-net-premium-fee065x3-v1"

// PaperAtomicNetV2 binds atomic package premiums, a modeled $0.65 per contract
// for three rounds, and durable reply classification. It is paper only.
const PaperAtomicNetV2 = "paper-atomic-net-premium-fee065x3-v2"

// ErrOptionQuarantined means contradictory execution custody requires inspection.
var ErrOptionQuarantined = errors.New("option execution custody quarantined")

// ErrOptionPolicy denies mutation of legacy or unknown-policy lifecycles.
var ErrOptionPolicy = errors.New("explicit supported paper execution policy required; inspection only")

// MLegOpen pins the policy, admission evidence and exact wire order. Chain
// estimates are caller-supplied indicative evidence, never live qualification.
type MLegOpen struct {
	ExecutionPolicy string `json:",omitempty"`
	Funding         account.CappedState
	Order           MLegOrder
	Chain           options.Chain
	Structure       options.Structure
}

// OptionPaperOrder retains parent and child venue identity and cumulative fills.
// Raw responses, including all other provider fields, remain in the journal.
type OptionPaperOrder struct {
	PaperOrder
	OrderClass     string             `json:"order_class"`
	PositionIntent string             `json:"position_intent"`
	Legs           []OptionPaperOrder `json:"legs,omitempty"`
}

type optionRequest struct {
	MLegOrder
	Symbol         string `json:"symbol,omitempty"`
	Side           string `json:"side,omitempty"`
	PositionIntent string `json:"position_intent,omitempty"`
}

func optionInteger(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n <= 1000000 && strconv.Itoa(n) == s
}
func signedOptionLimit(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	return positivePaperDecimal(s)
}

// Validate checks the multi-leg wire grammar, including reduced integer ratios.
func (o MLegOrder) Validate() error {
	if !paperID.MatchString(o.ClientOrderID) || o.OrderClass != "mleg" || o.Type != "limit" || !slices.Contains([]string{"day", "gtc"}, o.TimeInForce) || !signedOptionLimit(o.LimitPrice) || len(o.Legs) < 2 || len(o.Legs) > 4 {
		return errors.New("invalid multi-leg option order")
	}
	q, ok := optionInteger(o.Qty)
	if !ok || q == 0 {
		return errors.New("options require positive integer strategy units")
	}
	seen := map[string]bool{}
	gcd := 0
	direction := ""
	for _, l := range o.Legs {
		n, ok := optionInteger(l.RatioQty)
		if !ok || n == 0 || n*q > 1000000 || !standardOption.MatchString(l.Symbol) || seen[l.Symbol] {
			return errors.New("invalid option leg or ratio")
		}
		seen[l.Symbol] = true
		if l.PositionIntent != l.Side+"_to_open" && l.PositionIntent != l.Side+"_to_close" || l.Side != "buy" && l.Side != "sell" {
			return errors.New("option side/intent mismatch")
		}
		d := strings.TrimPrefix(l.PositionIntent, l.Side+"_to_")
		if direction != "" && direction != d {
			return errors.New("mixed open/close legs")
		}
		direction = d
		for n != 0 {
			gcd, n = n, gcd%n
		}
	}
	if gcd != 1 {
		return errors.New("option ratios must have GCD one")
	}
	return nil
}

func (o MLegOpen) validate(now time.Time) error {
	if err := o.Order.Validate(); err != nil {
		return err
	}
	if o.Structure.Kind != "vertical" && o.Structure.Kind != "iron_condor" && o.Structure.Kind != "iron_butterfly" && o.Structure.Kind != "long_options" {
		return errors.New("paper mleg requires independent option protection; stock collateral unsupported")
	}
	if len(o.Structure.Legs) != len(o.Order.Legs) {
		return errors.New("structure/wire leg mismatch")
	}
	q, _ := optionInteger(o.Order.Qty)
	net := new(big.Rat)
	for i, l := range o.Order.Legs {
		s := o.Structure.Legs[i]
		ratio, _ := optionInteger(l.RatioQty)
		n := q * ratio
		if l.Side == "sell" {
			n = -n
		}
		if l.PositionIntent != l.Side+"_to_open" || s.Symbol != l.Symbol || s.Contracts != n {
			return errors.New("structure/wire contracts mismatch")
		}
		price, ok := new(big.Rat).SetString(strconv.FormatFloat(s.Limit, 'f', -1, 64))
		if !ok {
			return errors.New("invalid leg price")
		}
		net.Add(net, new(big.Rat).Mul(price, big.NewRat(int64(n), 1)))
	}
	// Per-leg prices are model/admission assumptions, not wire-enforced caps.
	// The signed package limit must be at least as conservative as these prices;
	// never use a cheaper risk declaration to send a dearer order.
	limit, _ := new(big.Rat).SetString(o.Order.LimitPrice)
	if new(big.Rat).Mul(limit, big.NewRat(int64(q), 1)).Cmp(net) > 0 {
		return errors.New("package limit exceeds admitted leg limits")
	}
	if o.Chain.AsOf.After(now) || now.Sub(o.Chain.AsOf) > 24*time.Hour {
		return errors.New("opening admission evidence unavailable or older than 24 hours")
	}

	return nil
}

// OpenMLeg commits one lifecycle in a dedicated journal. The caller must bind
// OptionBook admission through beforePost; use mlegpaper.Open for product calls.
// Fixed local capital, scheduled deposits and risk caps ignore Alpaca balances. Capital and reservations are never reused, even after closing.
// An exact retry recovers by GET only and does not re-admit historical evidence.
func (p *PaperTrader) OpenMLeg(ctx context.Context, o MLegOpen, beforePost func() error) (OptionPaperOrder, error) {
	if err := o.Order.Validate(); err != nil {
		return OptionPaperOrder{}, err
	}
	if err := p.enter(); err != nil {
		return OptionPaperOrder{}, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return OptionPaperOrder{}, err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return OptionPaperOrder{}, err
	}
	old, err := p.journal.read(optionLifecycle, MaxBytes)
	if err == nil {
		if !bytes.Equal(raw, old) {
			return OptionPaperOrder{}, ErrJournalConflict
		}
		return p.optionLookup(ctx, optionRequest{MLegOrder: o.Order})
	}
	if !errors.Is(err, os.ErrNotExist) {
		return OptionPaperOrder{}, err
	}
	// Control records and generated closing IDs own this prefix. Apply the
	// restriction only to new lifecycles so retained orders still recover.
	if strings.HasPrefix(o.Order.ClientOrderID, "mleg-") {
		return OptionPaperOrder{}, errors.New("new multi-leg opening client ID uses reserved mleg- prefix")
	}
	names, err := directoryNames(p.journal.root)
	if err != nil {
		return OptionPaperOrder{}, err
	}
	for _, name := range names {
		if strings.HasPrefix(name, "paper-intent-") || strings.HasPrefix(name, "paper-abort-") {
			return OptionPaperOrder{}, errors.New("mleg requires a dedicated unused paper journal")
		}
	}
	if o.ExecutionPolicy != PaperAtomicNetV2 {
		return OptionPaperOrder{}, ErrOptionPolicy
	}
	if beforePost == nil {
		return OptionPaperOrder{}, errors.New("opening requires local admission immediately before POST")
	}
	if err = p.optionOpeningFunding(o); err != nil {
		return OptionPaperOrder{}, err
	}
	if err = o.validate(time.Now().UTC()); err != nil {
		return OptionPaperOrder{}, err
	}
	if err = p.optionPermission(ctx, o); err != nil {
		return OptionPaperOrder{}, err
	}
	if err = p.optionInventory(ctx, o.Order.Legs, make([]int, len(o.Order.Legs))); err != nil {
		return OptionPaperOrder{}, err
	}
	if err = p.journal.writeOnce(optionLifecycle, raw); err != nil {
		return OptionPaperOrder{}, err
	}
	return p.optionSubmitChecked(ctx, optionRequest{MLegOrder: o.Order}, beforePost)
}

func optionIntentName(id string) string  { return "paper-intent-" + id + ".json" }
func optionRefusalName(id string) string { return "paper-intent-mleg-refused-" + id + ".json" }

// optionSubmit requires the adapter gate and lifecycle custody. A retained intent
// is immutable and GET-only, including crashes between fsync and POST.
func (p *PaperTrader) optionSubmit(ctx context.Context, r optionRequest) (OptionPaperOrder, error) {
	return p.optionSubmitChecked(ctx, r, nil)
}
func (p *PaperTrader) optionSubmitChecked(ctx context.Context, r optionRequest, beforePost func() error) (OptionPaperOrder, error) {
	if err := p.optionMutationAuthority(); err != nil {
		return OptionPaperOrder{}, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return OptionPaperOrder{}, err
	}
	old, err := p.journal.read(optionIntentName(r.ClientOrderID), MaxBytes)
	if err == nil {
		if !bytes.Equal(raw, old) {
			return OptionPaperOrder{}, ErrJournalConflict
		}
		return p.optionLookup(ctx, r)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return OptionPaperOrder{}, err
	}
	if err := p.optionMutationCapacity(); err != nil {
		return OptionPaperOrder{}, err
	}
	if err = p.journal.writeOnce(optionIntentName(r.ClientOrderID), raw); err != nil {
		return OptionPaperOrder{}, err
	}
	if beforePost != nil {
		if err := beforePost(); err != nil {
			return OptionPaperOrder{}, errors.Join(err, p.journal.writeOnce(optionRefusalName(r.ClientOrderID), []byte("local_admission_refused")))
		}
	}
	body, status, err := p.request(ctx, http.MethodPost, "/v2/orders", raw)
	if err == nil && status >= 200 && status < 300 {
		return p.optionObserve(body, r)
	}
	// Only a retained numeric API validation refusal is deterministic. Mutable
	// message text, timeouts, 5xx, authentication and ambiguous replies never are.
	var api struct {
		Code int `json:"code"`
	}
	if err == nil && status == 422 && json.Unmarshal(body, &api) == nil && api.Code == 42210000 {
		if err = p.journal.writeOnce(optionRefusalName(r.ClientOrderID), body); err != nil {
			return OptionPaperOrder{}, err
		}
	}
	return p.optionLookup(ctx, r)
}

func (p *PaperTrader) optionLookup(ctx context.Context, r optionRequest) (OptionPaperOrder, error) {
	// The lifecycle may have been retained immediately before process death,
	// without a wire intent. It still consumes the ID and remains GET-only.
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/orders:by_client_order_id?"+url.Values{"client_order_id": {r.ClientOrderID}, "nested": {"true"}}.Encode(), nil)
	if err != nil {
		return OptionPaperOrder{}, ErrSubmissionUnknown
	}
	if status == 404 {
		if _, e := p.journal.read(optionRefusalName(r.ClientOrderID), MaxBytes); e == nil {
			return OptionPaperOrder{}, ErrOptionRefused
		}
	}
	if status != 200 {
		return OptionPaperOrder{}, ErrSubmissionUnknown
	}
	return p.optionObserve(raw, r)
}

func decodeOptionOrder(raw []byte, r optionRequest) (OptionPaperOrder, error) {
	var o OptionPaperOrder
	if !validOptionRequest(r) {
		return o, errors.New("invalid retained option request")
	}
	if json.Unmarshal(raw, &o) != nil || !paperDecimal.MatchString(o.Qty) || !signedOptionLimit(o.LimitPrice) || !paperID.MatchString(o.ID) || o.ClientOrderID != r.ClientOrderID || o.Type != r.Type || o.TimeInForce != r.TimeInForce || !samePaperDecimal(o.Qty, r.Qty) || !samePaperDecimal(o.LimitPrice, r.LimitPrice) || !optionStatus(o.Status) {
		return o, errors.New("option order response mismatches retained request")
	}
	if r.OrderClass == "mleg" {
		if o.OrderClass != "mleg" || o.Symbol != "" || o.Side != "" || len(o.Legs) != len(r.Legs) {
			return o, errors.New("invalid mleg parent")
		}
		q, _ := optionInteger(r.Qty)
		ids := map[string]bool{}
		matched := make([]OptionPaperOrder, len(r.Legs))
		for i, l := range r.Legs {
			found := false
			ratio, _ := optionInteger(l.RatioQty)
			for _, leg := range o.Legs {
				if leg.Symbol != l.Symbol {
					continue
				}
				if found || !paperID.MatchString(leg.ID) || ids[leg.ID] || leg.ID == o.ID || leg.Side != l.Side || leg.PositionIntent != "" && leg.PositionIntent != l.PositionIntent || !samePaperDecimal(leg.Qty, strconv.Itoa(q*ratio)) || !validOptionFill(leg, q*ratio) {
					return o, errors.New("invalid owned option leg")
				}
				ids[leg.ID] = true
				found = true
				matched[i] = leg
			}
			if !found {
				return o, errors.New("missing owned option leg")
			}
		}
		o.Legs = matched
		if !validOptionParent(o, q) {
			return o, errors.New("invalid parent fills")
		}
	} else {
		q, _ := optionInteger(r.Qty)
		if o.Symbol != r.Symbol || o.Side != r.Side || o.PositionIntent != "" && o.PositionIntent != r.PositionIntent || len(o.Legs) != 0 || !validOptionFill(o, q) {
			return o, errors.New("invalid closing option response")
		}
	}
	if err := atomicOptionExecution(o, r); err != nil {
		return o, err
	}
	return o, nil
}
func filledContracts(o OptionPaperOrder) (int, bool) {
	if !paperDecimal.MatchString(o.FilledQty) {
		return 0, false
	}
	n, ok := new(big.Rat).SetString(o.FilledQty)
	if !ok || !n.IsInt() || !n.Num().IsInt64() {
		return 0, false
	}
	v := n.Num().Int64()
	return int(v), v >= 0 && v <= 1000000
}
func validOptionParent(o OptionPaperOrder, q int) bool {
	n, ok := filledContracts(o)
	return ok && n <= q && (n == 0 || o.FilledAvgPrice != nil && validSignedQty(*o.FilledAvgPrice)) && (o.Status != "filled" || n == q)
}
func validOptionFill(o OptionPaperOrder, q int) bool {
	n, ok := filledContracts(o)
	return ok && n <= q && optionStatus(o.Status) && (n == 0 || o.FilledAvgPrice != nil && positivePaperDecimal(*o.FilledAvgPrice)) && (o.Status != "filled" || n == q)
}
func optionStatus(s string) bool {
	return slices.Contains([]string{"new", "accepted", "pending_new", "partially_filled", "filled", "canceled", "expired", "rejected", "pending_cancel", "done_for_day", "stopped", "suspended", "calculated", "replaced", "pending_replace", "accepted_for_bidding"}, s)
}
func optionTerminal(s string) bool {
	return slices.Contains([]string{"filled", "canceled", "expired", "rejected"}, s)
}

// optionInventory refuses any overlapping pending order and requires the exact
// journal-owned remaining quantities. Unowned stock or options never collateralize
// this lifecycle. A full page is ambiguous and therefore fails closed.
func (p *PaperTrader) optionInventory(ctx context.Context, legs []MLeg, want []int) error {
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/orders?status=open&nested=true&limit=500", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return errors.New("option pending-order preflight unavailable")
	}
	var orders []OptionPaperOrder
	if json.Unmarshal(raw, &orders) != nil || orders == nil || len(orders) >= 500 {
		return errors.New("invalid or incomplete pending orders")
	}
	symbols := map[string]bool{}
	for _, l := range legs {
		symbols[l.Symbol] = true
	}
	var overlaps func(OptionPaperOrder) bool
	overlaps = func(o OptionPaperOrder) bool {
		if symbols[o.Symbol] {
			return true
		}
		for _, l := range o.Legs {
			if overlaps(l) {
				return true
			}
		}
		return false
	}
	for _, o := range orders {
		if o.OrderClass == "mleg" && len(o.Legs) < 2 || o.Symbol == "" && len(o.Legs) == 0 {
			return errors.New("pending order lacks complete symbol/leg inventory")
		}
		if overlaps(o) {
			return errors.New("overlapping pending option order; reconcile before continuing")
		}
	}
	raw, status, err = p.request(ctx, http.MethodGet, "/v2/positions", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return errors.New("option positions unavailable")
	}
	var positions []struct{ Symbol, Qty string }
	if json.Unmarshal(raw, &positions) != nil || positions == nil {
		return errors.New("invalid option position inventory")
	}
	seen := map[string]bool{}
	for _, v := range positions {
		if !symbols[v.Symbol] {
			continue
		}
		if seen[v.Symbol] {
			return errors.New("duplicate option position")
		}
		seen[v.Symbol] = true
		for i, l := range legs {
			if l.Symbol == v.Symbol && !samePaperDecimal(v.Qty, strconv.Itoa(want[i])) {
				return errors.New("option inventory differs from journal-owned fills")
			}
		}
	}
	for i, l := range legs {
		if !seen[l.Symbol] && want[i] != 0 {
			return errors.New("journal-owned option position missing")
		}
	}
	return nil
}

// optionPermission checks level and standard deliverables; no account balance
// participates in local admission. All source replies are retained before use.
func (p *PaperTrader) optionPermission(ctx context.Context, o MLegOpen) error {
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/account", nil)
	if err != nil {
		return err
	}
	var a struct {
		ID    string `json:"id"`
		Level int    `json:"options_trading_level"`
	}
	if status != 200 || json.Unmarshal(raw, &a) != nil || a.ID != p.account || a.Level < 3 {
		return errors.New("paper multi-leg requires confirmed options trading level 3")
	}
	for _, l := range o.Order.Legs {
		raw, status, err = p.request(ctx, http.MethodGet, "/v2/options/contracts/"+url.PathEscape(l.Symbol), nil)
		if err != nil {
			return err
		}
		var c struct {
			Symbol     string `json:"symbol"`
			Underlying string `json:"underlying_symbol"`
			Status     string `json:"status"`
			Tradable   bool   `json:"tradable"`
			Size       string `json:"size"`
		}
		if status != 200 || json.Unmarshal(raw, &c) != nil || c.Symbol != l.Symbol || c.Underlying != o.Chain.Underlying || c.Status != "active" || !c.Tradable || c.Size != "100" {
			return errors.New("paper multi-leg requires active tradable standard 100-share contracts")
		}
	}
	return nil
}

// LocalFunding replays a dedicated, operator-declared capped ledger. The fixed
// schedule may grow local cash; broker balances, loans, trading proceeds, stock
// and reservations never do. This snapshot is immutable for the lifecycle.
func (o MLegOpen) LocalFunding(expectedAccount string) (string, error) {
	cfg := o.Funding.Config
	if cfg.BrokerAccountID != expectedAccount || cfg.OpeningCash != "2000" || cfg.DepositAmount != "500" || cfg.EveryWeeks != 2 {
		return "", errors.New("option funding requires the fixed local capped-book schedule and expected account")
	}
	for _, e := range o.Funding.Events {
		if e.Kind != "deposit" {
			return "", errors.New("option lifecycle requires dedicated funding without trading or reservations")
		}
	}
	snap, err := account.SnapshotCapped(o.Funding, nil, o.Chain.AsOf)
	if err != nil {
		return "", err
	}
	if len(snap.Positions) != 0 || len(snap.Pending) != 0 || snap.Halted || !positivePaperDecimal(snap.SettledCash) {
		return "", errors.New("option funding unavailable")
	}
	return snap.SettledCash, nil
}
