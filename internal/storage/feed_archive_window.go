package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// The range is bounded by validated prefix counts at both ends. Consecutive
// counts and rederived chained digests detect omissions without scanning all
// preceding history. Only one archive segment is cached while walking a range.
type archivedFeedWindowReader struct {
	prefix  func(string, time.Time) (FeedArchivePrefix, error)
	archive func(int64) (FeedArchive, error)
	points  func(string, string, time.Time, time.Time, int) ([]feedArchivePrefixRef, error)
}

func archivedOutcomeFeed(ctx context.Context, current FeedEvidenceSnapshot, registration string, since, at time.Time, reader archivedFeedWindowReader) (FeedEvidenceSnapshot, error) {
	if current.Retention == nil {
		return current, nil
	}
	registered := current.Collectors.Entries[registration].RegisteredAt
	if registered.IsZero() {
		return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
	}
	lower := func(feed FeedEvidenceSnapshot, start time.Time) time.Time {
		pubs, _ := newFeedPrefixIndex(feed).window(registration, at, since)
		for id := range pubs {
			var clock struct {
				ReceivedAt time.Time `json:"received_at"`
			}
			if json.Unmarshal(feed.Records[id].Recording, &clock) != nil || clock.ReceivedAt.IsZero() {
				return registered
			}
			if clock.ReceivedAt.Before(start) {
				start = clock.ReceivedAt
			}
		}
		if start.Before(registered) {
			start = registered
		}
		return start
	}
	start := since
	if start.IsZero() {
		start = at
	}
	start = lower(current, start)
	if start.After(current.Retention.Checkpoint.CutAt) {
		return current, nil
	}
	edge := !since.IsZero() && !since.After(current.Retention.Checkpoint.CutAt)
	out, err := archivedOutcomeFeedRange(ctx, current, registration, start, at, edge, reader)
	if err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	// All forward publications were included in the first range. A delayed
	// publication can lower the receipt-time window into older history; fetch
	// that lower range too. Its pre-range coverage edge already accounts for
	// every capability, so earlier non-forward records cannot lower it again.
	if from := lower(out, start); from.Before(start) {
		return archivedOutcomeFeedRange(ctx, current, registration, from, at, edge, reader)
	}
	return out, nil
}

func archivedOutcomeFeedRange(ctx context.Context, current FeedEvidenceSnapshot, registration string, since, at time.Time, edge bool, reader archivedFeedWindowReader) (FeedEvidenceSnapshot, error) {
	if current.Retention == nil || since.After(current.Retention.Checkpoint.CutAt) {
		return current, nil
	}
	if at.Before(since) {
		return FeedEvidenceSnapshot{}, ErrPaperOutcomeInvalid
	}
	end := minClock(at, current.Retention.Checkpoint.CutAt)
	start, err := reader.prefix(registration, since.Add(-time.Nanosecond))
	if err != nil && !errors.Is(err, ErrFeedArchiveNotFound) {
		return FeedEvidenceSnapshot{}, err
	}
	last, err := reader.prefix(registration, end)
	if err != nil {
		return FeedEvidenceSnapshot{}, err
	}
	var segment FeedArchive
	out := clone(current)
	out.custody = current.custody
	if out.Routing == nil {
		return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
	}
	bytes := 0
	addPublication := func(id string, source FeedEvidenceSnapshot) error {
		v, ok := source.Records[id]
		if !ok || v.RegistrationID != registration {
			return ErrFeedArchiveConflict
		}
		if old, exists := out.Records[id]; exists {
			if !sameFeedRecord(old, v) {
				return ErrFeedArchiveConflict
			}
			return nil
		}
		bytes += len(v.Recording)
		if bytes > MaxPaperOutcomeBytes {
			return ErrPaperOutcomeCapacity
		}
		out.Records[id] = v
		return nil
	}
	addAdmission := func(id string, source FeedEvidenceSnapshot) error {
		v, ok := source.Collectors.Admissions[id]
		if !ok || v.Fence.RegistrationID != registration || source.Routing == nil {
			return ErrFeedArchiveConflict
		}
		input, ok := source.Routing.Inputs[id]
		if !ok || v.InputSHA256 != feedcollector.InputDigest(input.Input) || !reflect.DeepEqual(v.Receipt, input.Receipt) {
			return ErrFeedArchiveConflict
		}
		if old, exists := out.Collectors.Admissions[id]; exists {
			if !reflect.DeepEqual(old, v) || !reflect.DeepEqual(out.Routing.Inputs[id], input) {
				return ErrFeedArchiveConflict
			}
			return nil
		}
		out.Collectors.Admissions[id], out.Routing.Inputs[id] = v, input
		return nil
	}
	// Retirement preserves coverage at its cut. The first segment covering
	// since therefore contains the actual pre-since coverage edge, including
	// receipt-time ties. Forward publications are gathered independently below.
	if edge {
		seed, err := reader.prefix(registration, since)
		if err != nil {
			return FeedEvidenceSnapshot{}, err
		}
		segment, err = reader.archive(seed.Archive.Sequence)
		if err != nil {
			return FeedEvidenceSnapshot{}, err
		}
		pubs, admissions := newFeedPrefixIndex(segment.Source).window(registration, since.Add(-time.Nanosecond), time.Time{})
		for id := range pubs {
			if err := addPublication(id, segment.Source); err != nil {
				return FeedEvidenceSnapshot{}, err
			}
		}
		for id := range admissions {
			if err := addAdmission(id, segment.Source); err != nil {
				return FeedEvidenceSnapshot{}, err
			}
		}
	}
	for _, kind := range []string{"publication", "admission"} {
		beforeCount, beforeSHA, afterCount, afterSHA := start.Prefix.Records, start.Prefix.RecordsSHA256, last.Prefix.Records, last.Prefix.RecordsSHA256
		limit := MaxFeedEvidence
		if kind == "admission" {
			beforeCount, beforeSHA, afterCount, afterSHA = start.Prefix.Admissions, start.Prefix.AdmissionsSHA256, last.Prefix.Admissions, last.Prefix.AdmissionsSHA256
			limit = feedcollector.MaxAdmissions
		}
		count := afterCount - beforeCount
		if count < 0 {
			return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
		}
		if count > limit {
			return FeedEvidenceSnapshot{}, ErrPaperOutcomeCapacity
		}
		points, err := reader.points(kind, registration, since, end, count+1)
		if err != nil {
			return FeedEvidenceSnapshot{}, err
		}
		if len(points) != count {
			return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
		}
		previous := beforeSHA
		for n, ref := range points {
			if err := ctx.Err(); err != nil {
				return FeedEvidenceSnapshot{}, err
			}
			p := ref.Point
			if p.Kind != kind || p.RegistrationID != registration || p.Count != beforeCount+n+1 || p.PreviousSHA256 != previous || p.at().Before(since) || p.at().After(end) {
				return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
			}
			if segment.Sequence != ref.Sequence {
				segment, err = reader.archive(ref.Sequence)
				if err != nil {
					return FeedEvidenceSnapshot{}, err
				}
			}
			var digest, sha string
			if kind == "publication" {
				v, ok := segment.Source.Records[p.SourceID]
				if !ok || !v.PublishedAt.Equal(p.at()) {
					return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
				}
				sha = workerplan.Hash(v)
				digest = publicationPrefixDigest(previous, p.SourceID, v)
				if err := addPublication(p.SourceID, segment.Source); err != nil {
					return FeedEvidenceSnapshot{}, err
				}
			} else {
				v, ok := segment.Source.Collectors.Admissions[p.SourceID]
				if !ok || !v.At.Equal(p.at()) || segment.Source.Routing == nil {
					return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
				}
				sha = workerplan.Hash(v)
				digest = admissionPrefixDigest(previous, p.SourceID, v, segment.Source.Routing.Inputs[p.SourceID])
				if err := addAdmission(p.SourceID, segment.Source); err != nil {
					return FeedEvidenceSnapshot{}, err
				}
			}
			if sha != p.RecordSHA256 || digest != p.Digest {
				return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
			}
			previous = digest
		}
		if previous != afterSHA {
			return FeedEvidenceSnapshot{}, ErrFeedArchiveConflict
		}
	}
	return out, nil
}

func minClock(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (m *Memory) archivedOutcomeFeedLocked(ctx context.Context, feed FeedEvidenceSnapshot, registration string, since, at time.Time) (FeedEvidenceSnapshot, error) {
	return archivedOutcomeFeed(ctx, feed, registration, since, at, m.feedWindowReaderLocked(ctx, feed.ReceiverID))
}
func (m *Memory) feedWindowReaderLocked(ctx context.Context, owner string) archivedFeedWindowReader {
	reader := archivedFeedWindowReader{
		prefix: func(reg string, cut time.Time) (FeedArchivePrefix, error) {
			return m.feedArchivePrefixLocked(ctx, reg, cut)
		},
		archive: func(sequence int64) (FeedArchive, error) {
			if sequence < 1 || sequence > int64(len(m.feedArchives)) {
				return FeedArchive{}, ErrFeedArchiveNotFound
			}
			a := m.feedArchives[sequence-1]
			if a.Validate() != nil || a.Sequence != sequence || a.ReceiverID != owner {
				return FeedArchive{}, ErrFeedArchiveConflict
			}
			return a, nil
		},
		points: func(kind, reg string, from, to time.Time, limit int) ([]feedArchivePrefixRef, error) {
			order := m.feedArchivePrefixOrder[kind+":"+reg]
			start, _ := slices.BinarySearchFunc(order, from, func(r feedArchivePrefixRef, t time.Time) int { return r.Point.at().Compare(t) })
			out := []feedArchivePrefixRef{}
			for _, ref := range order[start:] {
				if ref.Point.at().After(to) || len(out) == limit {
					break
				}
				if actual, ok := m.feedArchivePrefixPoints[prefixPointKey(ref.Point)]; !ok || actual != ref {
					return nil, ErrFeedArchiveConflict
				}
				out = append(out, ref)
			}
			return out, nil
		},
	}
	return reader
}

func readArchivedOutcomeFeed(ctx context.Context, tx pgx.Tx, feed FeedEvidenceSnapshot, registration string, since, at time.Time) (FeedEvidenceSnapshot, error) {
	return archivedOutcomeFeed(ctx, feed, registration, since, at, postgresFeedWindowReader(ctx, tx, feed.ReceiverID))
}
func postgresFeedWindowReader(ctx context.Context, tx pgx.Tx, owner string) archivedFeedWindowReader {
	reader := archivedFeedWindowReader{
		prefix: func(reg string, cut time.Time) (FeedArchivePrefix, error) {
			return readFeedArchivePrefix(ctx, tx, reg, cut)
		},
		archive: func(sequence int64) (FeedArchive, error) {
			a, err := readFeedArchive(ctx, tx, sequence)
			if err != nil {
				return FeedArchive{}, err
			}
			if a.ReceiverID != owner {
				return FeedArchive{}, ErrFeedArchiveConflict
			}
			points, err := archivePrefixPoints(a)
			if err != nil {
				return FeedArchive{}, err
			}
			if err := readFeedArchivePrefixBatch(ctx, tx, a, points); err != nil {
				return FeedArchive{}, err
			}
			return a, nil
		},
		points: func(kind, reg string, from, to time.Time, limit int) ([]feedArchivePrefixRef, error) {
			rows, err := tx.Query(ctx, "SELECT "+prefixColumns+" FROM mantik_feed_archive_prefixes WHERE kind=$1 AND registration_id=$2 AND (at_seconds,at_nanos)>=($3,$4) AND (at_seconds,at_nanos)<=($5,$6) ORDER BY at_seconds,at_nanos,source_id LIMIT $7", kind, reg, from.Unix(), from.Nanosecond(), to.Unix(), to.Nanosecond(), limit)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := []feedArchivePrefixRef{}
			for rows.Next() {
				ref, err := scanArchivePrefix(rows)
				if err != nil {
					return nil, err
				}
				out = append(out, ref)
			}
			return out, rows.Err()
		},
	}
	return reader
}

func outcomeObservationFeed(c PaperOutcomeCommand, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, feed FeedEvidenceSnapshot, now func() time.Time, load func(string, time.Time, time.Time) (FeedEvidenceSnapshot, error)) (FeedEvidenceSnapshot, func() time.Time, error) {
	if feed.Retention == nil || c.Validate() != nil || c.ReceiverID != feed.ReceiverID {
		return feed, now, nil
	}
	if _, duplicate := all[c.ID]; duplicate {
		return feed, now, nil
	}
	d, exists := decisions[c.DecisionID]
	if !exists {
		return feed, now, nil
	}
	if now == nil {
		return FeedEvidenceSnapshot{}, nil, ErrPaperOutcomeInvalid
	}
	at := now()
	if at.Before(d.Record.FinalizedAt) {
		return FeedEvidenceSnapshot{}, nil, ErrPaperOutcomeInvalid
	}
	observation, err := load(d.Record.Command.RegistrationID, d.Record.FinalizedAt, at)
	if err != nil {
		return FeedEvidenceSnapshot{}, nil, err
	}
	return observation, func() time.Time { return at }, nil
}

func decisionObservationFeed(c PaperDecisionCommand, all map[string]retainedPaperDecision, feed FeedEvidenceSnapshot, now func() time.Time, load func(string, time.Time, time.Time) (FeedEvidenceSnapshot, error)) (FeedEvidenceSnapshot, func() time.Time, error) {
	if feed.Retention == nil || c.Validate() != nil || c.ReceiverID != feed.ReceiverID {
		return feed, now, nil
	}
	if _, duplicate := all[c.ID]; duplicate {
		return feed, now, nil
	}
	entry, exists := feed.Collectors.Entries[c.RegistrationID]
	if !exists {
		return feed, now, nil
	}
	if now == nil {
		return FeedEvidenceSnapshot{}, nil, ErrPaperDecisionInvalid
	}
	at := now()
	if at.Before(entry.RegisteredAt) || at.Before(feed.Collectors.LastClock) {
		return FeedEvidenceSnapshot{}, nil, ErrPaperDecisionInvalid
	}
	observation, err := load(c.RegistrationID, time.Time{}, at)
	if errors.Is(err, ErrPaperOutcomeCapacity) {
		err = ErrPaperDecisionCapacity
	}
	if err != nil {
		return FeedEvidenceSnapshot{}, nil, err
	}
	return observation, func() time.Time { return at }, nil
}
