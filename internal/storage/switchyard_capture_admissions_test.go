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
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
)

type switchyardAdmissionBackend interface {
	switchyardCaptureBackend
	SwitchyardCaptureAdmissionStore
	NewsStore
}

func TestMemorySwitchyardReviewedCorrectionPreservesAllPredecessorEvents(t *testing.T) {
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
	attempt := importSwitchyardAdmissionFixture(t, s, receiver.ID, "all-events", a, "new-approved-event", raw, clock)
	command := extraction.SwitchyardAdmissionCommand{ReceiverID: receiver.ID, CaptureID: attempt.ID, CaptureSHA256: attempt.SHA256, ResultSHA256: attempt.Finished.Result.SHA256, ApprovalCode: extraction.SwitchyardApprovalCode}
	r, err := s.AdmitSwitchyardCapture(ctx, command, clock)
	if err != nil || len(r.NewsReceipt.Matches) != 17 {
		t.Fatal("correction lost predecessor event union", r, err)
	}
}

func TestMemoryReviewedSwitchyardCaptureAdmission(t *testing.T) {
	switchyardAdmissionContract(t, NewMemory())
}
func TestPostgresContractsReviewedSwitchyardCaptureAdmission(t *testing.T) {
	switchyardAdmissionContract(t, timerPostgres(t, "localreviewedadmission"))
}
func switchyardAdmissionContract(t *testing.T, s switchyardAdmissionBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	at := article.ReceivedAt.Add(time.Hour)
	clock := func() time.Time { return at }
	noClock := func() time.Time { t.Error("admission retry sampled clock"); return time.Time{} }
	fixture, _ := json.Marshal(extraction.FixtureResponse())
	complete := func(id string, a news.Article, key string, raw []byte) extraction.SwitchyardCapture {
		return importSwitchyardAdmissionFixture(t, s, receiver.ID, id, a, key, raw, clock)
	}
	command := func(a extraction.SwitchyardCapture) extraction.SwitchyardAdmissionCommand {
		return extraction.SwitchyardAdmissionCommand{ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: a.SHA256, ResultSHA256: a.Finished.Result.SHA256, ApprovalCode: extraction.SwitchyardApprovalCode}
	}
	a := complete("reviewed", article, "reviewed-event", fixture)
	c := command(a)
	for _, bad := range []extraction.SwitchyardAdmissionCommand{{ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: a.SHA256, ResultSHA256: a.Finished.Result.SHA256}, {ReceiverID: receiver.ID, CaptureID: a.ID, CaptureSHA256: strings.Repeat("f", 64), ResultSHA256: a.Finished.Result.SHA256, ApprovalCode: extraction.SwitchyardApprovalCode}} {
		if _, e := s.AdmitSwitchyardCapture(ctx, bad, clock); e == nil {
			t.Fatal("unapproved or changed result admitted")
		}
	}
	r, err := extraction.PrepareSwitchyardAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.AdmitNews(ctx, receiver.ID, r, clock); !errors.Is(e, newscase.ErrInvalid) {
		t.Fatal("generic recording submission bypassed provenance lookup", e)
	}
	var wg sync.WaitGroup
	results := make(chan extraction.SwitchyardAdmission, 8)
	fail := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, e := s.AdmitSwitchyardCapture(ctx, c, clock)
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
	var approval extraction.SwitchyardAdmission
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
	if target, e := s.SwitchyardCaptureAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(target, approval) {
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
	if retry, e := s.AdmitSwitchyardCapture(ctx, c, noClock); e != nil || !reflect.DeepEqual(retry, approval) {
		t.Fatal("retry changed approval", e)
	}
	cat, _ := s.NewsCases(ctx)
	if len(cat.Cases) != 1 || cat.Cases[0].Status != "closed" || cat.Cases[0].Version != 1 {
		t.Fatal("retry reopened or duplicated closed case", cat)
	}
	other := complete("same-source", article, "reviewed-event", fixture)
	if _, e := s.AdmitSwitchyardCapture(ctx, command(other), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("second attempt replaced same source revision", e)
	}
	abstainArticle := article
	abstainArticle.ArticleID = "abstention"
	abstain := complete("abstain", abstainArticle, "reviewed-event", []byte("malformed"))
	if _, e := s.AdmitSwitchyardCapture(ctx, command(abstain), clock); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("abstention admitted", e)
	}
	correction := article
	correction.Kind = "correction"
	correction.Revision = 2
	correction.Supersedes = &news.RevisionRef{SourceID: article.SourceID, ArticleID: article.ArticleID, Revision: 1, RecordingSHA256: approval.NewsReceipt.RecordingSHA256}
	at = at.Add(time.Minute)
	corrected := complete("correction", correction, "corrected-event", fixture)
	cr, err := s.AdmitSwitchyardCapture(ctx, command(corrected), clock)
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
	if receipt, e := s.AdmitSwitchyardCapture(ctx, command(retracted), clock); e != nil || len(receipt.NewsReceipt.Matches) != 2 {
		t.Fatal("retraction lost predecessor cases", receipt, e)
	}
	// Both the source record/case changes and sidecar receipt must roll back.
	rollbackArticle := article
	rollbackArticle.ArticleID = "rollback"
	rollback := complete("rollback", rollbackArticle, "rollback-event", fixture)
	before, _ := s.NewsCases(ctx)
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_capture_admissions ADD CONSTRAINT fail_admission_write CHECK(id!='rollback')"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.AdmitSwitchyardCapture(ctx, command(rollback), clock); e == nil {
			t.Fatal("failed receipt insert acknowledged")
		}
		after, _ := s.NewsCases(ctx)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed atomic admission changed cases")
		}
		if _, e := s.NewsRecord(ctx, newscase.RecordKey(news.Recording{SourceID: rollbackArticle.SourceID, ArticleID: rollbackArticle.ArticleID, EvidenceClass: rollbackArticle.EvidenceClass, Revision: 1})); !errors.Is(e, newscase.ErrNotFound) {
			t.Fatal("failed transaction retained partial original", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_capture_admissions DROP CONSTRAINT fail_admission_write"); e != nil {
			t.Fatal(e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_capture_admissions ADD CONSTRAINT deny_retry_write CHECK(id!='reviewed') NOT VALID"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.AdmitSwitchyardCapture(ctx, c, noClock); e != nil {
			t.Fatal("exact approval retry attempted row rewrite", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_capture_admissions DROP CONSTRAINT deny_retry_write"); e != nil {
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
		if restored, e := s.SwitchyardCaptureAdmission(ctx, a.ID); e != nil || !reflect.DeepEqual(restored, approval) {
			t.Fatal("restart lost approval", e)
		}
	}
	if unchanged, e := s.SwitchyardCapture(ctx, a.ID); e != nil || !reflect.DeepEqual(unchanged, a) {
		t.Fatal("admission rewrote extraction attempt", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, `UPDATE mantik_switchyard_capture_admissions SET payload=jsonb_set(payload,'{provenance,model_artifact_sha256}',to_jsonb(repeat('f',64))) WHERE id='reviewed'`); e != nil {
			t.Fatal(e)
		}
	} else {
		m := s.(*Memory)
		broken := m.switchyardAdmissions[a.ID]
		broken.Provenance.ModelArtifactSHA256 = strings.Repeat("f", 64)
		m.switchyardAdmissions[a.ID] = broken
	}
	if _, e := s.SwitchyardCaptureAdmission(ctx, a.ID); e == nil {
		t.Fatal("corrupt provenance returned success")
	}
}

func importSwitchyardAdmissionFixture(t *testing.T, s SwitchyardCaptureStore, receiver, id string, article news.Article, key string, raw []byte, now func() time.Time) extraction.SwitchyardCapture {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "openai/gpt-6.1-sol", "usage": map[string]int{}, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
	}))
	defer server.Close()
	var all []extraction.SwitchyardReceipt
	_, err := extraction.RunSwitchyard(context.Background(), article, key, extraction.SwitchyardConfig{Endpoint: server.URL, Model: "openai/gpt-6.1-sol", MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r extraction.SwitchyardReceipt) error { all = append(all, r); return nil })
	if err != nil || len(all) != 2 {
		t.Fatal(err)
	}
	r, err := s.ImportSwitchyardCapture(context.Background(), receiver, id, all[0], &all[1], now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresContractsSwitchyardAdmissionUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "switchyardadmissionupgrade")
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, f := switchyardCaptureFixture(t)
	capture, err := s.ImportSwitchyardCapture(ctx, receiver.ID, "existing-provider-failure", p, &f, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DELETE FROM mantik_schema WHERE version>=26"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	up, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	got, err := up.SwitchyardCapture(ctx, capture.ID)
	if err != nil || !reflect.DeepEqual(capture, got) {
		t.Fatal("upgrade changed existing capture", err)
	}
	after, err := up.NewsCases(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed news state", err)
	}
	if _, err = up.SwitchyardCaptureAdmission(ctx, capture.ID); !errors.Is(err, extraction.ErrNotFound) {
		t.Fatal("upgrade invented approval", err)
	}
	if _, err = up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=26"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration accepted")
	}
}
