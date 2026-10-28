package routing

import (
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const OwnedSocialResearchSource = "owned-social-research"

// DispatchSocialResearch is called by the source owner within its transaction.
// Its inputs are reconstructed from the retained journal, not public routing
// commands. The only resulting behavior is research on an unverified assertion.
func DispatchSocialResearch(s *domain.RoutingState, i domain.RoutingInput, requestSHA string, maxAge int64) (*domain.RoutingState, domain.TriggerDefinition, domain.RoutingReceipt, error) {
	if i.Source != OwnedSocialResearchSource || i.Kind != "event" || i.EventName != "social_research_requested" || !digestPattern.MatchString(requestSHA) || ValidateInput(i) != nil {
		return s, domain.TriggerDefinition{}, domain.RoutingReceipt{}, ErrInvalid
	}
	id := "social-owned-" + workerplan.Digest([]byte(requestSHA+"/"+i.Symbol))
	d := domain.TriggerDefinition{ID: id, Owner: OwnedSocialResearchSource, CaseKey: id, Behavior: "research", Condition: domain.Condition{Kind: "event", EventName: i.EventName}, ValidFrom: i.ReceivedAt, ValidUntil: i.ReceivedAt.Add(time.Second), RepeatPolicy: "once", Requirements: domain.SourceRequirements{Source: i.Source, Symbol: i.Symbol, EvidenceClass: i.EvidenceClass, MaxAgeSeconds: maxAge}}
	next, err := register(s, d, true)
	if err != nil {
		return s, d, domain.RoutingReceipt{}, err
	}
	next, receipt, err := Apply(next, i)
	if err != nil {
		return s, d, receipt, err
	}
	if len(receipt.Matches) != 1 || receipt.Matches[0].DefinitionID != id || receipt.Matches[0].Transition != "opened" || receipt.Duplicate {
		return s, d, receipt, ErrConflict
	}
	return next, d, receipt, nil
}
