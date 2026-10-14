// Package routing records trigger applicability and case work. Matching a
// condition never submits an order or supplies a finalized decision.
package routing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

var ErrConflict = errors.New("routing identity conflicts with retained content")
var ErrInvalid = errors.New("invalid routing operation")
var ErrNotFound = errors.New("routing case not found")
var key = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var inputID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func NewState() *domain.RoutingState {
	return &domain.RoutingState{InputContract: "attributed-routing-v1", Definitions: []domain.TriggerDefinition{}, Runtime: map[string]domain.TriggerRuntime{}, Cases: []domain.RoutingCase{}, Inputs: map[string]domain.RetainedRoutingInput{}, Timers: NewTimers()}
}

func copyState(s *domain.RoutingState) (*domain.RoutingState, error) {
	if s == nil {
		return NewState(), nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var result domain.RoutingState
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.InputContract == "" {
		result.InputContract = "attributed-routing-v1"
	}
	if result.Runtime == nil {
		result.Runtime = map[string]domain.TriggerRuntime{}
	}
	if result.Inputs == nil {
		result.Inputs = map[string]domain.RetainedRoutingInput{}
	}
	if result.Timers == nil {
		result.Timers = NewTimers()
	}
	return &result, nil
}

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}
func equal(a, b any) bool {
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && string(left) == string(right)
}

func ValidateDefinition(d domain.TriggerDefinition) error {
	if !key.MatchString(d.ID) || !key.MatchString(d.Owner) || !key.MatchString(d.CaseKey) || !slices.Contains([]string{"research", "decide", "act"}, d.Behavior) || !slices.Contains([]string{"once", "coalesce", "reopen"}, d.RepeatPolicy) || !validTime(d.ValidFrom) || !validTime(d.ValidUntil) || !d.ValidUntil.After(d.ValidFrom) || d.ValidUntil.Sub(d.ValidFrom) > 365*24*time.Hour {
		return errors.New("incomplete or unsupported trigger definition")
	}
	r := d.Requirements
	if !validSource(r.Source, r.Symbol, r.EvidenceClass) || r.MaxAgeSeconds < 1 || r.MaxAgeSeconds > 86400 {
		return errors.New("trigger requires bounded, attributed evidence")
	}
	if r.Source != "fixture" && d.Behavior != "research" {
		return errors.New("market evidence can only open research work")
	}
	if d.Condition.Kind == "all" {
		if len(d.Condition.All) < 2 || len(d.Condition.All) > 4 || d.CombinationWindowSeconds < 1 || d.CombinationWindowSeconds > 86400 {
			return errors.New("combined condition requires 2–4 leaves and a bounded window")
		}
		if !equal(d.Condition, domain.Condition{Kind: "all", All: d.Condition.All}) {
			return errors.New("combined condition contains incompatible fields")
		}
		seen := map[string]bool{}
		for _, condition := range d.Condition.All {
			if err := validateLeaf(condition); err != nil {
				return err
			}
			raw, _ := json.Marshal(condition)
			if seen[string(raw)] {
				return errors.New("duplicate combined leaf")
			}
			seen[string(raw)] = true
		}
	} else {
		if d.CombinationWindowSeconds != 0 {
			return errors.New("combination window applies only to combined conditions")
		}
		if IsTimed(d.Condition) {
			if err := validateTimed(d); err != nil {
				return err
			}
		} else if err := validateLeaf(d.Condition); err != nil {
			return err
		}
	}
	if d.Behavior == "act" && (d.Owner != "operations" || d.SourceDecisionID == "" || d.SubjectCaseID == "" || d.RepeatPolicy != "once") {
		return errors.New("action trigger lacks finalized-decision binding")
	}
	return nil
}

func validateLeaf(c domain.Condition) error {
	var expected domain.Condition
	switch c.Kind {
	case "event":
		if !key.MatchString(c.EventName) {
			return errors.New("event condition requires a name")
		}
		expected = domain.Condition{Kind: c.Kind, EventName: c.EventName}
	case "state_change":
		if !key.MatchString(c.StateKey) || !key.MatchString(c.StateValue) {
			return errors.New("state condition requires key and target")
		}
		expected = domain.Condition{Kind: c.Kind, StateKey: c.StateKey, StateValue: c.StateValue}
	case "threshold":
		if c.PriceCents < 1 || c.PriceCents > 100000000 || !slices.Contains([]string{"at_or_below", "at_or_above", "cross_below", "cross_above"}, c.Comparison) {
			return errors.New("threshold condition requires a supported comparator and price")
		}
		expected = domain.Condition{Kind: c.Kind, Comparison: c.Comparison, PriceCents: c.PriceCents}
	default:
		return errors.New("unsupported condition kind")
	}
	if !equal(c, expected) {
		return errors.New("condition contains incompatible fields")
	}
	return nil
}

func Register(s *domain.RoutingState, d domain.TriggerDefinition) (*domain.RoutingState, error) {
	d.ValidFrom, d.ValidUntil = d.ValidFrom.UTC(), d.ValidUntil.UTC()
	if !d.Condition.DueAt.IsZero() {
		d.Condition.DueAt = d.Condition.DueAt.UTC()
	}
	if err := ValidateOperatorDefinition(d); err != nil {
		return s, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	next, err := copyState(s)
	if err != nil {
		return s, err
	}
	for _, old := range next.Definitions {
		if old.ID == d.ID {
			if !equal(old, d) {
				return s, ErrConflict
			}
			return next, nil
		}
		if old.Owner == d.Owner && old.CaseKey == d.CaseKey && old.Behavior != d.Behavior {
			return s, fmt.Errorf("%w: case key already has a different behavior", ErrInvalid)
		}
	}
	count := 0
	for _, old := range next.Definitions {
		if old.Owner != "operations" {
			count++
		}
	}
	if count >= 32 {
		return s, fmt.Errorf("%w: operator trigger limit reached", ErrInvalid)
	}
	next.Definitions = append(next.Definitions, d)
	next.Runtime[d.ID] = domain.TriggerRuntime{Status: "active", Facts: map[string]domain.ConditionFact{}}
	SyncTimers(next)
	return next, nil
}

func ValidateOperatorDefinition(d domain.TriggerDefinition) error {
	if err := ValidateDefinition(d); err != nil {
		return err
	}
	if d.Behavior == "act" || d.Owner == "operations" || d.SourceDecisionID != "" || d.SubjectCaseID != "" || strings.HasPrefix(d.ID, "future-") {
		return errors.New("operator triggers cannot create action authority or use its reserved identities")
	}
	return nil
}

func ValidateInput(i domain.RoutingInput) error {
	if !inputID.MatchString(i.ID) || !validSource(i.Source, i.Symbol, i.EvidenceClass) || !validTime(i.OccurredAt) || !validTime(i.ReceivedAt) || i.OccurredAt.After(i.ReceivedAt) {
		return errors.New("routing requires a causal, attributed input")
	}
	if err := validateProvenance(i); err != nil {
		return err
	}
	base := domain.RoutingInput{Provenance: i.Provenance, ID: i.ID, Kind: i.Kind, Source: i.Source, Symbol: i.Symbol, EvidenceClass: i.EvidenceClass, OccurredAt: i.OccurredAt, ReceivedAt: i.ReceivedAt}
	switch i.Kind {
	case "quote":
		if i.PriceCents < 1 || i.PriceCents > 100000000 {
			return errors.New("invalid routing quote")
		}
		base.PriceCents = i.PriceCents
	case "event":
		if !key.MatchString(i.EventName) {
			return errors.New("invalid routing event")
		}
		base.EventName = i.EventName
	case "state":
		if !key.MatchString(i.StateKey) || !key.MatchString(i.StateValue) {
			return errors.New("invalid routing state")
		}
		base.StateKey, base.StateValue = i.StateKey, i.StateValue
	default:
		return errors.New("unsupported routing input kind")
	}
	if !equal(i, base) {
		return errors.New("routing input contains incompatible fields")
	}
	return nil
}

func Apply(s *domain.RoutingState, i domain.RoutingInput) (*domain.RoutingState, domain.RoutingReceipt, error) {
	return apply(s, i, true)
}

// ApplyQuote relies on the ingestion transaction's existing observation retry
// fence. It does not consume the separately bounded operator-input namespace.
func ApplyQuote(s *domain.RoutingState, o domain.Observation) (*domain.RoutingState, domain.RoutingReceipt, error) {
	i := domain.RoutingInput{ID: o.ID, Kind: "quote", Source: o.Source, Symbol: o.Symbol, EvidenceClass: "synthetic_unqualified", OccurredAt: o.OccurredAt, ReceivedAt: o.ReceivedAt, PriceCents: o.PriceCents}
	return apply(s, i, false)
}

func apply(s *domain.RoutingState, i domain.RoutingInput, retain bool) (*domain.RoutingState, domain.RoutingReceipt, error) {
	i.OccurredAt, i.ReceivedAt = i.OccurredAt.UTC(), i.ReceivedAt.UTC()
	if i.Provenance != nil {
		p := *i.Provenance
		p.AvailableAt = p.AvailableAt.UTC()
		i.Provenance = &p
	}
	if err := ValidateInput(i); err != nil {
		return s, domain.RoutingReceipt{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	next, err := copyState(s)
	if err != nil {
		return s, domain.RoutingReceipt{}, err
	}
	r := domain.RoutingReceipt{InputID: i.ID, Matches: []domain.RoutingMatch{}, ExpiredDefinitions: []string{}, RejectedDefinitions: []string{}}
	ref := "quote:" + i.ID
	if retain {
		ref = "routing:" + i.ID
		if old, ok := next.Inputs[i.ID]; ok {
			if !equal(old.Input, i) {
				return s, r, ErrConflict
			}
			r = old.Receipt
			r.Duplicate = true
			return next, r, nil
		}
		if len(next.Inputs) >= 4096 {
			return s, r, fmt.Errorf("%w: routing input limit reached", ErrInvalid)
		}
	}
	for _, d := range next.Definitions {
		if err := ValidateDefinition(d); err != nil {
			return s, r, err
		}
		if d.Requirements.Source != i.Source || d.Requirements.Symbol != i.Symbol || d.Requirements.EvidenceClass != i.EvidenceClass || i.Provenance != nil && i.Provenance.Quality != "usable" {
			continue
		}
		runtime := next.Runtime[d.ID]
		if runtime.Facts == nil {
			runtime.Facts = map[string]domain.ConditionFact{}
		}
		if !IsTimed(d.Condition) && !i.ReceivedAt.Before(d.ValidUntil) {
			runtime.Status = "expired"
			next.Runtime[d.ID] = runtime
			r.ExpiredDefinitions = append(r.ExpiredDefinitions, d.ID)
			continue
		}
		if i.ReceivedAt.Before(d.ValidFrom) || !i.OccurredAt.Before(d.ValidUntil) || runtime.Status == "expired" || d.RepeatPolicy == "once" && runtime.Fired || i.ReceivedAt.Sub(i.OccurredAt) > time.Duration(d.Requirements.MaxAgeSeconds)*time.Second {
			continue
		}
		if IsTimed(d.Condition) {
			observeExpected(d, i, ref, runtime.Facts)
			next.Runtime[d.ID] = runtime
			continue
		}
		matched := evaluate(d, i, ref, runtime.Facts)
		if matched {
			match, accepted := routeCase(next, d, i.ReceivedAt, ref)
			if accepted {
				runtime.Fired = true
				if d.RepeatPolicy == "once" {
					runtime.Status = "consumed"
				}
				r.Matches = append(r.Matches, match)
			} else {
				r.RejectedDefinitions = append(r.RejectedDefinitions, d.ID)
			}
		}
		next.Runtime[d.ID] = runtime
	}
	if retain {
		next.Inputs[i.ID] = domain.RetainedRoutingInput{Input: i, Receipt: r}
	}
	return next, r, nil
}

func evaluate(d domain.TriggerDefinition, i domain.RoutingInput, ref string, facts map[string]domain.ConditionFact) bool {
	leaves := []domain.Condition{d.Condition}
	combined := d.Condition.Kind == "all"
	if combined {
		leaves = d.Condition.All
	}
	anyEdge := false
	all := true
	for index, c := range leaves {
		path := strconv.Itoa(index)
		fact := facts[path]
		relevant := c.Kind == "threshold" && i.Kind == "quote" || c.Kind == "event" && i.Kind == "event" && c.EventName == i.EventName || c.Kind == "state_change" && i.Kind == "state" && c.StateKey == i.StateKey
		edge := false
		if relevant && (fact.LastOccurredAt.IsZero() || i.OccurredAt.After(fact.LastOccurredAt)) && !i.ReceivedAt.Before(fact.LastReceivedAt) {
			switch c.Kind {
			case "event":
				fact.Satisfied, edge = true, true
			case "state_change":
				edge = fact.LastState != "" && fact.LastState != i.StateValue && i.StateValue == c.StateValue
				if edge {
					fact.Satisfied = true
				} else if i.StateValue != c.StateValue {
					fact.Satisfied = false
				}
				fact.LastState = i.StateValue
			case "threshold":
				switch c.Comparison {
				case "at_or_below":
					edge = i.PriceCents <= c.PriceCents
				case "at_or_above":
					edge = i.PriceCents >= c.PriceCents
				case "cross_below":
					edge = fact.LastPrice > c.PriceCents && i.PriceCents <= c.PriceCents
					fact.Satisfied = edge || fact.Satisfied && i.PriceCents <= c.PriceCents
				case "cross_above":
					edge = fact.LastPrice > 0 && fact.LastPrice < c.PriceCents && i.PriceCents >= c.PriceCents
					fact.Satisfied = edge || fact.Satisfied && i.PriceCents >= c.PriceCents
				}
				if c.Comparison == "at_or_below" || c.Comparison == "at_or_above" {
					fact.Satisfied = edge
				}
				fact.LastPrice = i.PriceCents
			}
			fact.LastOccurredAt, fact.LastReceivedAt = i.OccurredAt, i.ReceivedAt
			if edge {
				fact.At, fact.InputRef = i.ReceivedAt, ref
			}
			facts[path] = fact
		}
		if !combined {
			return edge
		}
		anyEdge = anyEdge || edge
		all = all && fact.Satisfied && !fact.At.IsZero() && !fact.At.After(i.ReceivedAt) && i.ReceivedAt.Sub(fact.At) <= time.Duration(d.CombinationWindowSeconds)*time.Second
	}
	return all && anyEdge
}

func routeCase(s *domain.RoutingState, d domain.TriggerDefinition, at time.Time, ref string) (domain.RoutingMatch, bool) {
	latest := -1
	count := 0
	for index, c := range s.Cases {
		if c.Behavior == d.Behavior {
			count++
		}
		if c.Owner == d.Owner && c.CaseKey == d.CaseKey {
			latest = index
		}
	}
	transition := "opened"
	generation := 1
	if latest >= 0 {
		c := &s.Cases[latest]
		if at.Before(c.UpdatedAt) {
			return domain.RoutingMatch{}, false
		}
		if c.Status == "open" {
			c.MatchCount++
			c.UpdatedAt, c.LastInputRef, c.LastDefinitionID = at, ref, d.ID
			return domain.RoutingMatch{DefinitionID: d.ID, CaseID: c.ID, Transition: "updated"}, true
		}
		if d.RepeatPolicy != "reopen" {
			return domain.RoutingMatch{}, false
		}
		transition, generation = "reopened", c.Generation+1
	}
	limit := 128
	if d.Behavior == "act" {
		limit = 4
	}
	if count >= limit {
		return domain.RoutingMatch{}, false
	}
	hash := sha256.Sum256([]byte(d.Owner + "/" + d.CaseKey))
	id := fmt.Sprintf("case-%x-g%d", hash[:16], generation)
	c := domain.RoutingCase{ID: id, Owner: d.Owner, CaseKey: d.CaseKey, Behavior: d.Behavior, Generation: generation, Status: "open", OpenedAt: at, UpdatedAt: at, SourceDecisionID: d.SourceDecisionID, SubjectCaseID: d.SubjectCaseID, MatchCount: 1, LastInputRef: ref, LastDefinitionID: d.ID}
	s.Cases = append(s.Cases, c)
	return domain.RoutingMatch{DefinitionID: d.ID, CaseID: id, Transition: transition}, true
}

func Close(s *domain.RoutingState, id, owner string, at time.Time) (*domain.RoutingState, error) {
	if !validTime(at) {
		return s, fmt.Errorf("%w: case closure requires explicit time", ErrInvalid)
	}
	at = at.UTC()
	next, err := copyState(s)
	if err != nil {
		return s, err
	}
	for index := range next.Cases {
		c := &next.Cases[index]
		if c.ID != id {
			continue
		}
		if c.Owner != owner || c.Behavior == "act" || at.Before(c.UpdatedAt) {
			return s, fmt.Errorf("%w: case closure does not match current ownership/time", ErrInvalid)
		}
		if c.Status == "closed" {
			return next, nil
		}
		c.Status, c.UpdatedAt = "closed", at
		return next, nil
	}
	return s, ErrNotFound
}
