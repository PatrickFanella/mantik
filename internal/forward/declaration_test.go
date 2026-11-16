package forward

import (
	"testing"
	"time"
)

func TestForwardExactClocks(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r := Record{Declaration: Declaration{WindowStart: start, WindowEnd: start.Add(30 * 24 * time.Hour), OutcomeDeadline: start.Add(60 * 24 * time.Hour), EligibilityTTLSeconds: 28 * 86400, MaxHoldingSeconds: 28 * 86400, MaxSearchTrials: 8}}
	opened := start.Add(time.Hour)
	for _, tc := range []struct {
		name                    string
		at                      time.Time
		entry, outcome, expired bool
	}{
		{"before", start.Add(-time.Nanosecond), false, false, false},
		{"start", start, true, true, false},
		{"three-weeks", start.Add(21 * 24 * time.Hour), true, true, false},
		{"ttl-before", start.Add(28*24*time.Hour - time.Nanosecond), true, true, false},
		{"ttl-exact", start.Add(28 * 24 * time.Hour), false, true, false},
		{"hold-before", opened.Add(28*24*time.Hour - time.Nanosecond), false, true, false},
		{"hold-exact", opened.Add(28 * 24 * time.Hour), false, true, true},
		{"tail-before", r.Declaration.OutcomeDeadline.Add(-time.Nanosecond), false, true, true},
		{"tail-exact", r.Declaration.OutcomeDeadline, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := r.Clocks(tc.at, 8, opened)
			if v.EntryEligible != tc.entry || v.OutcomeOpen != tc.outcome || v.HoldExpired != tc.expired || v.QualificationCredit {
				t.Fatal(v)
			}
		})
	}
	r.Declaration.EligibilityTTLSeconds = 40 * 86400
	if r.Clocks(r.Declaration.WindowEnd, 8, time.Time{}).EntryEligible {
		t.Fatal("window end must win over TTL")
	}
	if r.Clocks(start, 9, time.Time{}).EntryEligible {
		t.Fatal("registry bound exceeded")
	}
}
