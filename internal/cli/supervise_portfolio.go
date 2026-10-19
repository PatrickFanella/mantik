package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type portfolioReadback struct {
	ReceiverID string
	Unbound    bool
	Capital    storage.FixtureCapitalReadback
	Account    paperaccount.Account
}
type portfolioFetch func(context.Context) (portfolioReadback, error)

func (p portfolioReadback) validate() error {
	if !storage.ValidReceiverID(p.ReceiverID) {
		return fixturecapital.ErrInvalid
	}
	if p.Unbound {
		if !reflect.DeepEqual(p.Capital, storage.FixtureCapitalReadback{}) || p.Account != (paperaccount.Account{}) {
			return fixturecapital.ErrInvalid
		}
		return nil
	}
	if p.Capital.Capital.ReceiverID != p.ReceiverID {
		return fixturecapital.ErrConflict
	}
	return p.Capital.Validate(p.Account)
}

func fetchPortfolio(ctx context.Context, base string) (portfolioReadback, error) {
	var v storage.FixtureCapitalReadback
	err := requestWithQuery(ctx, base, "/v1/fixture-capital", nil, &v, nil, 256<<10)
	var failure *httpFailure
	if errors.As(err, &failure) && failure.Status == 404 && failure.Code == service.FixtureCapitalUnboundCode {
		var c paperaccount.Catalog
		if err := requestWithQuery(ctx, base, "/v1/paper-accounts", nil, &c, nil, 256<<10); err != nil {
			return portfolioReadback{}, err
		}
		if err := c.Validate(); err != nil {
			return portfolioReadback{}, err
		}
		return portfolioReadback{ReceiverID: c.ReceiverID, Unbound: true}, nil
	}
	if err != nil {
		return portfolioReadback{}, err
	}
	a, err := validateCapitalReadback(ctx, base, v)
	if err != nil {
		return portfolioReadback{}, err
	}
	return portfolioReadback{ReceiverID: a.ReceiverID, Account: a, Capital: v}, nil
}

const portfolioSupervisionHelp = "Commands (then Enter): n next commitments | p previous | r refresh | d ACTION_ID displayed commitment | q quit"

func renderPortfolio(out io.Writer, p portfolioReadback, offset, limit int, stale bool) error {
	label := "READBACK"
	if stale {
		label = "STALE"
	}
	if _, err := fmt.Fprintf(out, "%s SYNTHETIC FIXTURE PORTFOLIO\nReceiver: %q\n", label, p.ReceiverID); err != nil {
		return err
	}
	if p.Unbound {
		_, err := fmt.Fprintln(out, "UNBOUND: no fixture capital account is active.\nRefresh after explicit activation elsewhere; this view is read-only.")
		return err
	}
	s := p.Capital.Capital
	mode := "durable"
	if !p.Capital.Durable {
		mode = "memory (no restart retention)"
	}
	if _, err := fmt.Fprintf(out, "Account: %q | Storage: %s\nUSD CENTS: available=%d reserved=%d position_at_cost=%d realized_fixture_pnl=%d\nOpening=%d | committed_capital_ceiling=%d\nBasis: synthetic zero-cost fills, position valued at entry cost; unqualified.\n", s.AccountID, mode, s.AvailableCashCents, s.ReservedCashCents, s.PositionCostCents, s.RealizedPnLCents, s.OpeningCashCents, s.CapitalLimitCents); err != nil {
		return err
	}
	if s.Position == nil {
		if _, err := fmt.Fprintln(out, "Position: none"); err != nil {
			return err
		}
	} else {
		p := s.Position
		if _, err := fmt.Fprintf(out, "Position: quantity=%d entry_price_cents=%d case=%q strategy=%q entry_action=%q\n", p.Entry.Quantity, p.Entry.PriceCents, p.CaseID, p.StrategyVersionID, p.EntryActionID); err != nil {
			return err
		}
	}
	end := min(offset+limit, len(s.Commitments))
	start := offset + 1
	if len(s.Commitments) == 0 {
		start = 0
	}
	if _, err := fmt.Fprintf(out, "COMMITMENTS %d..%d OF %d\n", start, end, len(s.Commitments)); err != nil {
		return err
	}
	for _, c := range s.Commitments[offset:end] {
		if _, err := fmt.Fprintf(out, "%s | status=%q amount_cents=%d code=%q\n", c.ActionID, c.Status, c.AmountCents, c.Code); err != nil {
			return err
		}
	}
	return nil
}

// supervisePortfolio owns only its stdin handle. Refresh is explicit; local
// navigation/details issue no requests and never mutate account or worker state.
func supervisePortfolio(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch portfolioFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	if err := (storage.ActivityQuery{Limit: limit}).Validate(); err != nil {
		return err
	}
	var page portfolioReadback
	var receiver, accountSHA, accountID string
	var durable bool
	offset := 0
	read, stale, havePage := true, true, false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if read {
			fresh, err := fetch(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = fresh.validate()
			}
			changed := err == nil && (receiver != "" && fresh.ReceiverID != receiver || accountSHA != "" && (fresh.Unbound || fresh.Capital.Capital.AccountID != accountID || fresh.Capital.Capital.AccountSHA256 != accountSHA || fresh.Capital.Durable != durable))
			if changed {
				err = fixturecapital.ErrConflict
			}
			if err != nil {
				stale = true
				message := "STALE: portfolio readback failed or was invalid. Retained figures are not a fresh read; use r to retry or q to quit."
				if changed {
					message = "STALE: portfolio receiver or account identity changed. Quit and reconnect to accept a different owner; r can retry the original owner."
				}
				if _, err := fmt.Fprintln(out, message); err != nil {
					return err
				}
			} else {
				// Detach retained data from the fetcher's buffers/pointers.
				raw, err := json.Marshal(fresh)
				if err != nil {
					return fixturecapital.ErrInvalid
				}
				if err := json.Unmarshal(raw, &page); err != nil {
					return fixturecapital.ErrInvalid
				}
				receiver = fresh.ReceiverID
				if !fresh.Unbound {
					accountSHA, accountID, durable = fresh.Capital.Capital.AccountSHA256, fresh.Capital.Capital.AccountID, fresh.Capital.Durable
				}
				havePage, stale, offset = true, false, 0
				if _, err := fmt.Fprintln(out, "READBACK OK (on demand; not a continuous health check)"); err != nil {
					return err
				}
			}
			read = false
		}
		if havePage {
			if err := renderPortfolio(out, page, offset, limit, stale); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, portfolioSupervisionHelp); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return scanner.Err()
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "q" {
			return nil
		}
		if len(fields) == 1 && fields[0] == "r" {
			read = true
			continue
		}
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: portfolio navigation and details are unavailable until a successful readback."); err != nil {
				return err
			}
			continue
		}
		if page.Unbound {
			if _, err := fmt.Fprintln(out, "UNBOUND: no commitments to inspect; no request sent."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if offset+limit < len(page.Capital.Capital.Commitments) {
				offset += limit
			} else {
				if _, err := fmt.Fprintln(out, "End of commitments; r reads fresh capital."); err != nil {
					return err
				}
			}
		case len(fields) == 1 && fields[0] == "p":
			offset = max(0, offset-limit)
		case len(fields) == 2 && fields[0] == "d":
			found := false
			for _, c := range page.Capital.Capital.Commitments[offset:min(offset+limit, len(page.Capital.Capital.Commitments))] {
				if c.ActionID == fields[1] {
					if _, err := fmt.Fprintln(out, "DISPLAYED FIXTURE COMMITMENT (retained readback; no execution request)"); err != nil {
						return err
					}
					if err := emit(out, c); err != nil {
						return err
					}
					found = true
					break
				}
			}
			if !found {
				if _, err := fmt.Fprintln(out, "Choose an action identity from the displayed page; no request sent."); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown portfolio command; no request sent."); err != nil {
				return err
			}
		}
	}
}
