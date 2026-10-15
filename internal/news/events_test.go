package news

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestEventGroupingRevisionsAndCutoffs(t *testing.T) {
	j, _ := testJournal(t)
	first := Fixture()
	receipt, err := j.Retain(first)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := first.ReceivedAt
	initial, err := j.Events(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Groups) != 1 || initial.Groups[0].Claims[0].State != "current" || initial.Groups[0].RequiresReview {
		t.Fatal(initial)
	}
	// A separately attributed syndicated copy shares the event, never independence.
	copy := Fixture()
	copy.SourceID = "distribution"
	copy.ArticleID = "copy"
	copy.ReceivedAt = cutoff.Add(time.Second)
	if _, err = j.Retain(copy); err != nil {
		t.Fatal(err)
	}
	// Identical event keys in different evidence classes cannot mix.
	separate := Fixture()
	separate.EvidenceClass = "recorded_market_unqualified"
	separate.ReceivedAt = copy.ReceivedAt
	if _, err = j.Retain(separate); err != nil {
		t.Fatal(err)
	}
	revised := successor(Fixture(), receipt, "correction")
	revised.AvailableAt = cutoff.Add(2 * time.Second)
	revised.ReceivedAt = revised.AvailableAt
	revised.Claims[0].EventKey = "corrected-event"
	second, err := j.Retain(revised)
	if err != nil {
		t.Fatal(err)
	}
	rawCopy, _ := json.Marshal(revised)
	var revisionCopy Recording
	_ = json.Unmarshal(rawCopy, &revisionCopy)
	retracted := successor(revisionCopy, second, "retraction")
	retracted.AvailableAt = cutoff.Add(3 * time.Second)
	retracted.ReceivedAt = retracted.AvailableAt
	if _, err = j.Retain(retracted); err != nil {
		t.Fatal(err)
	}
	before, err := j.Events(cutoff)
	if err != nil || !reflect.DeepEqual(before, initial) {
		t.Fatal("later knowledge leaked into receipt cutoff", err)
	}
	grouped, err := j.Events(retracted.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped.Groups) != 3 || grouped.Independence != "not_assessed" {
		t.Fatal(grouped)
	}
	var old, new, other *EventGroup
	for i := range grouped.Groups {
		g := &grouped.Groups[i]
		switch {
		case g.EvidenceClass == separate.EvidenceClass:
			other = g
		case g.EventKey == first.Claims[0].EventKey:
			old = g
		default:
			new = g
		}
	}
	if old == nil || new == nil || other == nil || !old.RequiresReview || !new.RequiresReview || other.RequiresReview || len(old.Claims) != 2 || len(old.Changes) != 1 || len(new.Changes) != 2 {
		t.Fatal(grouped)
	}
	states := map[string]string{}
	for _, c := range old.Claims {
		states[c.Claim.SourceID] = c.State
		if c.Claim.Independence != "not_assessed" || c.Reference == "" {
			t.Fatal(c)
		}
	}
	if states[first.SourceID] != "superseded" || states[copy.SourceID] != "current" {
		t.Fatal(states)
	}
	states = map[string]string{}
	for _, c := range new.Claims {
		states[c.Claim.Assessment] = c.State
	}
	if states["reported_unverified"] != "superseded" || states["retracted"] != "retracted" {
		t.Fatal(states)
	}
	for i := 0; i < 10; i++ {
		again, err := j.Events(retracted.ReceivedAt)
		if err != nil || !reflect.DeepEqual(again, grouped) {
			t.Fatal("nondeterministic projection", err)
		}
	}
	if _, err = j.Retain(retracted); err != nil {
		t.Fatal(err)
	}
	again, _ := j.Events(retracted.ReceivedAt)
	if !reflect.DeepEqual(again, grouped) {
		t.Fatal("retry duplicated grouping or notice")
	}
	// Returned claims are detached copies, not journal state.
	grouped.Groups[0].Claims[0].Claim.Entities[0].Name = "changed"
	again, _ = j.Events(retracted.ReceivedAt)
	if reflect.DeepEqual(again, grouped) {
		t.Fatal("projection aliased journal")
	}
}

func TestEventGroupingAuditAndEmpty(t *testing.T) {
	j, path := testJournal(t)
	asOf := Fixture().ReceivedAt
	empty, err := j.Events(asOf)
	if err != nil || len(empty.Groups) != 0 {
		t.Fatal(empty, err)
	}
	if _, err = j.Events(time.Time{}); err == nil {
		t.Fatal("zero cutoff accepted")
	}
	r, err := j.Retain(Fixture())
	if err != nil {
		t.Fatal(err)
	}
	// Even corruption in a future revision must fail the complete-journal audit.
	later := successor(Fixture(), r, "correction")
	later.ReceivedAt = asOf.Add(time.Hour)
	second, err := j.Retain(later)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "record-"+second.Key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact Retained
	if err = json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.Recording.Original = "tampered"
	raw, _ = json.Marshal(artifact)
	if err = os.WriteFile(filepath.Join(path, "record-"+second.Key+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Events(asOf); err == nil {
		t.Fatal("cutoff hid journal corruption")
	}
}

func TestEventGroupingRemovedClaimsAndNoticeDeduplication(t *testing.T) {
	j, path := testJournal(t)
	r := Fixture()
	c := r.Claims[0]
	c.ID = "removed"
	c.EventKey = "removed-event"
	duplicate := c
	duplicate.ID = "also-removed"
	r.Claims = append(r.Claims, c, duplicate)
	first, err := j.Retain(r)
	if err != nil {
		t.Fatal(err)
	}
	next := successor(Fixture(), first, "correction")
	next.Claims[0].EventKey = "new-event"
	if _, err = j.Retain(next); err != nil {
		t.Fatal(err)
	}
	out, err := j.Events(next.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 3 {
		t.Fatal(out)
	}
	for _, g := range out.Groups {
		if !g.RequiresReview || len(g.Changes) != 1 {
			t.Fatal("revision notice missing or duplicated", g)
		}
		for _, c := range g.Claims {
			if g.EventKey != "new-event" && c.State != "superseded" {
				t.Fatal("removed claim stayed current", c)
			}
		}
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Events(next.ReceivedAt)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("reopen changed groups", err)
	}
	other, _ := testJournal(t)
	if _, err = other.Retain(r); err != nil {
		t.Fatal(err)
	}
	before, err := other.Events(r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	own, err := reopened.Events(r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range own.Groups {
		for _, h := range before.Groups {
			if g.ID == h.ID {
				t.Fatal("group identity escaped journal scope")
			}
		}
	}
	if _, err = j.Events(next.ReceivedAt); err == nil {
		t.Fatal("closed journal inspected")
	}
}
