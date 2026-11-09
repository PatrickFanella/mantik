package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type archivePrefixBackend interface {
	CollectorSubmissionStore
	feedArchiveBackend
	FeedArchivePrefixStore
}

func TestMemoryFeedArchivePrefixes(t *testing.T) { archivePrefixContracts(t, NewMemory()) }
func TestPostgresContractsFeedArchivePrefixes(t *testing.T) {
	archivePrefixContracts(t, timerPostgres(t, "archiveprefixes"))
}

func archivePrefixContracts(t *testing.T, s archivePrefixBackend) {
	t.Helper()
	ctx := t.Context()
	base := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	at := base
	now := func() time.Time { return at }
	snapshot, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := snapshot.ReceiverID
	for _, id := range []string{"prefix-feed", "other-feed"} {
		reg := feedcollector.Registration{ID: id, Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: id + "-journal", AccessRef: "manufactured", LeaseSeconds: 300}
		if id == "other-feed" {
			reg.Symbol = "QQQ"
		}
		for _, command := range []feedcollector.Command{{RequestID: id + "-register", Operation: "register", RegistrationID: id, Registration: &reg}, {RequestID: id + "-claim", Operation: "claim", RegistrationID: id, OwnerID: "worker"}} {
			if _, err := s.FeedCollectorCommand(ctx, owner, command, now); err != nil {
				t.Fatalf("collector %s %s: %v", id, command.Operation, err)
			}
		}
	}
	publish := func(reg, key string) {
		t.Helper()
		f := feedcollector.Fence{RegistrationID: reg, OwnerID: "worker", Generation: 1}
		if _, err := s.PublishFeedEvidence(ctx, owner, reg, key, f, json.RawMessage(`{"manufactured":"prefix"}`), now, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	admit := func(id string) {
		t.Helper()
		f := feedcollector.Fence{RegistrationID: "prefix-feed", OwnerID: "worker", Generation: 1}
		input := collectorInput(at, id)
		input.Provenance.RawRef = "journal:prefix-feed-journal:capture-test-r1.json"
		input.Provenance.AccessRef = "manufactured"
		input.ID = routing.MarketInputID(input)
		if _, err := s.SubmitCollectorResearch(ctx, owner, f, input, now); err != nil {
			t.Fatalf("admit %s: %v", id, err)
		}
	}
	seal := func() FeedArchiveReceipt {
		t.Helper()
		r, err := s.ArchiveFeed(ctx, owner, now, func(FeedEvidenceSnapshot, time.Time) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	at = base.Add(100 * time.Nanosecond)
	admit("input-b")
	admit("input-a")
	publish("prefix-feed", "p-b")
	publish("prefix-feed", "p-a")
	at = base.Add(200 * time.Nanosecond)
	admit("input-c")
	publish("prefix-feed", "p-c")
	publish("other-feed", "other")
	at = base.Add(250 * time.Nanosecond)
	first := seal()
	at = base.Add(251 * time.Nanosecond)
	admit("input-d")
	publish("prefix-feed", "p-d")
	at = base.Add(300 * time.Nanosecond)
	second := seal()
	latest, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// All cuts fit within one PostgreSQL microsecond. Source order still retains
	// the exact nanoseconds, including equal-clock ID ties and registration scope.
	for _, reg := range []string{"prefix-feed", "other-feed"} {
		for _, ns := range []int64{99, 100, 199, 200, 249, 250, 251, 299, 300, 301} {
			cut := base.Add(time.Duration(ns))
			got, err := s.FeedArchivePrefix(ctx, reg, cut)
			if err != nil {
				t.Fatal(reg, ns, err)
			}
			through := cut
			if through.After(second.Header.ArchivedAt) {
				through = second.Header.ArchivedAt
			}
			want := newFeedPrefixIndex(latest).at(reg, through)
			sequence := int64(1)
			if cut.After(first.Header.ArchivedAt) {
				sequence = 2
			}
			if got.Prefix != want || !got.At.Equal(through) || got.Archive.Sequence != sequence || got.Archive.Validate() != nil {
				t.Fatal("archive prefix changed existing v3 cut", reg, ns, got, want)
			}
		}
	}
	if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", base.Add(-time.Nanosecond)); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("registration appeared before its owner clock", err)
	}
	if _, err := s.FeedArchivePrefix(ctx, "missing", at); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal(err)
	}
	if _, err := s.FeedArchivePrefix(ctx, "../bad", at); !errors.Is(err, ErrFeedArchiveInvalid) {
		t.Fatal(err)
	}
	if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", time.Time{}); !errors.Is(err, ErrFeedArchiveInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.FeedArchivePrefix(canceled, "prefix-feed", at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	original, err := s.FeedArchivePrefix(ctx, "prefix-feed", base.Add(100*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuditFeedArchiveSources(ctx); err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		got, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("restart rewrote prefix", got, err)
		}
		// A historical read selects one segment, even when an unrelated later
		// segment is corrupt. Full audit must still detect that corruption.
		var laterPayload []byte
		if err := reopened.pool.QueryRow(ctx, "SELECT payload FROM mantik_feed_archives WHERE sequence=2").Scan(&laterPayload); err != nil {
			t.Fatal(err)
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archives SET payload=convert_to('{}','UTF8') WHERE sequence=2"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("historical prefix loaded unrelated later payload", got, err)
		}
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit missed later source corruption", err)
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archives SET payload=$1 WHERE sequence=2", laterPayload); err != nil {
			t.Fatal(err)
		}
		// An index error rolls back archive, source index and both manifests.
		at = at.Add(time.Nanosecond)
		publish("prefix-feed", "rollback")
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_archive_prefixes ADD CONSTRAINT reject_prefix CHECK(source_id<>'prefix-feed:rollback')"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ArchiveFeed(ctx, owner, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }); err == nil {
			t.Fatal("prefix insertion failure acknowledged")
		}
		if _, err := s.FeedArchive(ctx, 3); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("partial archive survived prefix failure", err)
		}
		if _, err := s.FeedArchiveSource(ctx, "publication", "prefix-feed:rollback"); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("partial source index survived prefix failure", err)
		}
		active, err := s.FeedEvidence(ctx)
		if err != nil || len(active.Records) != len(latest.Records)+1 {
			t.Fatal("prefix failure changed active sources", err)
		}
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_archive_prefixes DROP CONSTRAINT reject_prefix"); err != nil {
			t.Fatal(err)
		}
		seal()
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archive_prefixes SET digest=$1 WHERE kind='publication' AND source_id='prefix-feed:p-b'", strings.Repeat("f", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("changed prefix accepted", err)
		}
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit missed changed prefix", err)
		}
		stored, err := s.FeedArchive(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		points, err := archivePrefixPoints(stored)
		if err != nil {
			t.Fatal(err)
		}
		originalManifest := workerplan.Hash(points)
		for i := range points {
			if points[i].Kind == "publication" && points[i].SourceID == "prefix-feed:p-b" {
				points[i].Digest = strings.Repeat("f", 64)
			}
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archive_prefix_batches SET prefix_sha256=$1 WHERE archive_sequence=1", workerplan.Hash(points)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("rehashed point manifest replaced original source proof", err)
		}
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit accepted rehashed point manifest", err)
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archive_prefix_batches SET prefix_sha256=$1 WHERE archive_sequence=1", originalManifest); err != nil {
			t.Fatal(err)
		}
		if _, err := reopened.pool.Exec(ctx, "DELETE FROM mantik_feed_archive_prefixes WHERE kind='publication' AND source_id='prefix-feed:p-b'"); err != nil {
			t.Fatal(err)
		}
	} else {
		m := s.(*Memory)
		m.mu.Lock()
		later := clone(m.feedArchives[1])
		m.feedArchives[1].Source.ReceiverID = "receiver-corrupt"
		m.mu.Unlock()
		if got, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("historical prefix loaded unrelated later payload", got, err)
		}
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit missed later source corruption", err)
		}
		m.mu.Lock()
		m.feedArchives[1] = later
		key := "publication:prefix-feed:p-b"
		ref := m.feedArchivePrefixPoints[key]
		changed := ref
		changed.Point.Digest = strings.Repeat("f", 64)
		m.feedArchivePrefixPoints[key] = changed
		m.mu.Unlock()
		if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("changed prefix accepted", err)
		}
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit missed changed prefix", err)
		}
		m.mu.Lock()
		m.feedArchivePrefixPoints[key] = ref
		group := "publication:prefix-feed"
		order := append([]feedArchivePrefixRef{}, m.feedArchivePrefixOrder[group]...)
		m.feedArchivePrefixOrder[group][0], m.feedArchivePrefixOrder[group][1] = m.feedArchivePrefixOrder[group][1], m.feedArchivePrefixOrder[group][0]
		m.mu.Unlock()
		if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("audit missed reordered Memory prefix index", err)
		}
		m.mu.Lock()
		m.feedArchivePrefixOrder[group] = order
		delete(m.feedArchivePrefixPoints, key)
		m.mu.Unlock()
	}
	if _, err := s.FeedArchivePrefix(ctx, "prefix-feed", original.At); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("deleted prefix accepted", err)
	}
	if _, err := s.AuditFeedArchiveSources(ctx); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("audit missed deleted prefix", err)
	}
}

func TestPostgresContractsFeedArchivePrefixUpgrade(t *testing.T) {
	ctx := t.Context()
	p := timerPostgres(t, "prefixupgrade")
	feed, err := p.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "upgrade", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "prefix-journal", AccessRef: "manufactured", LeaseSeconds: 300}
	at := time.Date(2026, 11, 9, 15, 0, 0, 123, time.UTC)
	now := func() time.Time { return at }
	if _, err := p.FeedCollectorCommand(ctx, feed.ReceiverID, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishFeedEvidence(ctx, feed.ReceiverID, reg.ID, "raw", feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}, json.RawMessage(`{"fixture":true}`), now, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	a, err := p.ArchiveFeed(ctx, feed.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	original, err := p.FeedArchivePrefix(ctx, reg.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER mantik_feed_archive_prefix_batch_required ON mantik_feed_archives; DROP FUNCTION mantik_require_feed_archive_prefix_batch(); DROP TABLE mantik_feed_archive_prefix_batches; DROP TABLE mantik_feed_archive_prefixes; DELETE FROM mantik_schema WHERE version=102"); err != nil {
		t.Fatal(err)
	}
	connection := p.pool.Config().ConnString()
	p.Close()
	reopened, err := OpenPostgres(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if _, err := reopened.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version=102"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.migrate(ctx); err != nil {
		t.Fatal("populated prefix DDL replay", err)
	}
	got, err := reopened.FeedArchivePrefix(ctx, reg.ID, at)
	if err != nil || !reflect.DeepEqual(got, original) || got.Archive != a.Header {
		t.Fatal("upgrade changed original prefix", got, err)
	}
	if _, err := reopened.AuditFeedArchiveSources(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresContractsFeedArchiveRequiresPrefixBatch(t *testing.T) {
	ctx := t.Context()
	p := timerPostgres(t, "prefixguard")
	source, err := p.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source, err = canonicalArchiveSource(source)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newFeedArchive(source, FeedArchiveHeader{}, time.Now(), func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a)
	header, _ := json.Marshal(a.FeedArchiveHeader)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archives(sequence,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5)", a.Sequence, a.SourceSHA256, a.SHA256, header, raw); err != nil {
		t.Fatal(err)
	}
	// A schema-101 writer supplies its source manifest but knows no prefix table.
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archive_source_batches VALUES($1,$2,0,$3)", a.Sequence, a.SHA256, workerplan.Hash([]feedArchiveSourceRef{})); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("older writer committed without prefix manifest")
	}
	if _, err := p.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("prefix guard left partial archive", err)
	}
	if _, err := p.ArchiveFeed(ctx, source.ReceiverID, time.Now, func(FeedEvidenceSnapshot, time.Time) error { return nil }); err != nil {
		t.Fatal("new writer failed after older commit rollback", err)
	}
}
