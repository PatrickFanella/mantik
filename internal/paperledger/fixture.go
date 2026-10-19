package paperledger

import (
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"time"
)

func Fixture() Recording {
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	a, _ := paperaccount.Create("receiver-synthetic_ledger_01", paperaccount.Config{ID: "synthetic-paper", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 60000}, at)
	entries := []Entry{}
	add := func(id, kind, reservation string, amount int64) {
		entries = append(entries, Entry{ID: id, Kind: kind, ReservationID: reservation, AmountCents: amount, OccurredAt: at.Add(time.Duration(len(entries)+1) * time.Second)})
	}
	add("reserve-a", "reserve", "a", 40000)
	add("partial-a", "settle", "a", 10000)
	add("unknown-a", "unknown", "a", 0)
	add("resolve-a", "resolve", "a", 0)
	add("settle-a", "settle", "a", 5000)
	add("release-a", "release", "a", 25000)
	add("reserve-b", "reserve", "b", 30000)
	add("unknown-b", "unknown", "b", 0)
	entries = append(entries, entries[0])
	return Recording{Schema: RecordingSchema, EvidenceClass: "synthetic_unqualified", Account: a, Entries: entries}
}
