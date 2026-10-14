package operational_test

import (
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func report(cap string, at time.Time, code string) domain.WorkerReport {
	return domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: cap, WorkerID: "worker-" + cap, At: at, IntervalMillis: 1000, ErrorCode: code}}
}

func TestCapabilityReadinessAndWorkerRecovery(t *testing.T) {
	s := workflow.InitialState()
	at := workflow.Fixtures()[0].ReceivedAt
	for _, cap := range []string{"action_delivery", "protective_monitoring"} {
		if _, err := operational.Report(&s, report(cap, at, "")); err != nil {
			t.Fatal(err)
		}
	}
	ready := operational.Readiness(s, at, true, "fixture_operation")
	if !ready.Ready || ready.Capabilities["feed_coverage"].Ready || ready.Mode != "paper_fixture" {
		t.Fatal("fixture readiness implied real feed")
	}
	outage := operational.Readiness(s, at, false, "storage")
	if outage.Ready || !outage.Capabilities["process"].Ready || outage.Capabilities["action_delivery"].Status != "unknown" {
		t.Fatal("storage outage masked")
	}
	failed := report("action_delivery", at.Add(time.Second), "cycle_failed")
	failed.EvidenceRef = "action:fixture-action"
	events, err := operational.Report(&s, failed)
	if err != nil || len(events) != 1 || operational.Readiness(s, failed.At, true, "action_delivery").Ready {
		t.Fatal("worker failure not visible", err)
	}
	for _, e := range s.Routing.Timers.Exceptions {
		if e.CaseID == "" || e.Severity == "" || len(e.EvidenceRefs) != 2 || e.Status != "open" {
			t.Fatal("incomplete persisted exception")
		}
	}
	healthy := report("action_delivery", at.Add(2*time.Second), "")
	events, err = operational.Report(&s, healthy)
	if err != nil || len(events) != 1 {
		t.Fatal("worker recovery not retained", err)
	}
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Status != "resolved" || e.TerminalOutcome != "condition_recovered" || e.At.IsZero() {
			t.Fatal("terminal evidence missing")
		}
	}
	stale := operational.Readiness(s, at.Add(time.Minute), true, "action_delivery")
	if stale.Ready || stale.Capabilities["action_delivery"].Code != "worker_heartbeat_stale" {
		t.Fatal("stale worker considered ready")
	}
	if _, err := operational.Report(&s, report("action_delivery", at, "")); !errors.Is(err, routing.ErrClockBackwards) {
		t.Fatal("worker clock rewind accepted")
	}
	if operational.Readiness(s, at, true, "action_delivery").Capabilities["action_delivery"].Code != "clock_backwards" {
		t.Fatal("readiness hid clock rewind")
	}
	healthy.Stopped = true
	healthy.At = at.Add(3 * time.Second)
	if _, err := operational.Report(&s, healthy); err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(s, healthy.At, true, "action_delivery").Ready {
		t.Fatal("stopped worker ready")
	}
}

func TestMissingProtectionAndOrderedCapabilityReports(t *testing.T) {
	s, _, err := workflow.Apply(workflow.InitialState(), workflow.Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	at := workflow.Fixtures()[1].ReceivedAt
	if _, err := operational.Report(&s, report("protective_monitoring", at.Add(2*time.Second), "")); err != nil {
		t.Fatal(err)
	}
	// Different capabilities can finish out of timestamp order without rewinding
	// the global recovery watermark or inventing a clock outage.
	if _, err := operational.Report(&s, report("action_delivery", at.Add(time.Second), "")); err != nil {
		t.Fatal(err)
	}
	if !s.Operations.LastClock.Equal(at.Add(2 * time.Second)) {
		t.Fatal("report rewound global clock")
	}
	saved := append([]domain.TriggerDefinition(nil), s.Routing.Definitions...)
	s.Routing.Definitions = s.Routing.Definitions[1:]
	events := operational.Sync(&s, at.Add(3*time.Second))
	if len(events) != 1 {
		t.Fatal("missing stop not recorded")
	}
	if operational.Readiness(s, at.Add(3*time.Second), true, "protective_monitoring").Ready {
		t.Fatal("missing protection ready")
	}
	s.Routing.Definitions = saved
	events = operational.Sync(&s, at.Add(4*time.Second))
	if len(events) != 1 {
		t.Fatal("restored protection not recorded")
	}
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Code == "missing_protection" && e.Status != "resolved" {
			t.Fatal("restoration lost lifecycle")
		}
	}
}

func TestPriorStorageFailureAndLegacyExceptionMetadata(t *testing.T) {
	s := workflow.InitialState()
	at := workflow.Fixtures()[0].ReceivedAt
	r := report("action_delivery", at.Add(time.Minute), "")
	r.PriorFailureAt = at
	r.PriorErrorCode = "storage_unavailable"
	events, err := operational.Report(&s, r)
	if err != nil || len(events) != 2 {
		t.Fatal("previous outage not recovered durably", err)
	}
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Status != "resolved" || !e.At.Equal(at) || e.Capability != "action_delivery" {
			t.Fatal("lost outage interval")
		}
	}
	s.Routing.Timers.Exceptions["legacy"] = domain.OperationalException{ID: "legacy", CaseID: s.Case.ID, DefinitionID: "timer-id", Code: "deadline_price_unavailable", Status: "resolved", At: at, ResolvedAt: at.Add(time.Second)}
	operational.Normalize(&s)
	legacy := s.Routing.Timers.Exceptions["legacy"]
	if legacy.Severity != "critical" || legacy.Capability != "protective_monitoring" || len(legacy.EvidenceRefs) != 2 || legacy.TerminalOutcome != "protective_exit_applied" {
		t.Fatal("legacy exception not upgraded")
	}
	bad := report("unknown", at, "")
	if _, err := operational.Report(&s, bad); err == nil {
		t.Fatal("unknown capability")
	}
}

func TestDeliveryStatesAndUnfulfilledProtection(t *testing.T) {
	for _, status := range []string{"pending", "unknown", "accepted", "partial", "expired", "rejected", "completed"} {
		t.Run(status, func(t *testing.T) {
			q := workflow.Fixtures()[1]
			s, _, err := workflow.Queue(workflow.InitialState(), q)
			if err != nil {
				t.Fatal(err)
			}
			for id, a := range s.Actions.Items {
				a.Status = status
				a.LeaseUntil = q.ReceivedAt
				s.Actions.Items[id] = a
			}
			for _, cap := range []string{"action_delivery", "protective_monitoring"} {
				if _, err := operational.Report(&s, report(cap, q.ReceivedAt, "")); err != nil {
					t.Fatal(err)
				}
			}
			ready := operational.Readiness(s, q.ReceivedAt, true, "action_delivery")
			expected := status == "pending" || status == "completed"
			if ready.Ready != expected {
				t.Fatal("delivery readiness masked unresolved outcome", status)
			}
			if !expected {
				for _, e := range s.Routing.Timers.Exceptions {
					if e.ActionID == "" || e.Capability != "action_delivery" || e.Status != "open" {
						t.Fatal("action fault lost evidence")
					}
				}
			}
		})
	}
	s, _, err := workflow.Apply(workflow.InitialState(), workflow.Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	now := s.Case.ExitBy.Add(16 * time.Second)
	if _, err := operational.Report(&s, report("protective_monitoring", now, "")); err != nil {
		t.Fatal(err)
	}
	found := false
	var id string
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Code == "timer_overdue" {
			found = true
			id = e.DefinitionID
		}
	}
	if !found {
		t.Fatal("overdue job not actionable")
	}
	job := s.Routing.Timers.Jobs[id]
	job.Status = "completed"
	job.CompletedAt = now
	job.Receipt = &domain.TimerReceipt{Status: "queued", DefinitionID: id}
	s.Routing.Timers.Jobs[id] = job
	operational.Sync(&s, now)
	found = false
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Code == "protective_action_unfulfilled" && e.Status == "open" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing queued effect considered protection")
	}
	q := workflow.Fixtures()[1]
	q.ID = "current-price"
	q.OccurredAt = now
	q.ReceivedAt = now
	s.LastQuote = &q
	s.LastOccurredAt = now
	s, _, outcome, err := workflow.RecoverProtection(s, id, "test-recovery", now)
	if err != nil || outcome != "queued" {
		t.Fatal("valid protection recovery not queued", err)
	}
	operational.Sync(&s, now)
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Code == "protective_action_unfulfilled" && e.Status != "resolved" {
			t.Fatal("valid pending obligation not recognized")
		}
	}
}

func TestInvalidExecutableAuthorityIsNotReady(t *testing.T) {
	s, _, err := workflow.Apply(workflow.InitialState(), workflow.Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	now := workflow.Fixtures()[1].ReceivedAt
	s.Strategy.Safety.TargetCents++
	if _, err := operational.Report(&s, report("protective_monitoring", now, "")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range s.Routing.Timers.Exceptions {
		if e.Code == "invalid_strategy_authority" && e.Status == "open" {
			found = true
		}
	}
	if !found || operational.Readiness(s, now, true, "protective_monitoring").Ready {
		t.Fatal("invalid executable contract considered protection")
	}
}
