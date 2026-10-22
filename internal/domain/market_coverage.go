package domain

import "time"

// CoverageSpec describes evidence required for one regular-session query. It
// cannot authorize execution, change retained quality, or configure readiness.
type MarketCoverageSpec struct {
	Feed                  string `json:"feed"`
	Symbol                string `json:"symbol"`
	EvidenceClass         string `json:"evidence_class"`
	MaxQuoteAgeSeconds    int64  `json:"max_quote_age_seconds"`
	MaxCaptureAgeSeconds  int64  `json:"max_capture_age_seconds"`
	MaxCalendarAgeSeconds int64  `json:"max_calendar_age_seconds"`
}
type MarketQuoteCoverage struct {
	CaptureID              string    `json:"capture_id,omitempty"`
	InputID                string    `json:"input_id,omitempty"`
	ReceivedAt             time.Time `json:"received_at,omitzero"`
	OccurredAt             time.Time `json:"occurred_at,omitzero"`
	Quality                string    `json:"quality"`
	Code                   string    `json:"code"`
	Usable                 bool      `json:"usable"`
	RegularSessionEligible bool      `json:"regular_session_eligible"`
}
type MarketSessionCoverage struct {
	CaptureID  string    `json:"capture_id,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitzero"`
	Date       string    `json:"date"`
	Status     string    `json:"status"`
	Code       string    `json:"code"`
	OpenAt     time.Time `json:"open_at,omitzero"`
	CloseAt    time.Time `json:"close_at,omitzero"`
	NextOpenAt time.Time `json:"next_open_at,omitzero"`
}
type MarketCoverageReport struct {
	Scope                     string                `json:"scope"`
	At                        time.Time             `json:"at"`
	Spec                      MarketCoverageSpec    `json:"spec"`
	Session                   MarketSessionCoverage `json:"session"`
	Quote                     MarketQuoteCoverage   `json:"quote"`
	PriceRequired             bool                  `json:"price_required"`
	RequiredEvidenceSatisfied bool                  `json:"required_evidence_satisfied"`
	CollectorLivenessVerified bool                  `json:"collector_liveness_verified"`
	GlobalReadinessConfigured bool                  `json:"global_readiness_configured"`
	QualificationCredit       bool                  `json:"qualification_credit"`
}
