package socialwatch

import (
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"testing"
	"time"
)

func TestResearchRequestGatesAndReplay(t *testing.T) {
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	j, _ := New(testOwner)
	policy := testPolicy("watch-a", "account-a")
	j, registered, err := Append(j, Command{Policy: &policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	original := testOriginal("post", "post", 1, at)
	c := testCapture("research", "watch-a", registered.Entry.Policy.SHA256, original)
	c.RequestResearch = true
	next, first, err := Append(j, Command{Capture: &c}, now)
	if err != nil || first.Entry.Capture.ResearchRequest == nil {
		t.Fatal(first, err)
	}
	// A synthetic request cannot suppress recorded research or become its source.
	synthetic := c
	synthetic.ID = "synthetic-research"
	synthetic.EvidenceClass = "synthetic_unqualified"
	separate := original
	separate.Source.PostID = "synthetic-post"
	separateRaw, _ := json.Marshal(separate)
	synthetic.Original = string(separateRaw)
	synthetic.OriginalSHA256 = workerplan.Digest(separateRaw)
	withSynthetic, separateReceipt, e := Append(next, Command{Capture: &synthetic}, now)
	if e != nil || separateReceipt.Entry.Capture.ResearchRequestDuplicate || separateReceipt.Entry.Capture.ResearchRequest.ID == first.Entry.Capture.ResearchRequest.ID {
		t.Fatal("synthetic/recorded research conflated", separateReceipt, e)
	}
	separateCatalog, e := Read(withSynthetic)
	if e != nil || len(separateCatalog.ResearchRequests) != 2 {
		t.Fatal(separateCatalog, e)
	}
	for _, change := range []func(*CaptureCommand){
		func(c *CaptureCommand) { c.PolicySHA256 = workerplan.Hash("wrong") },
		func(c *CaptureCommand) {
			o, _ := c.Decode()
			o.Kind = "forecast"
			raw, _ := json.Marshal(o)
			c.Original = string(raw)
			c.OriginalSHA256 = workerplan.Digest(raw)
		},
		func(c *CaptureCommand) {
			o, _ := c.Decode()
			o.Symbols = []string{"MSFT"}
			raw, _ := json.Marshal(o)
			c.Original = string(raw)
			c.OriginalSHA256 = workerplan.Digest(raw)
		},
		func(c *CaptureCommand) {
			o, _ := c.Decode()
			o.OccurredAt = at.Add(-61 * time.Second)
			raw, _ := json.Marshal(o)
			c.Original = string(raw)
			c.OriginalSHA256 = workerplan.Digest(raw)
		},
	} {
		bad := clone(c)
		change(&bad)
		out, receipt, e := Append(j, Command{Capture: &bad}, now)
		if e == nil || !reflect.DeepEqual(out, Journal{}) || !reflect.DeepEqual(receipt, Receipt{}) {
			t.Fatal("invalid research changed source", out, receipt, e)
		}
	}
	// Portable rehashing cannot invent an authoritative or differently sourced request.
	for _, change := range []func(*ResearchRequest){func(r *ResearchRequest) { r.ExecutionAvailable = true }, func(r *ResearchRequest) { r.PolicySHA256 = workerplan.Hash("other") }, func(r *ResearchRequest) { r.CaptureID = "other" }, func(r *ResearchRequest) { r.ClaimGroupSHA256 = workerplan.Hash("other") }} {
		bad := clone(next)
		record := bad.Entries[1].Capture
		change(record.ResearchRequest)
		record.ResearchRequest.SHA256 = ""
		record.ResearchRequest.SHA256 = workerplan.Hash(*record.ResearchRequest)
		record.SHA256 = ""
		record.SHA256 = workerplan.Hash(*record)
		bad.Entries[1].SHA256 = ""
		bad.Entries[1].SHA256 = workerplan.Hash(bad.Entries[1])
		if _, e := Read(bad); e == nil {
			t.Fatal("rehashed research request accepted")
		}
	}
	// Without explicit opt-in, historical wire bytes gain no request fields.
	c.RequestResearch = false
	old, receipt, e := Append(j, Command{Capture: &c}, now)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(receipt.Entry)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var capture map[string]json.RawMessage
	_ = json.Unmarshal(fields["capture"], &capture)
	if capture["research_request"] != nil {
		t.Fatal("old capture acquired request")
	}
	if _, _, e = Append(old, Command{Capture: &c}, func() time.Time { panic("old retry clock") }); e != nil {
		t.Fatal(e)
	}
	different := c
	different.RequestResearch = true
	if _, _, e = Append(old, Command{Capture: &different}, now); !errors.Is(e, ErrConflict) {
		t.Fatal("retry changed research opt-in", e)
	}
}
