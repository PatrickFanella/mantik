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

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

type candidateBackend interface {
	hypothesisBackend
	CandidateStore
}

func candidateFixture(t *testing.T, s candidateBackend) (candidatelineage.Command, []byte, hypothesis.Publication, time.Time) {
	t.Helper()
	ctx := context.Background()
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	d := candidateDeclaration(t, s, catalog.ReceiverID, "candidate-review", admitted.Matches[0].CaseID, 1, now)
	p, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "candidate-publication", "candidate-review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := dataset.FixtureDataset()
	raw, _ := json.Marshal(data)
	// Preserve exact formatting, not merely canonical dataset identity.
	raw = append(raw, '\n')
	pack, err := candidate.New([]byte("intentionally invalid Go; preserve later failures\n"), d, raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return candidatelineage.Command{ReceiverID: catalog.ReceiverID, RequestID: "root", PublicationID: p.ID, Candidate: pack}, raw, p, at
}

func TestMemoryCandidateRegistry(t *testing.T) { candidateContracts(t, NewMemory()) }
func TestPostgresContractsCandidateRegistry(t *testing.T) {
	candidateContracts(t, timerPostgres(t, "candidates"))
}

func candidateContracts(t *testing.T, s candidateBackend) {
	ctx := context.Background()
	c, raw, pub, at := candidateFixture(t, s)
	now := func() time.Time { return at }
	empty, err := s.CandidateRegistrations(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty registry", empty, err)
	}
	before, _ := s.Snapshot(ctx)
	registry, err := s.CandidateRegistry(ctx)
	if err != nil || registry.Validate() != nil || registry.ReceiverID != c.ReceiverID || len(registry.Registrations) != 0 {
		t.Fatal("empty registry envelope", err)
	}
	newsBefore, _ := s.NewsCases(ctx)
	publicationBefore, _ := s.Hypothesis(ctx, pub.Sealed.ID)
	// Concurrent identical requests retain one row and one exact artifact.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.RegisterCandidate(ctx, c, raw, now)
			if err != nil || r.Sequence != 1 || r.Validate() != nil {
				t.Errorf("concurrent registration: %v", err)
			}
		})
	}
	wg.Wait()
	first, err := s.CandidateRegistration(ctx, "root")
	if err != nil || first.VerifyBinding(pub, nil) != nil || first.ExecutionStarted || first.QualificationCredit || first.Candidate.ServiceBindingVerified {
		t.Fatal(first, err)
	}
	retry, err := s.RegisterCandidate(ctx, c, raw, func() time.Time { panic("retry consulted clock") })
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("retry changed", err)
	}
	for _, change := range []string{"receiver", "publication", "source", "parent", "raw-dataset"} {
		t.Run(change, func(t *testing.T) {
			changed, input := clone(c), bytes.Clone(raw)
			switch change {
			case "receiver":
				changed.ReceiverID = "receiver-abcdefghijklmnop"
			case "publication":
				changed.PublicationID = "missing"
			case "source":
				changed.Candidate, _ = candidate.New([]byte("different bad Go"), c.Candidate.Hypothesis.Declaration, input, c.Candidate.Toolchain.ImageSHA256)
			case "parent":
				changed.ParentID = "missing"
			case "raw-dataset":
				input = append(input, '\n')
			}
			if _, err := s.RegisterCandidate(ctx, changed, input, now); err == nil {
				t.Fatal("rebound existing request")
			}
		})
	}
	missing := clone(c)
	missing.RequestID, missing.PublicationID = "missing", "missing"
	if _, err := s.RegisterCandidate(ctx, missing, raw, now); !errors.Is(err, candidatelineage.ErrNotFound) {
		t.Fatal(err)
	}
	child := clone(c)
	child.RequestID, child.ParentID = "child", "root"
	child.Candidate, _ = candidate.New([]byte("package candidate\nfunc Entry(p int64) bool { return p <= 10000 }\n"), c.Candidate.Hypothesis.Declaration, raw, c.Candidate.Toolchain.ImageSHA256)
	second, err := s.RegisterCandidate(ctx, child, raw, now)
	if err != nil || second.Sequence != 2 || second.ParentSHA256 != first.SHA256 || second.Candidate.ID == first.Candidate.ID {
		t.Fatal(second, err)
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		actual, err := s.CandidateRegistration(ctx, "child")
		if err != nil || !reflect.DeepEqual(actual, second) {
			t.Fatal("restart changed lineage", err)
		}
		input, err := s.CandidateDataset(ctx, "root")
		if err != nil || !bytes.Equal(input, raw) {
			t.Fatal("restart changed raw bytes", err)
		}
		// Fail the second insert, after metadata was inserted in the transaction.
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_candidate_datasets ADD CONSTRAINT reject_candidate CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.RequestID = "rollback"
		if _, err = s.RegisterCandidate(ctx, failed, raw, now); err == nil {
			t.Fatal("failed artifact insert acknowledged")
		}
		if _, err = s.CandidateRegistration(ctx, "rollback"); !errors.Is(err, candidatelineage.ErrNotFound) {
			t.Fatal("partial metadata committed", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_candidate_datasets DROP CONSTRAINT reject_candidate"); err != nil {
			t.Fatal(err)
		}
	}
	input, err := s.CandidateDataset(ctx, "root")
	if err != nil || !bytes.Equal(input, raw) {
		t.Fatal("raw input changed", err)
	}
	input[0] = '!'
	evidence, err := s.CandidateEvidence(ctx, "root")
	if err != nil || evidence.Validate() != nil || evidence.Registration.SHA256 != first.SHA256 || !bytes.Equal(evidence.Dataset, raw) {
		t.Fatal("atomic artifact envelope invalid", err)
	}
	first.Candidate.Hypothesis.Declaration.Methods[0].ID = "caller-mutated"
	if actual, err := s.CandidateRegistration(ctx, "root"); err != nil || actual.Validate() != nil {
		t.Fatal("caller changed stored declaration", err)
	}
	if input, err = s.CandidateDataset(ctx, "root"); err != nil || !bytes.Equal(input, raw) {
		t.Fatal("caller changed stored bytes", err)
	}
	rollback := clone(c)
	rollback.RequestID = "early"
	if _, err = s.RegisterCandidate(ctx, rollback, raw, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, candidatelineage.ErrConflict) {
		t.Fatal(err)
	}
	for i := 2; i < candidatelineage.MaxRegistrations; i++ {
		copy := clone(c)
		copy.RequestID, copy.ParentID = fmt.Sprintf("candidate-%02d", i), "child"
		if _, err = s.RegisterCandidate(ctx, copy, raw, now); err != nil {
			t.Fatal(err)
		}
	}
	overflow := clone(c)
	overflow.RequestID = "overflow"
	if _, err = s.RegisterCandidate(ctx, overflow, raw, func() time.Time { panic("capacity consulted clock") }); !errors.Is(err, candidatelineage.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.RegisterCandidate(ctx, c, raw, func() time.Time { panic("capacity retry consulted clock") }); err != nil {
		t.Fatal(err)
	}
	history, err := s.CandidateRegistrations(ctx)
	if err != nil || len(history) != 32 || candidatelineage.ValidateHistory(c.ReceiverID, history) != nil {
		t.Fatal("history invalid", err)
	}
	for _, id := range []string{"missing", "../bad"} {
		if _, err = s.CandidateRegistration(ctx, id); err == nil {
			t.Fatal("missing/invalid selection accepted")
		}
		if _, err = s.CandidateDataset(ctx, id); err == nil {
			t.Fatal("missing/invalid artifact accepted")
		}
	}
	after, _ := s.Snapshot(ctx)
	newsAfter, _ := s.NewsCases(ctx)
	publicationAfter, _ := s.Hypothesis(ctx, pub.Sealed.ID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(newsBefore, newsAfter) || !reflect.DeepEqual(publicationBefore, publicationAfter) {
		t.Fatal("registration changed operations or research provenance")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CandidateRegistrations(cancelled); err == nil {
		t.Fatal("cancelled read accepted")
	}
	if _, err = s.RegisterCandidate(cancelled, c, raw, now); err == nil {
		t.Fatal("cancelled write accepted")
	}
}

func TestMemoryCandidateRegistryCorruption(t *testing.T) { candidateCorruption(t, NewMemory()) }
func TestPostgresContractsCandidateRegistryCorruption(t *testing.T) {
	candidateCorruption(t, timerPostgres(t, "candidate_corrupt"))
}

func candidateCorruption(t *testing.T, s candidateBackend) {
	ctx := context.Background()
	c, raw, pub, at := candidateFixture(t, s)
	now := func() time.Time { return at }
	root, err := s.RegisterCandidate(ctx, c, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	other := clone(c)
	other.RequestID = "unrelated"
	if _, err := s.RegisterCandidate(ctx, other, raw, now); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"dataset", "missing-dataset", "metadata", "publication"} {
		t.Run(kind, func(t *testing.T) {
			restore := corruptCandidate(t, s, kind, root, raw, pub)
			defer restore()
			if _, err := s.CandidateRegistrations(ctx); err == nil {
				t.Fatal("corrupt history accepted")
			}
			if _, err := s.CandidateRegistry(ctx); err == nil {
				t.Fatal("registry envelope hid damaged root")
			}
			if _, err := s.CandidateEvidence(ctx, "unrelated"); err == nil {
				t.Fatal("artifact envelope hid damaged root")
			}
			if _, err := s.CandidateRegistration(ctx, "unrelated"); err == nil {
				t.Fatal("selection hid damaged root")
			}
			if _, err := s.CandidateDataset(ctx, "unrelated"); err == nil {
				t.Fatal("artifact read hid damaged root")
			}
			if _, err := s.RegisterCandidate(ctx, other, raw, now); err == nil {
				t.Fatal("retry hid damaged root")
			}
			fresh := clone(c)
			fresh.RequestID = "fresh"
			if _, err := s.RegisterCandidate(ctx, fresh, raw, now); err == nil {
				t.Fatal("append hid damaged root")
			}
		})
	}
	if all, err := s.CandidateRegistrations(ctx); err != nil || len(all) != 2 {
		t.Fatal("failed operations retained extra rows", err)
	}
}

func corruptCandidate(t *testing.T, s candidateBackend, kind string, root candidatelineage.Registration, raw []byte, pub hypothesis.Publication) func() {
	t.Helper()
	if m, ok := s.(*Memory); ok {
		switch kind {
		case "dataset":
			m.candidateDatasets[root.ID] = append(bytes.Clone(raw), '\n')
		case "missing-dataset":
			delete(m.candidateDatasets, root.ID)
		case "metadata":
			r := clone(root)
			r.Candidate.Source.Text += "changed"
			m.candidates[root.ID] = r
		case "publication":
			p := clone(pub)
			p.ReviewID = "other-review"
			m.publications[pub.ID] = p
		}
		return func() {
			m.candidates[root.ID], m.candidateDatasets[root.ID], m.publications[pub.ID] = clone(root), bytes.Clone(raw), clone(pub)
		}
	}
	p := s.(*Postgres)
	ctx := context.Background()
	var err error
	switch kind {
	case "dataset":
		_, err = p.pool.Exec(ctx, "UPDATE mantik_candidate_datasets SET content=$2 WHERE id=$1", root.ID, append(bytes.Clone(raw), '\n'))
	case "missing-dataset":
		_, err = p.pool.Exec(ctx, "DELETE FROM mantik_candidate_datasets WHERE id=$1", root.ID)
	case "metadata":
		_, err = p.pool.Exec(ctx, "UPDATE mantik_candidate_registrations SET payload=jsonb_set(payload,'{candidate,source,text}','\"changed\"') WHERE id=$1", root.ID)
	case "publication":
		_, err = p.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=jsonb_set(payload,'{review_id}','\"other-review\"') WHERE id=$1", pub.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		metadata, _ := json.Marshal(root)
		publication, _ := json.Marshal(pub)
		if _, err := p.pool.Exec(ctx, "UPDATE mantik_candidate_registrations SET payload=$2 WHERE id=$1", root.ID, metadata); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pool.Exec(ctx, "INSERT INTO mantik_candidate_datasets(id,content) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET content=excluded.content", root.ID, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pool.Exec(ctx, "UPDATE mantik_hypothesis_publications SET payload=$2 WHERE id=$1", pub.ID, publication); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresContractsCandidateRegistryUpgrade(t *testing.T) {
	s := timerPostgres(t, "candidate_upgrade")
	ctx := context.Background()
	_, _, pub, _ := candidateFixture(t, s)
	before, _ := s.Snapshot(ctx)
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DELETE FROM mantik_schema WHERE version>=30"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if all, err := next.CandidateRegistrations(ctx); err != nil || len(all) != 0 {
		t.Fatal("upgrade fabricated registrations", err)
	}
	after, _ := next.Snapshot(ctx)
	question, err := next.Hypothesis(ctx, pub.Sealed.ID)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(question.Publications[0], pub) {
		t.Fatal("upgrade rewrote existing evidence", err)
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=30"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
