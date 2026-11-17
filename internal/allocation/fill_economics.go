package allocation

import (
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// ValidateExecution checks retained fill arithmetic and identity, not source
// custody. The execution owner must still replay its original books and quotes.
func (e Effect) ValidateExecution(schema string) error {
	if schema != FinancialLedgerSchema {
		if e.Execution != nil {
			return ErrLedgerConflict
		}
		return nil
	}
	if e.Kind != "fill" && e.Kind != "close" {
		if e.Execution != nil {
			return ErrLedgerConflict
		}
		return nil
	}
	f := e.Execution
	prefix := "entry-fill-"
	if e.Kind == "close" {
		prefix = "exit-fill-"
	}
	if !validHash(e.SHA256) || e.ID != prefix+e.SHA256 {
		return ErrLedgerConflict
	}
	if f == nil || !id(f.OrderID) || f.OrderID != e.DecisionID || !id(f.CommandID) || !validHash(f.SHA256) || !validHash(f.ModelSHA256) || !validHash(f.QuoteSHA256) || !validTime(f.At) || f.Method != paperfill.Method || f.Quantity < 1 || f.Quantity > paperfill.MaxQuantity || f.PriceCents < 1 || f.PriceCents > paperfill.MaxPriceCents || f.Quantity > paperfill.MaxMoneyCents/f.PriceCents || f.FeeCents < 0 || f.FeeCents > paperfill.MaxMoneyCents {
		return ErrLedgerConflict
	}
	if !id(f.QuoteID) || !slices.Contains([]string{"replay", "ongoing_paper", "fixture"}, f.Mode) || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, f.EvidenceClass) || f.MarketUpdateSHA256 != "" && !validHash(f.MarketUpdateSHA256) {
		return ErrLedgerConflict
	}
	copy := *f
	copy.SHA256 = ""
	if workerplan.Hash(copy) != f.SHA256 || f.GrossCents != f.PriceCents*f.Quantity {
		return ErrLedgerConflict
	}
	if e.Kind == "fill" {
		if f.Side != "buy" || f.GrossCents > paperfill.MaxMoneyCents-f.FeeCents || f.DebitCents != f.GrossCents+f.FeeCents || f.NetProceedsCents != 0 || e.AmountCents != f.DebitCents || e.ProceedsCents != 0 {
			return ErrLedgerConflict
		}
	} else if f.Side != "sell" || f.DebitCents != 0 || f.NetProceedsCents != f.GrossCents-f.FeeCents || e.ProceedsCents != f.NetProceedsCents {
		return ErrLedgerConflict
	}
	return nil
}
