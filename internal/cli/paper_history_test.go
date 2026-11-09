package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Both actual Memory and disposable PostgreSQL lifecycle tests call this after
// sealing their native decisions/outcomes. No replacement evaluator or clock
// participates in the service, HTTP or CLI readbacks.
func paperHistoryHTTP(t *testing.T, request func(string, string, any, int) []byte, base, owner string, clock *ownedDecisionClock) {
	t.Helper()
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
	var decisions storage.PaperDecisionCatalog
	var outcomes storage.PaperOutcomeCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-decisions", nil, 200), &decisions); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("GET", "/v1/paper-outcomes", nil, 200), &outcomes); err != nil {
		t.Fatal(err)
	}
	var first storage.PaperHistoryPage
	for _, kind := range []string{"decision", "outcome"} {
		seen := map[string]string{}
		after := ""
		for {
			var out, diag bytes.Buffer
			args := []string{"paper-history", "--api-url", base, "--receiver-id", owner, "--kind", kind, "--limit", "2", "--after", after}
			if err := Run(context.Background(), args, &out, &diag); err != nil {
				t.Fatal(err, diag.String())
			}
			q := storage.PaperHistoryQuery{ReceiverID: owner, Kind: kind, Limit: 2, After: after}
			var got storage.PaperHistoryPage
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Validate(q) != nil || got.Through == nil || got.Through.Sequence != 1 {
				t.Fatal("native history CLI envelope", got, err)
			}
			values := url.Values{"receiver_id": {owner}, "kind": {kind}, "limit": {"2"}, "after": {after}}
			var native storage.PaperHistoryPage
			if err := json.Unmarshal(request("GET", "/v1/paper-history?"+values.Encode(), nil, 200), &native); err != nil || !reflect.DeepEqual(native, got) {
				t.Fatal("CLI changed native history page", err)
			}
			if after == "" && kind == "decision" {
				first = got
			}
			for _, r := range got.Records {
				if _, duplicate := seen[r.ID]; duplicate {
					t.Fatal("history page repeated an original", r.ID)
				}
				seen[r.ID] = r.RecordSHA256
			}
			after = got.NextAfter
			if after == "" {
				break
			}
		}
		if kind == "decision" {
			if len(seen) != len(decisions.Records) {
				t.Fatal("history omitted actual decisions")
			}
			for _, d := range decisions.Records {
				if seen[d.Command.ID] != d.SHA256 {
					t.Fatal("history changed decision identity", d.Command.ID)
				}
			}
		} else {
			if len(seen) != len(outcomes.Records) {
				t.Fatal("history omitted actual outcomes")
			}
			for _, o := range outcomes.Records {
				if seen[o.Command.ID] != o.SHA256 {
					t.Fatal("history changed outcome identity", o.Command.ID)
				}
			}
		}
	}
	for _, extra := range []string{"&kind=decision", "&limit=65", "&unknown=1", "&after=bad", "&after=" + first.NextAfter + "&kind=outcome"} {
		request("GET", "/v1/paper-history?receiver_id="+owner+"&kind=decision"+extra, nil, 422)
	}
	request("GET", "/v1/paper-history?receiver_id="+owner+"&kind=decision", "{}", 422)
	request("GET", "/v1/paper-history", nil, 422)
	for name, mutate := range map[string]func(*storage.PaperHistoryPage){
		"receiver":      func(p *storage.PaperHistoryPage) { p.ReceiverID = "receiver-other" },
		"kind":          func(p *storage.PaperHistoryPage) { p.Kind = "outcome" },
		"qualification": func(p *storage.PaperHistoryPage) { p.QualificationCredit = true },
		"authority":     func(p *storage.PaperHistoryPage) { p.Authority = "execution_grant" },
		"boundary":      func(p *storage.PaperHistoryPage) { p.Through = nil },
		"order":         func(p *storage.PaperHistoryPage) { p.Records[0], p.Records[1] = p.Records[1], p.Records[0] },
		"epoch":         func(p *storage.PaperHistoryPage) { p.Records[0].CreatedEpoch = p.Through.Sequence },
		"original pin":  func(p *storage.PaperHistoryPage) { p.Records[0].RecordSHA256 = workerplan.Hash("other") },
		"next cursor":   func(p *storage.PaperHistoryPage) { p.NextAfter = "bad" },
	} {
		t.Run("history-presentation-"+name, func(t *testing.T) {
			raw, _ := json.Marshal(first)
			var bad storage.PaperHistoryPage
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			fake := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/paper-history" {
					t.Error("unexpected history path", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(bad)
			})))
			defer fake.Close()
			var out, diag bytes.Buffer
			if err := Run(context.Background(), []string{"paper-history", "--api-url", fake.URL, "--receiver-id", owner, "--limit", "2"}, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("counterfeit presentation printed history", err)
			}
		})
	}
}
