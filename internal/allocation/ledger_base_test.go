package allocation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type timedCommand struct {
	command LedgerCommand
	at      time.Time
}

// lifecycleCommands is TestLedgerPartialFillUnknownRecoveryAndClose's history.
func lifecycleCommands(admissions []Admission, at time.Time) []timedCommand {
	q := proposal("decision", "baseline-v1", "ALPHA", 40000, at)
	return []timedCommand{
		{compareCommand("compare", []Proposal{q}, admissions), at},
		{effectCommand("fill", q.ID, "fill", 10000, 0), at.Add(time.Second)},
		{effectCommand("unknown", q.ID, "unknown", 0, 0), at.Add(2 * time.Second)},
		{effectCommand("close-part", q.ID, "close", 4000, 3500), at.Add(3 * time.Second)},
		{compareCommand("expired", []Proposal{q}, []Admission{}), at.Add(2 * time.Hour)},
		{effectCommand("resolve", q.ID, "resolve", 0, 0), at.Add(2 * time.Hour)},
		{effectCommand("fill-rest", q.ID, "fill", 5000, 0), at.Add(2 * time.Hour)},
		{effectCommand("release-rest", q.ID, "release", 25000, 0), at.Add(2 * time.Hour)},
		{effectCommand("close-rest", q.ID, "close", 11000, 12500), at.Add(2 * time.Hour)},
	}
}

// Sealing at any point, once or twice, then appending the rest reaches the
// same projection, and every later entry hashes as it does unsealed.
func TestSealedLedgerMatchesUnsealedHistory(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	commands := lifecycleCommands(admissions, at)
	full := l
	for _, c := range commands {
		full, _ = ledgerAppend(t, full, c.command, c.at)
	}
	want, err := ReplayLedger(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	for first := 0; first <= len(commands); first++ {
		for second := first; second <= len(commands); second++ {
			sealed := l
			for i, c := range commands {
				if i == first || i == second {
					if sealed, err = SealLedger(context.Background(), sealed); err != nil {
						t.Fatal(first, second, err)
					}
				}
				var e error
				if sealed, _, _, e = AppendLedger(context.Background(), sealed, c.command, func() time.Time { return c.at }); e != nil {
					t.Fatal(first, second, i, e)
				}
			}
			if second == len(commands) {
				if sealed, err = SealLedger(context.Background(), sealed); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReplayLedger(context.Background(), sealed)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("sealed projection differs", first, second, err)
			}
			tail := full.Entries[len(full.Entries)-len(sealed.Entries):]
			for i := range sealed.Entries {
				if sealed.Entries[i].SHA256 != tail[i].SHA256 {
					t.Fatal("sealed entry hash differs", first, second, i)
				}
			}
			if sealed.Base == nil || sealed.Base.Sequence+len(sealed.Entries) != len(commands) {
				t.Fatal("sealed sequence", first, second)
			}
		}
	}
}

// A sealed command identity and sealed evidence stay consumed, and a base
// that contradicts itself is refused.
func TestSealedLedgerRefusesReuseAndInconsistentBases(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	commands := lifecycleCommands(admissions, at)
	for _, c := range commands[:2] {
		l, _ = ledgerAppend(t, l, c.command, c.at)
	}
	sealed, err := SealLedger(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	late := func() time.Time { return at.Add(time.Hour) }
	if _, _, _, err := AppendLedger(context.Background(), sealed, commands[0].command, late); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("sealed comparison retried", err)
	}
	reused := commands[1].command
	reused.ID = "reused-evidence"
	if _, _, _, err := AppendLedger(context.Background(), sealed, reused, late); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("sealed evidence reused", err)
	}
	for name, damage := range map[string]func(*LedgerBase){
		"orphan reservation": func(b *LedgerBase) { b.Proposals = []Proposal{} },
		"unsorted effects":   func(b *LedgerBase) { b.EffectIDs = append(b.EffectIDs, "a") },
		"lost command":       func(b *LedgerBase) { b.CommandIDs = b.CommandIDs[1:] },
		"other account":      func(b *LedgerBase) { b.Snapshot.Account.Config.ID = "other" },
	} {
		bad := ledgerDetach(sealed)
		damage(bad.Base)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if _, err := ReplayLedger(context.Background(), bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
	}
}

// A fully settled reservation leaves the compacted base with no reservation,
// commitment or proposal, while cash and the realized result carry over.
func TestSettledSealDropsOnlySettledReservations(t *testing.T) {
	l, admissions, at := ledgerFixture(t)
	for _, c := range lifecycleCommands(admissions, at) {
		l, _ = ledgerAppend(t, l, c.command, c.at)
	}
	want, err := ReplayLedger(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := SealSettledLedger(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReplayLedger(context.Background(), compact)
	if err != nil || len(got.Reservations) != 0 || len(got.Snapshot.Commitments) != 0 || len(compact.Base.Proposals) != 0 {
		t.Fatal("settled reservation kept", got, err)
	}
	if got.Snapshot.AvailableCashCents != want.Snapshot.AvailableCashCents || got.Snapshot.RealizedPnLCents != want.Snapshot.RealizedPnLCents || got.HeadSHA256 != want.HeadSHA256 {
		t.Fatal("compaction changed accounting", got.Snapshot, want.Snapshot)
	}
	// A held or filled-open reservation is live and stays.
	l2, admissions2, at2 := ledgerFixture(t)
	for _, c := range lifecycleCommands(admissions2, at2)[:2] {
		l2, _ = ledgerAppend(t, l2, c.command, c.at)
	}
	compact2, err := SealSettledLedger(context.Background(), l2)
	if err != nil || len(compact2.Base.Reservations) != 1 || len(compact2.Base.Proposals) != 1 || len(compact2.Base.Snapshot.Commitments) != 1 {
		t.Fatal("live reservation dropped", err)
	}
}

// Distinct decisions settle and are compacted away, so a long-lived ledger
// keeps reserving past MaxCommitments; an exact seal reaches the limit.
func TestSettledSealsKeepLedgersUnderCommitmentLimit(t *testing.T) {
	for _, compact := range []bool{false, true} {
		l, admissions, at := ledgerFixture(t)
		var failed error
		for i := 0; i < MaxCommitments+4 && failed == nil; i++ {
			clock := at.Add(time.Duration(i) * time.Second)
			q := proposal(fmt.Sprintf("decision-%03d", i), "baseline-v1", "ALPHA", 100, clock)
			next, _, _, err := AppendLedger(context.Background(), l, compareCommand(fmt.Sprintf("compare-%03d", i), []Proposal{q}, admissions), func() time.Time { return clock })
			if err != nil {
				failed = err
				break
			}
			if next, _, _, err = AppendLedger(context.Background(), next, effectCommand(fmt.Sprintf("release-%03d", i), q.ID, "release", 100, 0), func() time.Time { return clock }); err != nil {
				t.Fatal(i, err)
			}
			l = next
			if i%20 == 19 {
				seal := SealLedger
				if compact {
					seal = SealSettledLedger
				}
				if l, err = seal(context.Background(), l); err != nil {
					t.Fatal(i, err)
				}
			}
		}
		if compact && failed != nil || !compact && !errors.Is(failed, ErrLedgerCapacity) {
			t.Fatal("compact", compact, failed)
		}
	}
}
