package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type operationalBackend interface {
	actionBackend
	TimerStore
	OperationalStore
}

func health(cap string, at time.Time) domain.WorkerReport {
	return domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: cap, WorkerID: "worker-" + cap, At: at, IntervalMillis: 1000}}
}
func reportHealth(t *testing.T, store operationalBackend, at time.Time) {
	t.Helper()
	for _, cap := range []string{"action_delivery", "protective_monitoring"} {
		if err := store.ReportWorker(context.Background(), health(cap, at)); err != nil {
			t.Fatal(err)
		}
	}
}
func exceptionFor(t *testing.T, store Store, code string) domain.OperationalException {
	t.Helper()
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Code == code {
			return e
		}
	}
	t.Fatal("missing exception", code)
	return domain.OperationalException{}
}

func TestMemoryBoundedRecovery(t *testing.T) { boundedRecovery(t, NewQueuedMemory()) }
func TestPostgresContractsBoundedRecovery(t *testing.T) {
	boundedRecovery(t, timerPostgres(t, "boundedops"))
}
func boundedRecovery(t *testing.T, store operationalBackend) {
	t.Helper()
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	now := q.ReceivedAt.Add(31 * time.Second)
	drainFixtureActions(t, store, now)
	reportHealth(t, store, now)
	e := exceptionFor(t, store, "action_expired")
	if e.ActionID == "" || e.Capability != "action_delivery" || e.Severity != "warning" || len(e.EvidenceRefs) < 3 {
		t.Fatal("incomplete action exception")
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := store.RecoverException(ctx, e.ID, "request-1", "reconcile-action", now)
			if err != nil || r.Attempt.Outcome != "terminal_action_acknowledged" {
				t.Errorf("recovery: %+v %v", r, err)
			}
		})
	}
	wg.Wait()
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events)+1 || after.State.Case.Entry != nil || after.State.Actions.Items[e.ActionID].Intent.Decision.ValidUntil != before.State.Actions.Items[e.ActionID].Intent.Decision.ValidUntil {
		t.Fatal("recovery changed authority or multiplied events")
	}
	if !operational.Readiness(after.State, now, true, "action_delivery").Ready {
		t.Fatal("terminal acknowledgment did not recover delivery readiness")
	}
	if _, err := store.RecoverException(ctx, e.ID, "request-1", "retry-protection", now); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatal("changed retry accepted")
	}
	for _, id := range []string{"request-2", "request-3"} {
		if _, err := store.RecoverException(ctx, e.ID, id, "recheck", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecoverException(ctx, e.ID, "request-4", "recheck", now); !errors.Is(err, ErrRecoveryLimit) {
		t.Fatal("unbounded recovery allowed")
	}
	if r, err := store.RecoverException(ctx, e.ID, "request-1", "reconcile-action", now); err != nil || !r.Duplicate {
		t.Fatal("retained retry unavailable after limit", err)
	}
	if _, err := store.RecoverException(ctx, "unknown", "request", "recheck", now); !errors.Is(err, ErrExceptionNotFound) {
		t.Fatal("unknown exception accepted")
	}
	if pg, ok := store.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		before, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		after, err := reopened.Snapshot(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("restart lost recovery history")
		}
	}
}

func TestMemoryProtectiveRecovery(t *testing.T) { protectiveRecovery(t, NewQueuedMemory()) }
func TestPostgresContractsProtectiveRecovery(t *testing.T) {
	protectiveRecovery(t, timerPostgres(t, "protectops"))
}
func protectiveRecovery(t *testing.T, store operationalBackend) {
	t.Helper()
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	drainFixtureActions(t, store, q.ReceivedAt)
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	due := snapshot.State.Case.ExitBy
	claims, err := store.ClaimTimers(ctx, "timer", due, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTimer(ctx, claims[0], due); err != nil {
		t.Fatal(err)
	}
	reportHealth(t, store, due)
	e := exceptionFor(t, store, "deadline_price_unavailable")
	r, err := store.RecoverException(ctx, e.ID, "no-price", "retry-protection", due)
	if err != nil || r.Attempt.Outcome != "blocked_price_unavailable" {
		t.Fatal("missing price recovery fabricated authority", err)
	}
	// A new usable quote creates an exit that expires before delivery. Recovery
	// reevaluates the original overdue obligation with a new decision identity.
	q.ID = "late-price"
	q.PriceCents = 10300
	q.OccurredAt = due.Add(time.Second)
	q.ReceivedAt = q.OccurredAt
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	now := due.Add(32 * time.Second)
	drainFixtureActions(t, store, now)
	reportHealth(t, store, now)
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.RecoverException(ctx, e.ID, "usable-price", "retry-protection", now)
	if err != nil || r.Attempt.Outcome != "queued" || r.Attempt.ActionID == "" {
		t.Fatalf("protection recovery: %+v %v", r, err)
	}
	pending, err := store.Snapshot(ctx)
	if err != nil || pending.State.Case.Exit != nil || pending.State.Routing.Timers.Exceptions[e.ID].Status != "open" {
		t.Fatal("queue claimed completed protection")
	}
	for id, a := range before.State.Actions.Items {
		if !reflect.DeepEqual(a.Intent, pending.State.Actions.Items[id].Intent) {
			t.Fatal("recovery rewrote original validity")
		}
	}
	drainFixtureActions(t, store, now)
	reportHealth(t, store, now)
	after, err := store.Snapshot(ctx)
	if err != nil || after.State.Case.Exit == nil || after.State.Case.Exit.PriceCents != 10300 || after.State.Routing.Timers.Exceptions[e.ID].Status != "resolved" {
		t.Fatal("safe protective recovery did not apply", err)
	}
	if !operational.Readiness(after.State, now, true, "fixture_operation").Ready {
		t.Fatal("completed recovery remained unhealthy")
	}
}

func TestMemoryRecoveryRespectsLiveLease(t *testing.T) {
	ctx := context.Background()
	store := NewQueuedMemory()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimActions(ctx, "worker", q.ReceivedAt, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.BeginAction(ctx, claims[0], q.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	installEffect(t, store, domain.ActionEffect{Intent: a.Intent, Status: "accepted"})
	if _, err := store.ReconcileAction(ctx, claims[0], q.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	reportHealth(t, store, q.ReceivedAt)
	e := exceptionFor(t, store, "action_accepted")
	r, err := store.RecoverException(ctx, e.ID, "live-lease", "reconcile-action", q.ReceivedAt)
	if err != nil || r.Attempt.Outcome != "blocked_live_lease" || store.state.Actions.Items[a.Intent.ID].Generation != claims[0].Generation {
		t.Fatal("operator stole active lease", err)
	}
}

func TestPostgresContractsOperationalRecoveryRollback(t *testing.T) {
	store := timerPostgres(t, "opsrollback")
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	now := q.ReceivedAt.Add(31 * time.Second)
	drainFixtureActions(t, store, now)
	reportHealth(t, store, now)
	e := exceptionFor(t, store, "action_expired")
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT fail_recovery CHECK (payload->>'kind'<>'operations.recovery')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverException(ctx, e.ID, "rollback", "reconcile-action", now); err == nil {
		t.Fatal("forced recovery failure accepted")
	}
	after, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial recovery state or history persisted")
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT fail_recovery"); err != nil {
		t.Fatal(err)
	}
	if r, err := store.RecoverException(ctx, e.ID, "rollback", "reconcile-action", now); err != nil || r.Duplicate {
		t.Fatal("rollback consumed request identity", err)
	}
}

func stripLegacyException(e *domain.OperationalException) {
	e.Capability = ""
	e.Severity = ""
	e.EvidenceRefs = nil
	e.ActionID = ""
	e.LastObservedAt = time.Time{}
	e.RecoveryAttempts = nil
	e.TerminalOutcome = ""
}
func TestPostgresContractsV4OperationalUpgrade(t *testing.T) {
	store := timerPostgres(t, "v4ops")
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	drainFixtureActions(t, store, q.ReceivedAt)
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	due := snapshot.State.Case.ExitBy
	claims, err := store.ClaimTimers(ctx, "worker", due, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTimer(ctx, claims[0], due); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, e := range before.State.Routing.Timers.Exceptions {
		stripLegacyException(&e)
		before.State.Routing.Timers.Exceptions[id] = e
	}
	for index := range before.Events {
		if before.Events[index].Exception != nil {
			stripLegacyException(before.Events[index].Exception)
		}
	}
	before.State.Operations = nil
	if _, err := store.pool.Exec(ctx, "DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_input_contract; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_operations; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE mantik_feed_evidence; DROP TABLE mantik_feed_collectors; DELETE FROM mantik_schema WHERE version>=5"); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(before.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_state SET payload=$1::jsonb-'operations'", raw); err != nil {
		t.Fatal(err)
	}
	for _, event := range before.Events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, "UPDATE mantik_events SET payload=$1 WHERE id=$2", raw, event.ID); err != nil {
			t.Fatal(err)
		}
	}
	config := store.pool.Config().ConnString()
	store.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upgraded.Close)
	after, err := upgraded.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.State.Actions, after.State.Actions) || after.State.Operations == nil {
		t.Fatal("upgrade rewrote history or action authority", err)
	}
	for _, e := range after.State.Routing.Timers.Exceptions {
		if e.Capability != "protective_monitoring" || e.Severity != "critical" || len(e.EvidenceRefs) == 0 || e.At.IsZero() {
			t.Fatal("legacy exception lacks operational metadata")
		}
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_state SET payload=payload-'operations'"); err == nil {
		t.Fatal("older writer could discard health/recovery metadata")
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=5"); err != nil {
		t.Fatal(err)
	}
	if changed, err := OpenPostgres(ctx, config); err == nil {
		changed.Close()
		t.Fatal("operational checksum change accepted")
	}
}
