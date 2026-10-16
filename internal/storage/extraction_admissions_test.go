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

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
)

type admissionBackend interface {
	extractionBackend
	ExtractionAdmissionStore
	NewsStore
}

func TestMemoryReviewedCorrectionPreservesAllPredecessorEvents(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	parent := news.Fixture()
	parent.Claims = nil
	for i := 0; i < 16; i++ {
		c := news.Fixture().Claims[0]
		c.ID = fmt.Sprintf("claim-%d", i)
		c.EventKey = fmt.Sprintf("event-%d", i)
		parent.Claims = append(parent.Claims, c)
	}
	at := parent.ReceivedAt.Add(time.Hour)
	clock := func() time.Time { return at }
	p, err := s.AdmitNews(ctx, receiver.ID, parent, clock)
	if err != nil {
		t.Fatal(err)
	}
	a := news.FixtureArticle()
	a.Kind = "correction"
	a.Revision = 2
	a.Supersedes = &news.RevisionRef{SourceID: a.SourceID, ArticleID: a.ArticleID, Revision: 1, RecordingSHA256: p.RecordingSHA256}
	if _, err = s.StartExtraction(ctx, receiver.ID, "all-events", a, "new-approved-event", clock); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(extraction.FixtureResponse())
	attempt, err := s.CompleteExtraction(ctx, receiver.ID, "all-events", raw, clock)
	if err != nil {
		t.Fatal(err)
	}
	command := extraction.AdmissionCommand{ReceiverID: receiver.ID, AttemptID: attempt.ID, AttemptSHA256: attempt.SHA256, ResultSHA256: attempt.Result.SHA256, ApprovalCode: extraction.ApprovalCode}
	r, err := s.AdmitExtraction(ctx, command, clock)
	if err != nil || len(r.NewsReceipt.Matches) != 17 {
		t.Fatal("correction lost predecessor event union", r, err)
	}
}

func TestMemoryReviewedExtractionAdmission(t *testing.T) { admissionContract(t, NewMemory()) }
func TestPostgresContractsReviewedExtractionAdmission(t *testing.T) {
	admissionContract(t, timerPostgres(t, "reviewedadmission"))
}
func admissionContract(t *testing.T, s admissionBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	at := article.ReceivedAt.Add(time.Hour)
	clock := func() time.Time { return at }
	noClock := func() time.Time { t.Error("admission retry sampled clock"); return time.Time{} }
	fixture, _ := json.Marshal(extraction.FixtureResponse())
	complete := func(id string, a news.Article, key string, raw []byte) extraction.Attempt {
		t.Helper()
		if _, e := s.StartExtraction(ctx, receiver.ID, id, a, key, clock); e != nil {
			t.Fatal(e)
		}
		r, e := s.CompleteExtraction(ctx, receiver.ID, id, raw, clock)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	command := func(a extraction.Attempt) extraction.AdmissionCommand {
		return extraction.AdmissionCommand{ReceiverID: receiver.ID, AttemptID: a.ID, AttemptSHA256: a.SHA256, ResultSHA256: a.Result.SHA256, ApprovalCode: extraction.ApprovalCode}
	}
	a := complete("reviewed", article, "reviewed-event", fixture)
	c := command(a)
	for _, bad := range []extraction.AdmissionCommand{{ReceiverID: receiver.ID, AttemptID: a.ID, AttemptSHA256: a.SHA256, ResultSHA256: a.Result.SHA256}, {ReceiverID: receiver.ID, AttemptID: a.ID, AttemptSHA256: strings.Repeat("f", 64), ResultSHA256: a.Result.SHA256, ApprovalCode: extraction.ApprovalCode}} {
		if _, e := s.AdmitExtraction(ctx, bad, clock); e == nil {
			t.Fatal("unapproved or changed result admitted")
		}
	}
	r, err := extraction.PrepareAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.AdmitNews(ctx, receiver.ID, r, clock); !errors.Is(e, newscase.ErrInvalid) {
		t.Fatal("generic recording submission bypassed provenance lookup", e)
	}
	var wg sync.WaitGroup
	results := make(chan extraction.Admission, 8)
	fail := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, e := s.AdmitExtraction(ctx, c, clock)
			if e != nil {
				fail <- e
			} else {
				results <- receipt
			}
		}()
	}
	wg.Wait()
	close(results)
	close(fail)
	for e := range fail {
		t.Fatal(e)
	}
	var approval extraction.Admission
	for x := range results {
		if approval.Schema != "" && !reflect.DeepEqual(approval, x) {
			t.Fatal("concurrent approvals diverged")
		}
		approval = x
	}
	record, err := s.NewsRecord(ctx, approval.NewsReceipt.RecordKey)
	if err != nil || approval.Validate(a, record) != nil {
		t.Fatal("lost checked record", err)
	}
	if target, e := s.ExtractionAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(target, approval) {
		t.Fatal("admission read changed", e)
	}
	caseID := approval.NewsReceipt.Matches[0].CaseID
	if _, e := s.CloseNewsCase(ctx, receiver.ID, caseID, 1, clock); e != nil {
		t.Fatal(e)
	}
	if retry, e := s.AdmitExtraction(ctx, c, noClock); e != nil || !reflect.DeepEqual(retry, approval) {
		t.Fatal("retry changed approval", e)
	}
	cat, _ := s.NewsCases(ctx)
	if len(cat.Cases) != 1 || cat.Cases[0].Status != "closed" || cat.Cases[0].Version != 1 {
		t.Fatal("retry reopened or duplicated closed case", cat)
	}
	other := complete("same-source", article, "reviewed-event", fixture)
	if _, e := s.AdmitExtraction(ctx, command(other), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("second attempt replaced same source revision", e)
	}
	abstainArticle := article
	abstainArticle.ArticleID = "abstention"
	abstain := complete("abstain", abstainArticle, "reviewed-event", []byte("malformed"))
	if _, e := s.AdmitExtraction(ctx, command(abstain), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("abstention admitted", e)
	}
	correction := article
	correction.Kind = "correction"
	correction.Revision = 2
	correction.Supersedes = &news.RevisionRef{SourceID: article.SourceID, ArticleID: article.ArticleID, Revision: 1, RecordingSHA256: approval.NewsReceipt.RecordingSHA256}
	at = at.Add(time.Minute)
	corrected := complete("correction", correction, "corrected-event", fixture)
	cr, err := s.AdmitExtraction(ctx, command(corrected), clock)
	if err != nil || len(cr.NewsReceipt.Matches) != 2 {
		t.Fatal(cr, err)
	}
	cat, _ = s.NewsCases(ctx)
	found := false
	for _, c := range cat.Cases {
		if c.ID == caseID {
			found = true
			if c.Status != "open" || c.Version != 2 || c.Generation != 2 {
				t.Fatal("correction did not reopen predecessor case", c)
			}
		}
	}
	if !found {
		t.Fatal("old event lost")
	}
	// Both the source record/case changes and sidecar receipt must roll back.
	rollbackArticle := article
	rollbackArticle.ArticleID = "rollback"
	rollback := complete("rollback", rollbackArticle, "rollback-event", fixture)
	before, _ := s.NewsCases(ctx)
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_news_extraction_admissions ADD CONSTRAINT fail_admission_write CHECK(id!='rollback')"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.AdmitExtraction(ctx, command(rollback), clock); e == nil {
			t.Fatal("failed receipt insert acknowledged")
		}
		after, _ := s.NewsCases(ctx)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed atomic admission changed cases")
		}
		if _, e := s.NewsRecord(ctx, newscase.RecordKey(news.Recording{SourceID: rollbackArticle.SourceID, ArticleID: rollbackArticle.ArticleID, EvidenceClass: rollbackArticle.EvidenceClass, Revision: 1})); !errors.Is(e, newscase.ErrNotFound) {
			t.Fatal("failed transaction retained partial original", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_news_extraction_admissions DROP CONSTRAINT fail_admission_write"); e != nil {
			t.Fatal(e)
		}
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, e := OpenPostgres(ctx, config)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		s = reopened
		if restored, e := s.ExtractionAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(restored, approval) {
			t.Fatal("restart lost approval", e)
		}
	}
	if unchanged, e := s.Extraction(ctx, a.ID); e != nil || !reflect.DeepEqual(unchanged, a) {
		t.Fatal("admission rewrote extraction attempt", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, `UPDATE mantik_news_extraction_admissions SET payload=jsonb_set(payload,'{artifact_sha256}',to_jsonb(repeat('f',64))) WHERE id='reviewed'`); e != nil {
			t.Fatal(e)
		}
	} else {
		m := s.(*Memory)
		broken := m.extractionAdmissions[a.ID]
		broken.ArtifactSHA256 = strings.Repeat("f", 64)
		m.extractionAdmissions[a.ID] = broken
	}
	if _, e := s.ExtractionAdmission(ctx, a.ID); e == nil {
		t.Fatal("corrupt provenance returned success")
	}
}
