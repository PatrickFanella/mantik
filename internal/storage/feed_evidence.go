package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"github.com/jackc/pgx/v5"
)

const MaxFeedEvidence = 2048

var evidenceKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var ErrEvidenceConflict = errors.New("feed evidence identity or history conflict")
var ErrEvidenceInvalid = errors.New("invalid feed evidence")
var ErrEvidenceCapacity = errors.New("feed evidence capacity exhausted")

type FeedEvidenceRecord struct {
	RegistrationID string              `json:"registration_id"`
	Key            string              `json:"key"`
	Fence          feedcollector.Fence `json:"fence"`
	PublishedAt    time.Time           `json:"published_at"`
	Recording      json.RawMessage     `json:"recording"`
}
type FeedEvidenceReceipt = feedcollector.EvidenceReceipt
type FeedEvidenceSnapshot struct {
	ReceiverID string
	Collectors feedcollector.State
	Routing    *domain.RoutingState
	Records    map[string]FeedEvidenceRecord
}

// EvidenceCheck is a pure validation callback under the storage locks. It must
// perform no I/O. A previous record is supplied only for an immutable retry.
type EvidenceCheck func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error
type FeedEvidenceStore interface {
	FeedEvidence(context.Context) (FeedEvidenceSnapshot, error)
	PublishFeedEvidence(context.Context, string, string, string, feedcollector.Fence, json.RawMessage, func() time.Time, EvidenceCheck) (FeedEvidenceReceipt, error)
}

func evidenceReceipt(r FeedEvidenceRecord, duplicate bool) FeedEvidenceReceipt {
	var value any
	_ = json.Unmarshal(r.Recording, &value)
	canonical, _ := json.Marshal(value)
	return FeedEvidenceReceipt{RegistrationID: r.RegistrationID, Key: r.Key, Fence: r.Fence, PublishedAt: r.PublishedAt, RecordingSHA256: fmt.Sprintf("%x", sha256.Sum256(canonical)), Duplicate: duplicate}
}
func evidenceRequest(receiver, registration, key string, f feedcollector.Fence, raw json.RawMessage) error {
	if !ValidReceiverID(receiver) || !evidenceKey.MatchString(registration) || !evidenceKey.MatchString(key) || f.RegistrationID != registration || feedcollector.ValidateFence(f) != nil || len(raw) == 0 || len(raw) > 2<<20 || !json.Valid(raw) {
		return ErrEvidenceInvalid
	}
	return nil
}
func evidencePrevious(records map[string]FeedEvidenceRecord, registration, key string, raw json.RawMessage) (*FeedEvidenceRecord, error) {
	old, ok := records[registration+":"+key]
	if !ok {
		return nil, nil
	}
	var a, b any
	if json.Unmarshal(old.Recording, &a) != nil || json.Unmarshal(raw, &b) != nil {
		return nil, ErrEvidenceInvalid
	}
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	if string(aRaw) != string(bRaw) {
		return nil, ErrEvidenceConflict
	}
	return &old, nil
}
func (m *Memory) FeedEvidence(ctx context.Context) (FeedEvidenceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return FeedEvidenceSnapshot{m.receiverID, clone(m.collectors), clone(m.state.Routing), clone(m.evidence)}, nil
}
func (m *Memory) PublishFeedEvidence(ctx context.Context, receiver, registration, key string, f feedcollector.Fence, raw json.RawMessage, now func() time.Time, check EvidenceCheck) (FeedEvidenceReceipt, error) {
	if err := evidenceRequest(receiver, registration, key, f, raw); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID {
		return FeedEvidenceReceipt{}, ErrReceiverMismatch
	}
	previous, err := evidencePrevious(m.evidence, registration, key, raw)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	at := now().UTC()
	snapshot := FeedEvidenceSnapshot{m.receiverID, clone(m.collectors), clone(m.state.Routing), clone(m.evidence)}
	if check == nil {
		return FeedEvidenceReceipt{}, ErrEvidenceInvalid
	}
	if err := check(snapshot, at, previous); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if previous != nil {
		return evidenceReceipt(*previous, true), nil
	}
	if err := m.sourceObservationClockLocked(at); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if len(m.evidence) >= MaxFeedEvidence {
		return FeedEvidenceReceipt{}, ErrEvidenceCapacity
	}
	record := FeedEvidenceRecord{registration, key, f, at, append(json.RawMessage{}, raw...)}
	if m.evidence == nil {
		m.evidence = map[string]FeedEvidenceRecord{}
	}
	m.evidence[registration+":"+key] = record
	m.collectors.LastClock = at
	return evidenceReceipt(record, false), nil
}

func readFeedEvidence(ctx context.Context, tx pgx.Tx, lock bool) (FeedEvidenceSnapshot, error) {
	var out FeedEvidenceSnapshot
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ReceiverID); err != nil {
		return out, err
	}
	query := "SELECT payload FROM mantik_feed_collectors WHERE singleton"
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	if err := tx.QueryRow(ctx, query).Scan(&raw); err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out.Collectors); err != nil {
		return out, err
	}
	query = "SELECT payload FROM mantik_state WHERE id='demo'"
	if lock {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRow(ctx, query).Scan(&raw); err != nil {
		return out, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return out, err
	}
	out.Routing = state.Routing
	out.Records = map[string]FeedEvidenceRecord{}
	rows, err := tx.Query(ctx, "SELECT registration_id,capture_key,payload FROM mantik_feed_evidence ORDER BY registration_id,capture_key LIMIT 129")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var registration, key string
		if err := rows.Scan(&registration, &key, &raw); err != nil {
			return out, err
		}
		var record FeedEvidenceRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return out, err
		}
		if record.RegistrationID != registration || record.Key != key {
			return out, ErrEvidenceConflict
		}
		out.Records[registration+":"+key] = record
	}
	if len(out.Records) > MaxFeedEvidence {
		return out, ErrEvidenceCapacity
	}
	return out, rows.Err()
}
func (p *Postgres) FeedEvidence(ctx context.Context) (FeedEvidenceSnapshot, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	return out, nil
}
func (p *Postgres) PublishFeedEvidence(ctx context.Context, receiver, registration, key string, f feedcollector.Fence, raw json.RawMessage, now func() time.Time, check EvidenceCheck) (FeedEvidenceReceipt, error) {
	if err := evidenceRequest(receiver, registration, key, f, raw); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if receiver != snapshot.ReceiverID {
		return FeedEvidenceReceipt{}, ErrReceiverMismatch
	}
	previous, err := evidencePrevious(snapshot.Records, registration, key, raw)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	at := now().UTC()
	if check == nil {
		return FeedEvidenceReceipt{}, ErrEvidenceInvalid
	}
	if err := check(snapshot, at, previous); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if previous != nil {
		if err := tx.Commit(ctx); err != nil {
			return FeedEvidenceReceipt{}, err
		}
		return evidenceReceipt(*previous, true), nil
	}
	if err := readSourceObservationClock(ctx, tx, receiver, at); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if len(snapshot.Records) >= MaxFeedEvidence {
		return FeedEvidenceReceipt{}, ErrEvidenceCapacity
	}
	record := FeedEvidenceRecord{registration, key, f, at, append(json.RawMessage{}, raw...)}
	encoded, err := json.Marshal(record)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_evidence VALUES($1,$2,$3)", registration, key, encoded); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	snapshot.Collectors.LastClock = at
	encoded, err = json.Marshal(snapshot.Collectors)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", encoded); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	return evidenceReceipt(record, false), nil
}
