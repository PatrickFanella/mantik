package storage

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// FeedArchivePrefix is a read of archived custody only. At is the requested cut
// or the latest archived cut, whichever is earlier; later active sources are
// not silently included. The archive header binds the independently selected
// source snapshot used to verify both prefix ends.
type FeedArchivePrefix struct {
	Archive        FeedArchiveHeader       `json:"archive"`
	RegistrationID string                  `json:"registration_id"`
	At             time.Time               `json:"at"`
	Prefix         PaperDecisionFeedPrefix `json:"prefix"`
}

type FeedArchivePrefixStore interface {
	FeedArchivePrefix(context.Context, string, time.Time) (FeedArchivePrefix, error)
}

type feedArchivePrefixPoint struct {
	Kind           string `json:"kind"`
	RegistrationID string `json:"registration_id"`
	SourceID       string `json:"source_id"`
	AtSeconds      int64  `json:"at_seconds"`
	AtNanos        int    `json:"at_nanos"`
	Count          int    `json:"row_count"`
	PreviousSHA256 string `json:"previous_sha256"`
	Digest         string `json:"digest"`
	RecordSHA256   string `json:"record_sha256"`
}

type feedArchivePrefixRef struct {
	Point    feedArchivePrefixPoint
	Sequence int64
}

func (p feedArchivePrefixPoint) at() time.Time       { return time.Unix(p.AtSeconds, int64(p.AtNanos)).UTC() }
func prefixPointKey(p feedArchivePrefixPoint) string { return archivedSourceKey(p.Kind, p.SourceID) }
func prefixGroup(p feedArchivePrefixPoint) string    { return p.Kind + ":" + p.RegistrationID }

// The digests are exactly the existing v3 decision/outcome registration-prefix
// digests. No new interpretation, evaluator or timestamp replaces source data.
func archivePrefixPoints(a FeedArchive) ([]feedArchivePrefixPoint, error) {
	if a.Validate() != nil {
		return nil, ErrFeedArchiveConflict
	}
	registrations := make([]string, 0, len(a.Source.Collectors.Entries))
	for id, entry := range a.Source.Collectors.Entries {
		if !decisionRegistration.MatchString(id) || entry.Registration.ID != id || !declarationTime(entry.RegisteredAt) || entry.RegisteredAt.After(a.ArchivedAt) {
			return nil, ErrFeedArchiveConflict
		}
		registrations = append(registrations, id)
	}
	for key, r := range a.Source.Records {
		if key != r.RegistrationID+":"+r.Key {
			return nil, ErrFeedArchiveConflict
		}
		if _, ok := a.Source.Collectors.Entries[r.RegistrationID]; !ok {
			return nil, ErrFeedArchiveConflict
		}
	}
	for _, r := range a.Source.Collectors.Admissions {
		if _, ok := a.Source.Collectors.Entries[r.Fence.RegistrationID]; !ok {
			return nil, ErrFeedArchiveConflict
		}
	}
	slices.Sort(registrations)
	points := []feedArchivePrefixPoint{}
	index := newFeedPrefixIndex(a.Source)
	for _, id := range registrations {
		chain := index.chains(id)
		previous := ""
		for i, c := range chain.clocks {
			at := c.publishedAt
			if !declarationTime(at) || at.After(a.ArchivedAt) {
				return nil, ErrFeedArchiveConflict
			}
			points = append(points, feedArchivePrefixPoint{"publication", id, c.key, at.Unix(), at.Nanosecond(), i + 1, previous, chain.records[i].digest, workerplan.Hash(a.Source.Records[c.key])})
			previous = chain.records[i].digest
		}
		previous = ""
		for i, c := range chain.admissionClocks {
			at := c.at
			if !declarationTime(at) || at.After(a.ArchivedAt) {
				return nil, ErrFeedArchiveConflict
			}
			points = append(points, feedArchivePrefixPoint{"admission", id, c.id, at.Unix(), at.Nanosecond(), i + 1, previous, chain.admissions[i].digest, workerplan.Hash(a.Source.Collectors.Admissions[c.id])})
			previous = chain.admissions[i].digest
		}
	}
	return points, nil
}

func addArchivePrefixPoints(index map[string]feedArchivePrefixRef, a FeedArchive) (map[string]feedArchivePrefixRef, error) {
	points, err := archivePrefixPoints(a)
	if err != nil {
		return nil, err
	}
	additions := map[string]feedArchivePrefixRef{}
	for _, p := range points {
		key := prefixPointKey(p)
		if old, ok := index[key]; ok {
			if old.Point != p || old.Sequence < 1 || old.Sequence > a.Sequence {
				return nil, ErrFeedArchiveConflict
			}
		} else {
			additions[key] = feedArchivePrefixRef{p, a.Sequence}
		}
	}
	return additions, nil
}

func (m *Memory) prepareArchivePrefixes(a FeedArchive) ([]feedArchivePrefixRef, error) {
	additions, err := addArchivePrefixPoints(m.feedArchivePrefixPoints, a)
	if err != nil {
		return nil, err
	}
	ordered := make([]feedArchivePrefixRef, 0, len(additions))
	for _, ref := range additions {
		ordered = append(ordered, ref)
	}
	slices.SortFunc(ordered, func(a, b feedArchivePrefixRef) int {
		if c := strings.Compare(prefixGroup(a.Point), prefixGroup(b.Point)); c != 0 {
			return c
		}
		return cmp.Compare(a.Point.Count, b.Point.Count)
	})
	heads := map[string]feedArchivePrefixPoint{}
	for _, ref := range ordered {
		p := ref.Point
		group := prefixGroup(p)
		previous, ok := heads[group]
		if !ok {
			order := m.feedArchivePrefixOrder[group]
			if len(order) > 0 {
				previous = order[len(order)-1].Point
			}
		}
		if p.Count != previous.Count+1 || p.PreviousSHA256 != previous.Digest || previous.Count > 0 && (p.at().Before(previous.at()) || p.at().Equal(previous.at()) && p.SourceID <= previous.SourceID) {
			return nil, ErrFeedArchiveConflict
		}
		heads[group] = p
	}
	return ordered, nil
}

const archivePrefixRows = "jsonb_to_recordset($1::jsonb) AS w(kind text,registration_id text,source_id text,at_seconds bigint,at_nanos integer,row_count bigint,previous_sha256 text,digest text,record_sha256 text)"

func writeFeedArchivePrefixes(ctx context.Context, tx pgx.Tx, a FeedArchive) error {
	points, err := archivePrefixPoints(a)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(points)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archive_prefixes(kind,registration_id,source_id,at_seconds,at_nanos,row_count,previous_sha256,digest,record_sha256,archive_sequence) SELECT kind,registration_id,source_id,at_seconds,at_nanos,row_count,previous_sha256,digest,record_sha256,$2 FROM "+archivePrefixRows+" WHERE true ON CONFLICT(kind,source_id) DO NOTHING", raw, a.Sequence); err != nil {
		return err
	}
	if err := checkArchivePrefixRows(ctx, tx, raw, a.Sequence); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO mantik_feed_archive_prefix_batches(archive_sequence,archive_sha256,at_seconds,at_nanos,prefix_count,prefix_sha256) VALUES($1,$2,$3,$4,$5,$6)", a.Sequence, a.SHA256, a.ArchivedAt.Unix(), a.ArchivedAt.Nanosecond(), len(points), workerplan.Hash(points))
	return err
}

func checkArchivePrefixRows(ctx context.Context, tx pgx.Tx, raw []byte, sequence int64) error {
	var conflict bool
	err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+archivePrefixRows+" LEFT JOIN mantik_feed_archive_prefixes p ON p.kind=w.kind AND p.source_id=w.source_id WHERE p.source_id IS NULL OR p.registration_id<>w.registration_id OR p.at_seconds<>w.at_seconds OR p.at_nanos<>w.at_nanos OR p.row_count<>w.row_count OR p.previous_sha256<>w.previous_sha256 OR p.digest<>w.digest OR p.record_sha256<>w.record_sha256 OR p.archive_sequence>$2)", raw, sequence).Scan(&conflict)
	if err != nil {
		return err
	}
	if conflict {
		return ErrFeedArchiveConflict
	}
	return nil
}

func readFeedArchivePrefixBatch(ctx context.Context, tx pgx.Tx, a FeedArchive, points []feedArchivePrefixPoint) error {
	var sha, digest string
	var seconds int64
	var nanos, count int
	err := tx.QueryRow(ctx, "SELECT archive_sha256,at_seconds,at_nanos,prefix_count,prefix_sha256 FROM mantik_feed_archive_prefix_batches WHERE archive_sequence=$1", a.Sequence).Scan(&sha, &seconds, &nanos, &count, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFeedArchiveConflict
	}
	if err != nil {
		return err
	}
	if sha != a.SHA256 || seconds != a.ArchivedAt.Unix() || nanos != a.ArchivedAt.Nanosecond() || count != len(points) || digest != workerplan.Hash(points) {
		return ErrFeedArchiveConflict
	}
	return nil
}

func backfillFeedArchivePrefixes(ctx context.Context, tx pgx.Tx) error {
	// The schema and collector locks are already held by source-index backfill.
	for {
		var sequence int64
		err := tx.QueryRow(ctx, "SELECT a.sequence FROM mantik_feed_archives a LEFT JOIN mantik_feed_archive_prefix_batches b ON b.archive_sequence=a.sequence WHERE b.archive_sequence IS NULL ORDER BY a.sequence LIMIT 1").Scan(&sequence)
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
		if err := writeFeedArchivePrefixes(ctx, tx, a); err != nil {
			return err
		}
	}
}

func archivedPrefixSelection(a FeedArchive, points []feedArchivePrefixPoint, registration string, at time.Time) (FeedArchivePrefix, map[string]feedArchivePrefixPoint, error) {
	if entry, ok := a.Source.Collectors.Entries[registration]; !ok || entry.RegisteredAt.After(at) {
		return FeedArchivePrefix{}, nil, ErrFeedArchiveNotFound
	}
	if at.After(a.ArchivedAt) {
		at = a.ArchivedAt
	}
	out := FeedArchivePrefix{Archive: a.FeedArchiveHeader, RegistrationID: registration, At: at.UTC()}
	ends := map[string]feedArchivePrefixPoint{}
	for _, p := range points {
		if p.RegistrationID != registration || p.at().After(at) {
			continue
		}
		ends[p.Kind] = p
		if p.Kind == "publication" {
			out.Prefix.Records, out.Prefix.RecordsSHA256 = p.Count, p.Digest
		} else {
			out.Prefix.Admissions, out.Prefix.AdmissionsSHA256 = p.Count, p.Digest
		}
	}
	return out, ends, nil
}

func (m *Memory) FeedArchivePrefix(ctx context.Context, registration string, at time.Time) (FeedArchivePrefix, error) {
	if !decisionRegistration.MatchString(registration) || !declarationTime(at) {
		return FeedArchivePrefix{}, ErrFeedArchiveInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FeedArchivePrefix{}, err
	}
	return m.feedArchivePrefixLocked(ctx, registration, at)
}

// Caller holds the Memory owner lock; replay can share its source snapshot.
func (m *Memory) feedArchivePrefixLocked(ctx context.Context, registration string, at time.Time) (FeedArchivePrefix, error) {
	if len(m.feedArchives) == 0 {
		return FeedArchivePrefix{}, ErrFeedArchiveNotFound
	}
	n, _ := slices.BinarySearchFunc(m.feedArchives, at, func(a FeedArchive, t time.Time) int { return a.ArchivedAt.Compare(t) })
	if n == len(m.feedArchives) {
		n--
	}
	a := m.feedArchives[n]
	if a.Sequence != int64(n+1) || a.ReceiverID != m.receiverID {
		return FeedArchivePrefix{}, ErrFeedArchiveConflict
	}
	points, err := archivePrefixPoints(a)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	out, ends, err := archivedPrefixSelection(a, points, registration, at)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	for _, kind := range []string{"publication", "admission"} {
		order := m.feedArchivePrefixOrder[kind+":"+registration]
		end, _ := slices.BinarySearchFunc(order, out.At, func(r feedArchivePrefixRef, t time.Time) int {
			if r.Point.at().After(t) {
				return 1
			}
			return -1
		})
		want, exists := ends[kind]
		if end == 0 {
			if exists {
				return FeedArchivePrefix{}, ErrFeedArchiveConflict
			}
			continue
		}
		ref := order[end-1]
		actual, ok := m.feedArchivePrefixPoints[prefixPointKey(ref.Point)]
		if !exists || !ok || actual != ref || ref.Point != want || ref.Sequence < 1 || ref.Sequence > a.Sequence {
			return FeedArchivePrefix{}, ErrFeedArchiveConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return FeedArchivePrefix{}, err
	}
	return out, nil
}

const prefixColumns = "kind,registration_id,source_id,at_seconds,at_nanos,row_count,previous_sha256,digest,record_sha256,archive_sequence"

func scanArchivePrefix(row pgx.Row) (feedArchivePrefixRef, error) {
	var r feedArchivePrefixRef
	p := &r.Point
	err := row.Scan(&p.Kind, &p.RegistrationID, &p.SourceID, &p.AtSeconds, &p.AtNanos, &p.Count, &p.PreviousSHA256, &p.Digest, &p.RecordSHA256, &r.Sequence)
	return r, err
}

func (p *Postgres) FeedArchivePrefix(ctx context.Context, registration string, at time.Time) (FeedArchivePrefix, error) {
	if !decisionRegistration.MatchString(registration) || !declarationTime(at) {
		return FeedArchivePrefix{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out, err := readFeedArchivePrefix(ctx, tx, registration, at)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeedArchivePrefix{}, err
	}
	return out, nil
}

// Uses the caller's owner transaction instead of opening a second snapshot.
func readFeedArchivePrefix(ctx context.Context, tx pgx.Tx, registration string, at time.Time) (FeedArchivePrefix, error) {
	var sequence int64
	err := tx.QueryRow(ctx, "SELECT archive_sequence FROM mantik_feed_archive_prefix_batches WHERE (at_seconds,at_nanos)>=($1,$2) ORDER BY at_seconds,at_nanos,archive_sequence LIMIT 1", at.Unix(), at.Nanosecond()).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT sequence FROM mantik_feed_archives ORDER BY sequence DESC LIMIT 1").Scan(&sequence)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return FeedArchivePrefix{}, ErrFeedArchiveNotFound
	}
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	a, err := readFeedArchive(ctx, tx, sequence)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return FeedArchivePrefix{}, err
	}
	if a.ReceiverID != owner {
		return FeedArchivePrefix{}, ErrFeedArchiveConflict
	}
	points, err := archivePrefixPoints(a)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	if err := readFeedArchivePrefixBatch(ctx, tx, a, points); err != nil {
		return FeedArchivePrefix{}, err
	}
	out, ends, err := archivedPrefixSelection(a, points, registration, at)
	if err != nil {
		return FeedArchivePrefix{}, err
	}
	for _, kind := range []string{"publication", "admission"} {
		ref, err := scanArchivePrefix(tx.QueryRow(ctx, "SELECT "+prefixColumns+" FROM mantik_feed_archive_prefixes WHERE kind=$1 AND registration_id=$2 AND (at_seconds,at_nanos)<=($3,$4) ORDER BY at_seconds DESC,at_nanos DESC,source_id DESC LIMIT 1", kind, registration, out.At.Unix(), out.At.Nanosecond()))
		want, exists := ends[kind]
		if errors.Is(err, pgx.ErrNoRows) && !exists {
			continue
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return FeedArchivePrefix{}, ErrFeedArchiveConflict
			}
			return FeedArchivePrefix{}, err
		}
		if !exists || ref.Point != want || ref.Sequence < 1 || ref.Sequence > a.Sequence {
			return FeedArchivePrefix{}, ErrFeedArchiveConflict
		}
	}
	return out, nil
}

// Full audit reconstructs every point from original source snapshots, then
// checks the complete point index. It is deliberately separate from cut reads.
func auditArchivePrefixes(ctx context.Context, archives []FeedArchive, actual map[string]feedArchivePrefixRef, order map[string][]feedArchivePrefixRef) error {
	expected := map[string]feedArchivePrefixRef{}
	for _, a := range archives {
		if err := ctx.Err(); err != nil {
			return err
		}
		additions, err := addArchivePrefixPoints(expected, a)
		if err != nil {
			return err
		}
		for key, ref := range additions {
			expected[key] = ref
		}
	}
	if !reflect.DeepEqual(expected, actual) && !(len(expected) == 0 && len(actual) == 0) {
		return ErrFeedArchiveConflict
	}
	ordered := map[string]feedArchivePrefixRef{}
	for group, refs := range order {
		previous := ""
		for i, ref := range refs {
			if group != prefixGroup(ref.Point) || ref.Point.Count != i+1 || ref.Point.PreviousSHA256 != previous {
				return ErrFeedArchiveConflict
			}
			ordered[prefixPointKey(ref.Point)] = ref
			previous = ref.Point.Digest
		}
	}
	if !reflect.DeepEqual(ordered, expected) {
		return ErrFeedArchiveConflict
	}
	return nil
}

func auditPostgresArchivePrefixes(ctx context.Context, tx pgx.Tx) error {
	expected := map[string]feedArchivePrefixRef{}
	for sequence := int64(1); ; sequence++ {
		a, err := readFeedArchive(ctx, tx, sequence)
		if errors.Is(err, ErrFeedArchiveNotFound) {
			break
		}
		if err != nil {
			return err
		}
		points, err := archivePrefixPoints(a)
		if err != nil {
			return err
		}
		if err := readFeedArchivePrefixBatch(ctx, tx, a, points); err != nil {
			return err
		}
		additions, err := addArchivePrefixPoints(expected, a)
		if err != nil {
			return err
		}
		for key, ref := range additions {
			expected[key] = ref
		}
	}
	rows, err := tx.Query(ctx, "SELECT "+prefixColumns+" FROM mantik_feed_archive_prefixes ORDER BY kind,source_id")
	if err != nil {
		return err
	}
	actual := map[string]feedArchivePrefixRef{}
	for rows.Next() {
		ref, err := scanArchivePrefix(rows)
		if err != nil {
			rows.Close()
			return err
		}
		actual[prefixPointKey(ref.Point)] = ref
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, actual) {
		return ErrFeedArchiveConflict
	}
	var batches, archives int64
	if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM mantik_feed_archive_prefix_batches),(SELECT count(*) FROM mantik_feed_archives)").Scan(&batches, &archives); err != nil {
		return err
	}
	if batches != archives {
		return ErrFeedArchiveConflict
	}
	return nil
}

var _ FeedArchivePrefixStore = (*Memory)(nil)
var _ FeedArchivePrefixStore = (*Postgres)(nil)
