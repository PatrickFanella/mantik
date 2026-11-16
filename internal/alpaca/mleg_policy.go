package alpaca

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"slices"
	"strings"
)

const optionQuarantine = "paper-intent-mleg-quarantine.json"
const optionObservationPrefix = "paper-intent-mleg-observation-"
const maxOptionObservations = 1024

// optionPremiumAllowance deliberately ignores credits and price improvement.
// Callers reserve requested units until terminal reconciliation, then charge
// filled units. Separate orders can never spend one another's credits.
func optionPremiumAllowance(limit string, units int) *big.Rat {
	n, _ := new(big.Rat).SetString(limit)
	if n.Sign() < 0 {
		return new(big.Rat)
	}
	return n.Mul(n, big.NewRat(int64(units)*100, 1))
}

func optionFeePool(o MLegOpen) *big.Rat {
	q, _ := optionInteger(o.Order.Qty)
	total := 0
	for _, l := range o.Order.Legs {
		ratio, _ := optionInteger(l.RatioQty)
		total += q * ratio
	}
	// Retained policy identity fixes this modeled allowance, including both whole
	// and fallback rounds. It is not a bound on actual live regulatory fees.
	return new(big.Rat).Mul(big.NewRat(65, 100), big.NewRat(int64(total)*3, 1))
}

func (p *PaperTrader) optionOpeningFunding(o MLegOpen) error {
	cash, err := o.LocalFunding(p.account)
	if err != nil {
		return err
	}
	q, _ := optionInteger(o.Order.Qty)
	need := optionPremiumAllowance(o.Order.LimitPrice, q)
	need.Add(need, optionFeePool(o))
	available, _ := new(big.Rat).SetString(cash)
	if need.Cmp(available) > 0 {
		return errors.New("opening package and modeled fees exceed dedicated settled funding")
	}
	return nil
}

// atomicOptionExecution accepts only exact decimal evidence. Rounded averages
// that do not reconcile exactly provide no authority for subsequent actions.
func atomicOptionExecution(o OptionPaperOrder, r optionRequest) error {
	f, _ := filledContracts(o)
	limit, _ := new(big.Rat).SetString(r.LimitPrice)
	if r.OrderClass == "mleg" {
		net := new(big.Rat)
		for i, leg := range o.Legs {
			n, _ := filledContracts(leg)
			ratio, _ := optionInteger(r.Legs[i].RatioQty)
			if n != f*ratio {
				return errors.New("nonproportional atomic option fills")
			}
			if n == 0 {
				continue
			}
			price, _ := new(big.Rat).SetString(*leg.FilledAvgPrice)
			signed := int64(n)
			if leg.Side == "sell" {
				signed = -signed
			}
			net.Add(net, new(big.Rat).Mul(price, big.NewRat(signed, 1)))
		}
		if f > 0 {
			avg, _ := new(big.Rat).SetString(*o.FilledAvgPrice)
			if new(big.Rat).Mul(avg, big.NewRat(int64(f), 1)).Cmp(net) != 0 {
				return errors.New("parent and child executed net disagree; exact precision required")
			}
			if avg.Cmp(limit) > 0 {
				return errors.New("executed net exceeds signed package limit")
			}
		}
	} else if f > 0 {
		avg, _ := new(big.Rat).SetString(*o.FilledAvgPrice)
		if r.Side == "buy" && avg.Cmp(limit) > 0 || r.Side == "sell" && avg.Cmp(limit) < 0 {
			return errors.New("simple option execution violates transmitted limit")
		}
	}
	return nil
}

type optionObservation struct {
	Request optionRequest
	Order   OptionPaperOrder
}

func optionObservationName(n int) string {
	return fmt.Sprintf("%s%08d.json", optionObservationPrefix, n)
}

func optionProgress(old, next OptionPaperOrder) error {
	a, _ := filledContracts(old)
	b, _ := filledContracts(next)
	if old.ID != next.ID || b < a || optionTerminal(old.Status) && (b != a || !optionTerminal(next.Status)) {
		return errors.New("option identity changed or execution regressed")
	}
	if a > 0 && a == b && (old.FilledAvgPrice == nil || next.FilledAvgPrice == nil || !samePaperDecimal(*old.FilledAvgPrice, *next.FilledAvgPrice)) {
		return errors.New("option price changed without additional fills")
	}
	if len(old.Legs) != len(next.Legs) {
		return errors.New("option child custody changed")
	}
	for i := range old.Legs {
		if err := optionProgress(old.Legs[i], next.Legs[i]); err != nil {
			return err
		}
	}
	return nil
}

func (p *PaperTrader) quarantineOption(cause error) error {
	// Raw offending provider bytes are already retained by request(). This durable
	// fence also covers malformed local custody, across restart and rollover.
	return errors.Join(ErrOptionQuarantined, cause, p.journal.writeOnce(optionQuarantine, []byte(`{"state":"quarantined"}`)))
}

// optionObservations audits the small, append-only custody history. Every record
// remains bound to its original immutable intent. Only changed observations add
// records; a finite history limit fails closed rather than discarding authority.
func (p *PaperTrader) optionObservations() (map[string]optionObservation, int, error) {
	latest := map[string]optionObservation{}
	if _, err := p.journal.read(optionQuarantine, MaxBytes); err == nil {
		return nil, 0, ErrOptionQuarantined
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	names, err := directoryNames(p.journal.root)
	if err != nil {
		return nil, 0, err
	}
	slices.Sort(names)
	seq := 0
	for _, name := range names {
		if !strings.HasPrefix(name, optionObservationPrefix) {
			continue
		}
		seq++
		if seq > maxOptionObservations || name != optionObservationName(seq) {
			return nil, 0, errors.New("invalid option custody sequence")
		}
		raw, err := p.journal.read(name, MaxBytes)
		if err != nil {
			return nil, 0, err
		}
		var record optionObservation
		if json.Unmarshal(raw, &record) != nil {
			return nil, 0, errors.New("malformed option custody")
		}
		intent, err := p.journal.read(optionIntentName(record.Request.ClientOrderID), MaxBytes)
		if errors.Is(err, os.ErrNotExist) {
			open, e := p.retainedOpen()
			if e != nil || record.Request.ClientOrderID != open.Order.ClientOrderID {
				return nil, 0, errors.New("unbound option custody")
			}
			intent, err = json.Marshal(optionRequest{MLegOrder: open.Order})
		}
		request, e := json.Marshal(record.Request)
		if err != nil || e != nil || !bytes.Equal(intent, request) {
			return nil, 0, errors.New("custody differs from retained intent")
		}
		order, e := json.Marshal(record.Order)
		if e != nil {
			return nil, 0, e
		}
		decoded, e := decodeOptionOrder(order, record.Request)
		if e != nil {
			return nil, 0, e
		}
		record.Order = decoded
		if old, ok := latest[record.Request.ClientOrderID]; ok {
			if e := optionProgress(old.Order, decoded); e != nil {
				return nil, 0, e
			}
		}
		latest[record.Request.ClientOrderID] = record
	}
	return latest, seq, nil
}

func (p *PaperTrader) optionMutationAuthority() error {
	open, err := p.retainedOpen()
	if err != nil {
		return err
	}
	if open.ExecutionPolicy != PaperAtomicNetV2 {
		return ErrOptionPolicy
	}
	if err := p.optionPendingAuthority(); err != nil {
		return err
	}
	if _, _, err := p.optionObservations(); err != nil {
		return p.quarantineOption(err)
	}
	return nil
}

func (p *PaperTrader) optionPendingAuthority() error {
	names, err := directoryNames(p.journal.root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasPrefix(name, optionReplyPendingPrefix) {
			return p.quarantineOption(errors.New("option reply classification interrupted; inspection required"))
		}
	}
	return nil
}

func (p *PaperTrader) optionObserve(raw []byte, r optionRequest) (OptionPaperOrder, error) {
	pending := p.optionReply
	o, err := p.optionRetainObservation(raw, r)
	if pending != nil && pending.ClientOrderID == r.ClientOrderID && pending.ResponseSHA256 == Digest(string(raw)) {
		classified := err == nil
		if !classified {
			_, fenceErr := p.journal.read(optionQuarantine, MaxBytes)
			classified = fenceErr == nil
		}
		if classified {
			err = errors.Join(err, p.finishOptionReply(pending))
		}
	}
	if err == nil {
		// A failed earlier response write can be followed by a coherent GET in
		// the same call. That GET cannot classify the older reply or authorize a
		// closed declaration before the caller reaches another mutation fence.
		err = p.optionPendingAuthority()
	}
	return o, err
}

func (p *PaperTrader) optionRetainObservation(raw []byte, r optionRequest) (OptionPaperOrder, error) {
	// GET recovery still retains raw replies when already quarantined, but cannot
	// restore authority with a later convenient response.
	latest, seq, err := p.optionObservations()
	if err != nil {
		return OptionPaperOrder{}, p.quarantineOption(err)
	}
	o, err := decodeOptionOrder(raw, r)
	if err != nil {
		return o, p.quarantineOption(err)
	}
	record := optionObservation{Request: r, Order: o}
	body, err := json.Marshal(record)
	if err != nil {
		return o, err
	}
	if old, ok := latest[r.ClientOrderID]; ok {
		if err := optionProgress(old.Order, o); err != nil {
			return o, p.quarantineOption(err)
		}
		prev, err := json.Marshal(old)
		if err != nil {
			return o, err
		}
		if bytes.Equal(prev, body) {
			return o, nil
		}
	}
	// Legacy/unknown work is inspection only; it cannot acquire new authority.
	open, err := p.retainedOpen()
	if err != nil {
		return o, err
	}
	if open.ExecutionPolicy != PaperAtomicNetV2 {
		return o, nil
	}
	if seq >= maxOptionObservations {
		return o, p.quarantineOption(errors.New("option custody history full"))
	}
	if err := p.journal.writeOnce(optionObservationName(seq+1), body); err != nil {
		return o, err
	}
	return o, nil
}

// Existing GET recovery requires no new authority. A new external mutation must
// have room for its resulting observation before any intent or wire effect.
func (p *PaperTrader) optionMutationCapacity() error {
	_, n, err := p.optionObservations()
	if err != nil {
		return p.quarantineOption(err)
	}
	if n >= maxOptionObservations {
		return errors.Join(ErrOptionQuarantined, errors.New("option custody history full; inspection only"))
	}
	return nil
}

// Dedicated option journals cannot use the stock API to bypass option custody.
func (p *PaperTrader) requireStockJournal() error {
	if raw, err := p.journal.read(optionLifecycle, MaxBytes); err == nil {
		// This filename is also the ordinary intent for a valid stock client ID.
		var stock StockOrder
		if json.Unmarshal(raw, &stock) == nil && stock.ClientOrderID == "mleg-lifecycle" && stock.Validate() == nil {
			return nil
		}
		return errors.New("dedicated option journal requires option lifecycle actions")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validOptionRequest(r optionRequest) bool {
	if r.OrderClass == "mleg" {
		return r.MLegOrder.Validate() == nil && r.Symbol == "" && r.Side == "" && r.PositionIntent == ""
	}
	q, ok := optionInteger(r.Qty)
	return ok && q > 0 && paperID.MatchString(r.ClientOrderID) && r.OrderClass == "simple" && r.Type == "limit" && slices.Contains([]string{"day", "gtc"}, r.TimeInForce) && positivePaperDecimal(r.LimitPrice) && len(r.Legs) == 0 && standardOption.MatchString(r.Symbol) && (r.Side == "buy" || r.Side == "sell") && r.PositionIntent == r.Side+"_to_close"
}
