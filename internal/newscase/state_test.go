package newscase

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func revision(r news.Recording, receipt Receipt, kind string) news.Recording {
	raw, _ := json.Marshal(r)
	var detached news.Recording
	_ = json.Unmarshal(raw, &detached)
	r = detached
	r.Revision++
	r.Kind = kind
	r.AvailableAt = r.AvailableAt.Add(time.Second)
	r.ReceivedAt = r.ReceivedAt.Add(time.Second)
	r.Supersedes = &news.RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: r.Revision - 1, RecordingSHA256: receipt.RecordingSHA256}
	if kind == "retraction" {
		for i := range r.Claims {
			r.Claims[i].Assessment = "retracted"
		}
	}
	return r
}
func TestAdmissionRevisionsReopenAndFencedClosure(t *testing.T) {
	initial := InitialState()
	r := news.Fixture()
	now := r.ReceivedAt.Add(time.Hour)
	s, first, err := Admit(initial, r, now)
	if err != nil || len(first.Matches) != 1 {
		t.Fatal(first, err)
	}
	id := first.Matches[0].CaseID
	closed, c, err := Close(s, id, 1, now)
	if err != nil || c.Status != "closed" {
		t.Fatal(err)
	}
	// Closing must also detach nested case references from the input state.
	sourceCopy := copyOf(s)
	detached, _, err := Close(sourceCopy, id, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	sourceCopy.Cases[id].RecordKeys[0] = "changed"
	if detached.Cases[id].RecordKeys[0] == "changed" {
		t.Fatal("closure aliased input case references")
	}
	repeated, _, err := Close(closed, id, 1, time.Time{})
	if err != nil || !reflect.DeepEqual(repeated, closed) {
		t.Fatal("closure retry mutated", err)
	}
	same, retry, err := Admit(closed, r, time.Time{})
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(same, closed) {
		t.Fatal("retry reopened case", err)
	}
	corrected := revision(r, first, "correction")
	corrected.Claims[0].EventKey = "corrected-event"
	next, second, err := Admit(closed, corrected, now)
	if err != nil || len(second.Matches) != 2 {
		t.Fatal(err)
	}
	if next.Cases[id].Status != "open" || next.Cases[id].Generation != 2 || next.Cases[id].Version != 2 {
		t.Fatal(next.Cases)
	}
	if _, _, err = Close(next, id, 1, now); !errors.Is(err, ErrConflict) {
		t.Fatal("stale evidence close succeeded", err)
	}
	// Closing either affected group does not change the retained source or receipts.
	for key, c := range next.Cases {
		next, _, err = Close(next, key, c.Version, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	retracted := revision(corrected, second, "retraction")
	final, third, err := Admit(next, retracted, now)
	if err != nil || len(third.Matches) != 1 || third.Matches[0].Transition != "reopened" {
		t.Fatal(third, err)
	}
	if final.Cases[id].Status != "closed" {
		t.Fatal("unaffected historical group reopened")
	}
	revived := revision(retracted, third, "correction")
	revived.Claims[0].Assessment = "reported_unverified"
	if _, _, err = Admit(final, revived, now); !errors.Is(err, ErrConflict) {
		t.Fatal("retraction revived", err)
	}
	if len(initial.Records) != 0 || len(initial.Cases) != 0 || s.Cases[id].Status != "open" {
		t.Fatal("input state mutated")
	}
	corrected.Original = "changed"
	second.Matches[0].CaseID = "changed"
	if final.Records[third.RecordKey].Recording.Original != r.Original {
		t.Fatal("caller alias altered original")
	}
}
func TestAdmissionSourceIsolationConflictAndCausalBounds(t *testing.T) {
	r := news.Fixture()
	at := r.ReceivedAt
	s, receipt, err := Admit(InitialState(), r, at)
	if err != nil {
		t.Fatal(err)
	}
	changed := news.Fixture()
	changed.Claims[0].Assertion = "different assertion"
	if _, _, err = Admit(s, changed, at); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for _, test := range []string{"missing", "wrong_digest", "cross_class", "availability", "receipt"} {
		t.Run(test, func(t *testing.T) {
			next := revision(r, receipt, "correction")
			switch test {
			case "missing":
				next.ArticleID = "absent"
				next.Supersedes.ArticleID = "absent"
			case "wrong_digest":
				next.Supersedes.RecordingSHA256 = news.SHA256("wrong")
			case "cross_class":
				next.EvidenceClass = "recorded_market_unqualified"
			case "availability":
				next.AvailableAt = r.PublishedAt
			case "receipt":
				next.ReceivedAt = r.AvailableAt
				next.AvailableAt = r.AvailableAt
			}
			if _, _, err := Admit(s, next, at.Add(time.Hour)); err == nil {
				t.Fatal("bad predecessor admitted")
			}
		})
	}
	for _, test := range []string{"source", "article", "class"} {
		copy := news.Fixture()
		switch test {
		case "source":
			copy.SourceID = "syndicated"
		case "article":
			copy.ArticleID = "second-article"
		case "class":
			copy.EvidenceClass = "recorded_market_unqualified"
		}
		next, rec, err := Admit(s, copy, at)
		if err != nil || len(next.Records) != 2 {
			t.Fatal(err)
		}
		if test == "class" {
			if len(next.Cases) != 2 {
				t.Fatal("evidence mixed")
			}
		} else if len(next.Cases) != 1 || next.Cases[rec.Matches[0].CaseID].Independence != "not_assessed" {
			t.Fatal("copy implied independence or split event")
		}
	}
	future := news.Fixture()
	future.SourceID = "future"
	if _, _, err = Admit(s, future, at.Add(-time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("future receipt or clock rewind accepted", err)
	}
}
func TestAdmissionCapacityIsAtomicAndRetriesStillWork(t *testing.T) {
	r := news.Fixture()
	s, receipt, err := Admit(InitialState(), r, r.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Fill synthetic cases to the exact bound. A revision moving to a new event
	// must not partially retain its original or reopen the existing closed case.
	for len(s.Cases) < MaxCases {
		id := news.SHA256(string(rune(len(s.Cases))))
		s.Cases[id] = Case{ID: id}
	}
	before := copyOf(s)
	next := revision(r, receipt, "correction")
	next.Claims[0].EventKey = "new"
	actual, _, err := Admit(s, next, r.ReceivedAt.Add(time.Hour))
	if !errors.Is(err, ErrCapacity) || !reflect.DeepEqual(actual, before) {
		t.Fatal("partial capacity mutation", err)
	}
	_, retry, err := Admit(s, r, r.ReceivedAt)
	if err != nil || !retry.Duplicate {
		t.Fatal("capacity blocked retry")
	}
	for len(s.Records) < news.MaxRecords {
		s.Records[news.SHA256(string(rune(len(s.Records))))] = Record{}
	}
	copy := news.Fixture()
	copy.ArticleID = "new"
	if _, _, err = Admit(s, copy, r.ReceivedAt); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}
