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

func TestResearchRequestRequiresCurrentCausalSource(t *testing.T) {
	for _, scenario := range []string{"evidence_class", "future_parent", "changed_ancestor", "deleted_ancestor", "edited_fault"} {
		t.Run(scenario, func(t *testing.T) {
			f := newApplicabilityFixture(t)
			start := f.at
			parent := testOriginal("parent", "post", 1, start)
			cmd := testCapture("parent", "watch-a", f.policy.SHA256, parent)
			if scenario == "evidence_class" || scenario == "edited_fault" {
				cmd.EvidenceClass = "synthetic_unqualified"
			}
			f.append(Command{Capture: &cmd})
			f.at = start.Add(time.Second)
			child := testOriginal("child", "repost", 1, f.at)
			child.RepostOf = &parent.Source
			if scenario == "future_parent" {
				child.OccurredAt = start.Add(-time.Second)
			}
			if scenario == "changed_ancestor" || scenario == "deleted_ancestor" || scenario == "edited_fault" {
				f.capture("child", child)
				f.at = start.Add(2 * time.Second)
				if scenario == "edited_fault" {
					child = testOriginal("child", "edit", 2, f.at)
				} else {
					edit := testOriginal("parent", "edit", 2, f.at)
					if scenario == "deleted_ancestor" {
						edit.Kind = "delete"
						edit.Text = ""
						edit.Symbols = []string{}
					}
					f.capture("parent-update", edit)
					child = testOriginal("nested", "repost", 1, f.at)
					child.RepostOf = &SourceRef{Provider: parent.Source.Provider, AccountID: parent.Source.AccountID, PostID: "child"}
				}
			}
			c := testCapture("request", "watch-a", f.policy.SHA256, child)
			c.RequestResearch = true
			out, receipt, err := Append(f.j, Command{Capture: &c}, func() time.Time { return f.at })
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(out, Journal{}) || !reflect.DeepEqual(receipt, Receipt{}) {
				t.Fatalf("inapplicable source queued: %+v %v", receipt, err)
			}
			retained := c
			retained.RequestResearch = false
			retained.ID = "retained-invalid-source"
			journal, source, e := Append(f.j, Command{Capture: &retained}, func() time.Time { return f.at })
			if e != nil {
				t.Fatal(e)
			}
			selected := ResearchCommand{ID: "select-invalid-source", ReceiverID: testOwner, CaptureID: retained.ID, CaptureSHA256: source.Entry.Capture.SHA256}
			out, receipt, e = Append(journal, Command{Research: &selected}, func() time.Time { return f.at })
			if !errors.Is(e, ErrConflict) || !reflect.DeepEqual(out, Journal{}) || !reflect.DeepEqual(receipt, Receipt{}) {
				t.Fatal("retained invalid source selected", receipt, e)
			}

		})
	}
}

func TestResearchRequestLegacyReplayAndCanonicalApplicability(t *testing.T) {
	f := newApplicabilityFixture(t)
	parent := testOriginal("parent", "post", 1, f.at)
	c := testCapture("parent", "watch-a", f.policy.SHA256, parent)
	c.EvidenceClass = "synthetic_unqualified"
	f.append(Command{Capture: &c})
	f.at = f.at.Add(time.Second)
	child := testOriginal("child", "repost", 1, f.at)
	child.RepostOf = &parent.Source
	c = testCapture("legacy-request", "watch-a", f.policy.SHA256, child)
	c.RequestResearch = true
	// The original writer had no applicability method marker. Preserve that exact
	// historical receipt, including its unsafe source, without creating new work.
	p, err := replay(f.j)
	if err != nil {
		t.Fatal(err)
	}
	e, err := p.apply(testOwner, Command{Capture: &c}, f.at, len(f.j.Entries)+1, "")
	if err != nil {
		t.Fatal(err)
	}
	f.j.Entries = append(f.j.Entries, e)
	if _, err = Read(f.j); err != nil {
		t.Fatal("legacy receipt no longer replayable", err)
	}
	if _, r, err := Append(f.j, Command{Capture: &c}, func() time.Time { panic("legacy retry clock") }); err != nil || !r.Duplicate || !reflect.DeepEqual(r.Entry, e) {
		t.Fatal("legacy retry changed", err)
	}
	// A fresh direct source cannot recycle the invalid canonical request.
	o := testOriginal("fresh", "post", 1, f.at)
	fresh := testCapture("fresh", "watch-a", f.policy.SHA256, o)
	fresh.RequestResearch = true
	if _, _, err = Append(f.j, Command{Capture: &fresh}, func() time.Time { return f.at }); !errors.Is(err, ErrConflict) {
		t.Fatal("invalid canonical source reused", err)
	}
	// Tagging a historical invalid receipt with the new method and rehashing it
	// must not make it a valid source-bound issuance.
	bad := clone(f.j)
	last := &bad.Entries[len(bad.Entries)-1]
	last.ResearchRequestMethod = researchRequestSourceMethod
	last.SHA256 = ""
	last.SHA256 = workerplan.Hash(*last)
	if _, err = Read(bad); err == nil {
		t.Fatal("forged applicability method accepted")
	}
}

func TestResearchRequestCausalRepostAndImmutableRetry(t *testing.T) {
	f := newApplicabilityFixture(t)
	parent := testOriginal("parent", "post", 1, f.at)
	f.capture("parent", parent)
	f.at = f.at.Add(time.Second)
	child := testOriginal("child", "repost", 1, f.at)
	child.RepostOf = &parent.Source
	f.capture("child", child)
	f.at = f.at.Add(time.Second)
	nested := testOriginal("nested", "repost", 1, f.at)
	nested.RepostOf = &child.Source
	c := testCapture("nested-request", "watch-a", f.policy.SHA256, nested)
	c.RequestResearch = true
	j, r, err := Append(f.j, Command{Capture: &c}, func() time.Time { return f.at })
	if err != nil || r.Entry.ResearchRequestMethod != researchRequestSourceMethod || r.Entry.Capture.ResearchRequest == nil {
		t.Fatal(r, err)
	}
	f.j = j
	if _, err = Read(j); err != nil {
		t.Fatal(err)
	}
	f.at = f.at.Add(time.Second)
	f.capture("parent-edit", testOriginal("parent", "edit", 2, f.at))
	if _, retry, err := Append(f.j, Command{Capture: &c}, func() time.Time { panic("source retry clock") }); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, r.Entry) {
		t.Fatal("later edit changed immutable retry", err)
	}
}

func TestRetainedSocialResearchSelection(t *testing.T) {
	f := newApplicabilityFixture(t)
	captured := f.capture("retained", testOriginal("post", "post", 1, f.at))
	original := clone(f.j)
	c := ResearchCommand{ID: "select-research", ReceiverID: testOwner, CaptureID: captured.Command.ID, CaptureSHA256: captured.SHA256}
	f.at = f.at.Add(time.Second)
	next, r, err := Append(f.j, Command{Research: &c}, func() time.Time { return f.at })
	if err != nil || r.Entry.Research == nil || r.Entry.Capture != nil || r.Entry.Research.Request.CaptureID != captured.Command.ID || r.Entry.Research.ResearchRequestDuplicate || !reflect.DeepEqual(next.Entries[1], original.Entries[1]) {
		t.Fatal(r, err)
	}
	request := r.Entry.Research.Request
	if request.DispatchAvailable || request.AdmissionAvailable || request.ExecutionAvailable || request.QualificationCredit || !request.RequestedAt.Equal(f.at) {
		t.Fatal(request)
	}
	catalog, err := Read(next)
	if err != nil || len(catalog.ResearchRequests) != 1 {
		t.Fatal(catalog, err)
	}
	// New command identity reuses the same research request, without rerecording.
	alias := c
	alias.ID = "select-again"
	again, duplicate, err := Append(next, Command{Research: &alias}, func() time.Time { return f.at })
	if err != nil || !duplicate.Entry.Research.ResearchRequestDuplicate || !reflect.DeepEqual(duplicate.Entry.Research.Request, request) {
		t.Fatal(duplicate, err)
	}
	if _, err = InspectApplicability(again, captured.Command.ID, f.at); err != nil {
		t.Fatal("request entry broke source inspection", err)
	}
	for _, change := range []func(*ResearchCommand){func(c *ResearchCommand) { c.CaptureID = "absent" }, func(c *ResearchCommand) { c.CaptureSHA256 = workerplan.Hash("wrong") }, func(c *ResearchCommand) { c.ReceiverID = "receiver-otherowner" }} {
		bad := c
		bad.ID = "bad-selection"
		change(&bad)
		out, got, e := Append(next, Command{Research: &bad}, func() time.Time { return f.at })
		if e == nil || !reflect.DeepEqual(out, Journal{}) || !reflect.DeepEqual(got, Receipt{}) {
			t.Fatal("invalid selection retained", got, e)
		}
	}
	// Source or watch changes cannot authorize a new request; the original exact
	// retry remains a fact and never reads the current clock.
	f.j = again
	f.at = f.at.Add(time.Second)
	f.capture("edit", testOriginal("post", "edit", 2, f.at))
	if _, got, e := Append(f.j, Command{Research: &c}, func() time.Time { panic("selection retry clock") }); e != nil || !got.Duplicate || !reflect.DeepEqual(got.Entry, r.Entry) {
		t.Fatal(got, e)
	}
	alias.ID = "select-superseded"
	if _, _, e := Append(f.j, Command{Research: &alias}, func() time.Time { return f.at }); !errors.Is(e, ErrConflict) {
		t.Fatal("superseded source selected", e)
	}
	bad := clone(next)
	last := &bad.Entries[len(bad.Entries)-1]
	last.Research.Request.ExecutionAvailable = true
	last.Research.Request.SHA256 = ""
	last.Research.Request.SHA256 = workerplan.Hash(last.Research.Request)
	last.Research.SHA256 = ""
	last.Research.SHA256 = workerplan.Hash(last.Research)
	last.SHA256 = ""
	last.SHA256 = workerplan.Hash(*last)
	if _, e := Read(bad); e == nil {
		t.Fatal("rehashed authoritative selection accepted")
	}
}
