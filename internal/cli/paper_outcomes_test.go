package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOwnedPaperOutcomeHTTP(t *testing.T) { ownedPaperOutcomeHTTP(t, storage.NewMemory()) }
func ownedPaperOutcomeHTTP(t *testing.T, s ownedDecisionHTTPBackend) {
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
	unauth, err := http.Get(server.URL + "/v1/paper-outcomes")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatal(unauth.StatusCode)
	}
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	d := strategyHTTPDefinition()
	d.ID = "owned-baseline"
	d.Safety.MaxHoldingSeconds = 10
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
	var first storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 201), &first); err != nil {
		t.Fatal(err)
	}
	if first.Record.Validate() != nil || first.Record.Evaluation.Action != "entry_candidate" || first.Record.Evaluation.PriceCents != 10000 || !first.Record.Evaluation.Signal || first.Record.ExecutionAvailable || first.Record.AdmissionAvailable || first.Record.QualificationCredit {
		t.Fatal(first)
	}

	commandOutcome := storage.PaperOutcomeCommand{ID: "early", ReceiverID: owner, DecisionID: first.Record.Command.ID, DecisionSHA256: first.Record.SHA256}
	assess := func(id, status string) storage.PaperOutcomeReceipt {
		t.Helper()
		commandOutcome.ID = id
		var r storage.PaperOutcomeReceipt
		if err := json.Unmarshal(request("POST", "/v1/paper-outcomes", commandOutcome, 201), &r); err != nil {
			t.Fatal(err)
		}
		if r.Record.Validate() != nil || r.Record.Evaluation.Status != status || r.Record.AdmissionAvailable || r.Record.ExecutionAvailable || r.Record.QualificationCredit {
			t.Fatal(r)
		}
		return r
	}
	early := assess("early", "waiting_horizon")
	clock.at = clock.at.Add(10 * time.Second)
	waiting := assess("due", "waiting_price")
	// A fresh quote timestamp alone cannot make a late publication timely.
	clock.at = clock.at.Add(61 * time.Second)
	missing := assess("missing", "price_unavailable")
	late := quote("late", "102")
	late.ReceivedAt = first.Record.FinalizedAt.Add(11 * time.Second)
	late.Original = fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":102,"bp":101,"as":10,"bs":10}}}`, late.ReceivedAt.Format(time.RFC3339Nano))
	late.SHA256 = alpaca.Digest(late.Original)
	publish(late)
	assess("after-late", "price_unavailable")
	// Historical assessments are immutable and do not read the current clock.
	for _, old := range []storage.PaperOutcomeReceipt{early, waiting, missing} {
		clock.panicRead = true
		var got storage.PaperOutcomeReceipt
		if err := json.Unmarshal(request("POST", "/v1/paper-outcomes", old.Record.Command, 200), &got); err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, old.Record) {
			t.Fatal(got, err)
		}
		clock.panicRead = false
	}
	// A separate actual decision has its own horizon. New capture within that
	// window becomes a real service-selected observation, without granting a fill.
	clock.at = clock.at.Add(time.Second)
	publish(quote("entry2", "100"))
	command.ID = "second"
	var second storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 201), &second); err != nil || second.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal(second, err)
	}
	commandOutcome.DecisionID = second.Record.Command.ID
	commandOutcome.DecisionSHA256 = second.Record.SHA256
	clock.at = clock.at.Add(11 * time.Second)
	publish(quote("forward2", "102"))
	observed := assess("observed", "observed")
	if observed.Record.Evaluation.EntryPriceCents != 10000 || observed.Record.Evaluation.ObservedPriceCents != 10200 || observed.Record.Evaluation.PriceDeltaCents != 200 || !observed.Record.Evaluation.AvailableAt.Equal(clock.at) || observed.Record.Evaluation.InputID == "" || observed.Record.Evaluation.CaptureSHA256 == "" {
		t.Fatal(observed)
	}
	// A later missing capture does not rewrite an earlier timely observation.
	clock.at = clock.at.Add(time.Second)
	bad := quote("unusable", "102")
	bad.Original = `{"quotes":{}}`
	bad.SHA256 = alpaca.Digest(bad.Original)
	publish(bad)
	again := assess("observed-after-bad", "observed")
	if !reflect.DeepEqual(observed.Record.Evaluation, again.Record.Evaluation) {
		t.Fatal(again)
	}
	// Equal-receipt conflicting quotes are evaluated as one publication-time
	// group. Capture identity cannot choose a winner by map order.
	clock.at = clock.at.Add(time.Second)
	publish(quote("entry3", "100"))
	command.ID = "third"
	var third storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 201), &third); err != nil || third.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal(third, err)
	}
	commandOutcome.DecisionID = third.Record.Command.ID
	commandOutcome.DecisionSHA256 = third.Record.SHA256
	clock.at = clock.at.Add(11 * time.Second)
	publish(quote("conflict-a", "101"))
	publish(quote("conflict-b", "102"))
	assess("conflict", "waiting_price")
	// A newer admitted input without its raw publication suppresses an older
	// published quote. The pending input supplies no invented observation.
	clock.at = clock.at.Add(time.Second)
	pending := quote("pending", "103")
	normalized, err := alpaca.Normalize(pending)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range normalized.Inputs {
		if _, err := s.SubmitCollectorResearch(ctx, owner, fence, i, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	assess("pending", "waiting_price")
	// Publishing the missing evidence allows a fresh assessment to observe it.
	publish(pending)
	assess("published", "observed")
	// Raw-publication pending is independently tested with an otherwise usable
	// quote, rather than relying on the earlier conflicting pair to suppress it.
	clock.at = clock.at.Add(time.Second)
	publish(quote("entry4", "100"))
	command.ID = "fourth"
	var fourth storage.PaperDecisionReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decisions", command, 201), &fourth); err != nil || fourth.Record.Evaluation.Action != "entry_candidate" {
		t.Fatal(fourth, err)
	}
	commandOutcome.DecisionID = fourth.Record.Command.ID
	commandOutcome.DecisionSHA256 = fourth.Record.SHA256
	clock.at = clock.at.Add(11 * time.Second)
	pending4 := quote("pending4", "103")
	n4, err := alpaca.Normalize(pending4)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range n4.Inputs {
		if _, err := s.SubmitCollectorResearch(ctx, owner, fence, i, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	publish(quote("otherwise-good", "102"))
	assess("pending-over-good", "waiting_price")
	clock.at = clock.at.Add(time.Second)
	publish(pending4)
	assess("pending-complete", "waiting_price")
	// The equal-received conflict remains a conflict even after both raw
	// captures arrive. A genuinely newer quote can supply an observation.
	clock.at = clock.at.Add(time.Second)
	publish(quote("newer-good", "104"))
	assess("newer-good", "observed")
	raw, _ := json.Marshal(commandOutcome)
	for _, field := range []string{`"price_cents":123,`, `"evaluated_at":"2099-01-01T00:00:00Z",`, `"horizon":1,`, `"qualification_credit":true,`, `"admission":{},`, `"effect":{},`} {
		request("POST", "/v1/paper-outcomes", strings.Replace(string(raw), `"id":`, field+`"id":`, 1), 422)
	}
	request("POST", "/v1/paper-outcomes?", commandOutcome, 422)
	request("GET", "/v1/paper-outcomes?at=future", nil, 422)
	wrong := commandOutcome
	wrong.ID = "wrong-pin"
	wrong.DecisionSHA256 = strings.Repeat("0", 64)
	request("POST", "/v1/paper-outcomes", wrong, 409)
	var catalog storage.PaperOutcomeCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-outcomes", nil, 200), &catalog); err != nil || len(catalog.Records) != 12 {
		t.Fatal(catalog, err)
	}
	request("GET", "/v1/paper-outcomes/observed", nil, 200)
	request("GET", "/v1/paper-outcomes/absent", nil, 404)
	// Source submission changes routing as expected. The outcome API itself owns
	// no operation or balance writes; the original case is still waiting.
	after, err := s.Snapshot(ctx)
	if err != nil || after.State.Case.Status != before.State.Case.Status {
		t.Fatal(after, err)
	}
}

func TestPostgresContractsOwnedPaperOutcomeHTTP(t *testing.T) {
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
	db := "mantik_test_owned_outcome_http_" + strings.ToLower(rand.Text())
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
	ownedPaperOutcomeHTTP(t, s)

}
