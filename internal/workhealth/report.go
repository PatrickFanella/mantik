// Package workhealth describes observed admission evidence, never authority.
package workhealth

import "time"

// Check preserves the reason, observation clock and source identities.
type Check struct {
	Ready        bool      `json:"ready"`
	Status       string    `json:"status"`
	Code         string    `json:"code"`
	At           time.Time `json:"at"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	EvidenceRefs []string  `json:"evidence_refs"`
}

// Report separates entry admission from independently executable safety work.
type Report struct {
	At                  time.Time        `json:"at"`
	Checks              map[string]Check `json:"checks"`
	Work                map[string]Check `json:"work"`
	QualificationCredit bool             `json:"qualification_credit"`
}
