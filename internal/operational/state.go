// Package operational retains structured faults without granting action authority.
package operational

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

// NewState initializes bounded capability worker records.
func NewState() *domain.OperationalState {
	return &domain.OperationalState{Workers: map[string]domain.WorkerHealth{}}
}

// Normalize upgrades existing exception metadata without rewriting audit events.
func Normalize(s *domain.State) {
	if s.Operations == nil {
		s.Operations = NewState()
	}
	if s.Operations.Workers == nil {
		s.Operations.Workers = map[string]domain.WorkerHealth{}
	}
	if s.Routing == nil {
		s.Routing = routing.NewState()
	}
	if s.Routing.Timers == nil {
		s.Routing.Timers = routing.NewTimers()
	}
	for id, e := range s.Routing.Timers.Exceptions {
		if e.Capability == "" {
			e.Capability = "protective_monitoring"
		}
		if e.Severity == "" {
			e.Severity = "critical"
		}
		if len(e.EvidenceRefs) == 0 {
			e.EvidenceRefs = []string{"case:" + e.CaseID, "timer:" + e.DefinitionID}
		}
		if e.LastObservedAt.IsZero() {
			e.LastObservedAt = e.At
		}
		if e.Status == "resolved" && e.TerminalOutcome == "" {
			e.TerminalOutcome = "protective_exit_applied"
		}
		s.Routing.Timers.Exceptions[id] = e
	}
}

func faultID(capability, code, subject string) string {
	hash := sha256.Sum256([]byte(capability + "/" + code + "/" + subject))
	return fmt.Sprintf("exception-%x", hash[:16])
}

// Sync retains detected action, timer, and protection failures and their recovery.
// Its stable fault identities coalesce repeated observations rather than hiding them.
func Sync(s *domain.State, now time.Time) []domain.Event {
	Normalize(s)
	before := map[string]domain.OperationalException{}
	for id, e := range s.Routing.Timers.Exceptions {
		before[id] = e
	}
	active := map[string]bool{}
	upsert := func(capability, code, subject, severity, actionID, definitionID string, at time.Time, refs []string) {
		id := faultID(capability, code, subject)
		e, exists := s.Routing.Timers.Exceptions[id]
		if !exists {
			e = domain.OperationalException{ID: id, CaseID: s.Case.ID, At: at, Code: code, Capability: capability, Severity: severity, ActionID: actionID, DefinitionID: definitionID, EvidenceRefs: refs}
		}
		for _, ref := range refs {
			if !slices.Contains(e.EvidenceRefs, ref) {
				e.EvidenceRefs = append(e.EvidenceRefs, ref)
			}
		}
		e.Status, e.LastObservedAt, e.TerminalOutcome = "open", now.UTC(), ""
		e.ResolvedAt = time.Time{}
		s.Routing.Timers.Exceptions[id] = e
		active[id] = true
	}
	if s.Actions != nil {
		for id, a := range s.Actions.Items {
			code := ""
			switch a.Status {
			case "expired", "rejected":
				code = "action_" + a.Status
			case "accepted", "partial":
				code = "action_" + a.Status
			case "unknown":
				if !now.Before(a.LeaseUntil) {
					code = "action_outcome_unknown"
				}
			}
			if code != "" {
				if a.Intent.Decision.Action == "exit" && s.Case.Exit != nil && (a.Status == "expired" || a.Status == "rejected") {
					continue
				}
				cap, severity := "action_delivery", "warning"
				if a.Intent.Decision.Action == "exit" {
					cap, severity = "protective_monitoring", "critical"
				}
				// A bounded reconciliation can acknowledge a terminal result. It cannot
				// claim an unfulfilled protective exit is now safe.
				idFault := faultID(cap, code, id)
				old := s.Routing.Timers.Exceptions[idFault]
				if (a.Status == "expired" || a.Status == "rejected") && old.TerminalOutcome == "terminal_action_acknowledged" {
					continue
				}
				upsert(cap, code, id, severity, id, a.Intent.TimerID, a.UpdatedAt, []string{"action:" + id, "decision:" + a.Intent.Decision.ID, "quote:" + a.Intent.Quote.ID})
			}
		}
	}
	if s.Case.Status == "open" && s.Case.Entry != nil && s.Case.Exit == nil {
		if _, err := library.ResolveStrategy(s.Strategy); err != nil {
			upsert("protective_monitoring", "invalid_strategy_authority", s.Case.ID, "critical", "", "", now, []string{"case:" + s.Case.ID, "strategy:" + s.Strategy.ID})
		}
		protected, stop, target := false, false, false
		for _, d := range s.Routing.Definitions {
			if d.Behavior != "act" || d.SubjectCaseID != s.Case.ID || d.SourceDecisionID != s.Case.Entry.DecisionID || routing.ValidateDefinition(d) != nil {
				continue
			}
			if d.Condition.Kind == "threshold" && d.Condition.Comparison == "at_or_below" && d.Condition.PriceCents == s.Strategy.Safety.StopCents {
				stop = true
			}
			if d.Condition.Kind == "threshold" && d.Condition.Comparison == "at_or_above" && d.Condition.PriceCents == s.Strategy.Safety.TargetCents {
				target = true
			}
			if d.Condition.Kind != "deadline" || !d.Condition.DueAt.Equal(s.Case.ExitBy) {
				continue
			}
			job, ok := s.Routing.Timers.Jobs[d.ID]
			if !ok {
				continue
			}
			protected = true
			if job.Status != "completed" && now.Sub(job.DueAt) > 15*time.Second {
				upsert("protective_monitoring", "timer_overdue", d.ID, "critical", "", d.ID, job.DueAt, []string{"timer:" + d.ID, "case:" + s.Case.ID})
			}
			if job.Status == "completed" && job.Receipt != nil && job.Receipt.Status == "queued" {
				fulfilled := false
				if s.Actions != nil {
					for _, a := range s.Actions.Items {
						decision := a.Intent.Decision
						if a.Intent.Version == "paper-action-v1" && a.Intent.TimerID == d.ID && a.Intent.Mode == "deadline" && !actions.Terminal(a.Status) && decision.Status == "finalized" && decision.Action == "exit" && decision.ReasonCode == "holding_deadline" && decision.CaseID == s.Case.ID && decision.StrategyVersionID == s.Strategy.ID && decision.Safety == s.Strategy.Safety && decision.Quantity == s.Strategy.Quantity && (a.Status != "pending" || now.Before(decision.ValidUntil)) {
							fulfilled = true
						}
					}
				}
				if !fulfilled {
					upsert("protective_monitoring", "protective_action_unfulfilled", d.ID, "critical", "", d.ID, job.CompletedAt, []string{"timer:" + d.ID, "case:" + s.Case.ID})
				}
			}
		}
		if !protected || !stop || !target {
			upsert("protective_monitoring", "missing_protection", s.Case.ID, "critical", "", "", now, []string{"case:" + s.Case.ID, "decision:" + s.Case.Entry.DecisionID})
		}
	}
	for cap, w := range s.Operations.Workers {
		if w.ErrorCode != "" {
			refs := []string{"worker:" + w.WorkerID}
			if w.EvidenceRef != "" {
				refs = append(refs, w.EvidenceRef)
			}
			upsert(cap, "worker_"+w.ErrorCode, cap, "error", "", "", w.At, refs)
		}
	}
	for id, e := range s.Routing.Timers.Exceptions {
		if e.Status != "open" || active[id] || e.Code == "deadline_price_unavailable" {
			continue
		}
		if _, exists := before[id]; !exists {
			continue
		}
		e.Status, e.ResolvedAt, e.TerminalOutcome = "resolved", now.UTC(), "condition_recovered"
		s.Routing.Timers.Exceptions[id] = e
	}
	events := []domain.Event{}
	for id, e := range s.Routing.Timers.Exceptions {
		old, exists := before[id]
		if exists && old.Status == e.Status {
			continue
		}
		raw, _ := json.Marshal(struct {
			ID, Status string
			At         time.Time
		}{id, e.Status, now.UTC()})
		hash := sha256.Sum256(raw)
		copy := e
		events = append(events, domain.Event{ID: fmt.Sprintf("ops-%x", hash[:16]), Kind: "operations.exception." + e.Status, ExceptionID: id, At: now.UTC(), Exception: &copy})
	}
	slices.SortFunc(events, func(a, b domain.Event) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return events
}

// Report persists worker progress and previously unrecordable outage evidence.
func Report(s *domain.State, r domain.WorkerReport) ([]domain.Event, error) {
	Normalize(s)
	if !slices.Contains([]string{"action_delivery", "protective_monitoring", "paper_outcome_collection", "paper_evaluation_assessment", "social_research_dispatch", "paper_execution"}, r.Capability) || r.WorkerID == "" || len(r.WorkerID) > 80 || r.At.IsZero() || r.IntervalMillis < 100 || r.IntervalMillis > 60000 {
		return nil, errors.New("invalid worker report")
	}
	if r.At.Before(s.Operations.Workers[r.Capability].At) {
		return nil, routing.ErrClockBackwards
	}
	events := []domain.Event{}
	if !r.PriorFailureAt.IsZero() && r.PriorErrorCode != "" {
		if r.PriorFailureAt.After(r.At) {
			return nil, errors.New("worker failure is after report")
		}
		prior := r.WorkerHealth
		prior.At, prior.ErrorCode = r.PriorFailureAt, r.PriorErrorCode
		s.Operations.Workers[r.Capability] = prior
		events = append(events, Sync(s, r.At)...)
	}
	previous := s.Operations.Workers[r.Capability]
	w := r.WorkerHealth
	w.LastSuccessAt = previous.LastSuccessAt
	if w.Stopped && w.ErrorCode == "" {
		w.ErrorCode = previous.ErrorCode
	}
	if w.ErrorCode == "" && !w.Stopped {
		w.LastSuccessAt = w.At
	}
	s.Operations.Workers[r.Capability] = w
	if r.At.After(s.Operations.LastClock) {
		s.Operations.LastClock = r.At.UTC()
	}
	events = append(events, Sync(s, r.At)...)
	return events, nil
}
