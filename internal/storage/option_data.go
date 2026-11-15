package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"slices"
	"strings"
	"time"
)

// OptionRecord retains one reference, daily bar or trade observation. Data is
// the original source object; source pages and their digests live in the journal.
// Key identifies a session or source trade within a contract, not a ticker join.
type OptionRecord = alpacarecord.OptionRecord

// OptionReceipt counts new and replayed option observations.
type OptionReceipt = alpacarecord.OptionReceipt

var ErrOptionData = errors.New("invalid or conflicting option observation")

func optionIdentity(r OptionRecord) string {
	return r.Kind + "/" + r.Symbol + "/" + r.Key + "/" + r.AvailableAt.Format(time.RFC3339Nano)
}
func optionKey(r OptionRecord) string { return r.Kind + "/" + r.Symbol + "/" + r.Key }
func optionBatch(records []OptionRecord) ([]OptionRecord, error) {
	if len(records) < 1 || len(records) > 20000 {
		return nil, ErrOptionData
	}
	out := slices.Clone(records)
	seen := map[string][]byte{}
	for i := range out {
		r := &out[i]
		r.OccurredAt = dailyBarTime(r.OccurredAt)
		r.ObservedAt = dailyBarTime(r.ObservedAt)
		r.AvailableAt = dailyBarTime(r.AvailableAt)
		if !slices.Contains([]string{"contract", "bar", "trade"}, r.Kind) || len(r.Symbol) > 32 || !dailyBarAsset.MatchString(r.Symbol) || !dailyBarSymbol.MatchString(r.Underlying) || len(r.Key) < 1 || len(r.Key) > 128 || strings.ContainsAny(r.Key, "/\x00\r\n") || r.OccurredAt.IsZero() || r.ObservedAt.IsZero() || r.AvailableAt.Before(r.ObservedAt) || r.OccurredAt.After(r.AvailableAt) || (r.Kind == "contract" && r.Feed != "reference") || (r.Kind != "contract" && (r.Feed != "indicative" || r.FeedSelection != "provider_default_unverified")) || len(r.Data) > 65536 {
			return nil, ErrOptionData
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(r.Data, &obj) != nil || obj == nil {
			return nil, ErrOptionData
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		r.Data = raw
		raw, err = json.Marshal(r)
		if err != nil {
			return nil, err
		}
		key := optionIdentity(*r)
		if prior, ok := seen[key]; ok && !bytes.Equal(prior, raw) {
			return nil, ErrOptionData
		}
		seen[key] = raw
	}
	slices.SortFunc(out, func(a, b OptionRecord) int { return strings.Compare(optionIdentity(a), optionIdentity(b)) })
	return out, nil
}

// RecordOptionData atomically appends a page of normalized option observations.
// Replaying the same page changes nothing; a changed record at the same clock
// conflicts. New clocks retain corrections without rewriting earlier reads.
func (m *Memory) RecordOptionData(ctx context.Context, records []OptionRecord) (OptionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return OptionReceipt{}, err
	}
	records, err := optionBatch(records)
	if err != nil {
		return OptionReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := map[string]OptionRecord{}
	out := OptionReceipt{}
	for _, r := range records {
		id := optionIdentity(r)
		old, ok := m.optionData[id]
		if !ok {
			old, ok = pending[id]
		}
		if ok {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(r)
			if !bytes.Equal(a, b) {
				return OptionReceipt{}, ErrOptionData
			}
			out.Unchanged++
		} else {
			pending[id] = r
			out.Inserted++
		}
	}
	if m.optionData == nil {
		m.optionData = map[string]OptionRecord{}
	}
	for id, r := range pending {
		m.optionData[id] = r
	}
	return out, nil
}

// OptionDataAsOf returns the latest available version of each reference/bar/
// trade for one contract symbol, ordered by kind and source key.
func (m *Memory) OptionDataAsOf(ctx context.Context, symbol string, at time.Time) ([]OptionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !dailyBarAsset.MatchString(symbol) || at.IsZero() {
		return nil, ErrOptionData
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	latest := map[string]OptionRecord{}
	for _, r := range m.optionData {
		if r.Symbol != symbol || r.AvailableAt.After(at) {
			continue
		}
		k := optionKey(r)
		if old, ok := latest[k]; !ok || r.AvailableAt.After(old.AvailableAt) {
			latest[k] = r
		}
	}
	out := []OptionRecord{}
	for _, r := range latest {
		r.Data = bytes.Clone(r.Data)
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b OptionRecord) int { return strings.Compare(optionKey(a), optionKey(b)) })
	return out, nil
}

// RecordOptionData is the transactional PostgreSQL implementation. See Memory.
func (p *Postgres) RecordOptionData(ctx context.Context, records []OptionRecord) (OptionReceipt, error) {
	records, err := optionBatch(records)
	if err != nil {
		return OptionReceipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return OptionReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_option_data',0))"); err != nil {
		return OptionReceipt{}, err
	}
	out := OptionReceipt{}
	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			return OptionReceipt{}, err
		}
		tag, err := tx.Exec(ctx, "INSERT INTO mantik_option_data(kind,symbol,source_key,available_at,observed_at,feed,record) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", r.Kind, r.Symbol, r.Key, r.AvailableAt, r.ObservedAt, r.Feed, raw)
		if err != nil {
			return OptionReceipt{}, err
		}
		if tag.RowsAffected() == 1 {
			out.Inserted++
			continue
		}
		var old []byte
		if err := tx.QueryRow(ctx, "SELECT record FROM mantik_option_data WHERE kind=$1 AND symbol=$2 AND source_key=$3 AND available_at=$4", r.Kind, r.Symbol, r.Key, r.AvailableAt).Scan(&old); err != nil {
			return OptionReceipt{}, err
		}
		if !bytes.Equal(old, raw) {
			return OptionReceipt{}, ErrOptionData
		}
		out.Unchanged++
	}
	return out, tx.Commit(ctx)
}

// OptionDataAsOf reads one contract's point-in-time observations. See Memory.
func (p *Postgres) OptionDataAsOf(ctx context.Context, symbol string, at time.Time) ([]OptionRecord, error) {
	if !dailyBarAsset.MatchString(symbol) || at.IsZero() {
		return nil, ErrOptionData
	}
	rows, err := p.pool.Query(ctx, "SELECT DISTINCT ON(kind,source_key) record FROM mantik_option_data WHERE symbol=$1 AND available_at<=$2 ORDER BY kind,source_key,available_at DESC", symbol, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OptionRecord{}
	for rows.Next() {
		var raw []byte
		var r OptionRecord
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("decode option observation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
