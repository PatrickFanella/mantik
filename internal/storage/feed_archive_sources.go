package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxFeedArchiveSourceBytes = (2 << 20) + 16384

// FeedArchivedSource selects original owner bytes by identity. Its envelope
// binds one field of a stored archive, not a new source or operational grant.
type FeedArchivedSource struct {
	Schema       string            `json:"schema"`
	Archive      FeedArchiveHeader `json:"archive"`
	Kind         string            `json:"kind"`
	ID           string            `json:"id"`
	RecordSHA256 string            `json:"record_sha256"`
	Payload      json.RawMessage   `json:"payload"`
}

func validArchivedSourceKey(kind, id string) bool {
	return slices.Contains([]string{"publication", "admission", "input", "command"}, kind) &&
		len(id) > 0 && len(id) <= 256 && !strings.ContainsFunc(id, func(r rune) bool { return r < 32 || r == 127 })
}

func (s FeedArchivedSource) Validate() error {
	if s.Schema != "mantik-feed-archive-source-v1" || s.Archive.Validate() != nil ||
		!validArchivedSourceKey(s.Kind, s.ID) || len(s.Payload) == 0 || len(s.Payload) > MaxFeedArchiveSourceBytes ||
		!json.Valid(s.Payload) || !decisionPin.MatchString(s.RecordSHA256) || workerplan.Hash(s.Payload) != s.RecordSHA256 {
		return ErrFeedArchiveInvalid
	}
	return nil
}

type feedArchiveSourceRef struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Sequence     int64  `json:"sequence"`
	RecordSHA256 string `json:"record_sha256"`
}

type FeedArchiveSourceStore interface {
	FeedArchiveSource(context.Context, string, string) (FeedArchivedSource, error)
	AuditFeedArchiveSources(context.Context) (FeedArchiveSourceAudit, error)
}

// FeedArchiveSourceAudit reports a full source/index comparison. It does not
// execute research, re-evaluate strategies or grant qualification.
type FeedArchiveSourceAudit struct {
	ReceiverID   string `json:"receiver_id"`
	Status       string `json:"status"`
	Archives     int64  `json:"archives"`
	Sources      int64  `json:"sources"`
	HeadSHA256   string `json:"head_sha256"`
	SourceSHA256 string `json:"source_sha256"`
}

func archivedSourceKey(kind, id string) string { return kind + ":" + id }

func archiveSourceRecords(a FeedArchive) (map[string]FeedArchivedSource, error) {
	if a.Validate() != nil {
		return nil, ErrFeedArchiveConflict
	}
	out := map[string]FeedArchivedSource{}
	add := func(kind, id string, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		s := FeedArchivedSource{Schema: "mantik-feed-archive-source-v1", Archive: a.FeedArchiveHeader,
			Kind: kind, ID: id, Payload: payload, RecordSHA256: workerplan.Hash(json.RawMessage(payload))}
		if s.Validate() != nil {
			return ErrFeedArchiveInvalid
		}
		out[archivedSourceKey(kind, id)] = s
		return nil
	}
	for id, record := range a.Source.Records {
		if err := add("publication", id, record); err != nil {
			return nil, err
		}
	}
	for id, record := range a.Source.Collectors.Admissions {
		if err := add("admission", id, record); err != nil {
			return nil, err
		}
	}
	for id, record := range a.Source.Collectors.Commands {
		if err := add("command", id, record); err != nil {
			return nil, err
		}
	}
	if a.Source.Routing != nil {
		for id, record := range a.Source.Routing.Inputs {
			if err := add("input", id, record); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func sortedSourceRefs(records map[string]FeedArchivedSource) []feedArchiveSourceRef {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	refs := make([]feedArchiveSourceRef, 0, len(keys))
	for _, key := range keys {
		r := records[key]
		refs = append(refs, feedArchiveSourceRef{r.Kind, r.ID, r.Archive.Sequence, r.RecordSHA256})
	}
	return refs
}

func feedArchiveIndexAdditions(index map[string]feedArchiveSourceRef, a FeedArchive) (map[string]feedArchiveSourceRef, error) {
	records, err := archiveSourceRecords(a)
	if err != nil {
		return nil, err
	}
	additions := map[string]feedArchiveSourceRef{}
	for key, record := range records {
		if old, ok := index[key]; ok {
			if old.Kind != record.Kind || old.ID != record.ID || old.RecordSHA256 != record.RecordSHA256 || old.Sequence > a.Sequence {
				return nil, ErrFeedArchiveConflict
			}
			continue
		}
		additions[key] = feedArchiveSourceRef{record.Kind, record.ID, a.Sequence, record.RecordSHA256}
	}
	return additions, nil
}

// Audit builds its private expected index one segment at a time. Routine
// archive creation applies only the prevalidated segment additions under lock.
func indexFeedArchive(index map[string]feedArchiveSourceRef, a FeedArchive) (map[string]feedArchiveSourceRef, error) {
	additions, err := feedArchiveIndexAdditions(index, a)
	if err != nil {
		return nil, err
	}
	if index == nil {
		index = map[string]feedArchiveSourceRef{}
	}
	for key, ref := range additions {
		index[key] = ref
	}
	return index, nil
}

func (m *Memory) FeedArchiveSource(ctx context.Context, kind, id string) (FeedArchivedSource, error) {
	if !validArchivedSourceKey(kind, id) {
		return FeedArchivedSource{}, ErrFeedArchiveInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FeedArchivedSource{}, err
	}
	ref, ok := m.feedArchiveRecords[archivedSourceKey(kind, id)]
	if !ok {
		return FeedArchivedSource{}, ErrFeedArchiveNotFound
	}
	if ref.Kind != kind || ref.ID != id || ref.Sequence < 1 || ref.Sequence > int64(len(m.feedArchives)) {
		return FeedArchivedSource{}, ErrFeedArchiveConflict
	}
	records, err := archiveSourceRecords(m.feedArchives[ref.Sequence-1])
	if err != nil {
		return FeedArchivedSource{}, err
	}
	record, ok := records[archivedSourceKey(kind, id)]
	if !ok || record.RecordSHA256 != ref.RecordSHA256 || record.Archive.ReceiverID != m.receiverID {
		return FeedArchivedSource{}, ErrFeedArchiveConflict
	}
	return clone(record), nil
}

func writeFeedArchiveSourceIndex(ctx context.Context, tx pgx.Tx, a FeedArchive) error {
	records, err := archiveSourceRecords(a)
	if err != nil {
		return err
	}
	refs := sortedSourceRefs(records)
	raw, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	// One bounded segment is one insert batch, rather than two network round
	// trips per source. Conflicts retain the original reference and are checked
	// against every selected source before the manifest can commit.
	const wanted = `jsonb_to_recordset($1::jsonb) AS w(kind text,id text,sequence bigint,record_sha256 text)`
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archive_sources(kind,source_id,archive_sequence,record_sha256) SELECT kind,id,sequence,record_sha256 FROM "+wanted+" WHERE true ON CONFLICT(kind,source_id) DO NOTHING", raw); err != nil {
		return err
	}
	var conflict bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+wanted+" LEFT JOIN mantik_feed_archive_sources i ON i.kind=w.kind AND i.source_id=w.id WHERE i.source_id IS NULL OR i.archive_sequence>w.sequence OR i.record_sha256<>w.record_sha256)", raw).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return ErrFeedArchiveConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO mantik_feed_archive_source_batches(archive_sequence,archive_sha256,source_count,source_sha256) VALUES($1,$2,$3,$4)", a.Sequence, a.SHA256, len(refs), workerplan.Hash(refs))
	return err
}

func readFeedArchiveSourceBatch(ctx context.Context, tx pgx.Tx, a FeedArchive, refs []feedArchiveSourceRef) error {
	var count int
	var archiveSHA, sourceSHA string
	err := tx.QueryRow(ctx, "SELECT archive_sha256,source_count,source_sha256 FROM mantik_feed_archive_source_batches WHERE archive_sequence=$1", a.Sequence).Scan(&archiveSHA, &count, &sourceSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFeedArchiveConflict
	}
	if err != nil {
		return err
	}
	if archiveSHA != a.SHA256 || count != len(refs) || sourceSHA != workerplan.Hash(refs) {
		return ErrFeedArchiveConflict
	}
	return nil
}

func backfillFeedArchiveSourceIndex(ctx context.Context, tx pgx.Tx) error {
	// Startup already holds the schema advisory lock. Collector ownership also
	// serializes this bounded-per-segment backfill with any running archiver.
	if _, err := tx.Exec(ctx, "SELECT singleton FROM mantik_feed_collectors WHERE singleton FOR UPDATE"); err != nil {
		return err
	}
	for {
		var sequence int64
		err := tx.QueryRow(ctx, "SELECT a.sequence FROM mantik_feed_archives a LEFT JOIN mantik_feed_archive_source_batches b ON b.archive_sequence=a.sequence WHERE b.archive_sequence IS NULL ORDER BY a.sequence LIMIT 1").Scan(&sequence)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		a, err := readFeedArchive(ctx, tx, sequence)
		if err != nil {
			return err
		}
		if err := writeFeedArchiveSourceIndex(ctx, tx, a); err != nil {
			return err
		}
	}
}

func (p *Postgres) FeedArchiveSource(ctx context.Context, kind, id string) (FeedArchivedSource, error) {
	if !validArchivedSourceKey(kind, id) {
		return FeedArchivedSource{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedArchivedSource{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var sequence int64
	var digest string
	err = tx.QueryRow(ctx, "SELECT archive_sequence,record_sha256 FROM mantik_feed_archive_sources WHERE kind=$1 AND source_id=$2", kind, id).Scan(&sequence, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeedArchivedSource{}, ErrFeedArchiveNotFound
	}
	if err != nil {
		return FeedArchivedSource{}, err
	}
	a, err := readFeedArchive(ctx, tx, sequence)
	if err != nil {
		return FeedArchivedSource{}, err
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return FeedArchivedSource{}, err
	}
	if owner != a.ReceiverID {
		return FeedArchivedSource{}, ErrFeedArchiveConflict
	}
	records, err := archiveSourceRecords(a)
	if err != nil {
		return FeedArchivedSource{}, err
	}
	if err := readFeedArchiveSourceBatch(ctx, tx, a, sortedSourceRefs(records)); err != nil {
		return FeedArchivedSource{}, err
	}
	r, ok := records[archivedSourceKey(kind, id)]
	if !ok || r.RecordSHA256 != digest {
		return FeedArchivedSource{}, ErrFeedArchiveConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedArchivedSource{}, err
	}
	return r, nil
}

func (m *Memory) AuditFeedArchiveSources(ctx context.Context) (FeedArchiveSourceAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := FeedArchiveSourceAudit{ReceiverID: m.receiverID, Status: "verified"}
	expected := map[string]feedArchiveSourceRef{}
	previous := ""
	for index, a := range m.feedArchives {
		if err := ctx.Err(); err != nil {
			return FeedArchiveSourceAudit{}, err
		}
		if a.Sequence != int64(index+1) || a.PreviousSHA256 != previous || a.ReceiverID != m.receiverID {
			return FeedArchiveSourceAudit{}, ErrFeedArchiveConflict
		}
		var err error
		expected, err = indexFeedArchive(expected, a)
		if err != nil {
			return FeedArchiveSourceAudit{}, err
		}
		out.Archives, previous = a.Sequence, a.SHA256
	}
	if workerplan.Hash(expected) != workerplan.Hash(m.feedArchiveRecords) && !(len(expected) == 0 && len(m.feedArchiveRecords) == 0) {
		return FeedArchiveSourceAudit{}, ErrFeedArchiveConflict
	}
	if err := ctx.Err(); err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	out.Sources, out.HeadSHA256, out.SourceSHA256 = int64(len(expected)), previous, workerplan.Hash(expected)
	return out, nil
}

func (p *Postgres) AuditFeedArchiveSources(ctx context.Context) (FeedArchiveSourceAudit, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out := FeedArchiveSourceAudit{Status: "verified"}
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ReceiverID); err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	expected := map[string]feedArchiveSourceRef{}
	previous := ""
	for sequence := int64(1); ; sequence++ {
		a, err := readFeedArchive(ctx, tx, sequence)
		if errors.Is(err, ErrFeedArchiveNotFound) {
			break
		}
		if err != nil {
			return FeedArchiveSourceAudit{}, err
		}
		if a.PreviousSHA256 != previous || a.ReceiverID != out.ReceiverID {
			return FeedArchiveSourceAudit{}, ErrFeedArchiveConflict
		}
		records, err := archiveSourceRecords(a)
		if err != nil {
			return FeedArchiveSourceAudit{}, err
		}
		refs := sortedSourceRefs(records)
		if err := readFeedArchiveSourceBatch(ctx, tx, a, refs); err != nil {
			return FeedArchiveSourceAudit{}, err
		}
		for _, ref := range refs {
			key := archivedSourceKey(ref.Kind, ref.ID)
			if old, ok := expected[key]; ok {
				if old.RecordSHA256 != ref.RecordSHA256 {
					return FeedArchiveSourceAudit{}, ErrFeedArchiveConflict
				}
				continue
			}
			expected[key] = ref
		}
		previous, out.Archives = a.SHA256, sequence
	}
	rows, err := tx.Query(ctx, "SELECT kind,source_id,archive_sequence,record_sha256 FROM mantik_feed_archive_sources ORDER BY kind,source_id")
	if err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	actual := map[string]feedArchiveSourceRef{}
	for rows.Next() {
		var ref feedArchiveSourceRef
		if err := rows.Scan(&ref.Kind, &ref.ID, &ref.Sequence, &ref.RecordSHA256); err != nil {
			rows.Close()
			return FeedArchiveSourceAudit{}, err
		}
		actual[archivedSourceKey(ref.Kind, ref.ID)] = ref
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	var total int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM mantik_feed_archives").Scan(&total); err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	if total != out.Archives || workerplan.Hash(actual) != workerplan.Hash(expected) {
		return FeedArchiveSourceAudit{}, fmt.Errorf("%w: source index differs from archived owner records", ErrFeedArchiveConflict)
	}
	out.Sources, out.HeadSHA256, out.SourceSHA256 = int64(len(expected)), previous, workerplan.Hash(expected)
	if err := tx.Commit(ctx); err != nil {
		return FeedArchiveSourceAudit{}, err
	}
	return out, nil
}
