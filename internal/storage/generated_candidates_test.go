package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type generatedBackend interface {
	generationOutcomeBackend
	GeneratedCandidateStore
}

func generatedFixture(t *testing.T, s generatedBackend) (generatedcandidate.Command, []byte, time.Time) {
	t.Helper()
	ctx := context.Background()
	c, raw, _, at := generationFixture(t, s)
	i, err := s.StartCandidateGeneration(ctx, c, raw, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: "deliberately invalid Go; preserve compile failure\r\n"})
	r := generationReportFixture(t, i, raw, output)
	o, err := s.ReportCandidateGeneration(ctx, r, func() time.Time { return r.Recording.CapturedAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	return generatedcandidate.Command{ReceiverID: c.ReceiverID, RequestID: "generated-first", GenerationRequestID: c.RequestID, IntentSHA256: i.SHA256, OutcomeSHA256: o.SHA256, RecordingSHA256: r.Recording.SHA256, PlanSHA256: c.Plan.SHA256, CandidateSHA256: r.Recording.Candidate.SHA256}, raw, o.RecordedAt.Add(time.Second)
}
func TestMemoryGeneratedCandidates(t *testing.T) { generatedContracts(t, NewMemory()) }
func TestPostgresContractsGeneratedCandidates(t *testing.T) {
	generatedContracts(t, timerPostgres(t, "generated_candidates"))
}
func generatedContracts(t *testing.T, s generatedBackend) {
	ctx := context.Background()
	c, raw, at := generatedFixture(t, s)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("invalid or exact request read clock") }
	state, _ := s.Snapshot(ctx)
	empty, err := s.GeneratedCandidateRegistry(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Links) != 0 || len(empty.UnlinkedRegistrationIDs) != 0 {
		t.Fatal("empty", err)
	}
	// Ordinary manual source remains unlinked even when it is byte-identical to a
	// retained generation. Adding a provenance link is an explicit initial write.
	a, err := s.CandidateGenerationOutcomeEvidence(ctx, c.GenerationRequestID)
	if err != nil {
		t.Fatal(err)
	}
	manual := candidatelineage.Command{ReceiverID: c.ReceiverID, RequestID: "manual", PublicationID: a.Intent.Command.PublicationID, Candidate: *a.Recording.Candidate}
	old, err := s.RegisterCandidate(ctx, manual, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	adopt := c
	adopt.RequestID = old.ID
	if _, err := s.RegisterGeneratedCandidate(ctx, adopt, noClock); !errors.Is(err, generatedcandidate.ErrConflict) {
		t.Fatal("adopted old declaration", err)
	}
	c.ParentID = old.ID
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v, err := s.RegisterGeneratedCandidate(ctx, c, now)
			if err != nil || v.Validate() != nil || v.Registration.Sequence != 2 {
				t.Errorf("concurrent generated registration: %v", err)
			}
		})
	}
	wg.Wait()
	first, err := s.GeneratedCandidateEvidence(ctx, c.RequestID)
	if err != nil || first.Validate() != nil {
		t.Fatal("artifact", err)
	}
	if first.Link.ProviderCalled || first.Link.ModelAuthorshipVerified || first.Link.QualificationCredit || first.Registration.ExecutionStarted || first.Generation.Recording.Candidate.Source.Text != a.Recording.Candidate.Source.Text {
		t.Fatal("changed source/authority")
	}
	legacy, err := s.CandidateRegistration(ctx, c.RequestID)
	if err != nil || !reflect.DeepEqual(legacy, first.Registration) {
		t.Fatal("legacy changed", err)
	}
	if data, err := s.CandidateDataset(ctx, c.RequestID); err != nil || !bytes.Equal(data, raw) {
		t.Fatal("exact dataset changed", err)
	}
	for _, kind := range []string{"owner", "generation", "intent", "outcome", "recording", "plan", "candidate", "parent"} {
		bad := c
		switch kind {
		case "owner":
			bad.ReceiverID = "receiver-other_other_other"
		case "generation":
			bad.GenerationRequestID = "missing"
		case "intent":
			bad.IntentSHA256 = strings.Repeat("f", 64)
		case "outcome":
			bad.OutcomeSHA256 = strings.Repeat("f", 64)
		case "recording":
			bad.RecordingSHA256 = strings.Repeat("f", 64)
		case "plan":
			bad.PlanSHA256 = strings.Repeat("f", 64)
		case "candidate":
			bad.CandidateSHA256 = strings.Repeat("f", 64)
		case "parent":
			bad.ParentID = "missing"
		}
		if v, err := s.RegisterGeneratedCandidate(ctx, bad, noClock); err == nil || v.Schema != "" {
			t.Fatal("accepted changed pin", kind, err)
		}
	}
	backdated := c
	backdated.RequestID = "backdated"
	if v, err := s.RegisterGeneratedCandidate(ctx, backdated, func() time.Time { return a.Outcome.RecordedAt.Add(-time.Second) }); err == nil || v.Schema != "" {
		t.Fatal("backdated partial acknowledgment", err)
	}
	// Exact retries preserve metadata and legacy callers cannot erase the marker.
	if retry, err := s.RegisterGeneratedCandidate(ctx, c, noClock); err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("retry changed", err)
	}
	legacyCommand := manual
	legacyCommand.RequestID = c.RequestID
	legacyCommand.ParentID = c.ParentID
	if retry, err := s.RegisterCandidate(ctx, legacyCommand, raw, noClock); err != nil || !reflect.DeepEqual(retry, first.Registration) {
		t.Fatal("legacy retry changed link registration", err)
	}
	first.Generation.Recording.Output[0] ^= 1
	first.Registration.Candidate.Source.Text = "mutated"
	first, err = s.GeneratedCandidateEvidence(ctx, c.RequestID)
	if err != nil || first.Validate() != nil {
		t.Fatal("aliased evidence", err)
	}
	// Every registration slot, including mixed manual/generated lineage, shares
	// the legacy bound; all generated retries still work at capacity.
	commands := []generatedcandidate.Command{c}
	for n := 3; n <= candidatelineage.MaxRegistrations; n++ {
		next := c
		next.RequestID = fmt.Sprintf("generated-%02d", n)
		next.ParentID = commands[len(commands)-1].RequestID
		v, err := s.RegisterGeneratedCandidate(ctx, next, now)
		if err != nil || v.Registration.Sequence != n {
			t.Fatal(n, err)
		}
		commands = append(commands, next)
	}
	for _, cmd := range commands {
		if v, err := s.RegisterGeneratedCandidate(ctx, cmd, noClock); err != nil || v.Link.Command != cmd {
			t.Fatal("capacity retry", err)
		}
	}
	over := c
	over.RequestID = "overflow"
	if _, err := s.RegisterGeneratedCandidate(ctx, over, noClock); !errors.Is(err, candidatelineage.ErrCapacity) {
		t.Fatal("capacity", err)
	}
	history, err := s.GeneratedCandidateRegistry(ctx)
	if err != nil || history.Validate() != nil || len(history.Links) != 31 || !reflect.DeepEqual(history.UnlinkedRegistrationIDs, []string{old.ID}) {
		t.Fatal("mixed history", err)
	}
	// A separately retained rejected report stays inspectable but cannot enter the
	// registry, even when the candidate registry is already full.
	gc := a.Intent.Command
	gc.RequestID = "rejected-generation"
	i, err := s.StartCandidateGeneration(ctx, gc, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	report := generationReportFixture(t, i, raw, []byte{0xff, 0, 0xfe})
	o, err := s.ReportCandidateGeneration(ctx, report, func() time.Time { return report.Recording.CapturedAt })
	if err != nil {
		t.Fatal(err)
	}
	rejected := c
	rejected.RequestID = "rejected-registration"
	rejected.GenerationRequestID = gc.RequestID
	rejected.IntentSHA256 = i.SHA256
	rejected.OutcomeSHA256 = o.SHA256
	rejected.RecordingSHA256 = report.Recording.SHA256
	if _, err := s.RegisterGeneratedCandidate(ctx, rejected, noClock); err == nil {
		t.Fatal("registered rejected output")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.RegisterGeneratedCandidate(cancelled, c, noClock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.GeneratedCandidateEvidence(ctx, ""); err == nil {
		t.Fatal("empty selector")
	}
	if _, err := s.GeneratedCandidateEvidence(ctx, "manual"); !errors.Is(err, candidatelineage.ErrNotFound) {
		t.Fatal("manual gained link", err)
	}
	if got, _ := s.Snapshot(ctx); !reflect.DeepEqual(got, state) {
		t.Fatal("registration changed runtime/effects")
	}
	// Every full typed artifact survives an actual PostgreSQL pool reopen.
	if pg, ok := s.(*Postgres); ok {
		next, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		if got, err := next.GeneratedCandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, history) {
			t.Fatal("reopen history", err)
		}
		for _, cmd := range commands {
			got, err := next.GeneratedCandidateEvidence(ctx, cmd.RequestID)
			if err != nil || got.Validate() != nil || got.Generation.Recording.Candidate.Source.Text != a.Recording.Candidate.Source.Text {
				t.Fatal("reopen artifact", err)
			}
		}
	}
}
func TestMemoryGeneratedCandidateDamage(t *testing.T) { generatedDamage(t, NewMemory()) }
func TestPostgresContractsGeneratedCandidateDamage(t *testing.T) {
	generatedDamage(t, timerPostgres(t, "generated_damage"))
}
func generatedDamage(t *testing.T, s generatedBackend) {
	ctx := context.Background()
	c, raw, at := generatedFixture(t, s)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("corrupt prefix read clock") }
	first, err := s.RegisterGeneratedCandidate(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	second := c
	second.RequestID = "second"
	if _, err := s.RegisterGeneratedCandidate(ctx, second, now); err != nil {
		t.Fatal(err)
	}
	prior, err := s.GeneratedCandidateRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"marker", "link", "generation-recording", "generation-dataset", "publication", "candidate-dataset"} {
		t.Run(kind, func(t *testing.T) {
			var restore func()
			if m, ok := s.(*Memory); ok {
				switch kind {
				case "marker":
					old := m.generatedMarkers[c.RequestID]
					delete(m.generatedMarkers, c.RequestID)
					restore = func() { m.generatedMarkers[c.RequestID] = old }
				case "link":
					old := m.generatedLinks[c.RequestID]
					delete(m.generatedLinks, c.RequestID)
					restore = func() { m.generatedLinks[c.RequestID] = old }
				case "generation-recording":
					old := m.generationRecordings[c.GenerationRequestID]
					m.generationRecordings[c.GenerationRequestID] = []byte("null")
					restore = func() { m.generationRecordings[c.GenerationRequestID] = old }
				case "generation-dataset":
					old := m.generationDatasets[c.GenerationRequestID]
					m.generationDatasets[c.GenerationRequestID] = append(bytes.Clone(old), ' ')
					restore = func() { m.generationDatasets[c.GenerationRequestID] = old }
				case "publication":
					old := m.publications[first.Registration.PublicationID]
					delete(m.publications, old.ID)
					restore = func() { m.publications[old.ID] = old }
				case "candidate-dataset":
					old := m.candidateDatasets[c.RequestID]
					delete(m.candidateDatasets, c.RequestID)
					restore = func() { m.candidateDatasets[c.RequestID] = old }
				}
			} else {
				pg := s.(*Postgres)
				switch kind {
				case "marker", "link":
					// Self-hash a plausible but incorrect compact link. DB constraints allow
					// equal marker/hash; full source rederivation must still reject it.
					bad := first.Link
					bad.OutcomeRecordedAt = bad.OutcomeRecordedAt.Add(-time.Second)
					bad.SHA256 = ""
					bad.SHA256 = workerplan.Hash(bad)
					b, _ := json.Marshal(bad)
					tx, err := pg.pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					_, err = tx.Exec(ctx, "UPDATE mantik_candidate_registrations SET generation_link_sha256=$1 WHERE id=$2", bad.SHA256, c.RequestID)
					if err == nil {
						_, err = tx.Exec(ctx, "UPDATE mantik_generated_candidate_links SET sha256=$1,payload=$2 WHERE id=$3", bad.SHA256, b, c.RequestID)
					}
					if err == nil {
						err = tx.Commit(ctx)
					} else {
						_ = tx.Rollback(ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
					restore = func() {
						tx, err := pg.pool.Begin(ctx)
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback(ctx)
						b, _ := json.Marshal(first.Link)
						if _, err = tx.Exec(ctx, "UPDATE mantik_candidate_registrations SET generation_link_sha256=$1 WHERE id=$2", first.Link.SHA256, c.RequestID); err != nil {
							t.Fatal(err)
						}
						if _, err = tx.Exec(ctx, "UPDATE mantik_generated_candidate_links SET sha256=$1,payload=$2 WHERE id=$3", first.Link.SHA256, b, c.RequestID); err != nil {
							t.Fatal(err)
						}
						if err = tx.Commit(ctx); err != nil {
							t.Fatal(err)
						}
					}
				case "generation-recording", "generation-dataset", "candidate-dataset":
					table, column, id := "mantik_candidate_generation_outcomes", "recording", c.GenerationRequestID
					if kind == "generation-dataset" {
						table, column = "mantik_candidate_generation_datasets", "content"
					}
					if kind == "candidate-dataset" {
						table, column, id = "mantik_candidate_datasets", "content", c.RequestID
					}
					var old []byte
					if err := pg.pool.QueryRow(ctx, "SELECT "+column+" FROM "+table+" WHERE id=$1", id).Scan(&old); err != nil {
						t.Fatal(err)
					}
					if _, err := pg.pool.Exec(ctx, "UPDATE "+table+" SET "+column+"=$1 WHERE id=$2", []byte("null"), id); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if _, err := pg.pool.Exec(ctx, "UPDATE "+table+" SET "+column+"=$1 WHERE id=$2", old, id); err != nil {
							t.Fatal(err)
						}
					}
				case "publication":
					var old []byte
					id := first.Registration.PublicationID
					if err := pg.pool.QueryRow(ctx, "SELECT payload FROM mantik_hypothesis_publications WHERE id=$1", id).Scan(&old); err != nil {
						t.Fatal(err)
					}
					var pub map[string]any
					_ = json.Unmarshal(old, &pub)
					pub["published_at"] = first.Registration.RegisteredAt.Add(time.Hour).Format(time.RFC3339Nano)
					b, _ := json.Marshal(pub)
					if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", b, id); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if _, err := pg.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$1 WHERE id=$2", old, id); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			defer restore()
			if _, err := s.GeneratedCandidateRegistry(ctx); err == nil {
				t.Fatal("damaged discovery accepted")
			}
			if _, err := s.GeneratedCandidateEvidence(ctx, second.RequestID); err == nil {
				t.Fatal("selected artifact hid unrelated damage")
			}
			if _, err := s.RegisterGeneratedCandidate(ctx, second, noClock); err == nil {
				t.Fatal("retry hid unrelated damage")
			}
			fresh := second
			fresh.RequestID = "new-while-damaged"
			if _, err := s.RegisterGeneratedCandidate(ctx, fresh, noClock); err == nil {
				t.Fatal("new registration hid unrelated damage")
			}
			if _, err := s.CandidateRegistry(ctx); err == nil {
				t.Fatal("ordinary registry hid damage")
			}
			if _, err := s.CandidateDataset(ctx, second.RequestID); err == nil {
				t.Fatal("ordinary dataset hid damage")
			}
			ordinary := candidatelineage.Command{ReceiverID: c.ReceiverID, RequestID: second.RequestID, PublicationID: first.Registration.PublicationID, Candidate: first.Registration.Candidate}
			if _, err := s.RegisterCandidate(ctx, ordinary, raw, noClock); err == nil {
				t.Fatal("ordinary retry hid damage")
			}
		})
		if got, err := s.GeneratedCandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, prior) {
			t.Fatal("restore", err)
		}
	}
}
func TestPostgresContractsGeneratedCandidateAtomicity(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "generated_atomicity")
	c, raw, at := generatedFixture(t, s)
	now := func() time.Time { return at }
	prior, err := s.GeneratedCandidateRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_generated_candidate_links ADD CONSTRAINT injected_failure CHECK(id<>'generated-first')"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.RegisterGeneratedCandidate(ctx, c, now); err == nil || v.Schema != "" {
		t.Fatal("partial write acknowledged", err)
	}
	if got, err := s.GeneratedCandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, prior) {
		t.Fatal("failed link insert left registration", err)
	}
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_candidate_datasets").Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan dataset", count, err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_generated_candidate_links DROP CONSTRAINT injected_failure"); err != nil {
		t.Fatal(err)
	}
	a, err := s.RegisterGeneratedCandidate(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"DELETE FROM mantik_generated_candidate_links WHERE id=$1", "UPDATE mantik_candidate_registrations SET generation_link_sha256=NULL WHERE id=$1"} {
		if _, err := s.pool.Exec(ctx, sql, c.RequestID); err == nil {
			t.Fatal("dangling link constraint accepted", sql)
		}
	}
	if got, err := s.GeneratedCandidateEvidence(ctx, c.RequestID); err != nil || !reflect.DeepEqual(got, a) {
		t.Fatal("failed deletion changed receipt", err)
	}
	if got, err := s.CandidateDataset(ctx, c.RequestID); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("raw data", err)
	}
}
func TestPostgresContractsGeneratedCandidateUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "generated_upgrade")
	c, raw, at := generatedFixture(t, s)
	now := func() time.Time { return at }
	gen, err := s.CandidateGenerationOutcomeEvidence(ctx, c.GenerationRequestID)
	if err != nil {
		t.Fatal(err)
	}
	manual := candidatelineage.Command{ReceiverID: c.ReceiverID, RequestID: "manual-before-upgrade", PublicationID: gen.Intent.Command.PublicationID, Candidate: *gen.Recording.Candidate}
	if _, err := s.RegisterCandidate(ctx, manual, raw, now); err != nil {
		t.Fatal(err)
	}
	registry, _ := s.CandidateRegistry(ctx)
	state, _ := s.Snapshot(ctx)
	history, _ := s.CandidateGenerationOutcomeHistory(ctx)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DELETE FROM mantik_schema WHERE version>=35"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got, err := next.CandidateRegistry(ctx); err != nil || !reflect.DeepEqual(got, registry) {
		t.Fatal("upgrade changed legacy registry", err)
	}
	if got, _ := next.Snapshot(ctx); !reflect.DeepEqual(got, state) {
		t.Fatal("upgrade changed state")
	}
	if got, err := next.CandidateGenerationOutcomeHistory(ctx); err != nil || !reflect.DeepEqual(got, history) {
		t.Fatal("upgrade changed history", err)
	}
	if got, err := next.CandidateGenerationOutcomeEvidence(ctx, c.GenerationRequestID); err != nil || !reflect.DeepEqual(got, gen) {
		t.Fatal("upgrade changed exact recording", err)
	}
	h, err := next.GeneratedCandidateRegistry(ctx)
	if err != nil || len(h.Links) != 0 || !reflect.DeepEqual(h.UnlinkedRegistrationIDs, []string{manual.RequestID}) {
		t.Fatal("upgrade invented link", err)
	}
	if _, err := next.RegisterGeneratedCandidate(ctx, c, now); err != nil {
		t.Fatal("post upgrade write", err)
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=35"); err != nil {
		t.Fatal(err)
	}
	next.Close()
	bad, err := OpenPostgres(ctx, config)
	if err == nil {
		bad.Close()
		t.Fatal("changed checksum admitted")
	}
}
