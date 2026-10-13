package library

import (
	"fmt"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func FixtureStrategy() domain.StrategyVersion {
	return domain.StrategyVersion{
		ID: "demo-threshold-v1", Symbol: "DEMO", EntryAtOrBelowCents: 10000, Quantity: 10,
		Hypothesis: domain.Hypothesis{ID: "demo-hypothesis-v1", ClaimCode: "fixture_threshold", RefutationCode: "fixture_stop", EvidenceClass: "synthetic_unqualified"},
		Safety:     domain.Safety{MaxNotionalCents: 100000, StopCents: 9500, TargetCents: 11000, MaxHoldingSeconds: 3600, MaxQuoteAgeSeconds: 60},
	}
}

func FixtureEntry() Entry {
	return Entry{
		ID: "demo-threshold-v1", ContractVersion: ContractVersion, Kind: "strategy", Implementation: "fixture_threshold_v1",
		Inputs:      []Input{{Kind: "quote", Source: "fixture", Schema: "observation-quote-cents-v1"}},
		Instruments: []string{"DEMO"}, Outputs: []string{"enter", "exit", "abstain"}, EvidenceClass: "synthetic_unqualified",
		SafetyRequirements: append([]string(nil), safetyRequirements...),
		Evaluation: Recipe{ID: "demo-evaluation", Version: "v1", Required: []Check{
			{ID: "quote_freshness", Version: "v1", Actions: []string{"enter", "exit", "abstain"}},
			{ID: "event_order", Version: "v1", Actions: []string{"enter", "exit", "abstain"}},
			{ID: "notional_limit", Version: "v1", Actions: []string{"enter"}},
			{ID: "protective_levels", Version: "v1", Actions: []string{"enter"}},
		}},
	}
}

// ResolveFixture binds the persisted version to its immutable executable config.
// New versions must be explicitly registered rather than reusing an existing ID.
func ResolveFixture(s domain.StrategyVersion) (Entry, error) {
	r, err := New(FixtureEntry())
	if err != nil {
		return Entry{}, err
	}
	e, err := r.Resolve(s.ID, ContractVersion)
	if err != nil {
		return Entry{}, err
	}
	if s != FixtureStrategy() {
		return Entry{}, fmt.Errorf("incompatible configuration for immutable strategy %q", s.ID)
	}
	return e, nil
}
