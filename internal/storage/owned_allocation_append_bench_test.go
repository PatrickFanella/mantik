package storage

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Append cost must not grow with retained history: checkpoints seal every
// 64 active records, so an append replays at most the records since the last
// seal. This builds journals of two lengths at the same offset into the seal
// cycle and benchmarks one more append on each. Building the longer history
// takes a while, so it runs only with MANTIK_ALLOCATION_APPEND_BENCH=1:
//
//	MANTIK_ALLOCATION_APPEND_BENCH=1 go test ./internal/storage -run TestOwnedAllocationAppendCostIsBounded -v
func TestOwnedAllocationAppendCostIsBounded(t *testing.T) {
	if os.Getenv("MANTIK_ALLOCATION_APPEND_BENCH") != "1" {
		t.Skip("set MANTIK_ALLOCATION_APPEND_BENCH=1 to run")
	}
	f := newEligibilityFixture(t)
	journal := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
	archived := 0
	clock := f.at
	appendOne := func(id string) {
		t.Helper()
		c := f.command
		c.ID = id
		clock = clock.Add(time.Second)
		next, _, err := f.append(t, journal, c, clock, f.complete)
		if err != nil {
			t.Fatal(id, err)
		}
		archived += len(next.sealing)
		next.sealing = nil
		journal = next
	}
	measure := func(history int) (time.Duration, int) {
		t.Helper()
		for archived+len(journal.Records) < history {
			appendOne(fmt.Sprintf("history-%d", archived+len(journal.Records)))
		}
		at := clock.Add(time.Second)
		c := f.command
		c.ID = fmt.Sprintf("measured-%d", history)
		result := testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				if _, _, err := f.append(t, journal, c, at, f.complete); err != nil {
					b.Fatal(err)
				}
			}
		})
		return time.Duration(result.NsPerOp()), len(journal.Records)
	}
	// Both lengths leave 32 records active since the last checkpoint.
	short, shortActive := measure(128 + 32)
	long, longActive := measure(1024 + 32)
	t.Logf("append after %d records (%d active): %v", 128+32, shortActive, short)
	t.Logf("append after %d records (%d active): %v", 1024+32, longActive, long)
	if shortActive != longActive {
		t.Fatal("measurements at different points of the seal cycle", shortActive, longActive)
	}
	// Seven times the history may cost at most half again as much.
	if long > short*3/2 {
		t.Fatal("append cost grew with history", short, long)
	}
}
