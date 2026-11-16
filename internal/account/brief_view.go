package account

import (
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
)

// BriefBookView reads finalized balances and lifecycle without supplying marks or
// observing risk. Equity intentionally remains empty until independently valued.
func BriefBookView(s CappedState, at time.Time) (CappedSnapshot, map[string]allocation.Sleeve, error) {
	r, err := replay(s)
	if err != nil {
		return CappedSnapshot{}, nil, err
	}
	if at.Before(s.Config.OpenedAt) || len(s.Events) > 0 && at.Before(s.Events[len(s.Events)-1].At) {
		return CappedSnapshot{}, nil, errors.New("brief precedes finalized history")
	}
	r.funds.Advance(civil(at))
	out := CappedSnapshot{Cash: r.funds.Cash().FloatString(9), SettledCash: r.funds.Settled().FloatString(9), Positions: map[string]string{}, Pending: []BookOrder{}, Deposits: r.deposits, Halted: r.halted || r.risk.Halted, Risk: r.risk, Distribution: r.distribution}
	for a, q := range r.positions {
		out.Positions[a] = q.FloatString(9)
	}
	ids := []string{}
	for id := range r.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		if !o.released && q.Cmp(o.filled) > 0 {
			out.Pending = append(out.Pending, o.order)
		}
	}
	roster := r.sleeves
	if roster == nil {
		roster = map[string]allocation.Sleeve{}
	}
	// A finalized plan may exist even when sizing or submission subsequently
	// refused. Include its declared versions without inventing a budget/action.
	for _, e := range s.Events {
		if e.Incubation != nil {
			for _, v := range e.Incubation.Rebalance.Sleeves {
				if _, ok := roster[v.ID]; !ok {
					roster[v.ID] = v
				}
			}
		}
	}
	return out, roster, nil
}
