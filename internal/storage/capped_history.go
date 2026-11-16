package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"github.com/jackc/pgx/v5"
)

// MaxCappedHistoryPage bounds inspection/recovery independently of journal age.
const MaxCappedHistoryPage = 256
const maxCappedCommandBytes = 4 << 20

// CappedHistoryRecord retains an original command and the hashes of its replay
// transition. Legacy v121 records have empty transition hashes. CommandSHA256
// always hashes the original command's canonical JSON, never its checkpoint.
type CappedHistoryRecord struct {
	Sequence                                   int64
	Command                                    account.BookCommand
	CommandSHA256, PreviousSHA256, StateSHA256 string
}

// CappedHistoryStore exposes immutable originals for inspection and recovery.
// After is an exclusive original command sequence, not a hot-journal index.
type CappedHistoryStore interface {
	CappedBookHistory(context.Context, string, int64, int) ([]CappedHistoryRecord, error)
}

func cappedDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sameCappedCommand(a, b account.BookCommand) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}

func newCappedHistory(before, after account.CappedState, c account.BookCommand) (CappedHistoryRecord, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return CappedHistoryRecord{}, err
	}
	if len(raw) > maxCappedCommandBytes {
		return CappedHistoryRecord{}, errors.New("capped command exceeds archive bound")
	}
	a, err := json.Marshal(before)
	if err != nil {
		return CappedHistoryRecord{}, err
	}
	b, err := json.Marshal(after)
	if err != nil {
		return CappedHistoryRecord{}, err
	}
	var copied account.BookCommand
	if err = json.Unmarshal(raw, &copied); err != nil {
		return CappedHistoryRecord{}, err
	}
	return CappedHistoryRecord{after.Version, copied, cappedDigest(raw), cappedDigest(a), cappedDigest(b)}, nil
}

func cappedHistoryCommand(ctx context.Context, q cappedQuery, id, event string) (account.BookCommand, bool, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT payload FROM mantik_capped_book_history WHERE book_id=$1 AND event_id=$2 UNION ALL SELECT payload FROM mantik_capped_book_events WHERE book_id=$1 AND event_id=$2`, id, event).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return account.BookCommand{}, false, nil
	}
	if err != nil {
		return account.BookCommand{}, false, err
	}
	var c account.BookCommand
	err = json.Unmarshal(raw, &c)
	return c, true, err
}

func validCappedPage(after int64, limit int) bool {
	return after >= 0 && limit > 0 && limit <= MaxCappedHistoryPage
}

// CappedBookHistory returns detached immutable originals, including observations
// folded into the hot risk checkpoint. Pages contain at most 8 MiB of commands.
func (m *Memory) CappedBookHistory(ctx context.Context, id string, after int64, limit int) ([]CappedHistoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validCappedPage(after, limit) {
		return nil, errors.New("invalid capped history page")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.cappedBooks[id]; !ok {
		return nil, account.ErrBookNotFound
	}
	out := []CappedHistoryRecord{}
	order := m.cappedHistoryOrder[id]
	if after >= int64(len(order)) {
		return out, nil
	}
	end := min(int64(len(order)), after+int64(limit))
	for _, eventID := range order[after:end] {
		out = append(out, m.cappedHistory[id][eventID])
	}
	size := 0
	for i, r := range out {
		raw, err := json.Marshal(r.Command)
		if err != nil {
			return nil, err
		}
		size += len(raw)
		if size > 8<<20 {
			if i == 0 {
				return nil, errors.New("legacy capped command exceeds history page bound")
			}
			out = out[:i]
			break
		}
	}
	return clone(out), nil
}

// CappedBookHistory pages indexed immutable custody without reading older pages.
func (p *Postgres) CappedBookHistory(ctx context.Context, id string, after int64, limit int) ([]CappedHistoryRecord, error) {
	if !validCappedPage(after, limit) {
		return nil, errors.New("invalid capped history page")
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mantik_capped_books WHERE id=$1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, account.ErrBookNotFound
	}
	return readCappedHistory(ctx, p.pool, id, after, limit)
}

// readCappedHistory uses the caller's query/transaction for a pinned version.
func readCappedHistory(ctx context.Context, q cappedQuery, id string, after int64, limit int) ([]CappedHistoryRecord, error) {
	rows, err := q.Query(ctx, `SELECT sequence,payload,previous_sha256,state_sha256 FROM (
 (SELECT sequence,payload,'' AS previous_sha256,'' AS state_sha256 FROM mantik_capped_book_events WHERE book_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3)
 UNION ALL (SELECT sequence,payload,previous_sha256,state_sha256 FROM mantik_capped_book_history WHERE book_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3)
 ) history ORDER BY sequence LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CappedHistoryRecord{}
	size := 0
	for rows.Next() {
		var r CappedHistoryRecord
		var raw []byte
		if err = rows.Scan(&r.Sequence, &raw, &r.PreviousSHA256, &r.StateSHA256); err != nil {
			return nil, err
		}
		size += len(raw)
		if size > 8<<20 {
			if len(out) == 0 {
				return nil, errors.New("legacy capped command exceeds history page bound")
			}
			break
		}
		if err = json.Unmarshal(raw, &r.Command); err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(r.Command)
		if err != nil {
			return nil, err
		}
		r.CommandSHA256 = cappedDigest(canonical)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplayCappedBookHistory reconstructs an exact original-command version from
// bounded custody pages. It grants no write authority. Legacy records are kept
// in their original representation so existing review hashes remain valid;
// later transitions must match both immutable projection hashes.
func ReplayCappedBookHistory(ctx context.Context, source CappedHistoryStore, cfg account.CappedConfig, version int64) (account.CappedState, error) {
	state, err := account.NewCappedState(cfg)
	if err != nil {
		return state, err
	}
	if version < 0 {
		return account.CappedState{}, account.ErrBookConflict
	}
	modern := false
	for state.Version < version {
		if err = ctx.Err(); err != nil {
			return account.CappedState{}, err
		}
		limit := min(int64(MaxCappedHistoryPage), version-state.Version)
		page, err := source.CappedBookHistory(ctx, cfg.ID, state.Version, int(limit))
		if err != nil {
			return account.CappedState{}, err
		}
		if len(page) == 0 {
			return account.CappedState{}, account.ErrBookConflict
		}
		for _, r := range page {
			raw, err := json.Marshal(r.Command)
			if err != nil {
				return account.CappedState{}, err
			}
			if r.Sequence != state.Version+1 || r.Sequence > version || cappedDigest(raw) != r.CommandSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
			if r.StateSHA256 == "" {
				if modern || r.PreviousSHA256 != "" || state.Version >= 4096 || r.Command.RiskCheckpoint != nil || r.Command.Kind == "risk_checkpoint" {
					return account.CappedState{}, account.ErrBookConflict
				}
				state.Events = append(state.Events, r.Command)
				state.Version++
				continue
			}
			modern = true
			before, err := json.Marshal(state)
			if err != nil {
				return account.CappedState{}, err
			}
			if cappedDigest(before) != r.PreviousSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
			state, err = account.ApplyCappedCommand(state, r.Command)
			if err != nil {
				return account.CappedState{}, err
			}
			after, err := json.Marshal(state)
			if err != nil {
				return account.CappedState{}, err
			}
			if cappedDigest(after) != r.StateSHA256 {
				return account.CappedState{}, account.ErrBookConflict
			}
		}
	}
	if err = account.ValidateCappedState(state); err != nil {
		return account.CappedState{}, err
	}
	return state, nil
}

// retainCappedLocked publishes a validated transition under the caller's book lock.
// Privileged qualification and ordinary commands share the same immutable custody.
func (m *Memory) retainCappedLocked(before, after account.CappedState, c account.BookCommand) error {
	if after.Version != before.Version {
		record, err := newCappedHistory(before, after, c)
		if err != nil {
			return err
		}
		id := before.Config.ID
		if m.cappedHistory == nil {
			m.cappedHistory = map[string]map[string]CappedHistoryRecord{}
		}
		if m.cappedHistory[id] == nil {
			m.cappedHistory[id] = map[string]CappedHistoryRecord{}
		}
		if m.cappedHistoryOrder == nil {
			m.cappedHistoryOrder = map[string][]string{}
		}
		m.cappedHistory[id][c.ID] = record
		m.cappedHistoryOrder[id] = append(m.cappedHistoryOrder[id], c.ID)
	}
	m.cappedBooks[before.Config.ID] = after
	return nil
}

// retainCappedTx keeps the replay head and its original command in one transaction.
// The caller holds the book row and any required family locks. Nothing is
// published to the shared replay cache here: the enclosing commit can still fail.
// A later committed read derives and caches the newly retained transition.
func retainCappedTx(ctx context.Context, tx pgx.Tx, before, after account.CappedState, c account.BookCommand) error {
	record, err := newCappedHistory(before, after, c)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	id := before.Config.ID
	if _, err = tx.Exec(ctx, `INSERT INTO mantik_capped_book_history(book_id,sequence,event_id,payload,previous_sha256,state_sha256) VALUES($1,$2,$3,$4,$5,$6)`, id, after.Version, c.ID, string(raw), record.PreviousSHA256, record.StateSHA256); err != nil {
		return err
	}
	head, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mantik_capped_book_heads(book_id,version,state,state_sha256) VALUES($1,$2,$3,$4) ON CONFLICT(book_id) DO UPDATE SET version=EXCLUDED.version,state=EXCLUDED.state,state_sha256=EXCLUDED.state_sha256`, id, after.Version, string(head), record.StateSHA256)
	return err
}
