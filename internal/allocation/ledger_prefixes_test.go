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

func TestLedgerPrefixesMatchIndependentAppends(t *testing.T) {
	for _, costSchema := range []bool{false, true} {
		t.Run(fmt.Sprint(costSchema), func(t *testing.T) {
			ledger, admissions, at := ledgerFixture(t)
			if costSchema {
				var err error
				ledger, err = NewCostLedger(ledger.Policy)
				if err != nil {
					t.Fatal(err)
				}
			}
			q := proposal("decision", "baseline-v1", "ALPHA", 40000, at)
			commands := []LedgerCommand{
				compareCommand("comparison", []Proposal{q}, admissions),
				effectCommand("fill", q.ID, "fill", 20000, 0),
				effectCommand("unknown", q.ID, "unknown", 0, 0),
				effectCommand("resolve", q.ID, "resolve", 0, 0),
				effectCommand("release", q.ID, "release", 20000, 0),
				effectCommand("close", q.ID, "close", 20000, 19000),
			}
			if costSchema {
				commands[len(commands)-1].Effect.ProceedsCents = -100
			}
			expected := []LedgerPrefix{}
			for i, command := range commands {
				var projection LedgerProjection
				var err error
				ledger, projection, _, err = AppendLedger(context.Background(), ledger, command, func() time.Time { return at.Add(time.Duration(i) * time.Second) })
				if err != nil {
					t.Fatal(err)
				}
				expected = append(expected, LedgerPrefix{LedgerSHA256: ledger.SHA256, Projection: projection})
			}
			before := workerplan.Hash(ledger)
			got, err := ReplayLedgerPrefixes(context.Background(), ledger)
			if err != nil || !reflect.DeepEqual(got, expected) || workerplan.Hash(ledger) != before {
				t.Fatal("prefix accounting or bytes differ", got, err)
			}
			got[0].Projection.Reservations[0].Proposal.ID = "changed"
			got[0].Projection.Snapshot.Account.Config.OpeningCashCents++
			again, err := ReplayLedgerPrefixes(context.Background(), ledger)
			if err != nil || !reflect.DeepEqual(again, expected) || workerplan.Hash(ledger) != before {
				t.Fatal("prefix output aliases retained ledger or other prefixes", err)
			}
			// A self-rehashed invalid later transition cannot return earlier results.
			damaged := ledgerDetach(ledger)
			damaged.Entries[1].Command.Effect.AmountCents = 40001
			for i := range damaged.Entries {
				if i > 0 {
					damaged.Entries[i].PreviousSHA256 = damaged.Entries[i-1].SHA256
				}
				damaged.Entries[i].SHA256 = ""
				damaged.Entries[i].SHA256 = workerplan.Hash(damaged.Entries[i])
			}
			damaged.SHA256 = ""
			damaged.SHA256 = workerplan.Hash(damaged)
			if partial, err := ReplayLedgerPrefixes(context.Background(), damaged); err == nil || partial != nil {
				t.Fatal("invalid later entry leaked prefix results", partial, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if partial, err := ReplayLedgerPrefixes(ctx, ledger); !errors.Is(err, context.Canceled) || partial != nil {
				t.Fatal("canceled walk returned prefixes", partial, err)
			}
		})
	}
}

func BenchmarkLedgerPrefixReplay(b *testing.B) {
	initial, _, at := ledgerFixture(b)
	commands := make([]LedgerCommand, 32)
	complete := initial
	for i := range commands {
		commands[i] = compareCommand(fmt.Sprintf("comparison-%02d", i), []Proposal{proposal("decision", "baseline-v1", "ALPHA", 10000, at)}, []Admission{})
		var err error
		complete, _, _, err = AppendLedger(context.Background(), complete, commands[i], func() time.Time { return at })
		if err != nil {
			b.Fatal(err)
		}
	}
	b.Run("prefix_appends", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			ledger := initial
			for _, command := range commands {
				var err error
				ledger, _, _, err = AppendLedger(context.Background(), ledger, command, func() time.Time { return at })
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("single_walk", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := ReplayLedgerPrefixes(context.Background(), complete); err != nil {
				b.Fatal(err)
			}
		}
	})
}
