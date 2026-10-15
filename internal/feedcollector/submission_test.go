package feedcollector

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func TestSubmissionCapacityAndClock(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	r := registration()
	s := InitialState()
	s.Entries[r.ID] = Entry{Registration: r, Lease: &Lease{OwnerID: "owner", Generation: 1, ExpiresAt: at.Add(time.Minute)}}
	s.LastClock = at
	f := Fence{RegistrationID: r.ID, OwnerID: "owner", Generation: 1}
	i := domain.RoutingInput{ID: "input", Source: "alpaca-iex", Symbol: r.Symbol, EvidenceClass: r.EvidenceClass, ReceivedAt: at, Provenance: &domain.InputProvenance{SourceSymbol: r.Symbol, AccessRef: r.AccessRef, MappingVersion: "alpaca-us-equities-v1", RawRef: "journal:" + r.JournalID + ":capture-test-r1.json"}}
	if _, err := CheckSubmission(s, f, i, at.Add(-time.Second)); !errors.Is(err, ErrInvalid) {
		t.Fatal("future receipt admitted", err)
	}
	i.ReceivedAt = at.Add(-2 * time.Second)
	if _, err := CheckSubmission(s, f, i, at.Add(-time.Second)); !errors.Is(err, ErrClockBackwards) {
		t.Fatal("clock rewind admitted", err)
	}
	for n := 0; n < MaxAdmissions; n++ {
		s.Admissions[fmt.Sprint(n)] = Admission{}
	}
	if _, err := CheckSubmission(s, f, i, at); !errors.Is(err, ErrCapacity) {
		t.Fatal("admission capacity bypassed", err)
	}
	delete(s.Admissions, "0")
	s.Admissions[i.ID] = Admission{Fence: f, InputSHA256: InputDigest(i)}
	if replay, err := CheckSubmission(s, f, i, at.Add(time.Hour)); err != nil || !replay {
		t.Fatal("full expired registry lost exact replay", err)
	}
	f.OwnerID = "other"
	if _, err := CheckSubmission(s, f, i, at); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("historical identity granted foreign authority", err)
	}
}
