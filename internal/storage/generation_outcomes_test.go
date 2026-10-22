package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type generationOutcomeBackend interface {
	generationBackend
	CandidateGenerationOutcomeStore
}

func generationReportFixture(t *testing.T, i candidategeneration.Intent, raw, output []byte) candidategeneration.Report {
	t.Helper()
	r, err := candidategeneration.NewRecording(i.Command.Plan, raw, output, i.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: i.Command.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: r}
}
func TestMemoryGenerationOutcomes(t *testing.T) { generationOutcomeContracts(t, NewMemory()) }
func TestPostgresContractsGenerationOutcomes(t *testing.T) {
	generationOutcomeContracts(t, timerPostgres(t, "generation_outcomes"))
}
func generationOutcomeContracts(t *testing.T, s generationOutcomeBackend) {
	ctx := context.Background()
	c, raw, _, at := generationFixture(t, s)
	now := func() time.Time { return at }
	before, _ := s.Snapshot(ctx)
	registry, _ := s.CandidateRegistry(ctx)
	empty, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Outcomes) != 0 || len(empty.UnreportedRequestIDs) != 0 {
		t.Fatal(empty, err)
	}
	first, err := s.StartCandidateGeneration(ctx, c, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	source := "package candidate\r\nfunc Entry(p int64)bool{return p<=10000}\r\n"
	output, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: source})
	report := generationReportFixture(t, first, raw, output)
	retention := func() time.Time { return report.Recording.CapturedAt.Add(time.Second) }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			o, err := s.ReportCandidateGeneration(ctx, report, retention)
			if err != nil || o.Validate() != nil || o.IntentSequence != 1 {
				t.Errorf("concurrent generation report: %v", err)
			}
		})
	}
	wg.Wait()
	original, err := s.CandidateGenerationOutcome(ctx, c.RequestID)
	if err != nil || original.CandidateSHA256 != report.Recording.Candidate.SHA256 || original.ProviderCalled || original.ModelAuthorshipVerified || original.SourceCompiled || original.QualificationCredit {
		t.Fatal(original, err)
	}
	noClock := func() time.Time { panic("retry/invalid binding read clock") }
	if retry, err := s.ReportCandidateGeneration(ctx, report, noClock); err != nil || !reflect.DeepEqual(retry, original) {
		t.Fatal("retry changed receipt", err)
	}
	artifact, err := s.CandidateGenerationOutcomeEvidence(ctx, c.RequestID)
	if err != nil || artifact.Validate() != nil || !reflect.DeepEqual(artifact.Recording, report.Recording) || artifact.Recording.Candidate.Source.Text != source {
		t.Fatal("full artifact changed", err)
	}
	artifact.Recording.Output[0] ^= 1
	artifact.Intent.Command.Plan.Requirement = "mutated"
	fresh, err := s.CandidateGenerationOutcomeEvidence(ctx, c.RequestID)
	if err != nil || fresh.Validate() != nil {
		t.Fatal("artifact aliases storage", err)
	}
	for _, kind := range []string{"receiver", "request", "intent", "output", "capture-time", "model", "authority"} {
		bad := clone(report)
		switch kind {
		case "receiver":
			bad.ReceiverID = "receiver-other_other_other"
		case "request":
			bad.RequestID = "missing"
		case "intent":
			bad.IntentSHA256 = strings.Repeat("f", 64)
		case "output":
			bad.Recording, err = candidategeneration.NewRecording(c.Plan, raw, []byte{0xff}, report.Recording.CapturedAt)
		case "capture-time":
			bad.Recording, err = candidategeneration.NewRecording(c.Plan, raw, output, report.Recording.CapturedAt.Add(time.Nanosecond))
		case "model":
			p, e := candidategeneration.NewPlan(c.Plan.Hypothesis.Declaration, raw, c.Plan.Toolchain.ImageSHA256, "other-model", c.Plan.Requirement, c.Plan.RequestedAt)
			if e != nil {
				t.Fatal(e)
			}
			bad.Recording, err = candidategeneration.NewRecording(p, raw, output, report.Recording.CapturedAt)
		case "authority":
			bad.Recording.ModelAuthorshipVerified = true
			bad.Recording.SHA256 = ""
			bad.Recording.SHA256 = workerplan.Hash(bad.Recording)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReportCandidateGeneration(ctx, bad, noClock); err == nil {
			t.Fatal("report rebound", kind)
		}
	}
	// A second intent remains unreported while the third is reported first.
	secondCommand := c
	secondCommand.RequestID = "generation-second"
	second, err := s.StartCandidateGeneration(ctx, secondCommand, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	thirdCommand := c
	thirdCommand.RequestID = "generation-third"
	third, err := s.StartCandidateGeneration(ctx, thirdCommand, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	thirdReport := generationReportFixture(t, third, raw, []byte{0xff, 0, 0xfe})
	if _, err := s.ReportCandidateGeneration(ctx, thirdReport, retention); err != nil {
		t.Fatal(err)
	}
	sparse, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil || sparse.Validate() != nil || !reflect.DeepEqual(sparse.UnreportedRequestIDs, []string{secondCommand.RequestID}) || len(sparse.Outcomes) != 2 {
		t.Fatal("sparse report history changed", err)
	}
	early := generationReportFixture(t, second, raw, []byte{})
	early.Recording.CapturedAt = c.Plan.RequestedAt
	early.Recording.SHA256 = ""
	early.Recording.SHA256 = workerplan.Hash(early.Recording)
	if _, err := s.ReportCandidateGeneration(ctx, early, noClock); err == nil {
		t.Fatal("capture before intent accepted")
	}
	secondReport := generationReportFixture(t, second, raw, []byte{})
	if _, err := s.ReportCandidateGeneration(ctx, secondReport, func() time.Time { return secondReport.Recording.CapturedAt.Add(-time.Nanosecond) }); err == nil {
		t.Fatal("server retention before capture accepted")
	}
	if _, err := s.ReportCandidateGeneration(ctx, secondReport, retention); err != nil {
		t.Fatal(err)
	}
	h, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil || h.Validate() != nil || len(h.Outcomes) != 3 || len(h.UnreportedRequestIDs) != 0 || h.Outcomes[1].ReasonCode != "missing_output" || h.Outcomes[2].ReasonCode != "invalid_output" {
		t.Fatal(h, err)
	}
	detached := clone(h)
	h.IntentHistory.Intents[0].Command.Plan.Requirement = "caller changed"
	h.Outcomes[0].ReasonCode = "caller changed"
	if got, err := s.CandidateGenerationOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(got, detached) {
		t.Fatal("history aliases storage", err)
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
		if got, err := s.CandidateGenerationOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(got, detached) {
			t.Fatal("reopen changed history", err)
		}
		if got, err := s.CandidateGenerationOutcomeEvidence(ctx, c.RequestID); err != nil || !reflect.DeepEqual(got, fresh) {
			t.Fatal("reopen changed exact recording", err)
		}
		// A single row contains metadata+recording: a failed insert acknowledges none.
		rollback := c
		rollback.RequestID = "rollback"
		i, err := s.StartCandidateGeneration(ctx, rollback, raw, now)
		if err != nil {
			t.Fatal(err)
		}
		r := generationReportFixture(t, i, raw, []byte{0xfe})
		prior, err := s.CandidateGenerationOutcomeHistory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_generation_outcomes ADD CONSTRAINT reject_report CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReportCandidateGeneration(ctx, r, retention); err == nil {
			t.Fatal("failed insert acknowledged")
		}
		if _, err := s.CandidateGenerationOutcome(ctx, rollback.RequestID); !errors.Is(err, candidategeneration.ErrNotFound) {
			t.Fatal("partial report persisted", err)
		}
		if got, err := s.CandidateGenerationOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(got, prior) {
			t.Fatal("failed insert changed history", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_generation_outcomes DROP CONSTRAINT reject_report"); err != nil {
			t.Fatal(err)
		}
	}
	intents, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for n := len(intents.Intents) + 1; n <= candidategeneration.MaxIntents; n++ {
		next := c
		next.RequestID = fmt.Sprintf("generation-%02d", n)
		if _, err := s.StartCandidateGeneration(ctx, next, raw, now); err != nil {
			t.Fatal(n, err)
		}
	}
	intents, err = s.CandidateGenerationIntentHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Cover all intent slots, including failed outputs and the rolled-back request.
	for _, i := range intents.Intents {
		if i.Sequence <= 3 {
			continue
		}
		r := generationReportFixture(t, i, raw, []byte{0xff, byte(i.Sequence)})
		if _, err := s.ReportCandidateGeneration(ctx, r, retention); err != nil {
			t.Fatal(i.Sequence, err)
		}
	}
	full, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil || full.Validate() != nil || len(full.Outcomes) != candidategeneration.MaxIntents || len(full.UnreportedRequestIDs) != 0 {
		t.Fatal("full bounded history", err)
	}
	if got, err := s.ReportCandidateGeneration(ctx, report, noClock); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal("full retry changed", err)
	}
	after, _ := s.Snapshot(ctx)
	regAfter, _ := s.CandidateRegistry(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(registry, regAfter) {
		t.Fatal("report changed operations or registrations")
	}
	for _, id := range []string{"missing", "../invalid"} {
		if _, err := s.CandidateGenerationOutcome(ctx, id); err == nil {
			t.Fatal("bad selected accepted")
		}
		if _, err := s.CandidateGenerationOutcomeEvidence(ctx, id); err == nil {
			t.Fatal("bad artifact accepted")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReportCandidateGeneration(canceled, report, noClock); err == nil {
		t.Fatal("canceled report accepted")
	}
	if _, err := s.CandidateGenerationOutcomeHistory(canceled); err == nil {
		t.Fatal("canceled read accepted")
	}
}
func TestMemoryGenerationOutcomeCorruption(t *testing.T) { generationOutcomeCorruption(t, NewMemory()) }
func TestPostgresContractsGenerationOutcomeCorruption(t *testing.T) {
	generationOutcomeCorruption(t, timerPostgres(t, "generation_outcome_corrupt"))
}
func generationOutcomeCorruption(t *testing.T, s generationOutcomeBackend) {
	ctx := context.Background()
	c, raw, pub, at := generationFixture(t, s)
	now := func() time.Time { return at }
	first, err := s.StartCandidateGeneration(ctx, c, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	other := c
	other.RequestID = "other"
	second, err := s.StartCandidateGeneration(ctx, other, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	report := generationReportFixture(t, first, raw, []byte{0xff})
	otherReport := generationReportFixture(t, second, raw, []byte{})
	retention := func() time.Time { return report.Recording.CapturedAt }
	o, err := s.ReportCandidateGeneration(ctx, report, retention)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportCandidateGeneration(ctx, otherReport, retention); err != nil {
		t.Fatal(err)
	}
	prior, err := s.CandidateGenerationOutcomeHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"recording", "recording-plan", "metadata", "ordinal", "dataset", "publication", "missing-recording"} {
		t.Run(kind, func(t *testing.T) {
			bad := clone(o)
			badRec := clone(report.Recording)
			badPub := clone(pub)
			switch kind {
			case "metadata":
				bad.OutputSHA256 = strings.Repeat("f", 64)
			case "ordinal":
				bad.IntentSequence = 3
			case "recording-plan":
				badRec.ModelAuthorshipVerified = true
				badRec.SHA256 = ""
				badRec.SHA256 = workerplan.Hash(badRec)
			case "publication":
				badPub.PublishedAt = badPub.PublishedAt.Add(time.Nanosecond)
			}
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			encoded, _ := json.Marshal(badRec)
			if kind == "recording" || kind == "missing-recording" {
				encoded = []byte("null")
			}
			if m, ok := s.(*Memory); ok {
				m.generationOutcomes[c.RequestID] = bad
				m.generationRecordings[c.RequestID] = encoded
				switch kind {
				case "missing-recording":
					delete(m.generationRecordings, c.RequestID)
				case "dataset":
					m.generationDatasets[c.RequestID] = append(bytes.Clone(raw), ' ')
				case "publication":
					m.publications[pub.ID] = badPub
				}
			} else {
				pg := s.(*Postgres)
				b, _ := json.Marshal(bad)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_outcomes SET sequence=$1,payload=$2,recording=$3 WHERE id=$4", bad.IntentSequence, b, encoded, c.RequestID); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "dataset":
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_datasets SET content=$1 WHERE id=$2", append(bytes.Clone(raw), ' '), c.RequestID); err != nil {
						t.Fatal(err)
					}
				case "publication":
					b, _ := json.Marshal(badPub)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", b, pub.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.CandidateGenerationOutcomeHistory(ctx); err == nil {
				t.Fatal("corrupt history accepted", kind)
			}
			if _, err := s.CandidateGenerationOutcome(ctx, other.RequestID); err == nil {
				t.Fatal("selected hid corruption", kind)
			}
			if _, err := s.CandidateGenerationOutcomeEvidence(ctx, other.RequestID); err == nil {
				t.Fatal("artifact hid corruption", kind)
			}
			if _, err := s.ReportCandidateGeneration(ctx, otherReport, func() time.Time { panic("corrupt retry read clock") }); err == nil {
				t.Fatal("retry hid corruption", kind)
			}
			if m, ok := s.(*Memory); ok {
				m.generationOutcomes[c.RequestID] = o
				encoded, _ := json.Marshal(report.Recording)
				m.generationRecordings[c.RequestID] = encoded
				m.generationDatasets[c.RequestID] = bytes.Clone(raw)
				m.publications[pub.ID] = pub
			} else {
				pg := s.(*Postgres)
				b, _ := json.Marshal(o)
				encoded, _ := json.Marshal(report.Recording)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_outcomes SET sequence=$1,payload=$2,recording=$3 WHERE id=$4", o.IntentSequence, b, encoded, c.RequestID); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_datasets SET content=$1 WHERE id=$2", raw, c.RequestID); err != nil {
					t.Fatal(err)
				}
				b, _ = json.Marshal(pub)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", b, pub.ID); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.CandidateGenerationOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(got, prior) {
				t.Fatal("restore failed", err)
			}
		})
	}
}
func TestPostgresContractsGenerationOutcomeUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "generation_outcome_upgrade")
	c, raw, _, at := generationFixture(t, s)
	now := func() time.Time { return at }
	intent, err := s.StartCandidateGeneration(ctx, c, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot(ctx)
	registry, _ := s.CandidateRegistry(ctx)
	intents, _ := s.CandidateGenerationIntentHistory(ctx)
	artifact, _ := s.CandidateGenerationIntentEvidence(ctx, c.RequestID)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DELETE FROM mantik_schema WHERE version>=34"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	if got, err := next.CandidateGenerationIntentHistory(ctx); err != nil || !reflect.DeepEqual(got, intents) {
		t.Fatal("upgrade changed intents", err)
	}
	if got, err := next.CandidateGenerationIntentEvidence(ctx, c.RequestID); err != nil || !reflect.DeepEqual(got, artifact) {
		t.Fatal("upgrade changed exact dataset", err)
	}
	if got, _ := next.CandidateRegistry(ctx); !reflect.DeepEqual(got, registry) {
		t.Fatal("upgrade changed registry")
	}
	if got, _ := next.Snapshot(ctx); !reflect.DeepEqual(got, state) {
		t.Fatal("upgrade changed state")
	}
	empty, err := next.CandidateGenerationOutcomeHistory(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Outcomes) != 0 || !reflect.DeepEqual(empty.UnreportedRequestIDs, []string{c.RequestID}) {
		t.Fatal("upgrade invented reports", err)
	}
	r := generationReportFixture(t, intent, raw, []byte{})
	if _, err := next.ReportCandidateGeneration(ctx, r, func() time.Time { return r.Recording.CapturedAt }); err != nil {
		t.Fatal(err)
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=34"); err != nil {
		t.Fatal(err)
	}
	next.Close()
	bad, err := OpenPostgres(ctx, config)
	if err == nil {
		bad.Close()
		t.Fatal("schema checksum mismatch accepted")
	}
}
