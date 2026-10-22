package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func drainFixtureActions(t *testing.T, store Store, now time.Time) {
	t.Helper()
	backend, ok := store.(ActionStore)
	if !ok {
		return
	}
	ctx := context.Background()
	claims, err := backend.ClaimActions(ctx, "test-worker", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		a, err := backend.ReconcileAction(ctx, c, now)
		if err != nil {
			t.Fatal(err)
		}
		if actions.Terminal(a.Status) {
			continue
		}
		a, err = backend.BeginAction(ctx, c, now)
		if err != nil {
			t.Fatal(err)
		}
		if actions.Terminal(a.Status) {
			continue
		}
		if _, err := backend.SubmitAction(ctx, c, now); err != nil {
			t.Fatal(err)
		}
		if _, err := backend.ReconcileAction(ctx, c, now); err != nil {
			t.Fatal(err)
		}
	}
}

type actionBackend interface {
	Store
	ActionStore
}

func TestMemoryActionRecovery(t *testing.T) { actionRecovery(t, NewQueuedMemory()) }
func TestPostgresContractsActionRecovery(t *testing.T) {
	actionRecovery(t, timerPostgres(t, "actions"))
}

func actionRecovery(t *testing.T, store actionBackend) {
	t.Helper()
	ctx := context.Background()
	quote := workflow.Fixtures()[1]
	now := quote.ReceivedAt
	receipt, err := store.Ingest(ctx, quote)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range receipt.Events {
		if event.Outcome != nil {
			t.Fatal("ingestion enacted queued action")
		}
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || snapshot.State.Case.Entry != nil || len(snapshot.State.Actions.Items) != 1 {
		t.Fatal("intent was not atomically queued", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	all := []domain.ActionClaim{}
	for range 8 {
		wg.Go(func() {
			claims, err := store.ClaimActions(ctx, "worker", now, time.Second)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			all = append(all, claims...)
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(all) != 1 {
		t.Fatal("concurrent claims multiplied")
	}
	first := all[0]
	if _, err := store.BeginAction(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileAction(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	if retried, err := store.BeginAction(ctx, first, now); err != nil || retried.Attempts != 2 {
		t.Fatal("proven-absent same-lease retry failed", err)
	}
	// Crash after write-ahead intent, before adapter submission: absence is proven
	// under the fixture ledger lock, so a new generation may safely deliver.
	recovered := now.Add(time.Second)
	claims, err := store.ClaimActions(ctx, "recovered", recovered, 5*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	second := claims[0]
	if _, err := store.SubmitAction(ctx, first, recovered); !errors.Is(err, actions.ErrLeaseLost) {
		t.Fatal("old worker could submit")
	}
	if _, err := store.BeginAction(ctx, second, recovered); err == nil {
		t.Fatal("unknown outcome was retried without reconciliation")
	}
	a, err := store.ReconcileAction(ctx, second, recovered)
	if err != nil || a.Status != "pending" {
		t.Fatal("absent intent not recovered", err)
	}
	if _, err := store.BeginAction(ctx, second, recovered); err != nil {
		t.Fatal(err)
	}
	effect, err := store.SubmitAction(ctx, second, recovered)
	if err != nil || effect.Status != "completed" {
		t.Fatal("submission failed", err)
	}
	for range 8 {
		wg.Go(func() {
			again, err := store.SubmitAction(ctx, second, recovered)
			if err != nil || !reflect.DeepEqual(effect, again) {
				t.Errorf("idempotent effect changed: %v", err)
			}
		})
	}
	wg.Wait()
	before, err := store.Snapshot(ctx)
	if err != nil || before.State.Case.Entry != nil {
		t.Fatal("submission prematurely acknowledged", err)
	}
	if pg, ok := store.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		next, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(next.Close)
		store = next
	}
	// Crash after the effect, before acknowledgment. Even after expiry, recover
	// the original effect rather than discarding it or executing another one.
	late := now.Add(time.Minute)
	claims, err = store.ClaimActions(ctx, "late-worker", late, 5*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal("no acknowledgment recovery", err)
	}
	a, err = store.ReconcileAction(ctx, claims[0], late)
	if err != nil || a.Status != "completed" || !reflect.DeepEqual(a.Outcome, effect.Outcome) {
		t.Fatal("known effect lost after expiry", err)
	}
	after, err := store.Snapshot(ctx)
	if err != nil || after.State.Case.Entry == nil || len(after.Events) != len(before.Events)+1 {
		t.Fatal("reconciliation did not commit one effect", err)
	}
	for range 8 {
		wg.Go(func() {
			if _, err := store.ReconcileAction(ctx, claims[0], late); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	again, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(after, again) {
		t.Fatal("acknowledgment retry multiplied effects")
	}
	r, err := store.Ingest(ctx, quote)
	if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Events, receipt.Events) {
		t.Fatal("delivery rewrote input receipt", err)
	}
}

func TestMemoryActionExpiryAndAuthority(t *testing.T) {
	for _, test := range []struct {
		name    string
		changed bool
	}{{"expiry", false}, {"authority", true}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewQueuedMemory()
			quote := workflow.Fixtures()[1]
			if _, err := store.Ingest(ctx, quote); err != nil {
				t.Fatal(err)
			}
			now := quote.ReceivedAt.Add(30 * time.Second)
			if test.changed {
				now = quote.ReceivedAt.Add(time.Second)
				newQuote := workflow.Fixtures()[2]
				newQuote.OccurredAt = now
				newQuote.ReceivedAt = now
				if _, err := store.Ingest(ctx, newQuote); err != nil {
					t.Fatal(err)
				}
			}
			claims, err := store.ClaimActions(ctx, "worker", now, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			a, err := store.ReconcileAction(ctx, claims[0], now)
			if err != nil {
				t.Fatal(err)
			}
			want := "expired"
			if test.changed {
				want = "rejected"
			}
			if a.Status != want || len(store.effects) != 0 || store.state.Case.Entry != nil {
				t.Fatal("lost authority enacted")
			}
		})
	}
}

func TestPostgresContractsActionAcknowledgmentRollback(t *testing.T) {
	store := timerPostgres(t, "actionrollback")
	ctx := context.Background()
	quote := workflow.Fixtures()[1]
	now := quote.ReceivedAt
	if _, err := store.Ingest(ctx, quote); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimActions(ctx, "worker", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	if _, err := store.BeginAction(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitAction(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT fail_ack CHECK (payload->>'kind'<>'execution.completed')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileAction(ctx, c, now); err == nil {
		t.Fatal("forced acknowledgment failure accepted")
	}
	after, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial acknowledgment")
	}
	var effects int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_fixture_effects").Scan(&effects); err != nil || effects != 1 {
		t.Fatal("effect did not survive rollback")
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT fail_ack"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileAction(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_state SET payload=payload-'actions'"); err == nil {
		t.Fatal("older writer could discard actions")
	}
}

func TestMemoryAdapterStatusReconciliation(t *testing.T) { adapterStatuses(t, NewQueuedMemory()) }
func TestPostgresContractsAdapterStatusReconciliation(t *testing.T) {
	adapterStatuses(t, timerPostgres(t, "adapterstatus"))
}

func adapterStatuses(t *testing.T, store actionBackend) {
	t.Helper()
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	now := q.ReceivedAt
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimActions(ctx, "worker", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c := claims[0]
	a, err := store.BeginAction(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	// Adapter fixtures model a durable accepted/partial result without inventing
	// a full fill. Neither status permits a second submission.
	for _, status := range []string{"accepted", "partial"} {
		effect := domain.ActionEffect{Intent: a.Intent, Status: status, Code: "fixture_adapter_pending"}
		installEffect(t, store, effect)
		current, err := store.ReconcileAction(ctx, c, now)
		if err != nil || current.Status != status {
			t.Fatal("status not retained", err)
		}
		if _, err := store.BeginAction(ctx, c, now); err == nil {
			t.Fatal("known incomplete effect resubmitted")
		}
		if _, err := store.SubmitAction(ctx, c, now); err == nil {
			t.Fatal("known incomplete effect enacted again")
		}
		snapshot, err := store.Snapshot(ctx)
		if err != nil || snapshot.State.Case.Entry != nil {
			t.Fatal("partial result invented complete position")
		}
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := actions.Evaluate(snapshot.State, a, now)
	if err != nil {
		t.Fatal(err)
	}
	installEffect(t, store, domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out})
	final, err := store.ReconcileAction(ctx, c, now)
	if err != nil || final.Status != "completed" {
		t.Fatal("known completion not reconciled", err)
	}
}

func installEffect(t *testing.T, store actionBackend, e domain.ActionEffect) {
	t.Helper()
	if m, ok := store.(*Memory); ok {
		m.mu.Lock()
		m.effects[e.Intent.IdempotencyKey] = e
		m.mu.Unlock()
		return
	}
	pg := store.(*Postgres)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.pool.Exec(context.Background(), "INSERT INTO mantik_fixture_effects VALUES($1,$2) ON CONFLICT(idempotency_key) DO UPDATE SET payload=excluded.payload", e.Intent.IdempotencyKey, raw); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresContractsV3ActionUpgrade(t *testing.T) {
	store := timerPostgres(t, "v3actions")
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	legacy := NewMemory()
	if _, err := legacy.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	snapshot, err := legacy.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_input_contract; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_operations; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_actions; DROP TABLE mantik_fixture_effects; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE mantik_feed_evidence; DROP TABLE mantik_feed_collectors; DELETE FROM mantik_schema WHERE version>=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_state SET payload=$1::jsonb-'actions'-'operations'", raw); err != nil {
		t.Fatal(err)
	}
	qr, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := legacy.Ingest(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "INSERT INTO mantik_observations VALUES($1,$2,$3)", q.ID, qr, rr); err != nil {
		t.Fatal(err)
	}
	for _, event := range snapshot.Events {
		er, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,$2,$3)", event.ID, q.ID, er); err != nil {
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
	if err != nil || !reflect.DeepEqual(snapshot.Events, after.Events) || !reflect.DeepEqual(snapshot.State.Case, after.State.Case) || len(after.State.Actions.Items) != 0 {
		t.Fatal("upgrade rewrote or requeued historical execution", err)
	}
	claims, err := upgraded.ClaimActions(ctx, "worker", q.ReceivedAt, 30*time.Second)
	if err != nil || len(claims) != 0 {
		t.Fatal("historical entry enqueued again", err)
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=4"); err != nil {
		t.Fatal(err)
	}
	if changed, err := OpenPostgres(ctx, config); err == nil {
		changed.Close()
		t.Fatal("changed action migration admitted")
	}
}
