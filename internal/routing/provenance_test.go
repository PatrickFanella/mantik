package routing_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

func marketInput() domain.RoutingInput {
	i := domain.RoutingInput{Kind: "event", Source: "example-feed", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at.Add(time.Second), EventName: "market_open", Provenance: &domain.InputProvenance{RecordKey: "session-20260105-open", Revision: 1, RawRef: "artifact:session-20260105", SHA256: strings.Repeat("a", 64), AvailableAt: at, SourceSymbol: "SPY", MappingVersion: "equities-v1", AccessRef: "test-fixture-v1", Quality: "usable"}}
	i.ID = routing.MarketInputID(i)
	return i
}
func marketDefinition() domain.TriggerDefinition {
	d := definition("market-open", domain.Condition{Kind: "event", EventName: "market_open"})
	d.CaseKey = "market-research"
	d.Requirements = domain.SourceRequirements{Source: "example-feed", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}
	return d
}

func TestMarketSourceIsolationAndQuality(t *testing.T) {
	d := marketDefinition()
	s := register(t, nil, d)
	fixture := definition("fixture-open", d.Condition)
	s = register(t, s, fixture)
	for _, test := range []string{"source", "symbol", "evidence", "stale", "quarantined", "missing"} {
		t.Run(test, func(t *testing.T) {
			i := marketInput()
			switch test {
			case "source":
				i.Source = "other-feed"
			case "symbol":
				i.Symbol = "QQQ"
			case "evidence":
				i.EvidenceClass = "recorded_market_unqualified"
			case "stale":
				i.ReceivedAt = at.Add(61 * time.Second)
			case "quarantined", "missing":
				i.Provenance.Quality = test
				i.Provenance.Reason = "coverage_unavailable"
			}
			i.ID = routing.MarketInputID(i)
			next, r := apply(t, s, i)
			if len(r.Matches) != 0 || len(next.Cases) != 0 || len(next.Runtime[d.ID].Facts) != 0 || len(next.Inputs) != 1 {
				t.Fatal("mismatched or unusable input changed condition facts or was lost")
			}
		})
	}
	s, r := apply(t, s, marketInput())
	if len(r.Matches) != 1 || r.Matches[0].DefinitionID != d.ID || s.Cases[0].Behavior != "research" {
		t.Fatal("market input crossed source boundary")
	}
	if len(s.Runtime[fixture.ID].Facts) != 0 {
		t.Fatal("market evidence reached fixture conditions")
	}
	s, r = apply(t, s, marketInput())
	if !r.Duplicate || s.Cases[0].MatchCount != 1 {
		t.Fatal("retry repeated research work")
	}
	i := marketInput()
	i.Provenance.RawRef = "artifact:changed"
	if _, _, err := routing.Apply(s, i); !errors.Is(err, routing.ErrConflict) {
		t.Fatal("provenance overwritten under same identity")
	}
	i = marketInput()
	i.Provenance.Revision = 2
	i.ID = routing.MarketInputID(i)
	s, r = apply(t, s, i)
	if len(s.Inputs) != 2 || len(r.Matches) != 0 || s.Cases[0].MatchCount != 1 {
		t.Fatal("correction rewrote prior work or repeated same occurrence")
	}
}

func TestMarketInputValidationAndAuthority(t *testing.T) {
	for _, test := range []string{"missing", "identity", "digest", "availability_future", "availability_before_occurrence", "mapping", "access", "reason", "fixture_disguise", "promoted_class"} {
		t.Run(test, func(t *testing.T) {
			i := marketInput()
			switch test {
			case "missing":
				i.Provenance = nil
			case "identity":
				i.ID = "arbitrary-retry-id"
			case "digest":
				i.Provenance.SHA256 = "wrong"
			case "availability_future":
				i.Provenance.AvailableAt = i.ReceivedAt.Add(time.Second)
			case "availability_before_occurrence":
				i.Provenance.AvailableAt = at.Add(-time.Second)
			case "mapping":
				i.Provenance.MappingVersion = ""
			case "access":
				i.Provenance.AccessRef = ""
			case "reason":
				i.Provenance.Quality = "missing"
			case "fixture_disguise":
				i.Source = "fixture"
				i.Symbol = "DEMO"
			case "promoted_class":
				i.EvidenceClass = "qualified"
			}
			if routing.ValidateInput(i) == nil {
				t.Fatal("invalid provenance admitted")
			}
		})
	}
	for _, behavior := range []string{"act", "decide"} {
		d := marketDefinition()
		d.Behavior = behavior
		if routing.ValidateDefinition(d) == nil {
			t.Fatal("market evidence acquired non-research authority")
		}
	}
}

func TestExpectedMarketEventRequiresMatchingSource(t *testing.T) {
	d := marketDefinition()
	d.Condition = domain.Condition{Kind: "expected_event_missing", EventName: "market_open", DueAt: at.Add(30 * time.Second)}
	d.RepeatPolicy = "once"
	s := register(t, nil, d)
	i := marketInput()
	i.Source = "other-feed"
	i.ID = routing.MarketInputID(i)
	s, _ = apply(t, s, i)
	next, r, status, err := routing.ApplyTimer(s, d.ID, at.Add(31*time.Second))
	if err != nil || status != "matched" || len(r.Matches) != 1 || len(next.Cases) != 1 {
		t.Fatal("other feed suppressed missing-event obligation")
	}
	s = register(t, nil, d)
	s, _ = apply(t, s, marketInput())
	_, r, status, err = routing.ApplyTimer(s, d.ID, at.Add(31*time.Second))
	if err != nil || status != "event_observed" || len(r.Matches) != 0 {
		t.Fatal("matching timely event failed to suppress obligation")
	}
}
