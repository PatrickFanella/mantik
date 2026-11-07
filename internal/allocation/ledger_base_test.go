package allocation

import (
	"context"
	"errors"
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
