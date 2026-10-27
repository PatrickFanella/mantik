package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type portfolioHTTPBackend interface {
	ownedDecisionHTTPBackend
	storage.PaperEvaluationDeclarationStore
	storage.PaperEvaluationAssessmentStore
}

func TestPortfolioOwnerRejectsMissingCompiler(t *testing.T) {
	if m, err := storage.NewMemoryWithPortfolioEvaluator(nil); err == nil || m != nil {
		t.Fatal("missing compiler accepted", err)
	}
	if p, err := storage.OpenPostgresWithPortfolioEvaluator(context.Background(), "invalid-without-compiler", nil); err == nil || p != nil {
		t.Fatal("missing compiler reached database construction", err)
	}
}

func TestOwnedPortfolioAssessmentHTTP(t *testing.T) {
	m, err := storage.NewMemoryWithPortfolioEvaluator(service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	ownedPortfolioAssessmentHTTP(t, m)
}

// These are manufactured captures submitted through the actual owners and
// public handler. Their prospective labels do not give them qualification credit.
func ownedPortfolioAssessmentHTTP(t *testing.T, s portfolioHTTPBackend) storage.PaperEvaluationAssessment {
	t.Helper()
	ctx := context.Background()
	clock := &ownedDecisionClock{at: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(service.HandlerWithClock(s, "portfolio-fixture-token", clock))
	defer server.Close()
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		if text, ok := body.(string); ok {
			raw = []byte(text)
		}
		r, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer portfolio-fixture-token")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(response.Body)
		if response.StatusCode != want {
			t.Fatal(method, path, response.StatusCode, want, out.String())
		}
		return out.Bytes()
	}
	n, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := n.ReceiverID
	d := strategyHTTPDefinition()
	d.Safety.MaxHoldingSeconds = 10
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.CreatePaperAccount(ctx, owner, paperaccount.Config{ID: "portfolio-account", Currency: "USD", OpeningCashCents: 200000, CapitalLimitCents: 200000}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	policy, err := s.RegisterAllocationPolicy(ctx, allocation.PolicyCommand{ReceiverID: owner, AccountID: account.Account.Config.ID, ExecutionModel: &model, Policy: allocation.Policy{ID: "portfolio-policy", AccountSHA256: account.Account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: d.ID, ArtifactSHA256: strategy.Record.DefinitionSHA256, LimitCents: 200000}}, Symbols: []allocation.Symbol{{ID: "SPY", LimitCents: 200000, Groups: []string{}}}, Groups: []allocation.Group{}}}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	start := clock.at.Add(10 * time.Second)
	stress := model
	stress.FeeBPS = 1
	declaration, err := s.DeclarePaperEvaluation(ctx, storage.PaperEvaluationDeclarationCommand{ID: "portfolio-plan", ReceiverID: owner, Recipe: storage.PaperEvaluationRecipe, PolicyID: policy.Record.Command.Policy.ID, PolicySHA256: policy.Record.SHA256, Plan: storage.PaperEvaluationPlan{FamilyID: "portfolio-family", Variants: []storage.PaperEvaluationVariant{{ID: d.ID, SHA256: strategy.Record.SHA256}}, WindowStart: start, WindowEnd: start.Add(40 * time.Second), OutcomeDeadline: start.Add(110 * time.Second), MinIndependentDecisions: 2, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 60, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress}}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "portfolio-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-portfolio", AccessRef: "manufactured-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	publish := func(id string, bid, ask int64, calendar bool) {
		t.Helper()
		r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: id, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.at, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, RawRef: "journal:journal-portfolio:capture-" + id + "-r1.json", MaxQuoteAgeSeconds: 60}
		r.Original = fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%d,"bp":%d,"as":10,"bs":10}}}`, clock.at.Format(time.RFC3339Nano), ask, bid)
		if calendar {
			r.Capability, r.Feed, r.Start, r.End = "market_calendar", "regular_session", "2026-10-21", "2026-10-21"
			r.Original = `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`
		}
		r.SHA256 = alpaca.Digest(r.Original)
		inputs, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range inputs.Inputs {
			if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock.Now); err != nil {
				t.Fatal(err)
			}
		}
		request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": owner, "fence": fence, "recording": r}, 200)
	}
	clock.at = start
	publish("calendar", 99, 100, true)
	publish("reference", 99, 100, false)
	clock.at = start.Add(time.Second)
	var decision storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", storage.PaperDecisionCommand{ID: "portfolio-decision", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, 201), &decision); err != nil || decision.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal(decision, err)
	}
	for _, q := range []struct{ seconds, bid, ask int64 }{{2, 99, 100}, {4, 94, 95}, {5, 93, 94}, {12, 95, 96}} {
		clock.at = start.Add(time.Duration(q.seconds) * time.Second)
		publish(fmt.Sprintf("quote-%d", q.seconds), q.bid, q.ask, false)
	}
	request("POST", "/v1/paper-outcomes", storage.PaperOutcomeCommand{ID: "portfolio-outcome", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, 201)
	clock.at = declaration.Record.Command.Plan.OutcomeDeadline.Add(time.Second)
	c := storage.PaperEvaluationAssessmentCommand{ID: "portfolio-assessment", ReceiverID: owner, DeclarationID: declaration.Record.Command.ID, DeclarationSHA256: declaration.Record.SHA256}
	raw, _ := json.Marshal(c)
	request("POST", "/v1/paper-evaluation-assessments", strings.Replace(string(raw), `"id":`, `"portfolio":{},"id":`, 1), 422)
	var got storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 201), &got); err != nil {
		t.Fatal(err)
	}
	r := got.Record
	if r.Schema != "mantik-owned-paper-evaluation-assessment-v6" || r.Recipe != storage.PaperPortfolioAssessmentRecipe || r.Portfolio == nil || len(r.Portfolio.Variants) != 1 || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
	v := r.Portfolio.Variants[0]
	if v.Base == nil || v.Stress == nil || v.Base.EndingCashCents != 193000 || v.Base.RealizedNetCents != -7000 || v.Base.ClosedNetCents == nil || *v.Base.ClosedNetCents != -7000 || v.Base.ObservedMaximumDrawdownCents != 7000 || v.Base.TotalReturnAvailable || v.Stress.EndingCashCents >= v.Base.EndingCashCents {
		t.Fatal("owner-pinned known economics", v)
	}
	clock.at = clock.at.Add(time.Second)
	publish("later-source", 90, 91, false)
	clock.panicRead = true
	var again storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 200), &again); err != nil || !again.Duplicate || !reflect.DeepEqual(again.Record, r) {
		t.Fatal("later source rewrote immutable economics", again, err)
	}
	var read storage.PaperEvaluationAssessment
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments/"+c.ID, nil, 200), &read); err != nil || !reflect.DeepEqual(read, r) {
		t.Fatal("read sampled live clock or changed source cut", err)
	}
	var retries sync.WaitGroup
	for range 4 {
		retries.Go(func() {
			var duplicate storage.PaperEvaluationAssessmentReceipt
			if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", c, 200), &duplicate); err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, r) {
				t.Error("concurrent retry changed retained accounting", err)
			}
		})
	}
	retries.Wait()
	clock.panicRead = false
	clock.at = clock.at.Add(time.Second)
	if err := service.RunPaperEvaluationCycle(ctx, s, clock); err != nil {
		t.Fatal(err)
	}
	var all storage.PaperEvaluationAssessmentCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments", nil, 200), &all); err != nil || len(all.Records) != 2 {
		t.Fatal(all, err)
	}
	for _, a := range all.Records {
		if a.Command.ID != c.ID && (a.Recipe != storage.PaperPortfolioAssessmentRecipe || a.Command.ID == service.AutomaticPaperEvaluationID(declaration.Record) || !strings.HasPrefix(a.Command.ID, service.AutomaticPaperEvaluationPrefix)) {
			t.Fatal("worker reused legacy recipe identity", a)
		}
	}
	clock.at = clock.at.Add(time.Second)
	var comparison storage.OwnedAllocationReceipt
	if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", storage.OwnedAllocationCommand{ID: "portfolio-comparison", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}, 201), &comparison); err != nil || len(comparison.Record.AdmissionChecks) != 1 || comparison.Record.AdmissionChecks[0].AdmissionAvailable || comparison.Record.ExecutionAvailable || comparison.Record.QualificationCredit || comparison.Record.Entry.Plan == nil || comparison.Record.Entry.Plan.Decisions[0].Status != "reject" {
		t.Fatal("portfolio math changed allocation authority", comparison, err)
	}
	request("GET", "/v1/paper-attribution", nil, 200)
	return r
}

func TestPostgresContractsOwnedPortfolioAssessmentHTTP(t *testing.T) {
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	db := "mantik_test_portfolio_http_" + strings.ToLower(rand.Text())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{db}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + db
	s, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	expected := ownedPortfolioAssessmentHTTP(t, s)
	s.Close()
	legacy, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	legacyServer := httptest.NewServer(service.HandlerWithClock(legacy, "legacy-token", &ownedDecisionClock{panicRead: true}))
	legacyRequest, _ := http.NewRequestWithContext(ctx, "GET", legacyServer.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	legacyRequest.Header.Set("Authorization", "Bearer legacy-token")
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	legacyResponse.Body.Close()
	legacyServer.Close()
	legacy.Close()
	if legacyResponse.StatusCode != 503 {
		t.Fatal("legacy owner accepted v6 without its compiled evaluator", legacyResponse.StatusCode)
	}
	reopened, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(service.HandlerWithClock(reopened, "reopen-token", &ownedDecisionClock{panicRead: true}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	req.Header.Set("Authorization", "Bearer reopen-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var actual storage.PaperEvaluationAssessment
	if err = json.NewDecoder(response.Body).Decode(&actual); err != nil || response.StatusCode != 200 || !reflect.DeepEqual(actual, expected) {
		t.Fatal("restart lost original economics", actual, err)
	}
	uRecord := actual
	uRecord.SHA256 = ""
	if workerplan.Hash(uRecord) != expected.SHA256 {
		t.Fatal("restart record hash changed")
	}

	// A coherently rehashed arithmetic edit is still not an owner-derived
	// result. Catalogue replay must rerun the compiler against actual sources.
	data, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close(ctx)
	var original []byte
	if err := data.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_assessments WHERE id=$1", expected.Command.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var changed storage.PaperEvaluationAssessment
	if err := json.Unmarshal(original, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Portfolio.Variants[0].Base.EndingCashCents++
	changed.Portfolio.Variants[0].Base.SHA256 = ""
	changed.Portfolio.Variants[0].Base.SHA256 = workerplan.Hash(*changed.Portfolio.Variants[0].Base)
	changed.Portfolio.SHA256 = ""
	changed.Portfolio.SHA256 = workerplan.Hash(*changed.Portfolio)
	changed.SHA256 = ""
	changed.SHA256 = workerplan.Hash(changed)
	corrupt, _ := json.Marshal(changed)
	if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", corrupt, expected.Command.ID); err != nil {
		t.Fatal(err)
	}
	denied, err := http.DefaultClient.Do(req.Clone(ctx))
	if err != nil {
		t.Fatal(err)
	}
	denied.Body.Close()
	if denied.StatusCode != 503 {
		t.Fatal("rehashing fabricated accounting granted replay", denied.StatusCode)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", original, expected.Command.ID); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := data.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=75").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=75"); err != nil {
		t.Fatal(err)
	}
	if broken, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio); err == nil || broken != nil {
		t.Fatal("damaged migration accepted", err)
	}
	if _, err := data.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=75", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Exec(ctx, `DELETE FROM mantik_schema WHERE version=75;
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'pre-portfolio-migration';
CREATE FUNCTION reject_portfolio_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=75 THEN RAISE EXCEPTION 'manufactured migration failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_portfolio_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_portfolio_migration()`); err != nil {
		t.Fatal(err)
	}
	if failed, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio); err == nil || failed != nil {
		t.Fatal("failed migration acknowledged", err)
	}
	var comment string
	var retained []byte
	if err := data.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_assessments'::regclass)").Scan(&comment); err != nil || comment != "pre-portfolio-migration" {
		t.Fatal("migration rollback changed prior metadata", comment, err)
	}
	if err := data.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_assessments WHERE id=$1", expected.Command.ID).Scan(&retained); err != nil || !bytes.Equal(retained, original) {
		t.Fatal("migration rewrote retained economics", err)
	}
	if _, err := data.Exec(ctx, "DROP TRIGGER reject_portfolio_migration ON mantik_schema; DROP FUNCTION reject_portfolio_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()

	// Restore a closed test-owned database into another isolated database. This
	// exercises actual v6 source/assessment custody after a database copy, without
	// changing the original records or claiming funded-account recovery.
	server.Close()
	reopened.Close()
	_ = data.Close(ctx)
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{db}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
		t.Fatal(err)
	}
	restoredDB := db + "_restore"
	if len(restoredDB) > 63 {
		t.Fatal("fixture database name exceeds PostgreSQL identifier bound")
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" TEMPLATE "+pgx.Identifier{db}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" ALLOW_CONNECTIONS false"); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{restoredDB}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + restoredDB
	restored, err := storage.OpenPostgresWithPortfolioEvaluator(ctx, u.String(), service.EvaluateOwnedPaperPortfolio)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredServer := httptest.NewServer(service.HandlerWithClock(restored, "restored-token", &ownedDecisionClock{panicRead: true}))
	defer restoredServer.Close()
	restoredRequest, _ := http.NewRequestWithContext(ctx, "GET", restoredServer.URL+"/v1/paper-evaluation-assessments/"+expected.Command.ID, nil)
	restoredRequest.Header.Set("Authorization", "Bearer restored-token")
	restoredResponse, err := http.DefaultClient.Do(restoredRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredResponse.Body.Close()
	var restoredRecord storage.PaperEvaluationAssessment
	if err := json.NewDecoder(restoredResponse.Body).Decode(&restoredRecord); err != nil || restoredResponse.StatusCode != 200 || !reflect.DeepEqual(restoredRecord, expected) {
		t.Fatal("restored database lost source-bound accounting", restoredRecord, err)
	}
}
