package paperledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func copyRecording(r Recording) Recording {
	raw, _ := json.Marshal(r)
	var copy Recording
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func TestLedgerPartialUnknownReleaseAndExactRetry(t *testing.T) {
	r := Fixture()
	before := copyRecording(r)
	out, err := Audit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if out.AvailableCashCents != 55000 || out.ReservedCashCents != 30000 || out.SettledDebitsCents != 15000 || out.UnresolvedReservations != 1 || len(out.Entries) != 8 || len(out.Receipts) != 9 || !out.Receipts[8].Duplicate || out.Receipts[8].Sequence != 1 || out.Receipts[8].EntrySHA256 != out.Receipts[0].EntrySHA256 {
		t.Fatal(out)
	}
	if out.AllocationAvailable || out.ExecutionAttested || out.QualificationCredit || out.Authority != "recorded_synthetic_paper_ledger_only" {
		t.Fatal("authority", out)
	}
	if !reflect.DeepEqual(r, before) {
		t.Fatal("mutated recording")
	}
	if out.Reservations[0].OriginalCents != 40000 || out.Reservations[0].RemainingCents != 0 || out.Reservations[0].SettledCents != 15000 || out.Reservations[0].ReleasedCents != 25000 || !out.Reservations[1].Unresolved {
		t.Fatal(out.Reservations)
	}
	previous := r.Account.SHA256
	for i, e := range out.Entries {
		sha := e.SHA256
		e.SHA256 = ""
		if e.Sequence != i+1 || e.PreviousSHA256 != previous || digest(e) != sha {
			t.Fatal("chain", i)
		}
		previous = sha
	}
	if out.LedgerHeadSHA256 != previous {
		t.Fatal("head")
	}
	sha := out.SHA256
	out.SHA256 = ""
	if digest(out) != sha {
		t.Fatal("result digest")
	}
	again, err := Audit(context.Background(), r)
	if err != nil || again.SHA256 != sha {
		t.Fatal("unstable replay", err)
	}
	// Returning a detached result must not let a caller alter a subsequent audit.
	again.Reservations[0].RemainingCents = 999
	after, _ := Audit(context.Background(), r)
	if after.SHA256 != sha {
		t.Fatal("result aliases input")
	}
}
func TestLedgerRejectsUnsafeOrConflictingTransitions(t *testing.T) {
	base := Fixture()
	base.Entries = base.Entries[:1]
	entry := func(id, kind string, amount int64) Entry {
		return Entry{ID: id, Kind: kind, ReservationID: "a", AmountCents: amount, OccurredAt: base.Entries[0].OccurredAt.Add(time.Second)}
	}
	for name, mutate := range map[string]func(*Recording){
		"wrong class": func(r *Recording) { r.EvidenceClass = "prospective_market_unqualified" },
		"schema":      func(r *Recording) { r.Schema = "future" },
		"account":     func(r *Recording) { r.Account.OpeningFunding.AmountCents++ },
		"nil":         func(r *Recording) { r.Entries = nil },
		"oversize":    func(r *Recording) { r.Entries = make([]Entry, MaxEntries+1) },
		"bad id":      func(r *Recording) { r.Entries[0].ID = "../path" },
		"kind":        func(r *Recording) { r.Entries[0].Kind = "sell" },
		"negative":    func(r *Recording) { r.Entries[0].AmountCents = -1 },
		"zero":        func(r *Recording) { r.Entries[0].AmountCents = 0 },
		"overflow":    func(r *Recording) { r.Entries[0].AmountCents = 1 << 62 },
		"clock":       func(r *Recording) { r.Entries[0].OccurredAt = r.Account.CreatedAt.Add(-time.Second) },
		"reordered": func(r *Recording) {
			e := entry("backdate", "settle", 1)
			e.OccurredAt = r.Account.CreatedAt
			r.Entries = append(r.Entries, e)
		},
		"changed retry": func(r *Recording) { e := r.Entries[0]; e.AmountCents--; r.Entries = append(r.Entries, e) },
		"cash":          func(r *Recording) { r.Entries[0].AmountCents = r.Account.Config.OpeningCashCents + 1 },
		"ceiling":       func(r *Recording) { r.Entries[0].AmountCents = r.Account.Config.CapitalLimitCents + 1 },
		"competing": func(r *Recording) {
			e := entry("other", "reserve", 30000)
			e.ReservationID = "b"
			r.Entries = append(r.Entries, e)
		},
		"reused reservation": func(r *Recording) { r.Entries = append(r.Entries, entry("again", "reserve", 1)) },
		"over settlement":    func(r *Recording) { r.Entries = append(r.Entries, entry("over", "settle", 40001)) },
		"over release":       func(r *Recording) { r.Entries = append(r.Entries, entry("over", "release", 40001)) },
		"unknown release": func(r *Recording) {
			r.Entries = append(r.Entries, entry("u", "unknown", 0), entry("release", "release", 1))
		},
		"unknown settle": func(r *Recording) {
			r.Entries = append(r.Entries, entry("u", "unknown", 0), entry("settle", "settle", 1))
		},
		"unknown again":           func(r *Recording) { r.Entries = append(r.Entries, entry("u", "unknown", 0), entry("u2", "unknown", 0)) },
		"unknown with amount":     func(r *Recording) { r.Entries = append(r.Entries, entry("u", "unknown", 1)) },
		"resolve without unknown": func(r *Recording) { r.Entries = append(r.Entries, entry("resolved", "resolve", 0)) },
		"missing reservation":     func(r *Recording) { r.Entries = []Entry{entry("unknown", "settle", 1)} },
		"release closed": func(r *Recording) {
			r.Entries = append(r.Entries, entry("all", "release", 40000), entry("extra", "release", 1))
		},
		"unknown closed": func(r *Recording) {
			r.Entries = append(r.Entries, entry("all", "release", 40000), entry("u", "unknown", 0))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := copyRecording(base)
			mutate(&r)
			out, err := Audit(context.Background(), r)
			if err == nil || out.Schema != "" {
				t.Fatal("unsafe/partial result", out, err)
			}
		})
	}
}
func TestLedgerCompetingCashAndCapacity(t *testing.T) {
	r := Fixture()
	r.Entries = []Entry{}
	for i := range MaxReservations {
		r.Entries = append(r.Entries, Entry{ID: fmt.Sprintf("e%d", i), Kind: "reserve", ReservationID: fmt.Sprintf("r%d", i), AmountCents: 1, OccurredAt: r.Account.CreatedAt})
	}
	out, err := Audit(context.Background(), r)
	if err != nil || out.ReservedCashCents != MaxReservations {
		t.Fatal(err)
	}
	r.Entries = append(r.Entries, Entry{ID: "extra", Kind: "reserve", ReservationID: "extra", AmountCents: 1, OccurredAt: r.Account.CreatedAt})
	if _, err = Audit(context.Background(), r); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	empty := Fixture()
	empty.Entries = []Entry{}
	out, err = Audit(context.Background(), empty)
	if err != nil || out.AvailableCashCents != empty.Account.Config.OpeningCashCents || len(out.Entries) != 0 || out.LedgerHeadSHA256 != empty.Account.SHA256 {
		t.Fatal(out, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Audit(canceled, Fixture()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"schema":"x","schema":"y"}`, `{"unknown":true}`, `{} {}`, strings.Repeat(" ", MaxRecordingBytes+1)} {
		if _, err := Read(strings.NewReader(raw)); err == nil {
			t.Fatal("unsafe JSON accepted")
		}
	}
}
func FuzzLedgerConservation(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6})
	f.Add([]byte{255, 0, 128, 32})
	f.Fuzz(func(t *testing.T, commands []byte) {
		if len(commands) > 64 {
			commands = commands[:64]
		}
		r := Fixture()
		r.Entries = []Entry{}
		for i, b := range commands {
			r.Entries = append(r.Entries, Entry{ID: fmt.Sprintf("command%d", i), Kind: []string{"reserve", "settle", "release", "unknown", "resolve"}[int(b)%5], ReservationID: fmt.Sprintf("r%d", int(b)%4), AmountCents: int64(b) * 100, OccurredAt: r.Account.CreatedAt.Add(time.Duration(i) * time.Second)})
			if r.Entries[i].Kind == "unknown" || r.Entries[i].Kind == "resolve" {
				r.Entries[i].AmountCents = 0
			}
		}
		out, err := Audit(context.Background(), r)
		if err != nil {
			return
		}
		if out.AvailableCashCents < 0 || out.ReservedCashCents < 0 || out.SettledDebitsCents < 0 || out.AvailableCashCents+out.ReservedCashCents+out.SettledDebitsCents != out.OpeningCashCents || out.ReservedCashCents+out.SettledDebitsCents > out.CapitalLimitCents {
			t.Fatal(out)
		}
		var remaining, settled, released, original int64
		for _, v := range out.Reservations {
			if v.RemainingCents+v.SettledCents+v.ReleasedCents != v.OriginalCents {
				t.Fatal(v)
			}
			remaining += v.RemainingCents
			settled += v.SettledCents
			released += v.ReleasedCents
			original += v.OriginalCents
		}
		if remaining != out.ReservedCashCents || settled != out.SettledDebitsCents || original != remaining+settled+released {
			t.Fatal(out)
		}
	})
}
