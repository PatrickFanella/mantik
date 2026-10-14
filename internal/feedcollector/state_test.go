package feedcollector

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func registration() Registration {
	return Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-private-one", AccessRef: "access-receipt-one", LeaseSeconds: 30}
}

func TestOwnershipLifecycle(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	r := registration()
	s := InitialState()
	apply := func(c Command, when time.Time) Receipt {
		t.Helper()
		next, receipt, err := Apply(s, c, when)
		if err != nil {
			t.Fatal(err)
		}
		s = next
		return receipt
	}
	register := Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}
	apply(register, at)
	claim := Command{RequestID: "claim-one", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}
	first := apply(claim, at)
	if first.Entry.Lease.Generation != 1 || LeaseStatus(first.Entry, s.LastClock, at) != "lease_current" {
		t.Fatal(first)
	}
	before := s
	if _, _, err := Apply(s, Command{RequestID: "competitor", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-two"}, at.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s) {
		t.Fatal("rejection mutated state")
	}
	renew := Command{RequestID: "renew-one", Operation: "renew", RegistrationID: r.ID, OwnerID: "worker-one", Generation: 1}
	renewal := apply(renew, at.Add(10*time.Second))
	if !renewal.Entry.Lease.ExpiresAt.Equal(at.Add(40 * time.Second)) {
		t.Fatal(renewal)
	}
	before = s
	for _, c := range []Command{claim, renew, register} {
		next, receipt, err := Apply(s, c, at.Add(time.Hour))
		if err != nil || !reflect.DeepEqual(next, before) || !reflect.DeepEqual(receipt, s.Commands[c.RequestID].Receipt) {
			t.Fatal("lost-reply retry changed authority", err)
		}
	}
	expires := renewal.Entry.Lease.ExpiresAt
	if LeaseStatus(s.Entries[r.ID], s.LastClock, expires) != "expired" {
		t.Fatal("expiry boundary inclusive")
	}
	renew.RequestID = "expired-renew"
	if _, _, err := Apply(s, renew, expires); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired owner revived", err)
	}
	second := apply(Command{RequestID: "claim-two", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-two"}, expires)
	if second.Entry.Lease.Generation != 2 {
		t.Fatal("generation not increased")
	}
	for _, op := range []string{"renew", "stop"} {
		if _, _, err := Apply(s, Command{RequestID: "stale-" + op, Operation: op, RegistrationID: r.ID, OwnerID: "worker-one", Generation: 1}, expires); !errors.Is(err, ErrLeaseLost) {
			t.Fatal("old owner not fenced", err)
		}
	}
	stop := Command{RequestID: "stop-two", Operation: "stop", RegistrationID: r.ID, OwnerID: "worker-two", Generation: 2}
	stopped := apply(stop, expires)
	if LeaseStatus(stopped.Entry, s.LastClock, expires) != "stopped" {
		t.Fatal(stopped)
	}
	third := apply(Command{RequestID: "claim-three", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-two"}, expires)
	if third.Entry.Lease.Generation != 3 {
		t.Fatal("same owner reused generation")
	}
	apply(stop, expires.Add(time.Hour))
	if s.Entries[r.ID].Lease.Stopped || s.Entries[r.ID].Lease.Generation != 3 {
		t.Fatal("historical stop stopped successor")
	}
	if LeaseStatus(s.Entries[r.ID], s.LastClock, at) != "clock_uncertain" {
		t.Fatal("rewound read claimed current lease")
	}
	if _, _, err := Apply(s, Command{RequestID: "rewound", Operation: "stop", RegistrationID: r.ID, OwnerID: "worker-two", Generation: 3}, at); !errors.Is(err, ErrClockBackwards) {
		t.Fatal(err)
	}
}

func TestValidationAndConflicts(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	r := registration()
	c := Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}
	s, _, err := Apply(InitialState(), c, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Registration){
		func(r *Registration) { r.Provider = "other" }, func(r *Registration) { r.Feed = "best" }, func(r *Registration) { r.Symbol = "spy" }, func(r *Registration) { r.EvidenceClass = "synthetic_unqualified" }, func(r *Registration) { r.JournalID = "/private/path" }, func(r *Registration) { r.AccessRef = "key:value" }, func(r *Registration) { r.LeaseSeconds = 4 }, func(r *Registration) { r.LeaseSeconds = 301 },
	} {
		bad := r
		mutate(&bad)
		c.Registration = &bad
		if _, _, err := Apply(s, c, at); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid registration admitted", bad, err)
		}
	}
	c.Registration = &r
	changed := r
	changed.LeaseSeconds = 60
	c.Registration = &changed
	if _, _, err := Apply(s, c, at); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c.RequestID = "new-register"
	if _, _, err := Apply(s, c, at); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for _, duplicate := range []Registration{func() Registration { v := r; v.ID = "alias"; v.JournalID = "other-journal"; return v }(), func() Registration { v := r; v.ID = "alias"; v.Symbol = "QQQ"; return v }()} {
		c.RegistrationID = duplicate.ID
		c.Registration = &duplicate
		if _, _, err := Apply(s, c, at); !errors.Is(err, ErrConflict) {
			t.Fatal("parallel ownership alias admitted", err)
		}
	}
	for _, bad := range []Command{{RequestID: "x", Operation: "claim", RegistrationID: r.ID, OwnerID: "w", Generation: 1}, {RequestID: "x", Operation: "renew", RegistrationID: r.ID, OwnerID: "w"}, {RequestID: "x", Operation: "erase", RegistrationID: r.ID}, {RequestID: "x", Operation: "claim", RegistrationID: r.ID, OwnerID: "w", Registration: &r}} {
		if _, _, err := Apply(s, bad, at); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
}

func TestCapacityRetainsRetry(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	r := registration()
	c := Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}
	s, receipt, err := Apply(InitialState(), c, at)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n < MaxCommands; n++ {
		s.Commands[fmt.Sprint(n)] = RetainedCommand{}
	}
	if _, _, err := Apply(s, Command{RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker"}, at); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, got, err := Apply(s, c, at.Add(time.Hour)); err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("capacity rejected retained retry", err)
	}
	s = InitialState()
	for n := 0; n < MaxRegistrations; n++ {
		s.Entries[fmt.Sprint(n)] = Entry{}
	}
	if _, _, err := Apply(s, c, at); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}
