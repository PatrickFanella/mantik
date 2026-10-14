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
	if s != FixtureStrategy() {
		return Entry{}, fmt.Errorf("incompatible configuration for immutable strategy %q", s.ID)
	}
	return ResolveStrategy(s)
}

const HalfSizeID = "demo-half-size-v1"

func Strategy(id string) (domain.StrategyVersion, error) {
	s := FixtureStrategy()
	switch id {
	case s.ID:
	case HalfSizeID:
		s.ID, s.Quantity, s.Safety.MaxNotionalCents = HalfSizeID, 5, 50000
		s.Hypothesis.ID = "demo-half-size-hypothesis-v1"
	default:
		return domain.StrategyVersion{}, fmt.Errorf("unknown built-in strategy %q", id)
	}
	return s, nil
}

func StrategyEntry(id string) (Entry, error) {
	if _, err := Strategy(id); err != nil {
		return Entry{}, err
	}
	e := FixtureEntry()
	e.ID = id
	return e, nil
}

func ResolveStrategy(s domain.StrategyVersion) (Entry, error) {
	expected, err := Strategy(s.ID)
	if err != nil {
		return Entry{}, err
	}
	if s != expected {
		return Entry{}, fmt.Errorf("incompatible configuration for immutable strategy %q", s.ID)
	}
	e, err := StrategyEntry(s.ID)
	if err != nil {
		return Entry{}, err
	}
	r, err := New(e)
	if err != nil {
		return Entry{}, err
	}
	return r.Resolve(s.ID, ContractVersion)
}
