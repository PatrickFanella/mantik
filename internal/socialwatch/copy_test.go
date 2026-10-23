package socialwatch

import (
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"testing"
	"time"
)

func copyActivity(kind string) *Activity {
	a := &Activity{Kind: kind, State: "declared", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "operator-assertion-v1", Side: "buy", QuantityUnits: 5, QuantityUnitClaim: "shares", PriceCents: 10000}
	switch kind {
	case "observed_order":
		a.State = "open"
	case "observed_fill":
		a.State = "reported"
		a.OrderRef = "order-1"
	case "observed_holding":
		a.State = "snapshot"
		a.Side = "long"
		a.PriceCents = 0
	}
	return a
}
func copyOriginal(at time.Time, kind string) Original {
	o := testOriginal("activity-1", "post", 1, at)
	o.Schema = CopyRecordingSchema
	o.Text = "Unverified source recording"
	o.Symbols = []string{"SPY"}
	o.Activity = copyActivity(kind)
	return o
}
func copyPolicy() PolicyCommand {
	p := testPolicy("watch-a", "account-a")
	p.Policy.Symbols = []string{"SPY"}
	p.Policy.Behavior = "bounded_paper_follow"
	p.Policy.Follow = &FollowBounds{MaxOrderNotionalCents: 10000, MaxExposureCents: 50000, StopDistanceBPS: 500, MaxHoldingSeconds: 3600}
	return p
}
func TestRecordedCopyActivityAndBounds(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	for _, kind := range []string{"claimed_trade", "observed_order", "observed_fill", "observed_holding"} {
		t.Run(kind, func(t *testing.T) {
			j, _ := New(testOwner)
			p := copyPolicy()
			j, policy, err := Append(j, Command{Policy: &p}, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			o := copyOriginal(at, kind)
			c := testCapture("copy", "watch-a", policy.Entry.Policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			j, first, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			r := first.Entry.Capture
			if !r.SourceAccepted || r.Copy == nil || r.Copy.Kind != kind || r.Copy.SourceStatus == "" || r.Authority != "recorded_copy_source_assertion_only" || r.Copy.FollowStatus != "admission_and_execution_unavailable" || r.Copy.ProviderObservationVerified || r.Copy.FinancialEffectAvailable || r.ExecutionAvailable || r.AdmissionAvailable || r.InstrumentMappingVerified || r.IndependentEvidenceCredit || r.DispatchAvailable || r.QualificationCredit {
				t.Fatal(r)
			}
			if kind == "claimed_trade" && r.Copy.SourceStatus != "source_claim_not_observed_trade" {
				t.Fatal(r)
			}
			if kind != "claimed_trade" && r.Copy.SourceStatus != "recorded_provider_observation_unverified" {
				t.Fatal(r)
			}
			_, retry, err := Append(j, Command{Capture: &c}, func() time.Time { panic("historical retry clock") })
			if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, first.Entry) {
				t.Fatal(retry, err)
			}
			c.ID = "alias"
			j, alias, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
			if err != nil || !alias.Entry.Capture.SourceDuplicate || alias.Entry.Capture.ClaimGroupSHA256 != r.ClaimGroupSHA256 {
				t.Fatal(alias, err)
			}
			// Source IDs, prose and asserted mapping labels are not independent samples.
			o.Source.PostID = "other"
			o.Text = "Different wording"
			o.Activity.MappingVersion = "alias-mapping"
			c = testCapture("overlap", "watch-a", policy.Entry.Policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			j, overlap, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
			if err != nil || overlap.Entry.Capture.ClaimGroupSHA256 != r.ClaimGroupSHA256 {
				t.Fatal(overlap, err)
			}
			o.Source.PostID = "activity-1"
			o.Kind = "edit"
			o.Revision = 2
			if kind == "observed_holding" {
				o.Activity.Side = "flat"
				o.Activity.QuantityUnits = 0
			} else if kind == "claimed_trade" {
				o.Activity.State = "retracted"
			} else if kind == "observed_order" {
				o.Activity.State = "cancelled"
			} else {
				o.Activity.State = "reversed"
			}
			c = testCapture("reversal", "watch-a", policy.Entry.Policy.SHA256, o)
			c.EvidenceClass = "recorded_copy_unqualified"
			j, rev, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
			if err != nil || !rev.Entry.Capture.SourceAccepted || rev.Entry.Capture.PolicyStatus != "source_activity_terminal_or_reversed" {
				t.Fatal(rev, err)
			}
			applicability, err := InspectApplicability(j, "copy", at)
			if err != nil || applicability.Status != "source_superseded" || applicability.ExecutionAvailable {
				t.Fatal(applicability, err)
			}
			damaged := clone(j)
			e := &damaged.Entries[len(damaged.Entries)-1]
			e.Capture.Copy.ProviderObservationVerified = true
			e.Capture.SHA256 = ""
			e.Capture.SHA256 = workerplan.Hash(e.Capture)
			e.SHA256 = ""
			e.SHA256 = workerplan.Hash(e)
			if _, err := Read(damaged); err == nil {
				t.Fatal("rehashed provider observation forgery")
			}
		})
	}
}
func TestCopyCannotPromoteClaimAndRetainsAnnouncementVersion(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	j, _ := New(testOwner)
	p := copyPolicy()
	j, policy, err := Append(j, Command{Policy: &p}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	parent := testOriginal("announcement", "post", 1, at)
	parent.Symbols = []string{"SPY"}
	c := testCapture("announcement", "watch-a", policy.Entry.Policy.SHA256, parent)
	j, announcement, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	o := copyOriginal(at, "claimed_trade")
	o.Activity.Announcement = &parent.Source
	c = testCapture("copy", "watch-a", policy.Entry.Policy.SHA256, o)
	c.EvidenceClass = "recorded_copy_unqualified"
	j, r, err := Append(j, Command{Capture: &c}, func() time.Time { return at })
	if err != nil || r.Entry.Capture.Copy.Announcement == nil || r.Entry.Capture.Copy.Announcement.CaptureSHA256 != announcement.Entry.Capture.SHA256 || r.Entry.Capture.Copy.AnnouncementCorrelationVerified {
		t.Fatal(r, err)
	}
	o.Kind = "edit"
	o.Revision = 2
	o.Activity = copyActivity("observed_fill")
	c = testCapture("promote", "watch-a", policy.Entry.Policy.SHA256, o)
	c.EvidenceClass = "recorded_copy_unqualified"
	j, r, err = Append(j, Command{Capture: &c}, func() time.Time { return at })
	if err != nil || r.Entry.Capture.SourceAccepted || r.Entry.Capture.SourceStatus != "invalid_revision_transition" {
		t.Fatal(r, err)
	}
	parent.Kind = "edit"
	parent.Revision = 2
	parent.Text = "Changed announcement"
	c = testCapture("announcement-edit", "watch-a", policy.Entry.Policy.SHA256, parent)
	j, _, err = Append(j, Command{Capture: &c}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(j)
	if err != nil || got.History[2].Capture.Copy.Announcement.CaptureSHA256 != announcement.Entry.Capture.SHA256 {
		t.Fatal(got, err)
	}
}
func TestCopyValidation(t *testing.T) {
	for name, change := range map[string]func(*Policy){
		"missing_bounds": func(p *Policy) { p.Follow = nil }, "source_sizing": func(p *Policy) { p.Follow.MaxOrderNotionalCents = 0 }, "over_exposure": func(p *Policy) { p.Follow.MaxOrderNotionalCents = p.Follow.MaxExposureCents + 1 }, "no_exit": func(p *Policy) { p.Follow.StopDistanceBPS = 0 }, "no_horizon": func(p *Policy) { p.Follow.MaxHoldingSeconds = 0 }, "research_with_follow": func(p *Policy) { p.Behavior = "research" },
	} {
		t.Run(name, func(t *testing.T) {
			p := copyPolicy()
			change(&p.Policy)
			if p.Validate() == nil {
				t.Fatal("unsafe bounds")
			}
		})
	}
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*Original){
		"negative_quantity": func(o *Original) { o.Activity.QuantityUnits = -1 }, "zero_fill": func(o *Original) { o.Activity.QuantityUnits = 0 }, "kind": func(o *Original) { o.Activity.Kind = "execution_authority" }, "missing_mapping": func(o *Original) { o.Activity.MappingVersion = "" }, "symbol_mismatch": func(o *Original) { o.Activity.Symbol = "AAPL" }, "legacy_schema": func(o *Original) { o.Schema = "mantik-social-recording-v1" }, "repost_payload": func(o *Original) { o.Kind = "repost"; r := o.Source; r.PostID = "parent"; o.RepostOf = &r },
	} {
		t.Run(name, func(t *testing.T) {
			o := copyOriginal(at, "observed_fill")
			change(&o)
			if o.Validate() == nil {
				t.Fatal("invalid activity")
			}
		})
	}
}
