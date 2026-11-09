package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// The actual Memory and PostgreSQL HTTP lifecycles both exercise this route,
// including a journal whose records have not reached their sealing threshold.
func allocationHistoryHTTP(t *testing.T, request func(string, string, any, int) []byte, base, owner string, clock *ownedDecisionClock) {
	t.Helper()
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
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
	for _, path := range []string{
		"/v1/allocation-history",
		"/v1/allocation-history?",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=0",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=65",
		"/v1/allocation-history?receiver_id=" + owner + "&limit=1&limit=2",
		"/v1/allocation-history?receiver_id=" + owner + "&after=bad",
		"/v1/allocation-history?receiver_id=" + owner + "&unknown=1",
	} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/allocation-history?"+query.Encode(), map[string]string{"effect": "invented"}, 422)
	for _, flags := range [][]string{
		{"--limit", "65"},
		{"--after", "bad"},
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
