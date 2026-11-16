package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

type briefReadStore interface {
	CappedBookStore
	BoundedCappedBookStore
}

func cappedBriefReadContracts(t *testing.T, s briefReadStore) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	b, e := s.CreateCappedBook(ctx, account.DefaultCappedConfig("brief-read", "brief-read-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CappedBookBounded(ctx, "missing", 4096); !errors.Is(e, account.ErrBookNotFound) {
		t.Fatal(e)
	}
	for _, max := range []int{-1, 0, 1, 64} {
		if _, e = s.CappedBookBounded(ctx, b.Config.ID, max); !errors.Is(e, ErrCappedReadCapacity) {
			t.Fatal(max, e)
		}
	}
	b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, account.BookCommand{ID: "observation", Kind: "risk_observe", At: at, Marks: map[string]string{"SPY": "100"}})
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.CappedBookBounded(ctx, b.Config.ID, 1<<20)
	if e != nil || snapshot.Version != b.Version {
		t.Fatal(snapshot, e)
	}
	snapshot.Events[0].Marks["SPY"] = "1"
	snapshot, e = s.CappedBookBounded(ctx, b.Config.ID, 1<<20)
	if e != nil || snapshot.Events[0].Marks["SPY"] != "100" {
		t.Fatal("aliased bounded read", e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.CappedBookBounded(canceled, b.Config.ID, 4096); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 12; i++ {
			var err error
			b, err = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, account.BookCommand{ID: fmt.Sprint("halt-", i), Kind: "halt", At: at})
			if err != nil {
				t.Error(err)
				return
			}
		}
	})
	for i := 0; i < 12; i++ {
		v, e := s.CappedBookBounded(ctx, "brief-read", 1<<20)
		if e != nil || v.Version != int64(len(v.Events)) || v.Version < 1 {
			t.Fatal("torn bounded prefix", v, e)
		}
	}
	wg.Wait()
	// One large event must fail as a whole; SQL suppresses its payload before pgx.
	marks := map[string]string{}
	for i := 0; i < 2000; i++ {
		marks[fmt.Sprint("ASSET", i)] = "100"
	}
	b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, account.BookCommand{ID: "wide-mark", Kind: "risk_observe", At: at, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	if v, e := s.CappedBookBounded(ctx, b.Config.ID, 4096); !errors.Is(e, ErrCappedReadCapacity) || v.Version != 0 {
		t.Fatal("partial bounded read", v, e)
	}
	v, e := s.CappedBookBounded(ctx, b.Config.ID, 1<<20)
	if e != nil || v.Version != b.Version {
		t.Fatal(v, e)
	}
}
func TestMemoryCappedBookBoundedBrief(t *testing.T) { cappedBriefReadContracts(t, NewMemory()) }
func TestPostgresContractsCappedBookBoundedBrief(t *testing.T) {
	p := timerPostgres(t, "brief_read")
	cappedBriefReadContracts(t, p)
	other, e := OpenPostgres(context.Background(), p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	v, e := other.CappedBookBounded(context.Background(), "brief-read", 1<<20)
	if e != nil || v.Version != 14 {
		t.Fatal("restart read", v.Version, e)
	}
}
