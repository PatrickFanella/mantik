package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxFeedArchiveBytes = 64 << 20
const MaxFeedArchivePage = 32

var ErrFeedArchiveInvalid = errors.New("invalid feed archive")
var ErrFeedArchiveConflict = errors.New("feed archive source or chain conflict")
var ErrFeedArchiveNotFound = errors.New("feed archive not found")
var ErrFeedArchiveCapacity = errors.New("feed archive segment exceeds byte limit")

// FeedArchiveHeader binds an immutable source cut and its position in the
// receiver's archive chain. It grants no new interpretation or operation.
type FeedArchiveHeader struct {
	Schema              string    `json:"schema"`
	ReceiverID          string    `json:"receiver_id"`
	Sequence            int64     `json:"sequence"`
	ArchivedAt          time.Time `json:"archived_at"`
	PreviousSHA256      string    `json:"previous_sha256"`
	SourceSHA256        string    `json:"source_sha256"`
	Registrations       int       `json:"registrations"`
	Publications        int       `json:"publications"`
	Admissions          int       `json:"admissions"`
	Commands            int       `json:"commands"`
	Authority           string    `json:"authority"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}

func (h FeedArchiveHeader) Validate() error {
	sha := h.SHA256
	h.SHA256 = ""
	if h.Schema != "mantik-feed-archive-v1" || !ValidReceiverID(h.ReceiverID) || h.Sequence < 1 || !declarationTime(h.ArchivedAt) ||
		(h.Sequence == 1 && h.PreviousSHA256 != "") || (h.Sequence > 1 && !decisionPin.MatchString(h.PreviousSHA256)) ||
		!decisionPin.MatchString(h.SourceSHA256) || h.Registrations < 0 || h.Publications < 0 || h.Admissions < 0 || h.Commands < 0 ||
		h.Authority != "service_owned_feed_archive_only" || h.QualificationCredit || !decisionPin.MatchString(sha) || workerplan.Hash(h) != sha {
		return ErrFeedArchiveInvalid
	}
	return nil
}

// FeedArchive retains original publications, admissions, commands and routing
// state in one owner snapshot. Active sources remain unchanged in this version.
type FeedArchive struct {
	FeedArchiveHeader
	Source FeedEvidenceSnapshot `json:"source"`
}

func (a FeedArchive) Validate() error {
	h, s := a.FeedArchiveHeader, a.Source
	if h.Validate() != nil || h.ReceiverID != s.ReceiverID || workerplan.Hash(s) != h.SourceSHA256 ||
		h.Registrations != len(s.Collectors.Entries) || h.Publications != len(s.Records) ||
		h.Admissions != len(s.Collectors.Admissions) || h.Commands != len(s.Collectors.Commands) || s.Collectors.LastClock.After(h.ArchivedAt) {
		return ErrFeedArchiveInvalid
	}
	return nil
}

type FeedArchiveReceipt struct {
	Header    FeedArchiveHeader `json:"header"`
	Duplicate bool              `json:"duplicate"`
}

type FeedArchivePage struct {
	ReceiverID string              `json:"receiver_id"`
	Records    []FeedArchiveHeader `json:"records"`
	NextAfter  int64               `json:"next_after,omitempty"`
}

// FeedArchiveCheck replays source contracts under the ownership lock. Like an
// EvidenceCheck it is service code, must be pure, and is never supplied by HTTP.
type FeedArchiveCheck func(FeedEvidenceSnapshot, time.Time) error

type FeedArchiveStore interface {
	ArchiveFeed(context.Context, string, func() time.Time, FeedArchiveCheck) (FeedArchiveReceipt, error)
	FeedArchive(context.Context, int64) (FeedArchive, error)
	FeedArchives(context.Context, int64, int) (FeedArchivePage, error)
}

func canonicalArchiveSource(source FeedEvidenceSnapshot) (FeedEvidenceSnapshot, error) {
	source = clone(source)
	for key, record := range source.Records {
		raw, err := canonicalRecording(record.Recording)
		if err != nil {
			return FeedEvidenceSnapshot{}, ErrFeedArchiveInvalid
		}
		record.Recording = json.RawMessage(raw)
		source.Records[key] = record
	}
	return source, nil
}

// Before active pruning is supported, every previous immutable source must
// remain byte-equivalent. Mutable lease/routing projections may advance.
func extendsFeedArchive(previous, current FeedEvidenceSnapshot) error {
	if previous.ReceiverID != current.ReceiverID || current.Collectors.LastClock.Before(previous.Collectors.LastClock) {
		return ErrFeedArchiveConflict
	}
	for id, old := range previous.Collectors.Entries {
		next, ok := current.Collectors.Entries[id]
		if !ok || old.Registration != next.Registration || !old.RegisteredAt.Equal(next.RegisteredAt) {
			return ErrFeedArchiveConflict
		}
	}
	for id, old := range previous.Collectors.Commands {
		if next, ok := current.Collectors.Commands[id]; !ok || !reflect.DeepEqual(old, next) {
			return ErrFeedArchiveConflict
		}
	}
	for id, old := range previous.Collectors.Admissions {
		if next, ok := current.Collectors.Admissions[id]; !ok || !reflect.DeepEqual(old, next) {
			return ErrFeedArchiveConflict
		}
	}
	for key, old := range previous.Records {
		if next, ok := current.Records[key]; !ok || !sameFeedRecord(old, next) {
			return ErrFeedArchiveConflict
		}
	}
	if previous.Routing != nil {
		for id, old := range previous.Routing.Inputs {
			if current.Routing == nil || !reflect.DeepEqual(old, current.Routing.Inputs[id]) {
				return ErrFeedArchiveConflict
			}
		}
	}
	return nil
}

func newFeedArchive(source FeedEvidenceSnapshot, previous FeedArchiveHeader, at time.Time, check FeedArchiveCheck) (FeedArchive, error) {
	if check == nil || !declarationTime(at) || at.Before(source.Collectors.LastClock) ||
		previous.Sequence > 0 && (previous.Validate() != nil || previous.ReceiverID != source.ReceiverID || at.Before(previous.ArchivedAt)) {
		return FeedArchive{}, ErrFeedArchiveInvalid
	}
	if err := check(clone(source), at); err != nil {
		return FeedArchive{}, err
	}
	a := FeedArchive{Source: source, FeedArchiveHeader: FeedArchiveHeader{Schema: "mantik-feed-archive-v1",
		ReceiverID: source.ReceiverID, Sequence: previous.Sequence + 1, ArchivedAt: at.UTC(), PreviousSHA256: previous.SHA256,
		SourceSHA256: workerplan.Hash(source), Registrations: len(source.Collectors.Entries), Publications: len(source.Records),
		Admissions: len(source.Collectors.Admissions), Commands: len(source.Collectors.Commands), Authority: "service_owned_feed_archive_only"}}
	a.SHA256 = workerplan.Hash(a.FeedArchiveHeader)
	if err := a.Validate(); err != nil {
		return FeedArchive{}, err
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > MaxFeedArchiveBytes {
		return FeedArchive{}, ErrFeedArchiveCapacity
	}
	return a, nil
}

func (m *Memory) ArchiveFeed(ctx context.Context, receiver string, now func() time.Time, check FeedArchiveCheck) (FeedArchiveReceipt, error) {
	if !ValidReceiverID(receiver) || now == nil || check == nil {
		return FeedArchiveReceipt{}, ErrFeedArchiveInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FeedArchiveReceipt{}, err
	}
	if receiver != m.receiverID {
		return FeedArchiveReceipt{}, ErrReceiverMismatch
	}
	source, err := canonicalArchiveSource(m.decisionFeedLocked())
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	digest := workerplan.Hash(source)
	var previous FeedArchiveHeader
	if sequence, exists := m.feedArchiveSources[digest]; exists {
		if sequence < 1 || sequence != int64(len(m.feedArchives)) {
			return FeedArchiveReceipt{}, ErrFeedArchiveConflict
		}
		old := m.feedArchives[sequence-1]
		if old.Validate() != nil || old.SourceSHA256 != digest || old.Sequence != sequence {
			return FeedArchiveReceipt{}, ErrFeedArchiveConflict
		}
		if err := check(clone(old.Source), old.ArchivedAt); err != nil {
			return FeedArchiveReceipt{}, err
		}
		return FeedArchiveReceipt{old.FeedArchiveHeader, true}, nil
	}
	if len(m.feedArchives) > 0 {
		last := m.feedArchives[len(m.feedArchives)-1]
		if last.Validate() != nil || last.Sequence != int64(len(m.feedArchives)) {
			return FeedArchiveReceipt{}, ErrFeedArchiveConflict
		}
		previous = last.FeedArchiveHeader
		if err := extendsFeedArchive(last.Source, source); err != nil {
			return FeedArchiveReceipt{}, err
		}
	}
	a, err := newFeedArchive(source, previous, now(), check)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return FeedArchiveReceipt{}, err
	}
	m.feedArchives = append(m.feedArchives, clone(a))
	if m.feedArchiveSources == nil {
		m.feedArchiveSources = map[string]int64{}
	}
	m.feedArchiveSources[a.SourceSHA256] = a.Sequence
	return FeedArchiveReceipt{a.FeedArchiveHeader, false}, nil
}

func (m *Memory) FeedArchive(ctx context.Context, sequence int64) (FeedArchive, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FeedArchive{}, err
	}
	if sequence < 1 {
		return FeedArchive{}, ErrFeedArchiveInvalid
	}
	if sequence > int64(len(m.feedArchives)) {
		return FeedArchive{}, ErrFeedArchiveNotFound
	}
	a := m.feedArchives[sequence-1]
	if a.Validate() != nil || a.Sequence != sequence || a.ReceiverID != m.receiverID || sequence > 1 && a.PreviousSHA256 != m.feedArchives[sequence-2].SHA256 {
		return FeedArchive{}, ErrFeedArchiveConflict
	}
	return clone(a), nil
}

func validArchivePage(after int64, limit int) bool {
	return after >= 0 && limit >= 1 && limit <= MaxFeedArchivePage
}

func (m *Memory) FeedArchives(ctx context.Context, after int64, limit int) (FeedArchivePage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FeedArchivePage{}, err
	}
	if !validArchivePage(after, limit) {
		return FeedArchivePage{}, ErrFeedArchiveInvalid
	}
	out := FeedArchivePage{ReceiverID: m.receiverID, Records: []FeedArchiveHeader{}}
	for index := after; index < int64(len(m.feedArchives)) && len(out.Records) <= limit; index++ {
		h := m.feedArchives[index].FeedArchiveHeader
		if h.Validate() != nil || h.Sequence != index+1 || h.ReceiverID != m.receiverID || index > 0 && h.PreviousSHA256 != m.feedArchives[index-1].SHA256 {
			return FeedArchivePage{}, ErrFeedArchiveConflict
		}
		if len(out.Records) == limit {
			out.NextAfter = out.Records[len(out.Records)-1].Sequence
			break
		}
		out.Records = append(out.Records, h)
	}
	return out, nil
}

func readFeedArchive(ctx context.Context, tx pgx.Tx, sequence int64) (FeedArchive, error) {
	var raw, header []byte
	var sourceSHA, sha string
	err := tx.QueryRow(ctx, "SELECT source_sha256,sha256,header,payload FROM mantik_feed_archives WHERE sequence=$1", sequence).Scan(&sourceSHA, &sha, &header, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeedArchive{}, ErrFeedArchiveNotFound
	}
	if err != nil {
		return FeedArchive{}, err
	}
	var a FeedArchive
	var h FeedArchiveHeader
	if workerplan.ReadBoundedShape(raw, &a, MaxFeedArchiveBytes) != nil || workerplan.ReadBoundedShape(header, &h, 4096) != nil ||
		!reflect.DeepEqual(h, a.FeedArchiveHeader) || a.Validate() != nil || a.Sequence != sequence || a.SourceSHA256 != sourceSHA || a.SHA256 != sha {
		return FeedArchive{}, ErrFeedArchiveConflict
	}
	return a, nil
}

func (p *Postgres) ArchiveFeed(ctx context.Context, receiver string, now func() time.Time, check FeedArchiveCheck) (FeedArchiveReceipt, error) {
	if !ValidReceiverID(receiver) || now == nil || check == nil {
		return FeedArchiveReceipt{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Same collector -> routing lock order as publication and decision writers.
	source, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	if receiver != source.ReceiverID {
		return FeedArchiveReceipt{}, ErrReceiverMismatch
	}
	source, err = canonicalArchiveSource(source)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	var sequence int64
	err = tx.QueryRow(ctx, "SELECT sequence FROM mantik_feed_archives WHERE source_sha256=$1", workerplan.Hash(source)).Scan(&sequence)
	if err == nil {
		var latest int64
		if err := tx.QueryRow(ctx, "SELECT max(sequence) FROM mantik_feed_archives").Scan(&latest); err != nil {
			return FeedArchiveReceipt{}, err
		}
		if latest != sequence {
			return FeedArchiveReceipt{}, ErrFeedArchiveConflict
		}
		a, err := readFeedArchive(ctx, tx, sequence)
		if err != nil {
			return FeedArchiveReceipt{}, err
		}
		if err := check(clone(a.Source), a.ArchivedAt); err != nil {
			return FeedArchiveReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return FeedArchiveReceipt{}, err
		}
		return FeedArchiveReceipt{a.FeedArchiveHeader, true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return FeedArchiveReceipt{}, err
	}
	var raw []byte
	var previous FeedArchiveHeader
	err = tx.QueryRow(ctx, "SELECT header FROM mantik_feed_archives ORDER BY sequence DESC LIMIT 1").Scan(&raw)
	if err == nil {
		if workerplan.ReadBoundedShape(raw, &previous, 4096) != nil || previous.Validate() != nil || previous.ReceiverID != receiver {
			return FeedArchiveReceipt{}, ErrFeedArchiveConflict
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return FeedArchiveReceipt{}, err
	}
	if previous.Sequence > 0 {
		last, err := readFeedArchive(ctx, tx, previous.Sequence)
		if err != nil {
			return FeedArchiveReceipt{}, err
		}
		if err := extendsFeedArchive(last.Source, source); err != nil {
			return FeedArchiveReceipt{}, err
		}
	}
	a, err := newFeedArchive(source, previous, now(), check)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	raw, err = json.Marshal(a)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	header, err := json.Marshal(a.FeedArchiveHeader)
	if err != nil {
		return FeedArchiveReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archives(sequence,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5)", a.Sequence, a.SourceSHA256, a.SHA256, header, raw); err != nil {
		return FeedArchiveReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedArchiveReceipt{}, err
	}
	return FeedArchiveReceipt{a.FeedArchiveHeader, false}, nil
}

func (p *Postgres) FeedArchive(ctx context.Context, sequence int64) (FeedArchive, error) {
	if sequence < 1 {
		return FeedArchive{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedArchive{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	a, err := readFeedArchive(ctx, tx, sequence)
	if err != nil {
		return FeedArchive{}, err
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return FeedArchive{}, err
	}
	if a.ReceiverID != owner {
		return FeedArchive{}, ErrFeedArchiveConflict
	}
	if a.Sequence > 1 {
		var previous string
		if err := tx.QueryRow(ctx, "SELECT sha256 FROM mantik_feed_archives WHERE sequence=$1", a.Sequence-1).Scan(&previous); err != nil {
			return FeedArchive{}, ErrFeedArchiveConflict
		}
		if previous != a.PreviousSHA256 {
			return FeedArchive{}, ErrFeedArchiveConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedArchive{}, err
	}
	return a, nil
}

func (p *Postgres) FeedArchives(ctx context.Context, after int64, limit int) (FeedArchivePage, error) {
	if !validArchivePage(after, limit) {
		return FeedArchivePage{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedArchivePage{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out := FeedArchivePage{Records: []FeedArchiveHeader{}}
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ReceiverID); err != nil {
		return FeedArchivePage{}, err
	}
	rows, err := tx.Query(ctx, "SELECT sequence,source_sha256,sha256,header FROM mantik_feed_archives WHERE sequence>$1 ORDER BY sequence LIMIT $2", after, limit+1)
	if err != nil {
		return FeedArchivePage{}, err
	}
	for rows.Next() {
		var sequence int64
		var sourceSHA, sha string
		var raw []byte
		var h FeedArchiveHeader
		if err := rows.Scan(&sequence, &sourceSHA, &sha, &raw); err != nil {
			rows.Close()
			return FeedArchivePage{}, err
		}
		if workerplan.ReadBoundedShape(raw, &h, 4096) != nil || h.Validate() != nil || h.Sequence != sequence || h.Sequence != after+int64(len(out.Records))+1 || h.ReceiverID != out.ReceiverID || h.SHA256 != sha || h.SourceSHA256 != sourceSHA || len(out.Records) > 0 && h.PreviousSHA256 != out.Records[len(out.Records)-1].SHA256 {
			rows.Close()
			return FeedArchivePage{}, ErrFeedArchiveConflict
		}
		if len(out.Records) == limit {
			out.NextAfter = out.Records[len(out.Records)-1].Sequence
			break
		}
		out.Records = append(out.Records, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FeedArchivePage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedArchivePage{}, err
	}
	return out, nil
}
