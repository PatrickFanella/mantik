package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
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

type socialHTTPBackend interface {
	storage.Store
	storage.NewsStore
	storage.PaperAccountStore
	storage.SocialWatchStore
}

func TestOwnedSocialHTTP(t *testing.T) { ownedSocialHTTP(t, storage.NewMemory()) }
func ownedSocialHTTP(t *testing.T, s socialHTTPBackend) {
	t.Helper()
	ctx := context.Background()
	clock := &ownedDecisionClock{at: time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(service.HandlerWithClock(s, "social-fixture-token", clock))
	defer server.Close()
	request := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		if text, ok := body.(string); ok {
			raw = []byte(text)
		}
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer social-fixture-token")
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
	unauthorized, err := http.Get(server.URL + "/v1/social-history")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatal(unauthorized.StatusCode)
	}
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	c := socialwatch.PolicyCommand{ID: "watch-request", ReceiverID: owner, Policy: socialwatch.Policy{ID: "watched", Provider: "recorded", AccountID: "account-1", Symbols: []string{"SPY"}, Behavior: "decision_review", MaxObservationDelaySeconds: 30, Enabled: true}}
	var first, retry socialwatch.Receipt
	if err := json.Unmarshal(request("POST", "/v1/social-watches", c, 201), &first); err != nil || first.Entry.Policy == nil || first.Entry.Policy.Version != 1 {
		t.Fatal(first, err)
	}
	clock.panicRead = true
	if err := json.Unmarshal(request("POST", "/v1/social-watches", c, 200), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Entry, retry.Entry) {
		t.Fatal(retry, err)
	}
	clock.panicRead = false
	o := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "account-1", PostID: "post-1"}, Revision: 1, Kind: "declared_position", OccurredAt: clock.at, ObservedAt: clock.at, Text: "Source says it holds SPY. This is a claim, not an observed fill.", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(o)
	cap := socialwatch.CaptureCommand{ID: "recording", ReceiverID: owner, WatchID: c.Policy.ID, PolicySHA256: first.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
	var capture socialwatch.Receipt
	if err := json.Unmarshal(request("POST", "/v1/social-recordings", cap, 201), &capture); err != nil || capture.Entry.Capture == nil {
		t.Fatal(capture, err)
	}
	record := capture.Entry.Capture
	if record.PolicyStatus != "structured_extraction_unavailable" || record.Command.Original != string(raw) || record.Source.Kind != "declared_position" || record.ExecutionAvailable || record.DispatchAvailable || record.IndependentEvidenceCredit || record.QualificationCredit || record.ProviderAccessVerified || record.InstrumentMappingVerified {
		t.Fatal(record)
	}
	var applicability socialwatch.Applicability
	if err := json.Unmarshal(request("GET", "/v1/social-recordings/recording/applicability", nil, 200), &applicability); err != nil || applicability.Status != "structured_extraction_unavailable" || applicability.CaptureSHA256 != record.SHA256 || applicability.Selected.OriginalSHA256 != record.Command.OriginalSHA256 || applicability.AsOf != clock.at {
		t.Fatal(applicability, err)
	}
	if applicability.DispatchAvailable || applicability.ProviderAccessVerified || applicability.InstrumentMappingVerified || applicability.IndependentEvidenceCredit || applicability.AdmissionAvailable || applicability.ExecutionAvailable || applicability.QualificationCredit {
		t.Fatal("applicability invented authority", applicability)
	}
	for _, path := range []string{"/v1/social-recordings/recording/applicability?", "/v1/social-recordings/recording/applicability?as_of=2000-01-01T00:00:00Z", "/v1/social-recordings/recording/applicability?provider_access_verified=true"} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/social-recordings/missing/applicability", nil, 404)
	unauth, _ := http.Get(server.URL + "/v1/social-recordings/recording/applicability")
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatal(unauth.StatusCode)
	}
	actual := clock.at
	clock.at = actual.Add(-time.Nanosecond)
	if err := json.Unmarshal(request("GET", "/v1/social-recordings/recording/applicability", nil, 200), &applicability); err != nil || applicability.Status != "not_available_at_cutoff" || applicability.Current != nil {
		t.Fatal(applicability, err)
	}
	clock.at = actual
	clock.panicRead = true
	if err := json.Unmarshal(request("POST", "/v1/social-recordings", cap, 200), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(capture.Entry, retry.Entry) {
		t.Fatal(retry, err)
	}
	clock.panicRead = false
	var watch socialwatch.WatchView
	if err := json.Unmarshal(request("GET", "/v1/social-watches/watched", nil, 200), &watch); err != nil || watch.ConnectorStatus != "connector_not_configured" || watch.ConnectorScope.AccountID != "account-1" || watch.ConnectorScope.Provider != "recorded" || watch.DispatchAvailable || watch.ProviderAccessVerified {
		t.Fatal(watch, err)
	}
	request("GET", "/v1/social-watches/unknown", nil, 404)
	policyRaw, _ := json.Marshal(c)
	for _, field := range []string{`"receiverId":"other",`, `"qualification_credit":true,`, `"balance_cents":123,`, `"effect":{},`} {
		request("POST", "/v1/social-watches", strings.Replace(string(policyRaw), `"id":`, field+`"id":`, 1), 422)
	}
	request("POST", "/v1/social-watches?", c, 422)
	request("GET", "/v1/social-history?at=future", nil, 422)
	bad := cap
	bad.ID = "qualified"
	bad.EvidenceClass = "prospective_qualified"
	request("POST", "/v1/social-recordings", bad, 422)
	bad = cap
	bad.ID = "wrong-pin"
	bad.PolicySHA256 = strings.Repeat("0", 64)
	request("POST", "/v1/social-recordings", bad, 409)
	forged := o
	forged.Source.AccountID = "different"
	forgedRaw, _ := json.Marshal(forged)
	bad = cap
	bad.ID = "wrong-source"
	bad.Original = string(forgedRaw)
	bad.OriginalSHA256 = workerplan.Digest(forgedRaw)
	request("POST", "/v1/social-recordings", bad, 409)
	bad = cap
	bad.ID = "unknown-original-field"
	bad.Original = strings.Replace(cap.Original, `"schema":`, `"actual_fill":true,"schema":`, 1)
	bad.OriginalSHA256 = workerplan.Digest([]byte(bad.Original))
	request("POST", "/v1/social-recordings", bad, 422)
	// Historical retries survive an authorized watch-policy update. New captures
	// must select the current policy; no caller chooses a historical policy.
	update := c
	update.ID = "disable"
	update.PreviousSHA256 = first.Entry.Policy.SHA256
	update.Policy.Enabled = false
	var disabled socialwatch.Receipt
	if err := json.Unmarshal(request("POST", "/v1/social-watches", update, 201), &disabled); err != nil || disabled.Entry.Policy.Version != 2 {
		t.Fatal(disabled, err)
	}
	clock.panicRead = true
	request("POST", "/v1/social-recordings", cap, 200)
	request("POST", "/v1/social-watches", c, 200)
	clock.panicRead = false
	bad = cap
	bad.ID = "new-stale-policy"
	request("POST", "/v1/social-recordings", bad, 409)
	o.Source.PostID = "post-2"
	o.Kind = "post"
	raw, _ = json.Marshal(o)
	cap.ID = "disabled-capture"
	cap.PolicySHA256 = disabled.Entry.Policy.SHA256
	cap.Original = string(raw)
	cap.OriginalSHA256 = workerplan.Digest(raw)
	var ignored socialwatch.Receipt
	if err := json.Unmarshal(request("POST", "/v1/social-recordings", cap, 201), &ignored); err != nil || ignored.Entry.Capture.PolicyStatus != "watch_disabled" {
		t.Fatal(ignored, err)
	}
	if err := json.Unmarshal(request("GET", "/v1/social-recordings/recording/applicability", nil, 200), &applicability); err != nil || applicability.Status != "watch_policy_changed" {
		t.Fatal(applicability, err)
	}
	if err := json.Unmarshal(request("GET", "/v1/social-recordings/disabled-capture/applicability", nil, 200), &applicability); err != nil || applicability.Status != "watch_disabled" {
		t.Fatal(applicability, err)
	}
	var all socialwatch.Catalog
	if err := json.Unmarshal(request("GET", "/v1/social-history", nil, 200), &all); err != nil || len(all.History) != 4 || len(all.Watches) != 1 {
		t.Fatal(all, err)
	}
	after, _ := s.Snapshot(ctx)
	accountAfter, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountAfter) {
		t.Fatal("social API wrote operations/capital")
	}
}

func TestPostgresContractsOwnedSocialHTTP(t *testing.T) {
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
	db := "mantik_test_social_http_" + strings.ToLower(rand.Text())
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
	ownedSocialHTTP(t, s)

}
