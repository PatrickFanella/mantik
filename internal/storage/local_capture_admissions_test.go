package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
)

type localAdmissionBackend interface {
	localCaptureBackend
	LocalCaptureAdmissionStore
	NewsStore
}

func TestMemoryLocalReviewedCorrectionPreservesAllPredecessorEvents(t *testing.T) {
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
	raw, _ := json.Marshal(extraction.FixtureResponse())
	attempt := importLocalAdmissionFixture(t, s, receiver.ID, "all-events", a, "new-approved-event", raw, clock)
	command := extraction.LocalAdmissionCommand{ReceiverID: receiver.ID, CaptureID: attempt.ID, CaptureSHA256: attempt.SHA256, ResultSHA256: attempt.Finished.Result.SHA256, ApprovalCode: extraction.LocalApprovalCode}
	r, err := s.AdmitLocalCapture(ctx, command, clock)
	if err != nil || len(r.NewsReceipt.Matches) != 17 {
		t.Fatal("correction lost predecessor event union", r, err)
	}
}

func TestMemoryReviewedLocalCaptureAdmission(t *testing.T) { localAdmissionContract(t, NewMemory()) }
func TestPostgresContractsReviewedLocalCaptureAdmission(t *testing.T) {
	localAdmissionContract(t, timerPostgres(t, "localreviewedadmission"))
}
func localAdmissionContract(t *testing.T, s localAdmissionBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	at := article.ReceivedAt.Add(time.Hour)
	clock := func() time.Time { return at }
	noClock := func() time.Time { t.Error("admission retry sampled clock"); return time.Time{} }
	fixture, _ := json.Marshal(extraction.FixtureResponse())
	complete := func(id string, a news.Article, key string, raw []byte) extraction.LocalCapture {
		return importLocalAdmissionFixture(t, s, receiver.ID, id, a, key, raw, clock)
	}
	command := func(a extraction.LocalCapture) extraction.LocalAdmissionCommand {
		return extraction.LocalAdmissionCommand{ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: a.SHA256, ResultSHA256: a.Finished.Result.SHA256, ApprovalCode: extraction.LocalApprovalCode}
	}
	a := complete("reviewed", article, "reviewed-event", fixture)
	c := command(a)
	for _, bad := range []extraction.LocalAdmissionCommand{{ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: a.SHA256, ResultSHA256: a.Finished.Result.SHA256}, {ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: strings.Repeat("f", 64), ResultSHA256: a.Finished.Result.SHA256, ApprovalCode: extraction.LocalApprovalCode}} {
		if _, e := s.AdmitLocalCapture(ctx, bad, clock); e == nil {
			t.Fatal("unapproved or changed result admitted")
		}
	}
	r, err := extraction.PrepareLocalAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.AdmitNews(ctx, receiver.ID, r, clock); !errors.Is(e, newscase.ErrInvalid) {
		t.Fatal("generic recording submission bypassed provenance lookup", e)
	}
	var wg sync.WaitGroup
	results := make(chan extraction.LocalAdmission, 8)
	fail := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, e := s.AdmitLocalCapture(ctx, c, clock)
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
	var approval extraction.LocalAdmission
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
	if target, e := s.LocalCaptureAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(target, approval) {
		t.Fatal("admission read changed", e)
	}
	if r, ok := s.(ReviewStore); ok {
		v, e := r.StartReview(ctx, receiver.ID, "local-downstream", approval.NewsReceipt.Matches[0].CaseID, 1, clock)
		if e != nil || v.Attempt.Status != "pending" || !v.EvidenceCurrent {
			t.Fatal("local evidence did not enter ordinary research review", e)
		}
	}
	beforeFixture, e := s.Snapshot(ctx)
	if e != nil || beforeFixture.ObservationCount != 0 || len(beforeFixture.Events) != 0 {
		t.Fatal("research admission changed fixture execution", e)
	}
	caseID := approval.NewsReceipt.Matches[0].CaseID
	if _, e := s.CloseNewsCase(ctx, receiver.ID, caseID, 1, clock); e != nil {
		t.Fatal(e)
	}
	if retry, e := s.AdmitLocalCapture(ctx, c, noClock); e != nil || !reflect.DeepEqual(retry, approval) {
		t.Fatal("retry changed approval", e)
	}
	cat, _ := s.NewsCases(ctx)
	if len(cat.Cases) != 1 || cat.Cases[0].Status != "closed" || cat.Cases[0].Version != 1 {
		t.Fatal("retry reopened or duplicated closed case", cat)
	}
	other := complete("same-source", article, "reviewed-event", fixture)
	if _, e := s.AdmitLocalCapture(ctx, command(other), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("second attempt replaced same source revision", e)
	}
	abstainArticle := article
	abstainArticle.ArticleID = "abstention"
	abstain := complete("abstain", abstainArticle, "reviewed-event", []byte("malformed"))
	if _, e := s.AdmitLocalCapture(ctx, command(abstain), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("abstention admitted", e)
	}
	correction := article
	correction.Kind = "correction"
	correction.Revision = 2
	correction.Supersedes = &news.RevisionRef{SourceID: article.SourceID, ArticleID: article.ArticleID, Revision: 1, RecordingSHA256: approval.NewsReceipt.RecordingSHA256}
	at = at.Add(time.Minute)
	corrected := complete("correction", correction, "corrected-event", fixture)
	cr, err := s.AdmitLocalCapture(ctx, command(corrected), clock)
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
	// Retractions must retain their source revision and reopen affected events.
	retraction := correction
	retraction.Kind = "retraction"
	retraction.Revision = 3
	retraction.Supersedes = &news.RevisionRef{SourceID: article.SourceID, ArticleID: article.ArticleID, Revision: 2, RecordingSHA256: cr.NewsReceipt.RecordingSHA256}
	retractedOutput := extraction.FixtureResponse()
	for i := range retractedOutput.Claims {
		retractedOutput.Claims[i].Assessment = "retracted"
	}
	retractedBytes, _ := json.Marshal(retractedOutput)
	at = at.Add(time.Minute)
	retracted := complete("retraction", retraction, "retracted-event", retractedBytes)
	if retracted.Finished.Result.Outcome != "extracted" {
		t.Fatal("bad retraction fixture")
	}
	if receipt, e := s.AdmitLocalCapture(ctx, command(retracted), clock); e != nil || len(receipt.NewsReceipt.Matches) != 2 {
		t.Fatal("retraction lost predecessor cases", receipt, e)
	}
	// Both the source record/case changes and sidecar receipt must roll back.
	rollbackArticle := article
	rollbackArticle.ArticleID = "rollback"
	rollback := complete("rollback", rollbackArticle, "rollback-event", fixture)
	before, _ := s.NewsCases(ctx)
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_local_capture_admissions ADD CONSTRAINT fail_admission_write CHECK(id!='rollback')"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.AdmitLocalCapture(ctx, command(rollback), clock); e == nil {
			t.Fatal("failed receipt insert acknowledged")
		}
		after, _ := s.NewsCases(ctx)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed atomic admission changed cases")
		}
		if _, e := s.NewsRecord(ctx, newscase.RecordKey(news.Recording{SourceID: rollbackArticle.SourceID, ArticleID: rollbackArticle.ArticleID, EvidenceClass: rollbackArticle.EvidenceClass, Revision: 1})); !errors.Is(e, newscase.ErrNotFound) {
			t.Fatal("failed transaction retained partial original", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_local_capture_admissions DROP CONSTRAINT fail_admission_write"); e != nil {
			t.Fatal(e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_local_capture_admissions ADD CONSTRAINT deny_retry_write CHECK(id!='reviewed') NOT VALID"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.AdmitLocalCapture(ctx, c, noClock); e != nil {
			t.Fatal("exact approval retry attempted row rewrite", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_local_capture_admissions DROP CONSTRAINT deny_retry_write"); e != nil {
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
		if restored, e := s.LocalCaptureAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(restored, approval) {
			t.Fatal("restart lost approval", e)
		}
	}
	if unchanged, e := s.LocalCapture(ctx, a.ID); e != nil || !reflect.DeepEqual(unchanged, a) {
		t.Fatal("admission rewrote extraction attempt", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, `UPDATE mantik_local_capture_admissions SET payload=jsonb_set(payload,'{provenance,model_artifact_sha256}',to_jsonb(repeat('f',64))) WHERE id='reviewed'`); e != nil {
			t.Fatal(e)
		}
	} else {
		m := s.(*Memory)
		broken := m.localCaptureAdmissions[a.ID]
		broken.Provenance.ModelArtifactSHA256 = strings.Repeat("f", 64)
		m.localCaptureAdmissions[a.ID] = broken
	}
	if _, e := s.LocalCaptureAdmission(ctx, a.ID); e == nil {
		t.Fatal("corrupt provenance returned success")
	}
}

func importLocalAdmissionFixture(t *testing.T, s LocalCaptureStore, receiver, id string, article news.Article, key string, raw []byte, now func() time.Time) extraction.LocalCapture {
	t.Helper()
	var response extraction.Response
	content := raw
	if json.Unmarshal(raw, &response) == nil {
		q := extraction.QuoteResponse{Outcome: response.Outcome, ReasonCode: response.ReasonCode, Claims: []extraction.QuoteClaim{}}
		for _, c := range response.Claims {
			claim := extraction.QuoteClaim{ID: c.ID, Code: c.Code, Assertion: c.Assertion, Assessment: c.Assessment, Entities: c.Entities, Symbols: c.Symbols, Evidence: []extraction.Quote{}}
			for _, e := range c.Evidence {
				claim.Evidence = append(claim.Evidence, extraction.Quote{Quote: e.Quote})
			}
			q.Claims = append(q.Claims, claim)
		}
		content, _ = json.Marshal(q)
	}
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "ollama/fixture", "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	})))
	defer server.Close()
	var all []extraction.LocalReceipt
	_, err := extraction.RunLocal(context.Background(), article, key, extraction.LocalConfig{Endpoint: server.URL, Model: "ollama/fixture", MaxTokens: 512}, "", func(r extraction.LocalReceipt) error { all = append(all, r); return nil })
	if err != nil || len(all) != 2 {
		t.Fatal(err)
	}
	r, err := s.ImportLocalCapture(context.Background(), receiver, id, all[0], &all[1], now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
