package trial

import (
	"testing"
	"time"
)

func TestEvidenceIndexOrderingAndBindings(t *testing.T) {
	c, p, at := fixture(t)
	r, err := Register(c, p, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := StartAttempt(AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "a", TrialID: r.ID}, r, at)
	b, _ := StartAttempt(AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "b", TrialID: r.ID}, r, at)
	run, err := BeginRun(a, r, `{}`, "owner", at)
	if err != nil {
		t.Fatal(err)
	}
	index, err := MakeEvidenceIndex(r, []Attempt{b, a}, []Run{run})
	if err != nil || index.Validate() != nil || index.Attempts[0].AttemptID != "a" || index.Attempts[0].RunSHA256 != run.SHA256 || index.Attempts[1].RunSHA256 != "" {
		t.Fatal(index, err)
	}
	for _, change := range []func(*EvidenceIndex){
		func(i *EvidenceIndex) { i.Attempts = nil },
		func(i *EvidenceIndex) { i.Attempts[0], i.Attempts[1] = i.Attempts[1], i.Attempts[0] },
		func(i *EvidenceIndex) { i.Attempts[1] = i.Attempts[0] },
		func(i *EvidenceIndex) { i.Attempts[0].StartedAt = time.Time{} },
		func(i *EvidenceIndex) { i.Attempts[0].RunSHA256 = "bad" },
	} {
		copy := index
		copy.Attempts = append([]EvidenceRef{}, index.Attempts...)
		change(&copy)
		copy.SHA256 = ""
		copy.SHA256 = Hash(copy)
		if copy.Validate() == nil {
			t.Fatal("invalid index accepted")
		}
	}
	if _, err = MakeEvidenceIndex(r, []Attempt{a, a}, []Run{run}); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	if _, err = MakeEvidenceIndex(r, []Attempt{b}, []Run{run}); err == nil {
		t.Fatal("orphan run accepted")
	}
	if _, err = MakeEvidenceIndex(r, []Attempt{a}, []Run{run, run}); err == nil {
		t.Fatal("duplicate run accepted")
	}
}
