package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type candidateIntentBackend interface {
	candidateBackend
	CandidateWorkerIntentStore
}

func TestMemoryCandidateWorkerIntents(t *testing.T) { candidateIntentContracts(t, NewMemory()) }
func TestPostgresContractsCandidateWorkerIntents(t *testing.T) {
	candidateIntentContracts(t, timerPostgres(t, "candidate_intents"))
}

func candidateIntentFixture(t *testing.T, s candidateIntentBackend) (candidateattempt.Command, []byte, time.Time) {
	t.Helper()
	c, raw, _, at := candidateFixture(t, s)
	r, err := s.RegisterCandidate(context.Background(), c, raw, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(r.Candidate, raw, []bool{true, true, false, false})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(p, raw)
	if err != nil {
		t.Fatal(err)
	}
	return candidateattempt.Command{ReceiverID: r.ReceiverID, RequestID: "intent-root", RegistrationID: r.ID, RegistrationSHA256: r.SHA256, Job: j}, raw, at
}

func candidateIntentContracts(t *testing.T, s candidateIntentBackend) {
	ctx := context.Background()
	c, _, at := candidateIntentFixture(t, s)
	now := func() time.Time { return at }
	before, _ := s.Snapshot(ctx)
	registryBefore, _ := s.CandidateRegistry(ctx)
	emptyHistory, err := s.CandidateWorkerIntentHistory(ctx)
	if err != nil || emptyHistory.Validate() != nil || emptyHistory.ReceiverID != c.ReceiverID || len(emptyHistory.Intents) != 0 || len(emptyHistory.Registrations) != 0 {
		t.Fatal("empty intent history envelope", err)
	}
	empty, err := s.CandidateWorkerIntents(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			i, err := s.StartCandidateWorker(ctx, c, now)
			if err != nil || i.Validate() != nil || i.Sequence != 1 {
				t.Errorf("concurrent intent: %v", err)
			}
		})
	}
	wg.Wait()
	first, err := s.CandidateWorkerIntent(ctx, c.RequestID)
	if err != nil || first.Command.RegistrationSHA256 != c.RegistrationSHA256 || first.ExecutionStarted || first.HostStateKnown || first.QualificationCredit {
		t.Fatal(first, err)
	}
	read, err := s.StartCandidateWorker(ctx, c, func() time.Time { panic("retry consulted clock") })
	if err != nil || !reflect.DeepEqual(read, first) {
		t.Fatal("retry changed", err)
	}
	for _, kind := range []string{"receiver", "registration", "registration-sha", "annotations"} {
		t.Run(kind, func(t *testing.T) {
			changed := clone(c)
			switch kind {
			case "receiver":
				changed.ReceiverID = "receiver-other_other_other"
			case "registration":
				changed.RegistrationID = "missing"
			case "registration-sha":
				changed.RegistrationSHA256 = strings.Repeat("a", 64)
			case "annotations":
				p, err := candidatetest.New(c.Job.Plan.Candidate, mustCandidateDataset(t, s, c.RegistrationID), []bool{false, false, false, false})
				if err != nil {
					t.Fatal(err)
				}
				changed.Job, err = candidateexec.New(p, mustCandidateDataset(t, s, c.RegistrationID))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.StartCandidateWorker(ctx, changed, now); err == nil {
				t.Fatal("request rebound")
			}
		})
	}
	missing := clone(c)
	missing.RequestID, missing.RegistrationID = "missing", "missing"
	if _, err := s.StartCandidateWorker(ctx, missing, now); !errors.Is(err, candidateattempt.ErrNotFound) {
		t.Fatal(err)
	}
	c.RequestID = "physical-retry"
	at = at.Add(time.Second)
	second, err := s.StartCandidateWorker(ctx, c, now)
	if err != nil || second.Sequence != 2 || second.SHA256 == first.SHA256 || second.Command.Job.SHA256 != first.Command.Job.SHA256 {
		t.Fatal(second, err)
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		next, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(next.Close)
		s = next
		restored, err := s.CandidateWorkerIntent(ctx, first.Command.RequestID)
		if err != nil || !reflect.DeepEqual(first, restored) {
			t.Fatal("restart changed intent", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_worker_intents ADD CONSTRAINT reject_intent CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.RequestID = "rollback"
		if _, err := s.StartCandidateWorker(ctx, failed, now); err == nil {
			t.Fatal("failed insert acknowledged")
		}
		if _, err := s.CandidateWorkerIntent(ctx, "rollback"); !errors.Is(err, candidateattempt.ErrNotFound) {
			t.Fatal("partial intent persisted", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_worker_intents DROP CONSTRAINT reject_intent"); err != nil {
			t.Fatal(err)
		}
	}
	for n := 2; n < candidateattempt.MaxAttempts; n++ {
		copy := clone(c)
		copy.RequestID = fmt.Sprintf("intent-%02d", n)
		if _, err := s.StartCandidateWorker(ctx, copy, now); err != nil {
			t.Fatal(err)
		}
	}
	over := clone(c)
	over.RequestID = "overflow"
	if _, err := s.StartCandidateWorker(ctx, over, func() time.Time { panic("capacity consulted clock") }); !errors.Is(err, candidateattempt.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := s.StartCandidateWorker(ctx, first.Command, func() time.Time { panic("capacity retry consulted clock") }); err != nil {
		t.Fatal(err)
	}
	all, err := s.CandidateWorkerIntents(ctx)
	if err != nil || len(all) != 64 || candidateattempt.ValidateHistory(c.ReceiverID, all) != nil {
		t.Fatal("history invalid", err)
	}
	first.Command.Job.Plan.Cases[0].ID = "caller-mutated"
	actual, err := s.CandidateWorkerIntent(ctx, first.Command.RequestID)
	if err != nil || actual.Validate() != nil {
		t.Fatal("caller changed intent", err)
	}
	after, _ := s.Snapshot(ctx)
	registryAfter, _ := s.CandidateRegistry(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(registryBefore, registryAfter) {
		t.Fatal("intent changed operations or declarations")
	}
	for _, id := range []string{"missing", "../bad"} {
		if _, err := s.CandidateWorkerIntent(ctx, id); err == nil {
			t.Fatal("missing/invalid selection accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.CandidateWorkerIntents(cancelled); err == nil {
		t.Fatal("cancelled read accepted")
	}
	if _, err := s.StartCandidateWorker(cancelled, c, now); err == nil {
		t.Fatal("cancelled write accepted")
	}
}

func mustCandidateDataset(t *testing.T, s CandidateStore, id string) []byte {
	t.Helper()
	raw, err := s.CandidateDataset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMemoryCandidateIntentCorruption(t *testing.T) { candidateIntentCorruption(t, NewMemory()) }
func TestPostgresContractsCandidateIntentCorruption(t *testing.T) {
	candidateIntentCorruption(t, timerPostgres(t, "candidate_intent_corrupt"))
}

func candidateIntentCorruption(t *testing.T, s candidateIntentBackend) {
	ctx := context.Background()
	c, raw, at := candidateIntentFixture(t, s)
	now := func() time.Time { return at }
	first, err := s.StartCandidateWorker(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	other := clone(c)
	other.RequestID = "unrelated"
	if _, err := s.StartCandidateWorker(ctx, other, now); err != nil {
		t.Fatal(err)
	}
	r, err := s.CandidateRegistration(ctx, c.RegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	question, err := s.Hypothesis(ctx, r.Candidate.Hypothesis.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub := question.Publications[0]
	for _, kind := range []string{"metadata", "forged-case", "dataset", "publication"} {
		t.Run(kind, func(t *testing.T) {
			var restore func()
			if kind != "metadata" && kind != "forged-case" {
				restore = corruptCandidate(t, s, kind, r, raw, pub)
			} else {
				bad := clone(first)
				if kind == "forged-case" {
					p := bad.Command.Job.Plan
					p.Cases[0].PriceCents++
					p.ID, p.SHA256 = "", ""
					p.SHA256 = workerplan.Hash(p)
					p.ID = "candidate-test-" + p.SHA256
					bad.Command.Job.Plan = p
					bad.Command.Job.HarnessSHA256 = workerplan.Digest([]byte(candidateexec.Harness(p)))
					bad.Command.Job.SHA256 = ""
					bad.Command.Job.SHA256 = workerplan.Hash(bad.Command.Job)
					bad.SHA256 = ""
					bad.SHA256 = workerplan.Hash(bad)
					if bad.Validate() != nil {
						t.Fatal("forged case must retain valid self-hashes")
					}
				} else {
					bad.Command.Job.Plan.Cases[0].Expected = !bad.Command.Job.Plan.Cases[0].Expected
				}
				if m, ok := s.(*Memory); ok {
					m.candidateWorkerIntents[c.RequestID] = bad
					restore = func() { m.candidateWorkerIntents[c.RequestID] = clone(first) }
				} else {
					pg := s.(*Postgres)
					payload, _ := json.Marshal(bad)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_worker_intents SET payload=$2 WHERE id=$1", c.RequestID, payload); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						payload, _ := json.Marshal(first)
						if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_worker_intents SET payload=$2 WHERE id=$1", c.RequestID, payload); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			defer restore()
			if _, err := s.CandidateWorkerIntents(ctx); err == nil {
				t.Fatal("corrupt history accepted")
			}
			if _, err := s.CandidateWorkerIntent(ctx, "unrelated"); err == nil {
				t.Fatal("selected read hid corruption")
			}
			if _, err := s.StartCandidateWorker(ctx, other, now); err == nil {
				t.Fatal("retry hid corruption")
			}
			fresh := clone(c)
			fresh.RequestID = "fresh"
			if _, err := s.StartCandidateWorker(ctx, fresh, now); err == nil {
				t.Fatal("append hid corruption")
			}
		})
	}
	if all, err := s.CandidateWorkerIntents(ctx); err != nil || len(all) != 2 {
		t.Fatal("failed attempts changed history", err)
	}
}

func TestPostgresContractsCandidateIntentsUpgrade(t *testing.T) {
	s := timerPostgres(t, "candidate_intent_upgrade")
	ctx := context.Background()
	c, raw, at := candidateIntentFixture(t, s)
	before, _ := s.CandidateRegistry(ctx)
	snapshot, _ := s.Snapshot(ctx)
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=31"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after, err := next.CandidateRegistry(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed registry", err)
	}
	if got := mustCandidateDataset(t, next, c.RegistrationID); !reflect.DeepEqual(got, raw) {
		t.Fatal("upgrade changed bytes")
	}
	if all, err := next.CandidateWorkerIntents(ctx); err != nil || len(all) != 0 {
		t.Fatal("upgrade invented execution intents", err)
	}
	if _, err := next.StartCandidateWorker(ctx, c, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	state, _ := next.Snapshot(ctx)
	if !reflect.DeepEqual(state, snapshot) {
		t.Fatal("upgrade/start changed operations")
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=31"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
