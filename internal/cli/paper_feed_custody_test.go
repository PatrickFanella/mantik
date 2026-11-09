package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Both Memory and disposable PostgreSQL exercise actual service evaluators over
// this lifecycle's native decisions, forward outcomes and abstentions.
func paperFeedCustodyHTTP(t *testing.T, request func(string, string, any, int) []byte, owner string, clock *ownedDecisionClock) {
	t.Helper()
	decisionsRaw := request("GET", "/v1/paper-decisions", nil, 200)
	outcomesRaw := request("GET", "/v1/paper-outcomes", nil, 200)
	var decisions storage.PaperDecisionCatalog
	var outcomes storage.PaperOutcomeCatalog
	if err := json.Unmarshal(decisionsRaw, &decisions); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(outcomesRaw, &outcomes); err != nil {
		t.Fatal(err)
	}
	var receipt storage.FeedArchiveReceipt
	if err := json.Unmarshal(request("POST", "/v1/feed-archives", map[string]any{"receiver_id": owner}, 201), &receipt); err != nil || receipt.PaperCustody == nil || receipt.PaperCustody.Duplicate {
		t.Fatal("paper custody receipt missing", receipt, err)
	}
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
	raw := request("GET", "/v1/feed-archives/paper-custody/1", nil, 200)
	var checkpoint storage.PaperFeedCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil || checkpoint.Validate() != nil || checkpoint.PaperFeedCheckpointHeader != receipt.PaperCustody.Header || checkpoint.Archive != receipt.Header || checkpoint.ReceiverID != owner || checkpoint.Decisions != len(decisions.Records) || checkpoint.Outcomes != len(outcomes.Records) || checkpoint.Decisions == 0 || checkpoint.Outcomes == 0 {
		t.Fatal("service custody omitted actual paper owners", checkpoint, err)
	}
	pins := map[string]storage.PaperFeedSourcePin{}
	for _, pin := range checkpoint.Records {
		pins[pin.Kind+":"+pin.ID] = pin
	}
	for _, d := range decisions.Records {
		p := pins["decision:"+d.Command.ID]
		if p.RecordSHA256 != d.SHA256 || p.BasisSHA256 != d.BasisSHA256 || !p.At.Equal(d.FinalizedAt) || p.DecisionID != d.Command.ID || p.DecisionSHA256 != d.SHA256 {
			t.Fatal("checkpoint changed decision identity", p, d)
		}
	}
	for _, o := range outcomes.Records {
		p := pins["outcome:"+o.Command.ID]
		if p.RecordSHA256 != o.SHA256 || p.BasisSHA256 != o.BasisSHA256 || !p.At.Equal(o.EvaluatedAt) || p.DecisionID != o.Command.DecisionID || p.DecisionSHA256 != o.Command.DecisionSHA256 {
			t.Fatal("checkpoint changed outcome identity", p, o)
		}
	}
	var retry storage.FeedArchiveReceipt
	if err := json.Unmarshal(request("POST", "/v1/feed-archives", map[string]any{"receiver_id": owner}, 200), &retry); err != nil || !retry.Duplicate || retry.PaperCustody == nil || !retry.PaperCustody.Duplicate || retry.PaperCustody.Header != checkpoint.PaperFeedCheckpointHeader {
		t.Fatal("service retry moved paper cut", retry, err)
	}
	if !bytes.Equal(raw, request("GET", "/v1/feed-archives/paper-custody/1", nil, 200)) || !bytes.Equal(decisionsRaw, request("GET", "/v1/paper-decisions", nil, 200)) || !bytes.Equal(outcomesRaw, request("GET", "/v1/paper-outcomes", nil, 200)) {
		t.Fatal("archive changed paper owners or historical custody")
	}
}
