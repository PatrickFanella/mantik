package storage

import (
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// A budget is derived from owner policy and a finalized proposal. It is not an
// order, reservation, fill, admission or permission to mutate an account.
type OwnedExecutionCost struct {
	Method              string                  `json:"method"`
	DecisionID          string                  `json:"decision_id"`
	DecisionSHA256      string                  `json:"decision_sha256"`
	ModelSHA256         string                  `json:"model_sha256"`
	QuoteInputID        string                  `json:"quote_input_id"`
	ReferencePriceCents int64                   `json:"reference_price_cents"`
	LimitCents          int64                   `json:"limit_cents"`
	Quantity            int64                   `json:"quantity"`
	GrossCeilingCents   int64                   `json:"gross_ceiling_cents"`
	FeeCeilingCents     int64                   `json:"fee_ceiling_cents"`
	MaxDebitCents       int64                   `json:"max_debit_cents"`
	ValidUntil          time.Time               `json:"valid_until"`
	ProtectiveFunding   *OwnedProtectiveFunding `json:"protective_funding,omitempty"`
	SHA256              string                  `json:"sha256"`
}

// This bounds only fees that can exceed sale proceeds under this exact method.
// A funded execution owner must keep this cash separate from entry reservations.
type OwnedProtectiveFunding struct {
	Method                    string `json:"method"`
	ExitShortfallCeilingCents int64  `json:"exit_shortfall_ceiling_cents"`
	TotalFundingCents         int64  `json:"total_funding_cents"`
}

func executionCostWithProtection(policy allocation.PolicyRecord, decision PaperDecisionRecord, strategy paperstrategy.Record, until time.Time) (OwnedExecutionCost, error) {
	c, err := executionCost(policy, decision, strategy, until)
	if err != nil {
		return c, err
	}
	shortfall, err := paperfill.ExitShortfallCeiling(*policy.Command.ExecutionModel, c.Quantity)
	if err != nil || c.MaxDebitCents > paperfill.MaxMoneyCents-shortfall {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	c.ProtectiveFunding = &OwnedProtectiveFunding{Method: "whole-share-entry-and-exit-shortfall-v1", ExitShortfallCeilingCents: shortfall, TotalFundingCents: c.MaxDebitCents + shortfall}
	c.SHA256 = ""
	c.SHA256 = workerplan.Hash(c)
	return c, nil
}

func executionCost(policy allocation.PolicyRecord, decision PaperDecisionRecord, strategy paperstrategy.Record, until time.Time) (OwnedExecutionCost, error) {
	m := policy.Command.ExecutionModel
	if m == nil || m.Validate() != nil {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	d := strategy.Command.Definition
	price, quantity := decision.Evaluation.PriceCents, d.Quantity
	if price < 1 || price > paperfill.MaxPriceCents || quantity < 1 || quantity > paperfill.MaxQuantity {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	// Limit orders must retain the strategy's entry and gross-notional caps.
	// The ceiling permits modeled adverse slippage only within those bounds.
	adverse := (price*(10000+m.SlippageBPS) + 9999) / 10000
	limit := min(adverse, d.EntryAtOrBelowCents, d.Safety.MaxNotionalCents/quantity)
	if limit < price {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	// One-share partials maximize upward fee rounding and fixed commissions.
	// Whole shares bound the number of fills by the strategy's quantity.
	feePerShare := (limit*m.FeeBPS+9999)/10000 + m.CommissionPerFillCents
	if limit > paperfill.MaxMoneyCents/quantity || feePerShare > paperfill.MaxMoneyCents/quantity {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	gross, fees := limit*quantity, feePerShare*quantity
	if gross > paperfill.MaxMoneyCents-fees {
		return OwnedExecutionCost{}, ErrOwnedAllocationInvalid
	}
	c := OwnedExecutionCost{Method: "whole-share-buy-debit-ceiling-v1", DecisionID: decision.Command.ID, DecisionSHA256: decision.SHA256, ModelSHA256: workerplan.Hash(*m), QuoteInputID: decision.Evaluation.Coverage.Quote.InputID, ReferencePriceCents: price, LimitCents: limit, Quantity: quantity, GrossCeilingCents: gross, FeeCeilingCents: fees, MaxDebitCents: gross + fees, ValidUntil: until}
	c.SHA256 = workerplan.Hash(c)
	return c, nil
}
func ownedExecutionCosts(comparison allocation.Comparison, policy allocation.PolicyRecord, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, protective bool) ([]OwnedExecutionCost, error) {
	costs := make([]OwnedExecutionCost, 0, len(comparison.Proposals))
	for _, proposal := range comparison.Proposals {
		decision, ok := retained[proposal.ID]
		if !ok || decision.Record.SHA256 != proposal.DecisionSHA256 {
			return nil, ErrOwnedAllocationConflict
		}
		strategy, err := paperstrategy.Select(strategies, decision.Record.Command.StrategyID)
		if err != nil || strategy.SHA256 != decision.Record.Command.StrategySHA256 {
			return nil, ErrOwnedAllocationConflict
		}
		derive := executionCost
		if protective {
			derive = executionCostWithProtection
		}
		cost, err := derive(policy, decision.Record, strategy, proposal.ValidUntil)
		amount := cost.MaxDebitCents
		if cost.ProtectiveFunding != nil {
			amount = cost.ProtectiveFunding.TotalFundingCents
		}
		if err != nil || amount != proposal.NotionalCents {
			return nil, ErrOwnedAllocationInvalid
		}
		costs = append(costs, cost)
	}
	return costs, nil
}
