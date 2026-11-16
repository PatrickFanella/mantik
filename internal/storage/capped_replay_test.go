package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func replayCacheFixture(t *testing.T, n int) (*Memory, account.CappedState) {
	t.Helper()
	ctx := context.Background()
	m := NewMemory()
	state, err := m.CreateCappedBook(ctx, account.DefaultCappedConfig("cache", "cache-paper", account.Cash, time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		c := account.BookCommand{ID: fmt.Sprint(i), Kind: "risk_observe", At: state.Config.OpenedAt, Marks: map[string]string{"SPY": fmt.Sprint(100 + i)}}
		if i == 0 {
			c.Kind = "risk_control"
			c.RiskControl = &risk.Control{Action: "kill"}
		}
		state, err = m.ApplyCappedBook(ctx, state.Config.ID, state.Version, c)
		if err != nil {
			t.Fatal(err)
		}
	}
	return m, state
}

func TestCappedReplayWarmupResumesVerifiedPrefix(t *testing.T) {
	m, want := replayCacheFixture(t, 600)
	start, err := account.NewCappedState(want.Config)
	if err != nil {
		t.Fatal(err)
	}
	var cache cappedReplayCache
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var reads []int64
	read := func(after int64, limit int) ([]CappedHistoryRecord, error) {
		reads = append(reads, after)
		return m.CappedBookHistory(context.Background(), want.Config.ID, after, limit)
	}
	state, err := advanceCappedReplay(ctx, start, false, want.Version, read, func(v int64, modern bool, raw []byte) {
		cache.remember(want.Config.ID, v, modern, raw)
		if v == 17 {
			<-ctx.Done() // Expire after a known, fully verified prefix.
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) || state.Version != 0 {
		t.Fatal("partial warmup returned authority", state.Version, err)
	}
	raw, v, modern := cache.entry(want.Config.ID).snapshot()
	if v != 17 || !modern {
		t.Fatal("valid progress not retained", v, modern)
	}
	var prefix account.CappedState
	if err = json.Unmarshal(raw, &prefix); err != nil {
		t.Fatal(err)
	}
	reads = nil
	got, err := advanceCappedReplay(context.Background(), prefix, modern, want.Version, read, func(v int64, modern bool, raw []byte) { cache.remember(want.Config.ID, v, modern, raw) })
	if err != nil || !reflect.DeepEqual(got, want) || len(reads) != 3 || reads[0] != 17 {
		t.Fatal("warmup restarted or changed state", reads, err)
	}
	// Cache outputs and caller-owned source buffers never alias private custody.
	raw[0] = '!'
	for _, e := range got.Events {
		if e.RiskCheckpoint != nil {
			e.RiskCheckpoint.State.Kill = false
			e.RiskCheckpoint.Last.Marks["SPY"] = "1"
		}
	}
	raw, v, _ = cache.entry(want.Config.ID).snapshot()
	if err = json.Unmarshal(raw, &prefix); err != nil || v != want.Version || !reflect.DeepEqual(prefix, want) {
		t.Fatal("cache alias poisoned replay", err)
	}
}

func TestCappedReplayRejectsCorruptTransitions(t *testing.T) {
	m, want := replayCacheFixture(t, 8)
	for _, mode := range []string{"sequence", "command-digest", "source-command", "before", "after", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			start, err := account.NewCappedState(want.Config)
			if err != nil {
				t.Fatal(err)
			}
			var cache cappedReplayCache
			_, err = advanceCappedReplay(context.Background(), start, false, want.Version, func(after int64, limit int) ([]CappedHistoryRecord, error) {
				page, e := m.CappedBookHistory(context.Background(), want.Config.ID, after, limit)
				if e != nil {
					return nil, e
				}
				switch mode {
				case "sequence":
					page[0].Sequence++
				case "command-digest":
					page[0].CommandSHA256 = strings.Repeat("f", 64)
				case "source-command":
					page[0].Command.RiskControl.Action = "unkill"
				case "before":
					page[0].PreviousSHA256 = strings.Repeat("f", 64)
				case "after":
					page[0].StateSHA256 = strings.Repeat("f", 64)
				case "checkpoint":
					page[0].Command.Kind = "risk_checkpoint"
				}
				if mode == "source-command" || mode == "checkpoint" {
					raw, e := json.Marshal(page[0].Command)
					if e != nil {
						return nil, e
					}
					page[0].CommandSHA256 = cappedDigest(raw)
				}
				return page, nil
			}, func(v int64, modern bool, raw []byte) { cache.remember(want.Config.ID, v, modern, raw) })
			raw, v, _ := cache.entry(want.Config.ID).snapshot()
			if err == nil || v != 0 || raw != nil {
				t.Fatal("corruption entered cache", v, err)
			}
		})
	}
}

func TestCappedReplayCacheConcurrentPublicationIsDetachedAndMonotonic(t *testing.T) {
	var cache cappedReplayCache
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			raw := []byte(fmt.Sprint(i))
			cache.remember("book", int64(i), true, raw)
			raw[0] = '!'
			snapshot, _, _ := cache.entry("book").snapshot()
			if len(snapshot) > 0 {
				snapshot[0] = '?'
			}
		})
	}
	wg.Wait()
	raw, v, _ := cache.entry("book").snapshot()
	if v != 99 || string(raw) != "99" {
		t.Fatal("cache regressed or aliased", v, string(raw))
	}
}
