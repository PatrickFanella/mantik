package socialwatch

import (
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"testing"
	"time"
)

func TestRetainedCopyResearchSelection(t *testing.T) {
	for _, kind := range []string{"claimed_trade", "observed_order", "observed_fill", "observed_holding"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicabilityFixture(t)
			o := copyOriginal(f.at, kind)
			o.Symbols = []string{"AAPL"}
			o.Activity.Symbol = "AAPL"
			o.Activity.SourceSymbol = "AAPL.US"
			c := testCapture("copy-source", "watch-a", f.policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			source := f.append(Command{Capture: &c}).Capture
			original := clone(f.j)
			selectCommand := ResearchCommand{ID: "copy-selection", ReceiverID: testOwner, CaptureID: c.ID, CaptureSHA256: source.SHA256}
			next, receipt, err := Append(f.j, Command{Research: &selectCommand}, func() time.Time { return f.at })
			if err != nil || receipt.Entry.Research == nil {
				t.Fatal(receipt, err)
			}
			r := receipt.Entry.Research.Request
			if receipt.Entry.ResearchRequestMethod != copyResearchSourceMethod || r.Schema != "mantik-owned-copy-research-request-v1" || r.CaptureID != c.ID || r.OriginalSHA256 != c.OriginalSHA256 || r.EvidenceClass != "recorded_copy_unqualified" || r.Authority != "service_owned_recorded_copy_research_request_only" || r.DispatchAvailable || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || r.IndependentEvidenceCredit {
				t.Fatal(r)
			}
			if !reflect.DeepEqual(next.Entries[:len(original.Entries)], original.Entries) {
				t.Fatal("source rewritten")
			}
			if _, err = Read(next); err != nil {
				t.Fatal(err)
			}
			alias := selectCommand
			alias.ID = "copy-selection-alias"
			next, duplicate, err := Append(next, Command{Research: &alias}, func() time.Time { return f.at })
			if err != nil || !duplicate.Entry.Research.ResearchRequestDuplicate || !reflect.DeepEqual(duplicate.Entry.Research.Request, r) {
				t.Fatal(duplicate, err)
			}
			// Different source IDs, prose and mapping labels retain the canonical request.
			o.Source.PostID = "overlap"
			o.Text = "Other words"
			o.Activity.MappingVersion = "other-mapping"
			c = testCapture("overlap", "watch-a", f.policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			f.j = next
			overlap := f.append(Command{Capture: &c}).Capture
			alias.ID = "overlap-selection"
			alias.CaptureID = c.ID
			alias.CaptureSHA256 = overlap.SHA256
			next, duplicate, err = Append(f.j, Command{Research: &alias}, func() time.Time { return f.at })
			if err != nil || !duplicate.Entry.Research.ResearchRequestDuplicate || !reflect.DeepEqual(duplicate.Entry.Research.Request, r) {
				t.Fatal(duplicate, err)
			}
			// Retraction makes old selections historical facts, not current permissions.
			f.j = next
			o.Source.PostID = "activity-1"
			o.Kind = "edit"
			o.Revision = 2
			switch kind {
			case "claimed_trade":
				o.Activity.State = "retracted"
			case "observed_order":
				o.Activity.State = "cancelled"
			case "observed_fill":
				o.Activity.State = "reversed"
			case "observed_holding":
				o.Activity.Side = "flat"
				o.Activity.QuantityUnits = 0
			}
			c = testCapture("reversal", "watch-a", f.policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			reversal := f.append(Command{Capture: &c}).Capture
			if _, retry, e := Append(f.j, Command{Research: &selectCommand}, func() time.Time { panic("historical copy research clock") }); e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, receipt.Entry) {
				t.Fatal(retry, e)
			}
			for _, bad := range []ResearchCommand{
				{ID: "superseded", ReceiverID: testOwner, CaptureID: selectCommand.CaptureID, CaptureSHA256: selectCommand.CaptureSHA256},
				{ID: "terminal", ReceiverID: testOwner, CaptureID: c.ID, CaptureSHA256: reversal.SHA256},
				alias,
			} {
				bad.ID = "new-" + bad.ID
				if _, _, e := Append(f.j, Command{Research: &bad}, func() time.Time { return f.at }); !errors.Is(e, ErrConflict) {
					t.Fatal("ineligible or invalid canonical request accepted", e)
				}
			}
			// Rehashing cannot turn a request into dispatch or swap its source method.
			for _, mutate := range []func(*Entry){func(e *Entry) { e.Research.Request.ExecutionAvailable = true }, func(e *Entry) { e.ResearchRequestMethod = researchRequestSourceMethod }} {
				bad := clone(next)
				e := &bad.Entries[len(bad.Entries)-1]
				mutate(e)
				e.Research.Request.SHA256 = ""
				e.Research.Request.SHA256 = workerplan.Hash(e.Research.Request)
				e.Research.SHA256 = ""
				e.Research.SHA256 = workerplan.Hash(*e.Research)
				e.SHA256 = ""
				e.SHA256 = workerplan.Hash(*e)
				if _, err := Read(bad); err == nil {
					t.Fatal("rehashed copy research forgery accepted")
				}
			}
		})
	}
}

func TestCopyResearchRequiresCurrentResearchWatch(t *testing.T) {
	for _, scenario := range []string{"disabled", "changed", "stale", "follow", "review", "wrong_pin", "synthetic"} {
		t.Run(scenario, func(t *testing.T) {
			f := newApplicabilityFixture(t)
			o := copyOriginal(f.at, "observed_fill")
			o.Symbols = []string{"AAPL"}
			o.Activity.Symbol = "AAPL"
			o.Activity.SourceSymbol = "AAPL.US"
			c := testCapture("copy-source", "watch-a", f.policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			if scenario == "synthetic" {
				c.EvidenceClass = "synthetic_unqualified"
			}
			source := f.append(Command{Capture: &c}).Capture
			command := ResearchCommand{ID: "select-copy", ReceiverID: testOwner, CaptureID: c.ID, CaptureSHA256: source.SHA256}
			switch scenario {
			case "disabled", "changed", "follow", "review":
				policy := f.policy.Command
				policy.ID = "changed-policy"
				policy.PreviousSHA256 = f.policy.SHA256
				switch scenario {
				case "disabled":
					policy.Policy.Enabled = false
				case "changed":
					policy.Policy.MaxObservationDelaySeconds = 30
				case "follow":
					policy.Policy.Behavior = "bounded_paper_follow"
					policy.Policy.Follow = copyPolicy().Policy.Follow
				case "review":
					policy.Policy.Behavior = "decision_review"
				}
				f.append(Command{Policy: &policy})
			case "stale":
				f.at = f.at.Add(61 * time.Second)
			case "wrong_pin":
				command.CaptureSHA256 = workerplan.Hash("wrong")
			}
			next, r, err := Append(f.j, Command{Research: &command}, func() time.Time { return f.at })
			if scenario == "synthetic" {
				if err != nil || r.Entry.Research.Request.EvidenceClass != "synthetic_unqualified" || r.Entry.Research.Request.QualificationCredit {
					t.Fatal(r, err)
				}
				return
			}
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(next, Journal{}) || !reflect.DeepEqual(r, Receipt{}) {
				t.Fatal("invalid request partially retained", r, err)
			}
		})
	}
}
