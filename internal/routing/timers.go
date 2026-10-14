package routing

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

// ErrClockBackwards pauses work until the clock catches the durable watermark.
var ErrClockBackwards = errors.New("scheduler clock precedes durable watermark")

// ErrLeaseLost fences workers whose lease expired or was reclaimed.
var ErrLeaseLost = errors.New("timer lease no longer belongs to worker")

// NewTimers initializes bounded, durable scheduler state.
func NewTimers() *domain.TimerState {
	return &domain.TimerState{Jobs: map[string]domain.TimerJob{}, Exceptions: map[string]domain.OperationalException{}}
}

// IsTimed distinguishes one-shot clock obligations from incoming evidence.
func IsTimed(c domain.Condition) bool {
	return slices.Contains([]string{"deadline", "elapsed", "expected_event_missing"}, c.Kind)
}

// DueAt resolves elapsed time against a definition's declared validity start.
func DueAt(d domain.TriggerDefinition) time.Time {
	if d.Condition.Kind == "elapsed" {
		return d.ValidFrom.Add(time.Duration(d.Condition.AfterSeconds) * time.Second).UTC()
	}
	return d.Condition.DueAt.UTC()
}

func validateTimed(d domain.TriggerDefinition) error {
	c := d.Condition
	expected := domain.Condition{Kind: c.Kind}
	if c.Kind == "elapsed" {
		if c.AfterSeconds < 1 || c.AfterSeconds > 31536000 {
			return errors.New("elapsed condition requires bounded seconds")
		}
		expected.AfterSeconds = c.AfterSeconds
	} else {
		if !validTime(c.DueAt) {
			return errors.New("timer requires an explicit due_at")
		}
		expected.DueAt = c.DueAt
	}
	if c.Kind == "expected_event_missing" {
		if !key.MatchString(c.EventName) {
			return errors.New("missing-event condition requires event name")
		}
		expected.EventName = c.EventName
	}
	if !equal(c, expected) || DueAt(d).Before(d.ValidFrom) || !DueAt(d).Before(d.ValidUntil) || d.RepeatPolicy != "once" {
		return errors.New("timer requires compatible fields, once policy, and due time within validity")
	}
	return nil
}

// SyncTimers creates jobs in the same state mutation that installs definitions.
func SyncTimers(s *domain.RoutingState) {
	if s.Timers == nil {
		s.Timers = NewTimers()
	}
	for _, d := range s.Definitions {
		if !IsTimed(d.Condition) {
			continue
		}
		if _, exists := s.Timers.Jobs[d.ID]; !exists {
			s.Timers.Jobs[d.ID] = domain.TimerJob{DefinitionID: d.ID, DueAt: DueAt(d), Status: "pending"}
		}
	}
}

func observeExpected(d domain.TriggerDefinition, i domain.RoutingInput, ref string, facts map[string]domain.ConditionFact) {
	if d.Condition.Kind != "expected_event_missing" || i.Kind != "event" || i.EventName != d.Condition.EventName || i.OccurredAt.Before(d.ValidFrom) || !i.ReceivedAt.Before(DueAt(d)) {
		return
	}
	facts["expected"] = domain.ConditionFact{Satisfied: true, InputRef: ref, At: i.ReceivedAt, LastOccurredAt: i.OccurredAt, LastReceivedAt: i.ReceivedAt}
}

func advance(s *domain.RoutingState, now time.Time) error {
	if !validTime(now) {
		return fmt.Errorf("%w: scheduler requires explicit time", ErrInvalid)
	}
	if now.Before(s.Timers.LastClock) {
		return ErrClockBackwards
	}
	s.Timers.LastClock = now.UTC()
	return nil
}

// ClaimTimers leases overdue jobs in stable ID order. A crashed lease can only
// be reclaimed at its expiry, with a new monotonically increasing generation.
func ClaimTimers(s *domain.RoutingState, worker string, now time.Time, lease time.Duration) (*domain.RoutingState, []domain.TimerClaim, error) {
	if !key.MatchString(worker) || lease < time.Second || lease > 5*time.Minute {
		return s, nil, fmt.Errorf("%w: invalid worker or lease", ErrInvalid)
	}
	next, err := copyState(s)
	if err != nil {
		return s, nil, err
	}
	SyncTimers(next)
	if err := advance(next, now); err != nil {
		return s, nil, err
	}
	now = now.UTC()
	// Expire incoming-input definitions without waiting for another observation.
	for _, d := range next.Definitions {
		if !IsTimed(d.Condition) && !now.Before(d.ValidUntil) {
			runtime := next.Runtime[d.ID]
			runtime.Status = "expired"
			next.Runtime[d.ID] = runtime
		}
	}
	ids := make([]string, 0, len(next.Timers.Jobs))
	for id := range next.Timers.Jobs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	claims := []domain.TimerClaim{}
	for _, id := range ids {
		job := next.Timers.Jobs[id]
		if job.Status == "completed" || now.Before(job.DueAt) || job.Status == "claimed" && now.Before(job.LeaseUntil) {
			continue
		}
		job.Status, job.Worker, job.Generation, job.LeaseUntil = "claimed", worker, job.Generation+1, now.Add(lease)
		job.ClaimedAt = now
		next.Timers.Jobs[id] = job
		claims = append(claims, domain.TimerClaim{DefinitionID: id, Worker: worker, Generation: job.Generation})
		if len(claims) == 16 {
			break
		}
	}
	return next, claims, nil
}

// ApplyTimer matches clock evidence through the same routing-case contract.
// An overdue valid obligation remains deliverable even beyond valid_until.
func ApplyTimer(s *domain.RoutingState, id string, now time.Time) (*domain.RoutingState, domain.RoutingReceipt, string, error) {
	next, err := copyState(s)
	if err != nil {
		return s, domain.RoutingReceipt{}, "", err
	}
	receipt := domain.RoutingReceipt{InputID: "timer:" + id, Matches: []domain.RoutingMatch{}, ExpiredDefinitions: []string{}, RejectedDefinitions: []string{}}
	for _, d := range next.Definitions {
		if d.ID != id {
			continue
		}
		if err := ValidateDefinition(d); err != nil {
			return s, receipt, "", err
		}
		if !IsTimed(d.Condition) || now.Before(DueAt(d)) {
			return s, receipt, "", fmt.Errorf("%w: timer is not due", ErrInvalid)
		}
		runtime := next.Runtime[id]
		if runtime.Fired {
			return next, receipt, "already_applied", nil
		}
		if d.Condition.Kind == "expected_event_missing" && runtime.Facts["expected"].Satisfied {
			runtime.Status = "satisfied"
			next.Runtime[id] = runtime
			return next, receipt, "event_observed", nil
		}
		match, accepted := routeCase(next, d, now.UTC(), receipt.InputID)
		if !accepted {
			receipt.RejectedDefinitions = append(receipt.RejectedDefinitions, id)
			return next, receipt, "routing_rejected", nil
		}
		runtime.Fired, runtime.Status = true, "consumed"
		next.Runtime[id] = runtime
		receipt.Matches = append(receipt.Matches, match)
		return next, receipt, "matched", nil
	}
	return s, receipt, "", ErrNotFound
}

// CheckClaim returns a retained receipt for an identical completion retry;
// expiry or a different generation never permits another effect.
func CheckClaim(s *domain.RoutingState, claim domain.TimerClaim, now time.Time) (*domain.TimerReceipt, error) {
	if s == nil || s.Timers == nil || !validTime(now) {
		return nil, ErrLeaseLost
	}
	job, ok := s.Timers.Jobs[claim.DefinitionID]
	if !ok || job.Worker != claim.Worker || job.Generation != claim.Generation {
		return nil, ErrLeaseLost
	}
	if job.Status == "completed" {
		return job.Receipt, nil
	}
	if job.Status != "claimed" || !now.Before(job.LeaseUntil) {
		return nil, ErrLeaseLost
	}
	if now.Before(s.Timers.LastClock) {
		return nil, ErrClockBackwards
	}
	return nil, nil
}

// Complete records the result after routing and protection commit together.
func Complete(s *domain.RoutingState, claim domain.TimerClaim, receipt domain.TimerReceipt) {
	job := s.Timers.Jobs[claim.DefinitionID]
	job.Status, job.CompletedAt, job.Receipt = "completed", receipt.At, &receipt
	s.Timers.Jobs[claim.DefinitionID] = job
	s.Timers.LastClock = receipt.At
}
