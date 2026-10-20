package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type generationBackend interface {
	candidateBackend
	CandidateGenerationIntentStore
}

func generationFixture(t *testing.T, s generationBackend) (candidategeneration.Command, []byte, hypothesis.Publication, time.Time) {
	t.Helper()
	c, raw, pub, at := candidateFixture(t, s)
	p, err := candidategeneration.NewPlan(c.Candidate.Hypothesis.Declaration, raw, c.Candidate.Toolchain.ImageSHA256, "gpt-6.1-sol", "Propose a finite synthetic threshold example.", at)
	if err != nil {
		t.Fatal(err)
	}
	return candidategeneration.Command{ReceiverID: c.ReceiverID, RequestID: "generation-first", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: p}, raw, pub, at.Add(time.Second)
}
func TestMemoryCandidateGenerationIntents(t *testing.T) { generationIntentContracts(t, NewMemory()) }
func TestPostgresContractsCandidateGenerationIntents(t *testing.T) {
	generationIntentContracts(t, timerPostgres(t, "generation_intents"))
}
func generationIntentContracts(t *testing.T, s generationBackend) {
	ctx := context.Background()
	c, raw, _, at := generationFixture(t, s)
	before, _ := s.Snapshot(ctx)
	registry, _ := s.CandidateRegistry(ctx)
	empty, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil || empty.Validate() != nil || empty.Intents == nil || len(empty.Intents) != 0 || empty.ReceiverID != c.ReceiverID {
		t.Fatal(empty, err)
	}
	now := func() time.Time { return at }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			i, err := s.StartCandidateGeneration(ctx, c, raw, now)
			if err != nil || i.Validate() != nil || i.Sequence != 1 {
				t.Errorf("concurrent generation intent: %v", err)
			}
		})
	}
	wg.Wait()
	first, err := s.CandidateGenerationIntent(ctx, c.RequestID)
	if err != nil || first.Command.Plan.SHA256 != c.Plan.SHA256 || first.ProviderCalled || first.SourceGenerated || first.ModelIdentityVerified || first.QualificationCredit {
		t.Fatal(first, err)
	}
	if retry, err := s.StartCandidateGeneration(ctx, c, raw, func() time.Time { panic("retry read clock") }); err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("retry changed", err)
	}
	artifact, err := s.CandidateGenerationIntentEvidence(ctx, c.RequestID)
	if err != nil || artifact.Validate() != nil || !bytes.Equal(artifact.Dataset, raw) || !reflect.DeepEqual(artifact.Intent, first) {
		t.Fatal("artifact changed", err)
	}
	artifact.Dataset[0] ^= 1
	artifact.Intent.Command.Plan.Requirement = "caller changed"
	fresh, err := s.CandidateGenerationIntentEvidence(ctx, c.RequestID)
	if err != nil || fresh.Validate() != nil || !bytes.Equal(fresh.Dataset, raw) {
		t.Fatal("artifact aliases storage", err)
	}
	for _, kind := range []string{"model", "requirement", "publication-sha", "publication", "receiver"} {
		changed := clone(c)
		switch kind {
		case "model", "requirement":
			model, need := c.Plan.ModelLabel, c.Plan.Requirement
			if kind == "model" {
				model = "other-model"
			} else {
				need += " changed"
			}
			changed.Plan, err = candidategeneration.NewPlan(c.Plan.Hypothesis.Declaration, raw, c.Plan.Toolchain.ImageSHA256, model, need, c.Plan.RequestedAt)
			if err != nil {
				t.Fatal(err)
			}
		case "publication-sha":
			changed.PublicationSHA256 = strings.Repeat("b", 64)
		case "publication":
			changed.PublicationID = "missing"
		case "receiver":
			changed.ReceiverID = "receiver-other_other_other"
		}
		if _, err := s.StartCandidateGeneration(ctx, changed, raw, now); err == nil {
			t.Fatal("request rebound", kind)
		}
	}
	if _, err := s.StartCandidateGeneration(ctx, c, append(append([]byte{}, raw...), ' '), now); err == nil {
		t.Fatal("changed raw dataset retry accepted")
	}
	missing := c
	missing.RequestID = "missing-pub"
	missing.PublicationID = "missing"
	if _, err := s.StartCandidateGeneration(ctx, missing, raw, now); !errors.Is(err, candidategeneration.ErrNotFound) {
		t.Fatal("missing pub accepted", err)
	}
	early := c
	early.RequestID = "backdated-plan"
	early.Plan, err = candidategeneration.NewPlan(c.Plan.Hypothesis.Declaration, raw, c.Plan.Toolchain.ImageSHA256, c.Plan.ModelLabel, c.Plan.Requirement, c.Plan.RequestedAt.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartCandidateGeneration(ctx, early, raw, now); !errors.Is(err, candidategeneration.ErrConflict) {
		t.Fatal("plan before publication accepted", err)
	}
	late := c
	late.RequestID = "before-request"
	if _, err := s.StartCandidateGeneration(ctx, late, raw, func() time.Time { return c.Plan.RequestedAt.Add(-time.Second) }); !errors.Is(err, candidategeneration.ErrConflict) {
		t.Fatal("retention before request accepted", err)
	}
	second := c
	second.RequestID = "generation-second"
	if _, err := s.StartCandidateGeneration(ctx, second, raw, func() time.Time { return at.Add(-time.Nanosecond) }); err == nil {
		t.Fatal("nonmonotonic retention accepted")
	}
	if _, err := s.StartCandidateGeneration(ctx, second, raw, now); err != nil {
		t.Fatal(err)
	}
	beforeRestart, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil || beforeRestart.Validate() != nil || len(beforeRestart.Intents) != 2 {
		t.Fatal(err)
	}
	beforeRestart.Intents[0].Command.Plan.Requirement = "caller changed"
	clean, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil || clean.Validate() != nil {
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
		if read, err := s.CandidateGenerationIntentHistory(ctx); err != nil || !reflect.DeepEqual(clean, read) {
			t.Fatal("restart changed full history", err)
		}
		if read, err := s.CandidateGenerationIntentEvidence(ctx, c.RequestID); err != nil || !reflect.DeepEqual(fresh, read) {
			t.Fatal("restart changed exact dataset", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_generation_datasets ADD CONSTRAINT reject_generation CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		bad := c
		bad.RequestID = "rollback"
		if _, err := s.StartCandidateGeneration(ctx, bad, raw, now); err == nil {
			t.Fatal("failed artifact insert acknowledged")
		}
		if _, err := s.CandidateGenerationIntent(ctx, "rollback"); !errors.Is(err, candidategeneration.ErrNotFound) {
			t.Fatal("partial metadata row persisted", err)
		}
		if read, err := s.CandidateGenerationIntentHistory(ctx); err != nil || !reflect.DeepEqual(clean, read) {
			t.Fatal("failed insertion changed prefix", err)
		}
		if _, err := next.pool.Exec(ctx, "ALTER TABLE mantik_candidate_generation_datasets DROP CONSTRAINT reject_generation"); err != nil {
			t.Fatal(err)
		}
	}
	// Bounded history counts every request, even identical model/plan inputs.
	for n := 3; n <= candidategeneration.MaxIntents; n++ {
		next := c
		next.RequestID = fmt.Sprintf("generation-%02d", n)
		i, err := s.StartCandidateGeneration(ctx, next, raw, now)
		if err != nil || i.Sequence != n {
			t.Fatal(n, err)
		}
	}
	full, err := s.CandidateGenerationIntentHistory(ctx)
	if err != nil || full.Validate() != nil || len(full.Intents) != candidategeneration.MaxIntents {
		t.Fatal("capacity history", err)
	}
	if retry, err := s.StartCandidateGeneration(ctx, c, raw, func() time.Time { panic("full retry read clock") }); err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("capacity retry changed", err)
	}
	extra := c
	extra.RequestID = "one-too-many"
	if _, err := s.StartCandidateGeneration(ctx, extra, raw, func() time.Time { panic("capacity read clock") }); !errors.Is(err, candidategeneration.ErrCapacity) {
		t.Fatal("capacity overflow", err)
	}
	after, _ := s.Snapshot(ctx)
	regAfter, _ := s.CandidateRegistry(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(registry, regAfter) {
		t.Fatal("generation intent changed operation/candidate state")
	}
	for _, id := range []string{"missing", "../invalid"} {
		if _, err := s.CandidateGenerationIntent(ctx, id); err == nil {
			t.Fatal("bad selected ID accepted")
		}
		if _, err := s.CandidateGenerationIntentEvidence(ctx, id); err == nil {
			t.Fatal("bad artifact ID accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.CandidateGenerationIntentHistory(cancelled); err == nil {
		t.Fatal("cancelled history accepted")
	}
	if _, err := s.StartCandidateGeneration(cancelled, c, raw, now); err == nil {
		t.Fatal("cancelled start accepted")
	}
}
func TestMemoryGenerationIntentCorruption(t *testing.T) { generationIntentCorruption(t, NewMemory()) }
func TestPostgresContractsGenerationIntentCorruption(t *testing.T) {
	generationIntentCorruption(t, timerPostgres(t, "generation_corrupt"))
}
func generationIntentCorruption(t *testing.T, s generationBackend) {
	ctx := context.Background()
	c, raw, pub, at := generationFixture(t, s)
	now := func() time.Time { return at }
	first, err := s.StartCandidateGeneration(ctx, c, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	other := c
	other.RequestID = "other"
	if _, err := s.StartCandidateGeneration(ctx, other, raw, now); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"dataset", "missing-dataset", "plan", "ordinal", "publication"} {
		t.Run(kind, func(t *testing.T) {
			bad := clone(first)
			badPub := clone(pub)
			switch kind {
			case "plan":
				bad.Command.Plan.QualificationCredit = true
				bad.Command.Plan.SHA256 = ""
				bad.Command.Plan.SHA256 = workerplan.Hash(bad.Command.Plan)
			case "ordinal":
				bad.Sequence = 3
			case "publication":
				badPub.PublishedAt = badPub.PublishedAt.Add(time.Nanosecond)
			}
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			if m, ok := s.(*Memory); ok {
				m.generationIntents[c.RequestID] = bad
				switch kind {
				case "dataset":
					m.generationDatasets[c.RequestID] = append(append([]byte{}, raw...), ' ')
				case "missing-dataset":
					delete(m.generationDatasets, c.RequestID)
				case "publication":
					m.publications[pub.ID] = badPub
				}
			} else {
				pg := s.(*Postgres)
				payload, _ := json.Marshal(bad)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_intents SET sequence=$1,payload=$2 WHERE id=$3", bad.Sequence, payload, c.RequestID); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "dataset":
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_datasets SET content=$1 WHERE id=$2", append(append([]byte{}, raw...), ' '), c.RequestID); err != nil {
						t.Fatal(err)
					}
				case "missing-dataset":
					if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_candidate_generation_datasets WHERE id=$1", c.RequestID); err != nil {
						t.Fatal(err)
					}
				case "publication":
					b, _ := json.Marshal(badPub)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", b, pub.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.CandidateGenerationIntentHistory(ctx); err == nil {
				t.Fatal("corrupt full history accepted", kind)
			}
			if _, err := s.CandidateGenerationIntent(ctx, other.RequestID); err == nil {
				t.Fatal("selected row hid unrelated corruption", kind)
			}
			if _, err := s.CandidateGenerationIntentEvidence(ctx, other.RequestID); err == nil {
				t.Fatal("artifact hid unrelated corruption", kind)
			}
			if _, err := s.StartCandidateGeneration(ctx, other, raw, now); err == nil {
				t.Fatal("retry hid unrelated corruption", kind)
			}
			if m, ok := s.(*Memory); ok {
				m.generationIntents[c.RequestID] = first
				m.generationDatasets[c.RequestID] = slicesClone(raw)
				m.publications[pub.ID] = pub
			} else {
				pg := s.(*Postgres)
				b, _ := json.Marshal(first)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_candidate_generation_intents SET sequence=$1,payload=$2 WHERE id=$3", first.Sequence, b, c.RequestID); err != nil {
					t.Fatal(err)
				}
				if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_candidate_generation_datasets(id,content) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET content=EXCLUDED.content", c.RequestID, raw); err != nil {
					t.Fatal(err)
				}
				b, _ = json.Marshal(pub)
				if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", b, pub.ID); err != nil {
					t.Fatal(err)
				}
			}
			if h, err := s.CandidateGenerationIntentHistory(ctx); err != nil || h.Validate() != nil {
				t.Fatal("restore failed", err)
			}
		})
	}
}
func slicesClone(b []byte) []byte { return append([]byte{}, b...) }
func TestPostgresContractsGenerationIntentUpgrade(t *testing.T) {
	s := timerPostgres(t, "generation_upgrade")
	ctx := context.Background()
	c, raw, pub, at := candidateFixture(t, s)
	registered, err := s.RegisterCandidate(ctx, c, raw, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot(ctx)
	registry, _ := s.CandidateRegistry(ctx)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DELETE FROM mantik_schema WHERE version>=33"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	if got, err := next.CandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, registry) {
		t.Fatal("upgrade changed registry", err)
	}
	if got, err := next.CandidateDataset(ctx, registered.ID); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("upgrade changed old raw dataset", err)
	}
	if got, _ := next.Snapshot(ctx); !reflect.DeepEqual(got, state) {
		t.Fatal("upgrade changed operations")
	}
	empty, err := next.CandidateGenerationIntentHistory(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Intents) != 0 {
		t.Fatal("upgrade invented generation", err)
	}
	plan, err := candidategeneration.NewPlan(c.Candidate.Hypothesis.Declaration, raw, c.Candidate.Toolchain.ImageSHA256, "gpt-6.1-sol", "Retain a synthetic request.", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.StartCandidateGeneration(ctx, candidategeneration.Command{ReceiverID: c.ReceiverID, RequestID: "after-upgrade", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: plan}, raw, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=33"); err != nil {
		t.Fatal(err)
	}
	next.Close()
	bad, err := OpenPostgres(ctx, config)
	if err == nil {
		bad.Close()
		t.Fatal("migration checksum mismatch accepted")
	}
}
