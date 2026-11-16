// Package mlegpaper binds fixed local OptionBook admission to the paper adapter.
// It never simulates broker fills or reads broker buying power as local equity.
package mlegpaper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
)

type admissionChain struct{ options.Chain }

func (s admissionChain) OptionChain(_ context.Context, asset string, at time.Time) (options.Chain, error) {
	if err := s.Validate(at, asset); err != nil {
		return options.Chain{}, err
	}
	return s.Chain, nil
}
func (s admissionChain) OptionBars(context.Context, string, time.Time) (map[string]options.IndicativeBar, error) {
	return nil, errors.New("broker admission cannot simulate fills")
}

// Open checks OptionBook's fixed funding, structure and Greek rules immediately
// before the initial POST. Retries delegate to GET-only adapter recovery.
func Open(ctx context.Context, p *alpaca.PaperTrader, o alpaca.MLegOpen) (alpaca.OptionPaperOrder, error) {
	return p.OpenMLeg(ctx, o, func() error { return admit(ctx, o, time.Now().UTC()) })
}
func admit(ctx context.Context, o alpaca.MLegOpen, now time.Time) error {
	if o.Chain.AsOf.After(now) || now.Sub(o.Chain.AsOf) > 24*time.Hour || now.Sub(o.Chain.Spot.At) > o.Chain.Assumptions.MaxAge {
		return errors.New("opening evidence unavailable or older than 24 hours")
	}
	// Validate expiry at POST time without changing the retained source cutoff.
	post := o.Chain
	post.AsOf = now
	if err := post.Validate(now, o.Chain.AssetID); err != nil {
		return err
	}
	for _, leg := range o.Structure.Legs {
		found := false
		for _, entry := range o.Chain.Entries {
			if entry.Symbol != leg.Symbol {
				continue
			}
			found = true
			if entry.PriceOccurredAt.IsZero() || entry.PriceOccurredAt.After(now) || now.Sub(entry.PriceOccurredAt) > o.Chain.Assumptions.MaxAge {
				return errors.New("opening option prices exceed declared evidence age")
			}
			break
		}
		if !found {
			return errors.New("opening option evidence missing")
		}
	}
	cash, err := o.LocalFunding(o.Funding.Config.BrokerAccountID)
	if err != nil {
		return err
	}
	b, err := engine.NewOptionBook(ctx, engine.OptionConfig{Source: admissionChain{o.Chain}, Account: o.Funding.Config.Type, StrategyID: "paper-mleg", SettledCash: cash, FeePerContract: .65, SlippageBps: 5}, o.Chain.AsOf)
	if err != nil {
		return err
	}
	result, err := b.Submit(ctx, o.Chain.AsOf, o.Order.ClientOrderID, o.Chain.AssetID, o.Structure)
	if err != nil {
		return err
	}
	if len(result.Reasons) > 0 {
		return fmt.Errorf("option admission refused: %s", strings.Join(result.Reasons, ", "))
	}
	return nil
}
