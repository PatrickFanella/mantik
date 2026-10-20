package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type candidateOutcomeBackend interface {
	candidateIntentBackend
	CandidateWorkerOutcomeStore
}

// These are explicit caller-reported incomplete captures, not host execution.
func outcomeFixture(t *testing.T, i candidateattempt.Intent) candidateevidence.Report {
	t.Helper()
	at := i.CreatedAt
	local := candidateevidence.Intent{Schema: "mantik-registered-candidate-attempt-intent-v1", JournalID: "candidate-journal-ABCDEFGHIJKLMNOP", RequestID: i.Command.RequestID, Sequence: 1, Job: i.Command.Job, ServiceIntent: &i, CreatedAt: at.Add(time.Second), Authority: "local_candidate_attempt_intent_only"}
	local.SHA256 = workerplan.Hash(local)
	root := "/private/synthetic-reported-journal"
	a := candidateevidence.Acquisition{Schema: "mantik-candidate-behavior-acquisition-v1", ID: "reported-owner", Root: root + "/attempt-" + i.Command.RequestID, JobSHA256: i.Command.Job.SHA256, ImageSHA256: i.Command.Job.Plan.Candidate.Toolchain.ImageSHA256, ContainerName: "mantik-worker-reported-owner", CreatedAt: at.Add(2 * time.Second), Deadline: at.Add(32 * time.Second)}
	a.SHA256 = workerplan.Hash(a)
	stdout, stderr := []byte{0xff, 0, 0xfe}, []byte{'f', 0, 0xff, '\n'}
	r := candidateevidence.Receipt{Schema: "mantik-candidate-behavior-host-receipt-v1", AcquisitionSHA256: a.SHA256, ContainerID: strings.Repeat("c", 64), ImageSHA256: a.ImageSHA256, Status: "incomplete", ReasonCode: "missing_pre_start_receipt", ExitCode: 1, FinishedAt: at.Add(3 * time.Second), CollectedAt: at.Add(4 * time.Second), HostDeadlineMet: true, OutputComplete: true, StdoutSHA256: workerplan.Digest(stdout), StdoutBytes: len(stdout), StderrSHA256: workerplan.Digest(stderr), StderrBytes: len(stderr), Authority: "synthetic_candidate_behavior_host_receipt_only"}
	r.SHA256 = workerplan.Hash(r)
	o := candidateevidence.Outcome{Schema: "mantik-candidate-attempt-outcome-v1", IntentSHA256: local.SHA256, CapturedAt: at.Add(5 * time.Second), Receipt: r, Authority: "local_candidate_host_receipt_capture_only"}
	o.SHA256 = workerplan.Hash(o)
	c := candidateevidence.Capture{Schema: candidateevidence.Schema, JournalRoot: root, Attempt: candidateevidence.Attempt{Intent: local, Outcome: &o}, Acquisition: a, PreStart: []byte{}, Stdout: stdout, Stderr: stderr, Authority: "caller_reported_candidate_outcome_capture_only"}
	c.SHA256 = workerplan.Hash(c)
	if c.VerifyIntent(i) != nil {
		t.Fatal("invalid reported fixture")
	}
	return candidateevidence.Report{ReceiverID: i.Command.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Capture: c}
}

func TestMemoryCandidateWorkerOutcomes(t *testing.T) { candidateOutcomeContracts(t, NewMemory()) }
func TestPostgresContractsCandidateWorkerOutcomes(t *testing.T) {
	candidateOutcomeContracts(t, timerPostgres(t, "candidate_outcomes"))
}

func candidateOutcomeContracts(t *testing.T, s candidateOutcomeBackend) {
	ctx := context.Background()
	c, _, at := candidateIntentFixture(t, s)
	state, _ := s.Snapshot(ctx)
	registry, _ := s.CandidateRegistry(ctx)
	if all, err := s.CandidateWorkerOutcomes(ctx); err != nil || all == nil || len(all) != 0 {
		t.Fatal(all, err)
	}

	if h, err := s.CandidateWorkerOutcomeHistory(ctx); err != nil || h.Validate() != nil || len(h.IntentHistory.Intents) != 0 || len(h.Outcomes) != 0 || h.UnreportedRequestIDs == nil {
		t.Fatal("invalid empty outcome history", err)
	}
	first, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	c.RequestID = "second"
	second, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	r1, r2 := outcomeFixture(t, first), outcomeFixture(t, second)
	if h, err := s.CandidateWorkerOutcomeHistory(ctx); err != nil || h.Validate() != nil || !reflect.DeepEqual(h.UnreportedRequestIDs, []string{first.Command.RequestID, second.Command.RequestID}) {
		t.Fatal("pending intents missing", err)
	}

	now := func() time.Time { return at.Add(time.Minute) }
	// Outcomes can arrive out of intent order; a pending earlier intent is retained.
	secondRecord, err := s.ReportCandidateWorkerOutcome(ctx, r2, now)
	if err != nil || secondRecord.Validate() != nil || secondRecord.IntentSequence != 2 || secondRecord.ExecutionVerified || secondRecord.CurrentHostStateKnown || secondRecord.QualificationCredit {
		t.Fatal(secondRecord, err)
	}
	if all, err := s.CandidateWorkerOutcomes(ctx); err != nil || len(all) != 1 || all[0].IntentSequence != 2 {
		t.Fatal(all, err)
	}

	h, err := s.CandidateWorkerOutcomeHistory(ctx)
	if err != nil || h.Validate() != nil || len(h.Outcomes) != 1 || h.Outcomes[0] != secondRecord || !reflect.DeepEqual(h.UnreportedRequestIDs, []string{first.Command.RequestID}) {
		t.Fatal("out-of-order report hid pending intent", err)
	}
	h.IntentHistory.Intents[0].Command.Job.Plan.Cases[0].ID = "changed"
	h.Outcomes[0].ReasonCode = "changed"
	h.UnreportedRequestIDs[0] = "changed"
	if fresh, err := s.CandidateWorkerOutcomeHistory(ctx); err != nil || fresh.Validate() != nil || fresh.IntentHistory.Intents[0].Command.Job.Plan.Cases[0].ID == "changed" {
		t.Fatal("caller mutated history storage", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.ReportCandidateWorkerOutcome(ctx, r1, now)
			if err != nil || r.IntentSequence != 1 || r.VerifyCapture(first, r1.Capture) != nil {
				t.Errorf("concurrent report: %v", err)
			}
		})
	}

	for range 4 {
		wg.Go(func() {
			for range 3 {
				h, err := s.CandidateWorkerOutcomeHistory(ctx)
				if err != nil || h.Validate() != nil || len(h.IntentHistory.Intents) != 2 {
					t.Errorf("concurrent incoherent history: %v", err)
				}
			}
		})
	}
	wg.Wait()
	record, err := s.CandidateWorkerOutcome(ctx, first.Command.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := s.ReportCandidateWorkerOutcome(ctx, r1, func() time.Time { panic("retry consulted clock") }); err != nil || retry != record {
		t.Fatal("retry changed original retention", err)
	}
	artifact, err := s.CandidateWorkerOutcomeEvidence(ctx, r1.RequestID)
	if err != nil || artifact.Validate() != nil || !reflect.DeepEqual(artifact.Capture, r1.Capture) || artifact.Outcome != record {
		t.Fatal("single snapshot artifact changed", err)
	}
	artifact.Capture.Stdout[0] ^= 1
	fresh, err := s.CandidateWorkerOutcomeEvidence(ctx, r1.RequestID)
	if err != nil || fresh.Validate() != nil {
		t.Fatal("artifact aliased storage", err)
	}
	read, err := s.CandidateWorkerOutcomeCapture(ctx, r1.RequestID)
	if err != nil || !reflect.DeepEqual(read, r1.Capture) {
		t.Fatal("capture bytes changed", err)
	}
	read.Stdout[0] = 1
	read.Attempt.Intent.Job.Plan.Cases[0].ID = "caller-mutated"
	if read, err := s.CandidateWorkerOutcomeCapture(ctx, r1.RequestID); err != nil || !reflect.DeepEqual(read, r1.Capture) {
		t.Fatal("caller changed retained evidence", err)
	}
	changed := clone(r1)
	changed.Capture.Stderr = append(changed.Capture.Stderr, 'x')
	sealReportedCapture(&changed.Capture)
	if changed.Validate() != nil {
		t.Fatal("changed fixture is not self-consistent")
	}
	if _, err := s.ReportCandidateWorkerOutcome(ctx, changed, now); !errors.Is(err, candidateevidence.ErrConflict) {
		t.Fatal("changed outcome replaced original", err)
	}
	for _, change := range []string{"service-receipt", "owner"} {
		forged := clone(r1)
		local := &forged.Capture.Attempt.Intent
		if change == "service-receipt" {
			local.ServiceIntent.Sequence++
			local.ServiceIntent.SHA256 = ""
			local.ServiceIntent.SHA256 = workerplan.Hash(*local.ServiceIntent)
			forged.IntentSHA256 = local.ServiceIntent.SHA256
		} else {
			local.JournalID = "candidate-journal-QRSTUVWXYZABCDEF"
		}
		local.SHA256 = ""
		local.SHA256 = workerplan.Hash(*local)
		forged.Capture.Attempt.Outcome.IntentSHA256 = local.SHA256
		sealReportedCapture(&forged.Capture)
		if forged.Validate() != nil {
			t.Fatal("forged report must retain valid self-hashes")
		}
		if _, err := s.ReportCandidateWorkerOutcome(ctx, forged, func() time.Time { panic("invalid binding consulted clock") }); !errors.Is(err, candidateevidence.ErrConflict) {
			t.Fatal("report rebound service receipt or owner", change, err)
		}
	}
	unknown := clone(first)
	unknown.Command.RequestID = "unknown"
	unknown.SHA256 = ""
	unknown.SHA256 = workerplan.Hash(unknown)
	if _, err := s.ReportCandidateWorkerOutcome(ctx, outcomeFixture(t, unknown), now); !errors.Is(err, candidateattempt.ErrNotFound) {
		t.Fatal("invented intent accepted", err)
	}
	foreign := clone(r1)
	foreign.ReceiverID = "receiver-other_other_other"
	if _, err := s.ReportCandidateWorkerOutcome(ctx, foreign, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	c.RequestID = "third"
	third, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	r3 := outcomeFixture(t, third)
	if _, err := s.ReportCandidateWorkerOutcome(ctx, r3, func() time.Time { return at }); err == nil {
		t.Fatal("outcome accepted before its claimed capture time")
	}

	beforeRestart, err := s.CandidateWorkerOutcomeHistory(ctx)
	if err != nil || beforeRestart.Validate() != nil || !reflect.DeepEqual(beforeRestart.UnreportedRequestIDs, []string{"third"}) {
		t.Fatal("unreported third intent missing", err)
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
		if restored, err := s.CandidateWorkerOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(beforeRestart, restored) {
			t.Fatal("restart changed coherent outcome history", err)
		}

		if restored, err := s.CandidateWorkerOutcome(ctx, r1.RequestID); err != nil || restored != record {
			t.Fatal("restart changed record", err)
		}
		if restored, err := s.CandidateWorkerOutcomeCapture(ctx, r1.RequestID); err != nil || !reflect.DeepEqual(restored, r1.Capture) {
			t.Fatal("restart changed raw evidence", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_worker_outcomes ADD CONSTRAINT reject_report CHECK(id!='third')"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReportCandidateWorkerOutcome(ctx, r3, now); err == nil {
			t.Fatal("failed insertion acknowledged")
		}
		if _, err := s.CandidateWorkerOutcome(ctx, "third"); !errors.Is(err, candidateattempt.ErrNotFound) {
			t.Fatal("partial outcome persisted", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_worker_outcomes DROP CONSTRAINT reject_report"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReportCandidateWorkerOutcome(ctx, r3, now); err != nil {
		t.Fatal(err)
	}
	all, err := s.CandidateWorkerOutcomes(ctx)
	if err != nil || len(all) != 3 || all[0] != record || all[1] != secondRecord || all[2].IntentSequence != 3 {
		t.Fatal(all, err)
	}

	final, err := s.CandidateWorkerOutcomeHistory(ctx)
	if err != nil || final.Validate() != nil || len(final.Outcomes) != 3 || len(final.UnreportedRequestIDs) != 0 {
		t.Fatal("complete outcome history changed", err)
	}
	after, _ := s.Snapshot(ctx)
	afterRegistry, _ := s.CandidateRegistry(ctx)
	if !reflect.DeepEqual(state, after) || !reflect.DeepEqual(registry, afterRegistry) {
		t.Fatal("reported outcomes changed operations or registry")
	}
	for _, id := range []string{"missing", "../bad"} {
		if _, err := s.CandidateWorkerOutcome(ctx, id); err == nil {
			t.Fatal("invalid selected outcome accepted")
		}
		if _, err := s.CandidateWorkerOutcomeCapture(ctx, id); err == nil {
			t.Fatal("invalid selected artifact accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := s.CandidateWorkerOutcomeHistory(cancelled); err == nil {
		t.Fatal("cancelled history read accepted")
	}
	if _, err := s.CandidateWorkerOutcomes(cancelled); err == nil {
		t.Fatal("cancelled read accepted")
	}
	if _, err := s.ReportCandidateWorkerOutcome(cancelled, r1, now); err == nil {
		t.Fatal("cancelled report accepted")
	}
}

func sealReportedCapture(c *candidateevidence.Capture) {
	r := &c.Attempt.Outcome.Receipt
	r.StdoutBytes, r.StdoutSHA256 = len(c.Stdout), workerplan.Digest(c.Stdout)
	r.StderrBytes, r.StderrSHA256 = len(c.Stderr), workerplan.Digest(c.Stderr)
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	c.Attempt.Outcome.SHA256 = ""
	c.Attempt.Outcome.SHA256 = workerplan.Hash(*c.Attempt.Outcome)
	c.SHA256 = ""
	c.SHA256 = workerplan.Hash(*c)
}

func TestMemoryCandidateOutcomeCorruption(t *testing.T) { candidateOutcomeCorruption(t, NewMemory()) }
func TestPostgresContractsCandidateOutcomeCorruption(t *testing.T) {
	candidateOutcomeCorruption(t, timerPostgres(t, "candidate_outcome_corrupt"))
}

func candidateOutcomeCorruption(t *testing.T, s candidateOutcomeBackend) {
	ctx := context.Background()
	c, _, at := candidateIntentFixture(t, s)
	now := func() time.Time { return at.Add(time.Minute) }
	i, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	report := outcomeFixture(t, i)
	original, err := s.ReportCandidateWorkerOutcome(ctx, report, now)
	if err != nil {
		t.Fatal(err)
	}
	c.RequestID = "unrelated"
	i2, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	other := outcomeFixture(t, i2)
	if _, err := s.ReportCandidateWorkerOutcome(ctx, other, now); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"metadata", "capture", "rehashed-capture", "intent"} {
		t.Run(kind, func(t *testing.T) {
			badRecord, badCapture, badIntent := original, clone(report.Capture), clone(i)
			switch kind {
			case "metadata":
				badRecord.ExecutionVerified = true
				badRecord.SHA256 = ""
				badRecord.SHA256 = workerplan.Hash(badRecord)
			case "capture":
				badCapture.Stdout[0] = 1
			case "rehashed-capture":
				badCapture.Stderr = append(badCapture.Stderr, 'x')
				sealReportedCapture(&badCapture)
			case "intent":
				badIntent.Command.Job.Plan.Cases[0].Expected = !badIntent.Command.Job.Plan.Cases[0].Expected
			}
			write := func(record candidateevidence.OutcomeRecord, capture candidateevidence.Capture, intent candidateattempt.Intent) {
				t.Helper()
				raw, _ := json.Marshal(capture)
				if m, ok := s.(*Memory); ok {
					m.candidateWorkerOutcomes[report.RequestID], m.candidateOutcomeCaptures[report.RequestID], m.candidateWorkerIntents[report.RequestID] = record, raw, intent
				} else {
					pg := s.(*Postgres)
					payload, _ := json.Marshal(record)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_worker_outcomes SET payload=$2,capture=$3 WHERE id=$1", report.RequestID, payload, raw); err != nil {
						t.Fatal(err)
					}
					payload, _ = json.Marshal(intent)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_worker_intents SET payload=$2 WHERE id=$1", report.RequestID, payload); err != nil {
						t.Fatal(err)
					}
				}
			}
			// DB false-authority constraints already refuse the forged flag. Use a
			// self-hashed status mismatch to exercise application readback there.
			if _, ok := s.(*Postgres); ok && kind == "metadata" {
				badRecord = original
				badRecord.Status = "cancelled"
				badRecord.SHA256 = ""
				badRecord.SHA256 = workerplan.Hash(badRecord)
			}
			write(badRecord, badCapture, badIntent)
			defer write(original, report.Capture, i)

			if _, err := s.CandidateWorkerOutcomeHistory(ctx); err == nil {
				t.Fatal("coherent history hid corrupted capture/intent dependency")
			}
			if _, err := s.CandidateWorkerOutcomeEvidence(ctx, other.RequestID); err == nil {
				t.Fatal("coherent artifact hid unrelated corruption")
			}
			if _, err := s.CandidateWorkerOutcomes(ctx); err == nil {
				t.Fatal("corrupt full history accepted")
			}
			if _, err := s.CandidateWorkerOutcomeCapture(ctx, other.RequestID); err == nil {
				t.Fatal("selected artifact hid unrelated corruption")
			}
			if _, err := s.CandidateWorkerOutcome(ctx, other.RequestID); err == nil {
				t.Fatal("selected record hid unrelated corruption")
			}
			if _, err := s.ReportCandidateWorkerOutcome(ctx, other, now); err == nil {
				t.Fatal("exact retry hid unrelated corruption")
			}
		})
	}
}

func TestPostgresContractsCandidateOutcomesUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "candidate_outcome_upgrade")
	c, raw, at := candidateIntentFixture(t, s)
	i, err := s.StartCandidateWorker(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := s.CandidateRegistry(ctx)
	state, _ := s.Snapshot(ctx)
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DELETE FROM mantik_schema WHERE version>=32"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got, err := next.CandidateWorkerIntent(ctx, i.Command.RequestID); err != nil || !reflect.DeepEqual(got, i) {
		t.Fatal("upgrade changed intent", err)
	}
	if got, err := next.CandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, registry) {
		t.Fatal("upgrade changed registry", err)
	}
	if got := mustCandidateDataset(t, next, c.RegistrationID); !reflect.DeepEqual(raw, got) {
		t.Fatal("upgrade changed exact dataset")
	}
	if got, err := next.CandidateWorkerOutcomes(ctx); err != nil || len(got) != 0 {
		t.Fatal("upgrade invented outcomes", err)
	}
	if _, err := next.ReportCandidateWorkerOutcome(ctx, outcomeFixture(t, i), func() time.Time { return at.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}
	if got, _ := next.Snapshot(ctx); !reflect.DeepEqual(got, state) {
		t.Fatal("upgrade/report changed operations")
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=32"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
