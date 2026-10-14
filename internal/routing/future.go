package routing

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

func FutureDefinitions(d domain.Decision, symbol string) []domain.TriggerDefinition {
	if d.Action != "enter" {
		return nil
	}
	caseHash := sha256.Sum256([]byte(d.CaseID))
	caseKey := fmt.Sprintf("protect-%x", caseHash[:16])
	var definitions []domain.TriggerDefinition
	for _, level := range []struct {
		name, comparison string
		price            int64
	}{{"stop", "at_or_below", d.Safety.StopCents}, {"target", "at_or_above", d.Safety.TargetCents}} {
		hash := sha256.Sum256([]byte(d.ID + "/" + level.name))
		definitions = append(definitions, domain.TriggerDefinition{ID: fmt.Sprintf("future-%x", hash[:]), Owner: "operations", CaseKey: caseKey, Behavior: "act", Condition: domain.Condition{Kind: "threshold", Comparison: level.comparison, PriceCents: level.price}, ValidFrom: d.FinalizedAt.UTC(), ValidUntil: d.FinalizedAt.Add(time.Duration(d.Safety.MaxHoldingSeconds) * time.Second).UTC(), RepeatPolicy: "once", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: symbol, EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: d.Safety.MaxQuoteAgeSeconds}, SourceDecisionID: d.ID, SubjectCaseID: d.CaseID})
	}
	return definitions
}

// BindFuture accepts only the exact safety conditions derived from a finalized
// registered entry. The ingestion transaction retains these with that decision.
func BindFuture(s *domain.RoutingState, d domain.Decision, symbol string) (*domain.RoutingState, error) {
	strategy, err := library.Strategy(d.StrategyVersionID)
	if err != nil {
		return s, err
	}
	entry, err := library.ResolveStrategy(strategy)
	if err != nil {
		return s, err
	}
	if d.Status != "finalized" || d.Action != "enter" || d.ID == "" || d.CaseID == "" || d.Quantity != strategy.Quantity || d.Safety != strategy.Safety || symbol != strategy.Symbol {
		return s, errors.New("future conditions lack finalized entry authority")
	}
	if err := entry.Admit("enter", d.Evaluations); err != nil {
		return s, err
	}
	expected := FutureDefinitions(d, symbol)
	if !equal(d.FutureConditions, expected) {
		return s, errors.New("decision future conditions differ from its safety contract")
	}
	next, err := copyState(s)
	if err != nil {
		return s, err
	}
	for _, definition := range expected {
		if err := ValidateDefinition(definition); err != nil {
			return s, err
		}
		found := false
		for _, old := range next.Definitions {
			if old.ID == definition.ID {
				if !equal(old, definition) {
					return s, ErrConflict
				}
				found = true
				break
			}
		}
		if !found {
			next.Definitions = append(next.Definitions, definition)
			next.Runtime[definition.ID] = domain.TriggerRuntime{Status: "active", Facts: map[string]domain.ConditionFact{}}
		}
	}
	return next, nil
}

// CloseApplied records that the fixture already enacted its exit. A routed
// action case cannot become a second execution or be reopened by an operator.
func CloseApplied(s *domain.RoutingState, subject string, at time.Time) {
	for index := range s.Cases {
		c := &s.Cases[index]
		if c.Behavior == "act" && c.SubjectCaseID == subject && c.Status == "open" {
			c.Status, c.UpdatedAt = "closed", at.UTC()
		}
	}
}
