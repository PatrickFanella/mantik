package marketdata

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

// AlpacaActions turns retained Alpaca corporate-action snapshots into the
// splits and cash dividends for one ticker. Each action is known from its
// snapshot's AvailableAt. A source ID seen in several snapshots yields one
// action per snapshot; Adjust keeps the latest version known at its clock.
//
// Name changes are skipped: they do not change prices. Any other kind that
// names the ticker, such as a spin-off, merger or stock dividend, fails with
// ErrUnsupportedAction rather than leaving prices silently unadjusted.
func AlpacaActions(snapshots []alpacarecord.CorporateActionSnapshot, symbol string) ([]CorporateAction, error) {
	out := []CorporateAction{}
	for _, s := range snapshots {
		if s.AvailableAt.IsZero() {
			return nil, fmt.Errorf("%w: snapshot %s has no availability time", ErrInvalidAction, s.CaptureID)
		}
		for _, r := range s.Records {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(r.OriginalJSON), &fields); err != nil {
				return nil, fmt.Errorf("%w: record %s is not an object", ErrInvalidAction, r.SourceID)
			}
			if r.Kind == "name_changes" || !names(fields, symbol) {
				continue
			}
			a := CorporateAction{SourceID: r.SourceID, ExDate: text(fields, "ex_date"), KnownAt: s.AvailableAt}
			switch r.Kind {
			case "cash_dividends":
				a.Kind, a.Amount, a.PayDate = "cash_dividend", number(fields, "rate"), text(fields, "payable_date")
			case "forward_splits", "reverse_splits":
				a.Kind, a.From, a.To = "split", number(fields, "old_rate"), number(fields, "new_rate")
			default:
				return nil, fmt.Errorf("%w: %s %s for %s", ErrUnsupportedAction, r.Kind, r.SourceID, symbol)
			}
			if _, err := parse(a); err != nil {
				return nil, fmt.Errorf("record %s: %w", r.SourceID, err)
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// names reports whether any symbol field of the record is the ticker. Alpaca
// uses symbol, and source_symbol, acquirer_symbol and similar for other kinds.
func names(fields map[string]json.RawMessage, symbol string) bool {
	for key, raw := range fields {
		var v string
		if strings.HasSuffix(key, "symbol") && json.Unmarshal(raw, &v) == nil && v == symbol {
			return true
		}
	}
	return false
}

func text(fields map[string]json.RawMessage, key string) string {
	var v string
	_ = json.Unmarshal(fields[key], &v)
	return v
}

// number keeps a JSON number's exact text. A non-number gives "", which the
// action check rejects.
func number(fields map[string]json.RawMessage, key string) string {
	var v json.Number
	if json.Unmarshal(fields[key], &v) != nil {
		return ""
	}
	return v.String()
}
