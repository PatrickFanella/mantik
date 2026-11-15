package strategytrial

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeclarationValidation(t *testing.T) {
	base := Start{ID: "attempt-1", Family: "trend", StrategyID: "strategy-" + strings.Repeat("a", 64), Spec: json.RawMessage(`{"name":"trend"}`), Config: json.RawMessage(`{"cash":"2000"}`), DataSHA256: strings.Repeat("b", 64), EvidenceClass: "historical_market_unqualified", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Start){
		"id":                func(s *Start) { s.ID = "" },
		"family":            func(s *Start) { s.Family = "" },
		"strategy":          func(s *Start) { s.StrategyID = "trend" },
		"data":              func(s *Start) { s.DataSHA256 = "unknown" },
		"spec":              func(s *Start) { s.Spec = json.RawMessage(`null`) },
		"config":            func(s *Start) { s.Config = json.RawMessage(`[]`) },
		"clock":             func(s *Start) { s.StartedAt = time.Time{} },
		"self lineage":      func(s *Start) { s.Parents = []string{s.ID} },
		"duplicate lineage": func(s *Start) { s.Parents = []string{"parent", "parent"} },
		"qualification":     func(s *Start) { s.EvidenceClass = "qualified" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			change(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}
