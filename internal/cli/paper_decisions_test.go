package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type ownedDecisionClock struct {
	at        time.Time
	panicRead bool
}

func (c *ownedDecisionClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *ownedDecisionClock) Now() time.Time {
	if c.panicRead {
		panic("retry read new clock")
	}
	return c.at
}

type ownedDecisionHTTPBackend interface {
	strategyHTTPBackend
	storage.PaperDecisionStore
	storage.PaperOutcomeStore
	storage.OwnedAllocationStore
	storage.PaperAccountStore
	storage.AllocationPolicyStore
	storage.FeedCollectorStore
	storage.FeedEvidenceStore
	storage.CollectorSubmissionStore
}

func TestOwnedPaperDecisionHTTP(t *testing.T) { ownedPaperDecisionHTTP(t, storage.NewMemory()) }
func ownedPaperDecisionHTTP(t *testing.T, s ownedDecisionHTTPBackend) {
	t.Helper()
	ctx := context.Background()
	clock := &ownedDecisionClock{at: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(service.HandlerWithClock(s, "owned-decision-token", clock))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "owned-decision-token")
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		if text, ok := body.(string); ok {
			raw = []byte(text)
		}
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer owned-decision-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(res.Body)
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want, out.String())
		}
		return out.Bytes()
	}
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	d := strategyHTTPDefinition()
	d.ID = "owned-baseline"
	d.InputEvidenceClass = "prospective_market_unqualified"
	var strategy paperstrategy.Receipt
	if err := json.Unmarshal(request("POST", "/v1/paper-strategies", paperstrategy.Command{ReceiverID: owner, Definition: d}, 201), &strategy); err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "owned-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "owned-test-journal", AccessRef: "manufactured-fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "owned-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "owned-claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "owner"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "owner", Generation: 1}
	publish := func(r alpaca.Recording) {
		t.Helper()
		normalized, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range normalized.Inputs {
			if _, err := s.SubmitCollectorResearch(ctx, owner, fence, i, clock.Now); err != nil {
				t.Fatal(err)
			}
		}
		request("POST", "/v1/feed-evidence", map[string]any{"receiver_id": owner, "fence": fence, "recording": r}, 200)
	}
	quote := func(id, price string) alpaca.Recording {
		raw := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%s,"bp":99,"as":10,"bs":10}}}`, clock.at.Add(-time.Second).Format(time.RFC3339Nano), price)
		return alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: id, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.at, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, RawRef: "journal:owned-test-journal:capture-" + id + "-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	}
	calendar := quote("calendar", "100")
	calendar.Capability = "market_calendar"
	calendar.Feed = "regular_session"
	calendar.Start = "2026-10-21"
	calendar.End = "2026-10-21"
	calendar.Original = `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`
	calendar.SHA256 = alpaca.Digest(calendar.Original)
	publish(calendar)
	publish(quote("quote", "100"))
	before, _ := s.Snapshot(ctx)
	command := storage.PaperDecisionCommand{ID: "owned-first", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	var first, retry storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 201), &first); err != nil {
		t.Fatal(err)
	}
	if first.Record.Validate() != nil || first.Record.Evaluation.Action != "entry_candidate" || first.Record.Evaluation.PriceCents != 10000 || !first.Record.Evaluation.Signal || first.Record.ExecutionAvailable || first.Record.AdmissionAvailable || first.Record.QualificationCredit {
		t.Fatal(first)
	}
	explanationBefore, _ := s.Snapshot(ctx)
	var explanation service.DecisionExplanation
	clock.panicRead = true
	rawExplanation := request("GET", "/v1/paper-decisions/"+command.ID+"/explanation", nil, 200)
	clock.panicRead = false
	if err := json.Unmarshal(rawExplanation, &explanation); err != nil || explanation.DecisionSHA256 != first.Record.SHA256 || explanation.BasisSHA256 != first.Record.BasisSHA256 || explanation.StrategySHA256 != strategy.Record.SHA256 || explanation.ReferencePriceCents != 10000 || explanation.Action != "entry_candidate" || !reflect.DeepEqual(explanation.Coverage, first.Record.Evaluation.Coverage) {
		t.Fatal(explanation, err)
	}
	if explanation.ModelUsed || explanation.AdmissionAvailable || explanation.ExecutionAvailable || explanation.QualificationCredit || explanation.Authority != "finalized_research_decision_explanation_only" {
		t.Fatal(explanation)
	}
	if !strings.Contains(strings.Join(explanation.Statements, " "), "Admission is still required") || !strings.Contains(strings.Join(explanation.Statements, " "), "not a fill price") {
		t.Fatal(explanation.Statements)
	}
	explanationAfter, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(explanationBefore, explanationAfter) {
		t.Fatal("explanation changed operations")
	}
	for _, q := range []string{"?", "?prompt=claim-profit", "?model=unavailable", "?action=execute", "?as_of=tomorrow"} {
		request("GET", "/v1/paper-decisions/"+command.ID+"/explanation"+q, nil, 422)
	}
	request("GET", "/v1/paper-decisions/missing/explanation", nil, 404)
	unauth, err := http.Get(server.URL + "/v1/paper-decisions/" + command.ID + "/explanation")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatal(unauth.StatusCode)
	}
	// Actual public finalizer -> owner-selected funding policy -> atomic retained
	// comparison. No public proposal, balance or admission assertion is involved.
	account, err := s.CreatePaperAccount(ctx, owner, paperaccount.Config{ID: "allocation-owner", Currency: "USD", OpeningCashCents: 200000, CapitalLimitCents: 200000}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := allocation.PolicyCommand{ReceiverID: owner, AccountID: account.Account.Config.ID, Policy: allocation.Policy{ID: "owner-policy", AccountSHA256: account.Account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: d.ID, ArtifactSHA256: strategy.Record.DefinitionSHA256, LimitCents: 200000}}, Symbols: []allocation.Symbol{{ID: d.Symbol, LimitCents: 200000, Groups: []string{}}}, Groups: []allocation.Group{}}}
	if _, err = s.RegisterAllocationPolicy(ctx, policy, clock.Now); err != nil {
		t.Fatal(err)
	}
	compare := storage.OwnedAllocationCommand{ID: "compare-first", ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: first.Record.Command.ID, SHA256: first.Record.SHA256}}}
	var allocationFirst, allocationRetry storage.OwnedAllocationReceipt
	if err = json.Unmarshal(request("POST", "/v1/allocation-comparisons", compare, 201), &allocationFirst); err != nil {
		t.Fatal(err)
	}
	if allocationFirst.Record.ExecutionAvailable || allocationFirst.Record.QualificationCredit || allocationFirst.Record.Entry.Plan == nil || allocationFirst.Record.Entry.Plan.Decisions[0].Status != "defer" || allocationFirst.Record.Entry.Plan.Decisions[0].Reasons[0] != "admission_unavailable" {
		t.Fatal(allocationFirst)
	}
	checks := allocationFirst.Record.AdmissionChecks
	if len(checks) != 1 || checks[0].DecisionSHA256 != first.Record.SHA256 || checks[0].BasisSHA256 != first.Record.BasisSHA256 || checks[0].StrategySHA256 != strategy.Record.SHA256 || checks[0].QuoteInputID != first.Record.Evaluation.Coverage.Quote.InputID || checks[0].EvidenceClass != d.InputEvidenceClass || checks[0].Status != "deferred" || checks[0].AdmissionAvailable || checks[0].ExecutionAvailable || checks[0].QualificationCredit {
		t.Fatal(checks)
	}
	clock.panicRead = true
	if err = json.Unmarshal(request("POST", "/v1/allocation-comparisons", compare, 200), &allocationRetry); err != nil || !allocationRetry.Duplicate || !reflect.DeepEqual(allocationFirst.Record, allocationRetry.Record) {
		t.Fatal(allocationRetry, err)
	}
	clock.panicRead = false
	compareRaw, _ := json.Marshal(compare)
	for _, field := range []string{`"admission_checks":[],`, `"account_id":"allocation-owner",`, `"Snapshot":{},`, `"admissions":[],`, `"effect":{},`, `"balance_cents":100,`, `"policy_id":"owner-policy",`} {
		request("POST", "/v1/allocation-comparisons", strings.Replace(string(compareRaw), `"id":`, field+`"id":`, 1), 422)
	}
	request("POST", "/v1/allocation-comparisons?", compare, 422)
	request("GET", "/v1/allocation-comparisons?at=future", nil, 422)
	var allocationCatalog storage.OwnedAllocationCatalog
	_ = json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &allocationCatalog)
	if len(allocationCatalog.Records) != 1 || len(allocationCatalog.Ledgers) != 1 {
		t.Fatal(allocationCatalog)
	}
	projection, err := allocation.ReplayLedger(ctx, allocationCatalog.Ledgers[0])
	if err != nil || projection.Snapshot.AvailableCashCents != 200000 || len(projection.Snapshot.Orders) != 0 || len(projection.Snapshot.Positions) != 0 {
		t.Fatal(projection, err)
	}
	clock.panicRead = true
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 200), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	clock.panicRead = false
	// A later unusable quote suppresses the older good quote; retries keep the
	// original basis. This is synthetic diagnostic evidence, never qualification.
	clock.at = clock.at.Add(time.Second)
	missing := quote("missing", "100")
	missing.Original = `{"quotes":{}}`
	missing.SHA256 = alpaca.Digest(missing.Original)
	publish(missing)
	next := command
	next.ID = "owned-missing"
	var abstain storage.PaperDecisionReceipt
	_ = json.Unmarshal(request("POST", "/v1/paper-decisions", next, 201), &abstain)
	if abstain.Record.Evaluation.Action != "abstain" || abstain.Record.Evaluation.PriceCents != 0 {
		t.Fatal(abstain)
	}
	clock.panicRead = true
	if later := request("GET", "/v1/paper-decisions/"+command.ID+"/explanation", nil, 200); !bytes.Equal(later, rawExplanation) {
		t.Fatal("later evidence rewrote finalized explanation")
	}
	clock.panicRead = false
	var missingExplanation service.DecisionExplanation
	if err := json.Unmarshal(request("GET", "/v1/paper-decisions/"+next.ID+"/explanation", nil, 200), &missingExplanation); err != nil || missingExplanation.Action != "abstain" || missingExplanation.ReferencePriceCents != 0 || missingExplanation.ReasonCode != abstain.Record.Evaluation.ReasonCode {
		t.Fatal(missingExplanation, err)
	}
	if strings.Contains(strings.Join(missingExplanation.Statements, " "), "reference price was") {
		t.Fatal("missing price invented a price", missingExplanation)
	}
	request("POST", "/v1/paper-decisions", command, 200)
	request("GET", "/v1/paper-decisions/"+command.ID, nil, 200)
	clock.at = clock.at.Add(time.Second)
	publish(quote("above", "100.01"))
	next.ID = "owned-above"
	_ = json.Unmarshal(request("POST", "/v1/paper-decisions", next, 201), &abstain)
	if abstain.Record.Evaluation.ReasonCode != "threshold_not_met" {
		t.Fatal(abstain)
	}
	clock.at = clock.at.Add(61 * time.Second)
	next.ID = "owned-stale"
	_ = json.Unmarshal(request("POST", "/v1/paper-decisions", next, 201), &abstain)
	if abstain.Record.Evaluation.Action != "abstain" {
		t.Fatal(abstain)
	}
	changed := command
	changed.RegistrationID = "other"
	request("POST", "/v1/paper-decisions", changed, 409)
	raw, _ := json.Marshal(command)
	for _, body := range []string{string(raw) + "{}", strings.Replace(string(raw), `"id":`, `"Admission":true,"id":`, 1), strings.Replace(string(raw), `"id":`, `"id":"duplicate","id":`, 1), strings.Replace(string(raw), `"id":`, `"ID":`, 1), "null", strings.Repeat(" ", 4097)} {
		request("POST", "/v1/paper-decisions", body, 422)
	}
	request("POST", "/v1/paper-decisions?", command, 422)
	request("GET", "/v1/paper-decisions?at=future", nil, 422)
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", owner)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(err, diag.String())
		}
		return out.Bytes()
	}
	var selected storage.PaperDecisionRecord
	_ = json.Unmarshal(run("paper-decision", "--id", command.ID, "--record-sha256", first.Record.SHA256), &selected)
	if !reflect.DeepEqual(selected, first.Record) {
		t.Fatal(selected)
	}
	run("paper-decisions")
	run("paper-decision-finalize", "--id", command.ID, "--strategy-id", d.ID, "--strategy-sha256", strategy.Record.SHA256, "--registration-id", reg.ID)
	var out, diag bytes.Buffer
	if err := Run(ctx, []string{"paper-decision", "--api-url", server.URL, "--receiver-id", owner, "--id", command.ID, "--record-sha256", strings.Repeat("f", 64)}, &out, &diag); err == nil || out.Len() != 0 {
		t.Fatal("wrong pin emitted", err)
	}
	after, _ := s.Snapshot(ctx)
	if before.ObservationCount != after.ObservationCount || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.State.Case, after.State.Case) || !reflect.DeepEqual(before.State.Actions, after.State.Actions) {
		t.Fatal("finalization changed operational state")
	}
}

// Disposable integration only; no development/production database is accepted.
func TestPostgresContractsOwnedDecisionAllocationHTTP(t *testing.T) {
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
	db := "mantik_test_owned_allocation_http_" + strings.ToLower(rand.Text())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)")
		if e != nil {
			t.Error(e)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + db
	s, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ownedPaperDecisionHTTP(t, s)
	s.Close()
	reopened, err := storage.OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	clock := &ownedDecisionClock{panicRead: true}
	server := httptest.NewServer(service.HandlerWithClock(reopened, "restart-token", clock))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/allocation-comparisons", nil)
	req.Header.Set("Authorization", "Bearer restart-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var got storage.OwnedAllocationCatalog
	if err = json.NewDecoder(response.Body).Decode(&got); err != nil || response.StatusCode != 200 || len(got.Records) != 1 || len(got.Ledgers) != 1 || got.Records[0].Entry.Plan.Decisions[0].Reasons[0] != "admission_unavailable" {
		t.Fatal(got, err, response.StatusCode)
	}
}
