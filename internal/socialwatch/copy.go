package socialwatch

import (
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const CopyRecordingSchema = "mantik-copy-recording-v1"

// FollowBounds are independent operator limits, never copied source sizing or
// a grant. This owner has no account, qualified admission or execution issuer.
type FollowBounds struct {
	MaxOrderNotionalCents int64 `json:"max_order_notional_cents"`
	MaxExposureCents      int64 `json:"max_exposure_cents"`
	StopDistanceBPS       int64 `json:"stop_distance_bps"`
	MaxHoldingSeconds     int64 `json:"max_holding_seconds"`
}

func (b FollowBounds) validate() bool {
	return b.MaxOrderNotionalCents > 0 && b.MaxOrderNotionalCents <= b.MaxExposureCents && b.MaxExposureCents <= 1000000000000 && b.StopDistanceBPS >= 1 && b.StopDistanceBPS <= 5000 && b.MaxHoldingSeconds >= 1 && b.MaxHoldingSeconds <= 86400
}

// Activity is an operator recording manifest, not an authenticated provider
// order/fill/holding. Kind preserves what the source purports to describe.
type Activity struct {
	Kind              string     `json:"kind"`
	State             string     `json:"state"`
	SourceSymbol      string     `json:"source_symbol"`
	Symbol            string     `json:"symbol"`
	MappingVersion    string     `json:"mapping_version"`
	Side              string     `json:"side"`
	QuantityUnits     int64      `json:"quantity_units"`
	QuantityUnitClaim string     `json:"quantity_unit_claim"`
	PriceCents        int64      `json:"price_cents"`
	OrderRef          string     `json:"order_ref,omitempty"`
	Announcement      *SourceRef `json:"announcement,omitempty"`
}

func (a Activity) validate() bool {
	if len(a.SourceSymbol) < 1 || len(a.SourceSymbol) > 64 || strings.TrimSpace(a.SourceSymbol) != a.SourceSymbol || !symbolPattern.MatchString(a.Symbol) || !sourceID.MatchString(a.MappingVersion) || !slices.Contains([]string{"shares", "contracts", "provider_native"}, a.QuantityUnitClaim) || a.QuantityUnits < 0 || a.QuantityUnits > 1000000 || a.PriceCents < 0 || a.PriceCents > 100000000 || a.OrderRef != "" && !sourceID.MatchString(a.OrderRef) || a.Announcement != nil && !a.Announcement.valid() {
		return false
	}
	switch a.Kind {
	case "claimed_trade":
		return slices.Contains([]string{"declared", "retracted"}, a.State) && slices.Contains([]string{"buy", "sell"}, a.Side) && a.QuantityUnits > 0
	case "observed_order":
		return slices.Contains([]string{"open", "cancelled", "filled", "rejected"}, a.State) && slices.Contains([]string{"buy", "sell"}, a.Side) && a.QuantityUnits > 0
	case "observed_fill":
		return slices.Contains([]string{"reported", "reversed"}, a.State) && slices.Contains([]string{"buy", "sell"}, a.Side) && a.QuantityUnits > 0
	case "observed_holding":
		return a.State == "snapshot" && (slices.Contains([]string{"long", "short"}, a.Side) && a.QuantityUnits > 0 || a.Side == "flat" && a.QuantityUnits == 0) && a.PriceCents == 0 && a.OrderRef == ""
	}
	return false
}

type CopyObservation struct {
	AnnouncementEvidenceClass       string         `json:"announcement_evidence_class"`
	Kind                            string         `json:"kind"`
	State                           string         `json:"state"`
	SourceStatus                    string         `json:"source_status"`
	InstrumentStatus                string         `json:"instrument_status"`
	QuantityStatus                  string         `json:"quantity_status"`
	FollowStatus                    string         `json:"follow_status"`
	AnnouncementStatus              string         `json:"announcement_status"`
	Announcement                    *SourceVersion `json:"announcement,omitempty"`
	ProviderObservationVerified     bool           `json:"provider_observation_verified"`
	AnnouncementCorrelationVerified bool           `json:"announcement_correlation_verified"`
	FinancialEffectAvailable        bool           `json:"financial_effect_available"`
}

func copyGroup(o Original) string {
	a := *o.Activity
	// A repeated source claim cannot acquire independence by using a new capture,
	// source ID, mapping label, prose, observation time or announcement reference.
	return workerplan.Hash(struct {
		Kind, State, SourceSymbol, Symbol, Side, Unit, OrderRef string
		Quantity, Price                                         int64
		OccurredAt                                              time.Time
	}{a.Kind, a.State, a.SourceSymbol, a.Symbol, a.Side, a.QuantityUnitClaim, a.OrderRef, a.QuantityUnits, a.PriceCents, o.OccurredAt.UTC()})
}
func (p *projection) copyObservation(o Original, policy Policy) *CopyObservation {
	a := o.Activity
	if a == nil {
		return nil
	}
	out := &CopyObservation{Kind: a.Kind, State: a.State, SourceStatus: "recorded_provider_observation_unverified", InstrumentStatus: "source_mapping_unverified", QuantityStatus: "source_quantity_units_unverified", FollowStatus: "admission_and_execution_unavailable", AnnouncementStatus: "not_declared"}
	switch policy.Behavior {
	case "research":
		out.FollowStatus = "research_only"
	case "decision_review":
		out.FollowStatus = "decision_review_unbound"
	case "configured_rule":
		out.FollowStatus = "configured_rule_unbound"
	}
	if a.Kind == "claimed_trade" {
		out.SourceStatus = "source_claim_not_observed_trade"
	}
	if a.Announcement != nil {
		out.AnnouncementStatus = "source_reference_unavailable"
		if parent, ok := p.sources[sourceKey(*a.Announcement)]; ok && parent.Source.Kind != "delete" && !parent.RetainedAt.After(o.ObservedAt) && parent.Source.Activity == nil {
			v := sourceVersion(parent)
			out.Announcement = &v
			out.AnnouncementEvidenceClass = parent.Command.EvidenceClass
			out.AnnouncementStatus = "retained_reference_not_verified_correlation"
		}
	}
	return out
}
