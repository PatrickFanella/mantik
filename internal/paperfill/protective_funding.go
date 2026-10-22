package paperfill

// ExitShortfallCeiling bounds the additional cash needed if sale fees exceed
// proceeds. It assumes the method's whole shares, prices of at least one cent,
// fees at most 1000 bps, and at most one fill per share. It supplies no funds or
// execution authority. Missing quotes and unfilled orders remain unresolved.
func ExitShortfallCeiling(m Model, quantity int64) (int64, error) {
	if m.Validate() != nil || quantity < 1 || quantity > MaxQuantity {
		return 0, ErrInvalid
	}
	// For every supported price p, p-ceil(p*fee/10000) is nondecreasing.
	// Its minimum at p=1 is one cent when fee=0 and zero otherwise.
	// Splitting into one-share fills maximizes commissions and fee rounding.
	netBeforeCommission := int64(1)
	if m.FeeBPS > 0 {
		netBeforeCommission = 0
	}
	perShare := max(int64(0), m.CommissionPerFillCents-netBeforeCommission)
	if perShare > MaxMoneyCents/quantity {
		return 0, ErrInvalid
	}
	return perShare * quantity, nil
}
