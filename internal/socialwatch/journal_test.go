package socialwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testOwner = "receiver-abcdefghijklmnop"

func testPolicy(id, account string) PolicyCommand {
	return PolicyCommand{ID: "create-" + id, ReceiverID: testOwner, Policy: Policy{ID: id, Provider: "recorded", AccountID: account, Symbols: []string{"AAPL"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
}
func testOriginal(post, kind string, revision int64, at time.Time) Original {
	return Original{Schema: "mantik-social-recording-v1", Source: SourceRef{Provider: "recorded", AccountID: "account-a", PostID: post}, Revision: revision, Kind: kind, OccurredAt: at, ObservedAt: at, Text: "AAPL revenue claim", Symbols: []string{"AAPL"}}
}
func testCapture(id, watch, policy string, o Original) CaptureCommand {
	raw, _ := json.Marshal(o)
	return CaptureCommand{ID: id, ReceiverID: testOwner, WatchID: watch, PolicySHA256: policy, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
}
func TestAttributedSocialHistory(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	j, _ := New(testOwner)
	appendCommand := func(c Command) Entry {
		t.Helper()
		next, r, err := Append(j, c, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		j = next
		return r.Entry
	}
	pc := testPolicy("watch-a", "account-a")
	policy := appendCommand(Command{Policy: &pc}).Policy
	capture := func(id string, o Original) CaptureRecord {
		t.Helper()
		c := testCapture(id, "watch-a", policy.SHA256, o)
		r := appendCommand(Command{Capture: &c}).Capture
		if r.ExecutionAvailable || r.AdmissionAvailable || r.QualificationCredit || r.IndependentEvidenceCredit || r.ProviderAccessVerified || r.InstrumentMappingVerified || r.DispatchAvailable {
			t.Fatal(r)
		}
		return *r
	}
	o := testOriginal("post-1", "post", 1, at)
	first := capture("first", o)
	if !first.SourceAccepted || first.PolicyStatus != "dispatch_unbound" || first.Root != o.Source {
		t.Fatal(first)
	}
	// Capture bytes and observation assertions may differ while one source
	// revision remains one piece of attributed source material.
	at = at.Add(time.Second)
	o.ObservedAt = at
	duplicate := capture("repeat", o)
	if !duplicate.SourceDuplicate || duplicate.SourceAccepted || duplicate.ClaimGroupSHA256 != first.ClaimGroupSHA256 || duplicate.Command.Original == first.Command.Original {
		t.Fatal(duplicate)
	}
	fork := o
	fork.Text = "contradictory text"
	conflict := capture("fork", fork)
	if conflict.SourceStatus != "source_revision_conflict" || conflict.SourceAccepted {
		t.Fatal(conflict)
	}
	// Repeated claims across source IDs share an exact normalized grouping key,
	// but none receive independent-evidence credit.
	repeat := testOriginal("post-2", "post", 1, at)
	repeat.Text = " AAPL   REVENUE claim "
	grouped := capture("same-claim", repeat)
	if grouped.ClaimGroupSHA256 != first.ClaimGroupSHA256 || grouped.Root == first.Root {
		t.Fatal(grouped)
	}
	repost := testOriginal("repost", "repost", 1, at)
	repost.RepostOf = &first.Source.Source
	shared := capture("repost", repost)
	if !shared.SourceAccepted || shared.Root != first.Root || shared.ClaimGroupSHA256 != first.ClaimGroupSHA256 {
		t.Fatal(shared)
	}
	unknown := SourceRef{Provider: "recorded", AccountID: "account-a", PostID: "not-yet"}
	repost.Source.PostID = "unresolved"
	repost.RepostOf = &unknown
	r := capture("unresolved", repost)
	if r.SourceStatus != "unresolved_repost" || r.SourceAccepted {
		t.Fatal(r)
	}
	parent := testOriginal("not-yet", "post", 1, at)
	capture("parent", parent)
	resolved := capture("resolved", repost)
	if !resolved.SourceAccepted || resolved.Root != parent.Source {
		t.Fatal(resolved)
	}
	edit := testOriginal("post-1", "edit", 2, at)
	edit.Text = "revised assertion"
	edited := capture("edit", edit)
	if !edited.SourceAccepted || edited.Root != first.Root || edited.ClaimGroupSHA256 == first.ClaimGroupSHA256 {
		t.Fatal(edited)
	}
	deletion := testOriginal("post-1", "delete", 3, at)
	deletion.Text = ""
	deletion.Symbols = []string{}
	deleted := capture("delete", deletion)
	if !deleted.SourceAccepted || deleted.PolicyStatus != "source_deleted" || deleted.ClaimGroupSHA256 != edited.ClaimGroupSHA256 {
		t.Fatal(deleted)
	}
	edit.Revision = 4
	afterDelete := capture("after-delete", edit)
	if afterDelete.SourceAccepted || afterDelete.SourceStatus != "invalid_revision_transition" {
		t.Fatal(afterDelete)
	}
	repost.Source.PostID = "deleted-repost"
	repost.RepostOf = &first.Source.Source
	if r := capture("deleted-repost", repost); r.SourceAccepted {
		t.Fatal(r)
	}
	missing := testOriginal("missing", "edit", 2, at)
	if r := capture("missing-revision", missing); r.SourceStatus != "unresolved_revision" {
		t.Fatal(r)
	}
	back := testOriginal("post-2", "edit", 2, at.Add(-time.Hour))
	if r := capture("backdated-edit", back); r.SourceAccepted || r.SourceStatus != "invalid_revision_transition" {
		t.Fatal(r)
	}
	// Server availability is part of reliability. A late import cannot repair
	// its delay by asserting a prompt source-side observation timestamp.
	stale := testOriginal("stale", "post", 1, at.Add(-time.Hour))
	if r := capture("stale", stale); r.PolicyStatus != "observation_delay_exceeded" || r.ObservationDelaySeconds != 0 || r.AvailabilityDelaySeconds != 3600 {
		t.Fatal(r)
	}
	unmapped := testOriginal("other-symbol", "post", 1, at)
	unmapped.Symbols = []string{"MSFT"}
	if r := capture("unmapped", unmapped); r.PolicyStatus != "instrument_not_watched_or_unresolved" {
		t.Fatal(r)
	}
	for _, kind := range []string{"forecast", "declared_position"} {
		if r := capture(kind, testOriginal(kind, kind, 1, at)); r.PolicyStatus != "structured_extraction_unavailable" {
			t.Fatal(r)
		}
	}
	update := pc
	update.ID = "disable"
	update.PreviousSHA256 = policy.SHA256
	update.Policy.Enabled = false
	oldPolicy := policy
	policy = appendCommand(Command{Policy: &update}).Policy
	if policy.Version != 2 {
		t.Fatal(policy)
	}
	if r := capture("disabled", testOriginal("disabled", "post", 1, at)); r.PolicyStatus != "watch_disabled" {
		t.Fatal(r)
	}
	stalePolicy := testCapture("old-policy", "watch-a", oldPolicy.SHA256, testOriginal("late", "post", 1, at))
	if _, _, err := Append(j, Command{Capture: &stalePolicy}, func() time.Time { return at }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	all, err := Read(j)
	if err != nil || len(all.Watches) != 1 || all.Watches[0].ConnectorStatus != "connector_not_configured" || all.Watches[0].ConnectorScope.AccountID != "account-a" {
		t.Fatal(all, err)
	}
	if all.History[1].Capture.Command.Original != first.Command.Original {
		t.Fatal("original overwritten")
	}
	// Mutation of a returned catalogue cannot change journal custody.
	all.History[1].Capture.Source.Text = "changed"
	again, err := Read(j)
	if err != nil || again.History[1].Capture.Source.Text != "AAPL revenue claim" {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(j)
	var restored Journal
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("decode")
	}
	roundtrip, err := Read(restored)
	expected, _ := Read(j)
	if err != nil || !reflect.DeepEqual(roundtrip, expected) {
		t.Fatal(err)
	}
	// Exact retry after policy change remains clock-free and historical.
	_, retry, err := Append(j, j.Entries[1].Command, func() time.Time { panic("retry clock") })
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, j.Entries[1]) {
		t.Fatal(retry, err)
	}
	damaged := clone(j)
	damaged.Entries[1].Capture.QualificationCredit = true
	damaged.Entries[1].Capture.SHA256 = ""
	damaged.Entries[1].Capture.SHA256 = workerplan.Hash(damaged.Entries[1].Capture)
	damaged.Entries[1].SHA256 = ""
	damaged.Entries[1].SHA256 = workerplan.Hash(damaged.Entries[1])
	if _, err := Read(damaged); err == nil {
		t.Fatal("self-hashed qualification forgery")
	}
	if _, _, err := Append(damaged, j.Entries[0].Command, nil); err == nil {
		t.Fatal("retry hid corrupt prefix")
	}
}
func TestSocialPolicyAndRecordingGuards(t *testing.T) {
	at := time.Now().UTC()
	base := testPolicy("watch", "account-a")
	for _, change := range []func(*PolicyCommand){func(c *PolicyCommand) { c.Policy.Symbols = nil }, func(c *PolicyCommand) { c.Policy.Symbols = []string{"MSFT", "AAPL"} }, func(c *PolicyCommand) { c.Policy.Symbols = []string{"AAPL", "AAPL"} }, func(c *PolicyCommand) { c.Policy.MaxObservationDelaySeconds = 0 }, func(c *PolicyCommand) { c.Policy.Provider = "Bad Provider" }, func(c *PolicyCommand) { c.Policy.AccountID = " with spaces" }, func(c *PolicyCommand) { c.Policy.Behavior = "trade" }, func(c *PolicyCommand) { c.Policy.RuleID = "unexpected" }, func(c *PolicyCommand) { c.PreviousSHA256 = "bad" }, func(c *PolicyCommand) { c.ReceiverID = "other" }} {
		c := clone(base)
		change(&c)
		if c.Validate() == nil {
			t.Fatal(c)
		}
	}
	rule := clone(base)
	rule.Policy.Behavior = "configured_rule"
	if rule.Validate() == nil {
		t.Fatal("missing rule")
	}
	rule.Policy.RuleID = "rule-v1"
	if rule.Validate() != nil {
		t.Fatal("valid rule")
	}
	j, _ := New(testOwner)
	j, r, err := Append(j, Command{Policy: &base}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	policy := r.Entry.Policy
	copyPolicy := testPolicy("other-watch", "account-a")
	if _, _, err := Append(j, Command{Policy: &copyPolicy}, func() time.Time { return at }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	rebind := base
	rebind.ID = "rebind"
	rebind.PreviousSHA256 = policy.SHA256
	rebind.Policy.AccountID = "different"
	if _, _, err := Append(j, Command{Policy: &rebind}, func() time.Time { return at }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	original := testOriginal("post", "post", 1, at)
	capture := testCapture("capture", "watch", policy.SHA256, original)
	for _, change := range []func(*CaptureCommand){func(c *CaptureCommand) { c.EvidenceClass = "prospective_qualified" }, func(c *CaptureCommand) { c.OriginalSHA256 = strings.Repeat("0", 64) }, func(c *CaptureCommand) { c.Original += strings.Repeat(" ", MaxOriginalBytes) }, func(c *CaptureCommand) {
		c.Original = `{"unknown":true}`
		c.OriginalSHA256 = workerplan.Digest([]byte(c.Original))
	}} {
		c := capture
		change(&c)
		if _, err := c.Decode(); err == nil {
			t.Fatal(c)
		}
	}
	for _, change := range []func(*Original){func(o *Original) { o.Revision = 0 }, func(o *Original) { o.ObservedAt = at.Add(-time.Second) }, func(o *Original) { o.Kind = "observed_fill" }, func(o *Original) { o.Text = "" }, func(o *Original) { o.Symbols = nil }, func(o *Original) { o.Kind = "delete" }, func(o *Original) { o.Kind = "repost" }, func(o *Original) { o.RepostOf = &o.Source }} {
		o := clone(original)
		change(&o)
		if o.Validate() == nil {
			t.Fatal(o)
		}
	}
	future := testCapture("future", "watch", policy.SHA256, testOriginal("future", "post", 1, at.Add(time.Second)))
	if _, _, err := Append(j, Command{Capture: &future}, func() time.Time { return at }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	unknownWatch := capture
	unknownWatch.WatchID = "unknown"
	if _, _, err := Append(j, Command{Capture: &unknownWatch}, func() time.Time { return at }); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	wrong := capture
	wrong.ReceiverID = "receiver-qrstuvwxyzabcdef"
	if _, _, err := Append(j, Command{Capture: &wrong}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, _, err := Append(j, Command{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, _, err := Append(j, Command{Policy: &base, Capture: &capture}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, _, err := Append(j, Command{Capture: &capture}, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, _, err := Append(j, Command{Capture: &capture}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for i := 1; i < MaxEntries; i++ {
		c := base
		c.ID = fmt.Sprintf("version-%03d", i)
		c.PreviousSHA256 = j.Entries[len(j.Entries)-1].Policy.SHA256
		var e error
		j, _, e = Append(j, Command{Policy: &c}, func() time.Time { return at })
		if e != nil {
			t.Fatal(i, e)
		}
	}
	overflow := base
	overflow.ID = "overflow"
	overflow.PreviousSHA256 = j.Entries[len(j.Entries)-1].Policy.SHA256
	if _, _, err := Append(j, Command{Policy: &overflow}, nil); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, r, err := Append(j, j.Entries[0].Command, nil); err != nil || !r.Duplicate {
		t.Fatal(r, err)
	}
}

func TestSocialCrossAccountRepostAndWatchCapacity(t *testing.T) {
	at := time.Now().UTC()
	j, _ := New(testOwner)
	appendCommand := func(c Command) Entry {
		t.Helper()
		next, r, err := Append(j, c, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		j = next
		return r.Entry
	}
	a := testPolicy("a", "account-a")
	pa := appendCommand(Command{Policy: &a}).Policy
	b := testPolicy("b", "account-b")
	pb := appendCommand(Command{Policy: &b}).Policy
	original := testOriginal("source", "post", 1, at)
	ca := testCapture("source", "a", pa.SHA256, original)
	root := appendCommand(Command{Capture: &ca}).Capture
	repost := testOriginal("copy", "repost", 1, at)
	repost.Source.AccountID = "account-b"
	repost.RepostOf = &original.Source
	cb := testCapture("copy", "b", pb.SHA256, repost)
	child := appendCommand(Command{Capture: &cb}).Capture
	if !child.SourceAccepted || child.Root != root.Root || child.ClaimGroupSHA256 != root.ClaimGroupSHA256 || child.IndependentEvidenceCredit {
		t.Fatal(child)
	}
	for i := 2; i < MaxWatches; i++ {
		c := testPolicy(fmt.Sprintf("watch-%02d", i), fmt.Sprintf("account-%02d", i))
		appendCommand(Command{Policy: &c})
	}
	extra := testPolicy("too-many", "new-account")
	if _, _, err := Append(j, Command{Policy: &extra}, func() time.Time { return at }); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, r, err := Append(j, Command{Policy: &a}, nil); err != nil || !r.Duplicate {
		t.Fatal(r, err)
	}
}
