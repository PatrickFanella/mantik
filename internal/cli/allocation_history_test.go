package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// The actual Memory and PostgreSQL HTTP lifecycles both exercise this route,
// including a journal whose records have not reached their sealing threshold.
func allocationHistoryHTTP(t *testing.T, request func(string, string, any, int) []byte, base, owner string, clock *ownedDecisionClock) {
	t.Helper()
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
	allocationHistoryIndexHTTP(t, request, base, owner)
	query := url.Values{"receiver_id": {owner}, "limit": {"2"}}
	raw := request("GET", "/v1/allocation-history?"+query.Encode(), nil, 200)
	unauthenticated, err := http.Get(base + "/v1/allocation-history?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatal("allocation history read without operator authentication", unauthenticated.StatusCode)
	}
	var native storage.OwnedAllocationHistoryPage
	q := storage.OwnedAllocationHistoryQuery{ReceiverID: owner, Limit: 2}
	if err := json.Unmarshal(raw, &native); err != nil || native.Validate(q) != nil {
		t.Fatal("native allocation history envelope", err)
	}
	var out, diag bytes.Buffer
	if err := Run(context.Background(), []string{"allocation-history", "--api-url", base, "--receiver-id", owner, "--limit", "2"}, &out, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	var got storage.OwnedAllocationHistoryPage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, native) {
		t.Fatal("CLI changed the native allocation history", err)
	}
	out.Reset()
	diag.Reset()
	if err := Run(context.Background(), []string{"allocation-history", "--api-url", base, "--receiver-id", owner, "--decision-id", "selected-decision", "--limit", "2"}, &out, &diag); err != nil {
		t.Fatal("scoped allocation history CLI", err, diag.String())
	}
	var scoped storage.OwnedAllocationHistoryPage
	scopedQuery := storage.OwnedAllocationHistoryQuery{ReceiverID: owner, DecisionID: "selected-decision", Limit: 2}
	if err := json.Unmarshal(out.Bytes(), &scoped); err != nil || scoped.Validate(scopedQuery) != nil || scoped.DecisionID != scopedQuery.DecisionID || scoped.Schema != "mantik-allocation-history-page-v2" {
		t.Fatal("CLI lost decision history scope", err)
	}
	for _, path := range []string{
		"/v1/allocation-history",
		"/v1/allocation-history?",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=0",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=65",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=1&limit=2",
		"/v1/allocation-history?receiver_id=" + owner + "&after=bad",
		"/v1/allocation-history?receiver_id=" + owner + "&unknown=1",
		"/v1/allocation-history?receiver_id=" + owner + "&decision_id=one&decision_id=two",
		"/v1/allocation-history?receiver_id=" + owner + "&decision_id=../another",
	} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/allocation-history?"+query.Encode(), map[string]string{"effect": "invented"}, 422)
	for _, flags := range [][]string{
		{"--limit", "65"},
		{"--after", "bad"},
		{"--decision-id", "../another"},
		{"--api-url", "http://user:password@127.0.0.1"},
		{"unexpected"},
	} {
		out.Reset()
		diag.Reset()
		args := append([]string{"allocation-history", "--api-url", base, "--receiver-id", owner}, flags...)
		if err := Run(context.Background(), args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid allocation history CLI emitted a page")
		}
	}
}

func allocationHistoryIndexHTTP(t *testing.T, request func(string, string, any, int) []byte, base, owner string) {
	t.Helper()
	command := storage.OwnedAllocationIndexCommand{ReceiverID: owner}
	read := request("POST", "/v1/allocation-history/index", command, 200)
	var native storage.OwnedAllocationIndexReceipt
	if err := json.Unmarshal(read, &native); err != nil || native.Validate(command) != nil || native.ThroughSequence != 0 || native.SegmentsAdded != 0 {
		t.Fatal("unsealed history index maintenance", err)
	}
	var out, diag bytes.Buffer
	if err := Run(t.Context(), []string{"allocation-history-index", "--api-url", base, "--receiver-id", owner}, &out, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	var got storage.OwnedAllocationIndexReceipt
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, native) {
		t.Fatal("CLI changed native index receipt", err)
	}
	unauthenticated, err := http.Post(base+"/v1/allocation-history/index", "application/json", strings.NewReader(string(read)))
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatal("index maintenance without operator authentication", unauthenticated.StatusCode)
	}
	for _, body := range []any{nil, "{}", "null", "[]", `{"receiver_id":"` + owner + `","unknown":true}`, `{"receiver_id":"` + owner + `","receiver_id":"` + owner + `"}`, strings.Repeat(" ", 1025)} {
		request("POST", "/v1/allocation-history/index", body, 422)
	}
	for _, query := range []string{"?", "?receiver_id=" + owner} {
		request("POST", "/v1/allocation-history/index"+query, command, 422)
	}
	request("POST", "/v1/allocation-history/index", storage.OwnedAllocationIndexCommand{ReceiverID: "receiver-xxxxxxxxxxxxxxxx"}, 409)
	for _, flags := range [][]string{{"--receiver-id", ""}, {"--api-url", "http://user:password@127.0.0.1"}, {"--api-url", base + "/unexpected"}, {"--api-url", base + "?"}, {"unexpected"}} {
		out.Reset()
		diag.Reset()
		args := append([]string{"allocation-history-index", "--api-url", base, "--receiver-id", owner}, flags...)
		if err := Run(t.Context(), args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid index maintenance CLI emitted a receipt")
		}
	}
}
