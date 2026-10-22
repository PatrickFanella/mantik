package socialwatch

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type applicabilityFixture struct {
	t      *testing.T
	j      Journal
	at     time.Time
	policy PolicyRecord
}

func newApplicabilityFixture(t *testing.T) *applicabilityFixture {
	t.Helper()
	j, _ := New(testOwner)
	f := &applicabilityFixture{t: t, j: j, at: time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)}
	c := testPolicy("watch-a", "account-a")
	e := f.append(Command{Policy: &c})
	f.policy = *e.Policy
	return f
}
func (f *applicabilityFixture) append(c Command) Entry {
	f.t.Helper()
	j, r, err := Append(f.j, c, func() time.Time { return f.at })
	if err != nil {
		f.t.Fatal(err)
	}
	f.j = j
	return r.Entry
}
func (f *applicabilityFixture) capture(id string, o Original) CaptureRecord {
	c := testCapture(id, "watch-a", f.policy.SHA256, o)
	return *f.append(Command{Capture: &c}).Capture
}
func (f *applicabilityFixture) inspect(id, want string, at time.Time) Applicability {
	f.t.Helper()
	out, err := InspectApplicability(f.j, id, at)
	if err != nil || out.Status != want {
		f.t.Fatalf("%s: %+v %v", id, out, err)
	}
	if out.DispatchAvailable || out.ExecutionAvailable || out.AdmissionAvailable || out.QualificationCredit || out.IndependentEvidenceCredit || out.ProviderAccessVerified || out.InstrumentMappingVerified {
		f.t.Fatal("source read invented authority", out)
	}
	return out
}
func TestSourceApplicabilityCutoffRevisionPolicyAndCorruption(t *testing.T) {
	f := newApplicabilityFixture(t)
	start := f.at
	first := f.capture("first", testOriginal("post", "post", 1, start))
	original := clone(f.j)
	view := f.inspect("first", "current_source_assertion_dispatch_unbound", start)
	if view.Selected.CaptureSHA256 != first.SHA256 || view.Selected.OriginalSHA256 != first.Command.OriginalSHA256 || view.Current == nil || view.Current.Revision != 1 {
		t.Fatal(view)
	}
	f.at = f.at.Add(time.Second)
	edit := f.capture("edit", testOriginal("post", "edit", 2, f.at))
	f.inspect("first", "source_superseded", f.at)
	past := f.inspect("first", "current_source_assertion_dispatch_unbound", start)
	if past.Current.Revision != 1 || past.AvailableHistorySHA256 != original.Entries[len(original.Entries)-1].SHA256 || past.HistorySHA256 == past.AvailableHistorySHA256 {
		t.Fatal(past)
	}
	f.inspect("edit", "not_available_at_cutoff", start)
	f.inspect("edit", "source_stale", f.at.Add(61*time.Second))
	f.at = f.at.Add(time.Second)
	c := f.policy.Command
	c.ID = "disable"
	c.PreviousSHA256 = f.policy.SHA256
	c.Policy.Enabled = false
	f.policy = *f.append(Command{Policy: &c}).Policy
	f.inspect("edit", "watch_policy_changed", f.at)
	disabled := f.capture("disabled", testOriginal("post-2", "post", 1, f.at))
	f.inspect(disabled.Command.ID, "watch_disabled", f.at)
	f.at = f.at.Add(time.Second)
	deletion := testOriginal("post", "delete", 3, f.at)
	deletion.Text = ""
	deletion.Symbols = []string{}
	f.capture("delete", deletion)
	f.inspect("first", "source_deleted", f.at)
	f.inspect("edit", "source_deleted", f.at)
	if _, err := InspectApplicability(f.j, "unknown", f.at); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := InspectApplicability(f.j, "first", time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	tampered := clone(f.j)
	tampered.Entries[len(tampered.Entries)-1].Capture.Source.Text = "corrupted tail"
	if _, err := InspectApplicability(tampered, "first", start); err == nil {
		t.Fatal("future corruption accepted on historical read")
	}
	if !reflect.DeepEqual(original.Entries[1].Capture, &first) || edit.Source.Revision != 2 {
		t.Fatal("inspection changed immutable history")
	}
}
func TestRepostApplicabilityPinsCausalParentAndRejectsLaterVersions(t *testing.T) {
	f := newApplicabilityFixture(t)
	start := f.at
	first := f.capture("parent", testOriginal("parent", "post", 1, start))
	f.at = f.at.Add(time.Second)
	o := testOriginal("repost", "repost", 1, f.at)
	o.RepostOf = &first.Source.Source
	repost := f.capture("repost", o)
	view := f.inspect("repost", "current_source_assertion_dispatch_unbound", f.at)
	if len(view.Lineage) != 1 || view.Lineage[0].CaptureSHA256 != first.SHA256 {
		t.Fatal(view)
	}
	f.at = f.at.Add(time.Second)
	// A nested repost retains both exact ancestors, not just their root identity.
	nested := testOriginal("nested", "repost", 1, f.at)
	nested.RepostOf = &repost.Source.Source
	f.capture("nested", nested)
	view = f.inspect("nested", "current_source_assertion_dispatch_unbound", f.at)
	if len(view.Lineage) != 2 || view.Lineage[0].CaptureSHA256 != repost.SHA256 || view.Lineage[1].CaptureSHA256 != first.SHA256 {
		t.Fatal(view)
	}
	f.at = f.at.Add(time.Second)
	f.capture("parent-edit", testOriginal("parent", "edit", 2, f.at))
	f.inspect("repost", "repost_parent_changed", f.at)
	f.inspect("nested", "repost_parent_changed", f.at)
	// Even if the journal resolved an unversioned parent at capture time, the
	// view refuses a parent whose selected revision occurred after the repost.
	late := testOriginal("backdated-repost", "repost", 1, start.Add(time.Second))
	late.RepostOf = &first.Source.Source
	f.capture("backdated", late)
	f.inspect("backdated", "repost_parent_after_occurrence", f.at)
	f.at = f.at.Add(time.Second)
	// Edits cannot erase a repost's lineage or its unresolved chronology.
	f.capture("edited-backdated", testOriginal("backdated-repost", "edit", 2, f.at))
	f.inspect("edited-backdated", "repost_parent_after_occurrence", f.at)
	f.capture("edited-repost", testOriginal("repost", "edit", 2, f.at))
	f.inspect("edited-repost", "repost_parent_changed", f.at)
	f.at = f.at.Add(time.Second)
	deletion := testOriginal("parent", "delete", 3, f.at)
	deletion.Text = ""
	deletion.Symbols = []string{}
	f.capture("parent-delete", deletion)
	f.inspect("edited-repost", "repost_parent_deleted", f.at)
	f.inspect("nested", "repost_parent_changed", f.at) // direct parent was edited first
	// A historical read stays pinned to the original, valid parent.
	view = f.inspect("repost", "current_source_assertion_dispatch_unbound", start.Add(time.Second))
	if len(view.Lineage) != 1 || view.Lineage[0].Revision != 1 {
		t.Fatal(view)
	}
}
func TestSourceApplicabilityUnacceptedAndUnsupported(t *testing.T) {
	f := newApplicabilityFixture(t)
	o := testOriginal("unresolved", "edit", 2, f.at)
	f.capture("missing", o)
	f.inspect("missing", "unresolved_revision", f.at)
	o = testOriginal("forecast", "forecast", 1, f.at)
	f.capture("forecast", o)
	f.inspect("forecast", "structured_extraction_unavailable", f.at)
	o = testOriginal("original", "post", 1, f.at)
	f.capture("original", o)
	f.capture("duplicate", o)
	f.inspect("duplicate", "duplicate_source_revision", f.at)
	o.Text = "conflicting revision"
	f.capture("conflict", o)
	f.inspect("conflict", "source_revision_conflict", f.at)
	// Prior unsupported receipts are never promoted by later source recovery.
	o = testOriginal("unresolved", "post", 1, f.at)
	f.capture("recovered", o)
	f.inspect("missing", "unresolved_revision", f.at)
}

func TestRepostApplicabilityKeepsEvidenceClassesSeparate(t *testing.T) {
	f := newApplicabilityFixture(t)
	parent := testOriginal("synthetic-parent", "post", 1, f.at)
	cmd := testCapture("synthetic-parent", "watch-a", f.policy.SHA256, parent)
	cmd.EvidenceClass = "synthetic_unqualified"
	f.append(Command{Capture: &cmd})
	f.at = f.at.Add(time.Second)
	o := testOriginal("recorded-child", "repost", 1, f.at)
	o.RepostOf = &parent.Source
	f.capture("child", o)
	out := f.inspect("child", "repost_evidence_class_mismatch", f.at)
	if len(out.Lineage) != 1 || out.Lineage[0].CaptureID != "synthetic-parent" {
		t.Fatal(out)
	}
}
