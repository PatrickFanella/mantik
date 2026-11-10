package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// The Memory and PostgreSQL HTTP lifecycles both call this with a decision
// that has one retained comparison. The bounded total must match the complete
// paper attribution replay served by the same process.
func allocationContributionHTTP(t *testing.T, request func(string, string, any, int) []byte, base, owner string, clock *ownedDecisionClock, decision string) {
	t.Helper()
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
	query := url.Values{"receiver_id": {owner}, "decision_id": {decision}, "limit": {"2"}}
	path := "/v1/allocation-history/contributions?" + query.Encode()
	q := storage.OwnedAllocationContributionQuery{ReceiverID: owner, DecisionID: decision, Limit: 2}
	var page storage.OwnedAllocationContributionPage
	if err := json.Unmarshal(request("GET", path, nil, 200), &page); err != nil || page.Validate(q) != nil || page.Active == nil || len(page.Records)+len(page.Active.Records) == 0 || page.NextAfter != "" {
		t.Fatal("native contribution page", err)
	}
	unauthenticated, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatal("contribution read without operator authentication", unauthenticated.StatusCode)
	}
	want, err := storage.AssembleOwnedAllocationContribution(owner, decision, 2, []storage.OwnedAllocationContributionPage{page})
	if err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	if err := Run(t.Context(), []string{"allocation-contribution", "--api-url", base, "--receiver-id", owner, "--decision-id", decision, "--limit", "2"}, &out, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	var got storage.OwnedAllocationContributionTotal
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) || got.SealedRecords+got.ActiveRecords == 0 || got.QualificationCredit {
		t.Fatal("CLI changed the native contribution total", err)
	}
	var row storage.PaperAttributionRow
	if err := json.Unmarshal(request("GET", "/v1/paper-attribution/"+decision, nil, 200), &row); err != nil {
		t.Fatal(err)
	}
	e := row.Economics
	if got.Status != e.Status || !reflect.DeepEqual(got.ReservedCents, e.ReservedCents) || !reflect.DeepEqual(got.FilledCostCents, e.FilledCostCents) || !reflect.DeepEqual(got.ClosedCostCents, e.ClosedCostCents) || !reflect.DeepEqual(got.NetProceedsCents, e.NetProceedsCents) || !reflect.DeepEqual(got.ReleasedCents, e.ReleasedCents) || !reflect.DeepEqual(got.RealizedPnLCents, e.RealizedPnLCents) || !reflect.DeepEqual(got.Effects, e.Effects) {
		t.Fatalf("bounded contribution differs from complete attribution replay:\n got %+v\nwant %+v", got, e)
	}
	for _, bad := range []string{
		"/v1/allocation-history/contributions",
		"/v1/allocation-history/contributions?",
		"/v1/allocation-history/contributions?receiver_id=" + owner,
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=" + decision + "&limit=0",
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=" + decision + "&limit=65",
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=" + decision + "&after=bad",
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=" + decision + "&unknown=1",
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=one&decision_id=two",
		"/v1/allocation-history/contributions?receiver_id=" + owner + "&decision_id=../another",
	} {
		request("GET", bad, nil, 422)
	}
	request("GET", path, map[string]string{"effect": "invented"}, 422)
	request("GET", "/v1/allocation-history/contributions?receiver_id=receiver-xxxxxxxxxxxxxxxx&decision_id="+decision, nil, 409)
	for _, flags := range [][]string{
		{"--decision-id", ""},
		{"--decision-id", "../another"},
		{"--limit", "65"},
		{"--api-url", "http://user:password@127.0.0.1"},
		{"--api-url", base + "/unexpected"},
		{"unexpected"},
	} {
		out.Reset()
		diag.Reset()
		args := append([]string{"allocation-contribution", "--api-url", base, "--receiver-id", owner, "--decision-id", decision}, flags...)
		if err := Run(t.Context(), args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid contribution CLI emitted a total", flags)
		}
	}
}
