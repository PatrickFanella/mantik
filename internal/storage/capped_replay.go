package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"github.com/jackc/pgx/v5"
)

// This cache is private to one Postgres instance/database. Its bytes are derived
// from original commands, never imported from a writer's proposed replay head.
// mu protects only short in-memory operations. Only pool warmups take warm;
// transaction readers must not wait on a warmup that may need a pool connection.
type cappedReplayCache struct {
	mu    sync.Mutex
	books map[string]*cappedReplayEntry
}
type cappedReplayEntry struct {
	mu      sync.Mutex
	warm    chan struct{}
	raw     []byte
	version int64
	modern  bool
}

func (c *cappedReplayCache) entry(id string) *cappedReplayEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.books == nil {
		c.books = map[string]*cappedReplayEntry{}
	}
	if c.books[id] == nil {
		c.books[id] = &cappedReplayEntry{warm: make(chan struct{}, 1)}
	}
	return c.books[id]
}
func (c *cappedReplayCache) remember(id string, version int64, modern bool, raw []byte) {
	e := c.entry(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.raw == nil || version > e.version {
		e.raw = bytes.Clone(raw)
		e.version, e.modern = version, modern
	}
}
func (e *cappedReplayEntry) snapshot() ([]byte, int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return bytes.Clone(e.raw), e.version, e.modern
}

// loadCapped always pins a database version first. A cache hit is usable only
// for that exact source-bound version/hash; a verified older prefix is only a
// starting point for replay, never admission authority for a newer SQL head.
func (p *Postgres) loadCapped(ctx context.Context, q cappedQuery, id string) (account.CappedState, error) {
	return p.loadCappedBounded(ctx, q, id, 0)
}

// A positive max bounds the returned projection before config/head bytes enter
// pgx. Archive verification remains paged and can resume from verified progress.
// Zero retains the internal command path's existing unbounded projection read.
func (p *Postgres) loadCappedBounded(ctx context.Context, q cappedQuery, id string, max int) (account.CappedState, error) {
	var config, head []byte
	var version int64
	var headSHA, sourceSHA string
	var oversized bool
	err := q.QueryRow(ctx, `SELECT
 CASE WHEN $2::bigint<=0 OR octet_length(b.config::text)<=$2 THEN b.config ELSE NULL END,
 coalesce(h.version,(SELECT coalesce(max(sequence),0) FROM mantik_capped_book_events WHERE book_id=b.id)),
 CASE WHEN $2<=0 OR octet_length(h.state::text)<=$2 THEN h.state ELSE NULL END,
 coalesce(h.state_sha256,''),coalesce(j.state_sha256,''),
 $2>0 AND (octet_length(b.config::text)>$2 OR
 CASE WHEN h.book_id IS NOT NULL THEN octet_length(h.state::text)>$2
 ELSE octet_length(b.config::text)+128+
 (SELECT coalesce(sum(octet_length(payload::text)+2),0) FROM mantik_capped_book_events WHERE book_id=b.id)>$2 END)
 FROM mantik_capped_books b LEFT JOIN mantik_capped_book_heads h ON h.book_id=b.id
 LEFT JOIN mantik_capped_book_history j ON j.book_id=h.book_id AND j.sequence=h.version
 WHERE b.id=$1`, id, int64(max)).Scan(&config, &version, &head, &headSHA, &sourceSHA, &oversized)
	if errors.Is(err, pgx.ErrNoRows) {
		return account.CappedState{}, account.ErrBookNotFound
	}
	if err != nil {
		return account.CappedState{}, err
	}
	if oversized {
		return account.CappedState{}, ErrCappedReadCapacity
	}
	if version < 0 || head != nil && (headSHA != sourceSHA || cappedDigest(head) != headSHA) {
		return account.CappedState{}, account.ErrBookConflict
	}
	var cfg account.CappedConfig
	if err = json.Unmarshal(config, &cfg); err != nil {
		return account.CappedState{}, err
	}
	if cfg.ID != id {
		return account.CappedState{}, account.ErrBookConflict
	}
	entry := p.cappedReplay.entry(id)
	committed := q == p.pool
	if committed {
		select {
		case entry.warm <- struct{}{}:
			defer func() { <-entry.warm }()
		case <-ctx.Done():
			return account.CappedState{}, ctx.Err()
		}
	}
	raw, cachedVersion, modern := entry.snapshot()
	if cachedVersion > version {
		return account.CappedState{}, account.ErrBookConflict
	}
	state, err := account.NewCappedState(cfg)
	if err != nil {
		return account.CappedState{}, err
	}
	cfg = state.Config // Match legacy replay's UTC normalization.
	if raw != nil {
		if err = json.Unmarshal(raw, &state); err != nil {
			return account.CappedState{}, err
		}
		// The immutable configuration is part of the replay root, not the head.
		expected, err := json.Marshal(cfg)
		if err != nil {
			return account.CappedState{}, err
		}
		actual, err := json.Marshal(state.Config)
		if err != nil {
			return account.CappedState{}, err
		}
		if !bytes.Equal(expected, actual) || state.Version != cachedVersion {
			return account.CappedState{}, account.ErrBookConflict
		}
	}
	var publish func(int64, bool, []byte)
	if committed {
		publish = func(v int64, modern bool, raw []byte) { p.cappedReplay.remember(id, v, modern, raw) }
	}
	state, err = advanceCappedReplay(ctx, state, modern, version, func(after int64, limit int) ([]CappedHistoryRecord, error) {
		return readCappedHistory(ctx, q, id, after, limit)
	}, publish)
	if err != nil {
		return account.CappedState{}, err
	}

	raw, err = json.Marshal(state)
	if err != nil {
		return account.CappedState{}, err
	}
	if head != nil && cappedDigest(raw) != headSHA {
		return account.CappedState{}, account.ErrBookConflict
	}
	if err = ctx.Err(); err != nil {
		return account.CappedState{}, err
	}
	if committed {
		p.cappedReplay.remember(id, state.Version, head != nil, raw)
	}
	return state, nil
}

// advanceCappedReplay validates each transition before publishing a private
// prefix. The publisher is absent for transaction-local reads, whose source
// records might be rolled back. Legacy raw representations are validated by
// replay before publication, preserving their historical review hashes.
func advanceCappedReplay(ctx context.Context, state account.CappedState, modern bool, version int64,
	read func(int64, int) ([]CappedHistoryRecord, error), publish func(int64, bool, []byte)) (account.CappedState, error) {
	var err error
	for state.Version < version {
		if err = ctx.Err(); err != nil {
			return account.CappedState{}, err
		}
		page, err := read(state.Version, int(min(int64(MaxCappedHistoryPage), version-state.Version)))
		if err != nil {
			return account.CappedState{}, err
		}
		if len(page) == 0 {
			return account.CappedState{}, account.ErrBookConflict
		}
		legacyDirty := false
		save := func() error {
			if legacyDirty {
				if err := account.ValidateCappedState(state); err != nil {
					return err
				}
				legacyDirty = false
			}
			if publish != nil {
				raw, err := json.Marshal(state)
				if err != nil {
					return err
				}
				publish(state.Version, modern, raw)
			}
			return nil
		}
		for _, r := range page {
			if err = ctx.Err(); err != nil {
				// Validated legacy pages are retained below; never retain an unchecked
				// partial legacy page or transaction-local/uncommitted history.
				return account.CappedState{}, err
			}
			command, err := json.Marshal(r.Command)
			if err != nil {
				return account.CappedState{}, err
			}
			if r.Sequence != state.Version+1 || r.Sequence > version || cappedDigest(command) != r.CommandSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
			if r.StateSHA256 == "" {
				if modern || r.PreviousSHA256 != "" || state.Version >= 4096 || r.Command.RiskCheckpoint != nil || r.Command.Kind == "risk_checkpoint" {
					return account.CappedState{}, account.ErrBookConflict
				}
				state.Events = append(state.Events, r.Command)
				state.Version++
				legacyDirty = true
				continue
			}
			if legacyDirty {
				if err = save(); err != nil {
					return account.CappedState{}, err
				}
			}
			before, err := json.Marshal(state)
			if err != nil {
				return account.CappedState{}, err
			}
			if cappedDigest(before) != r.PreviousSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
			next, err := account.ApplyCappedCommand(state, r.Command)
			if err != nil {
				return account.CappedState{}, err
			}
			after, err := json.Marshal(next)
			if err != nil {
				return account.CappedState{}, err
			}
			if next.Version != r.Sequence || cappedDigest(after) != r.StateSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
			state, modern = next, true
			if publish != nil {
				publish(state.Version, true, after)
			}
		}
		if legacyDirty {
			if err = save(); err != nil {
				return account.CappedState{}, err
			}
		}
	}
	return state, nil
}
