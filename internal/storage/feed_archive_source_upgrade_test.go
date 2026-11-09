package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

func TestPostgresContractsFeedArchiveSourceIndexUpgrade(t *testing.T) {
	ctx := t.Context()
	p := timerPostgres(t, "archiveindexupgrade")
	feed, err := p.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := feed.ReceiverID
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	reg := feedcollector.Registration{ID: "indexed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "index-journal", AccessRef: "manufactured", LeaseSeconds: 300}
	if _, err := p.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "index-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	r, err := p.ArchiveFeed(ctx, owner, func() time.Time { return at }, func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want, err := p.FeedArchiveSource(ctx, "command", "index-register")
	if err != nil {
		t.Fatal(err)
	}
	// Construct the exact schema-100 predecessor on this disposable database.
	// The original source snapshot and its header remain untouched.
	for _, sql := range []string{
		"DROP TRIGGER mantik_feed_archive_source_batch_required ON mantik_feed_archives",
		"DROP FUNCTION mantik_require_feed_archive_source_batch()",
		"DROP TABLE mantik_feed_archive_source_batches",
		"DROP TABLE mantik_feed_archive_sources",
		"DELETE FROM mantik_schema WHERE version=101",
	} {
		if _, err := p.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	connection := p.pool.Config().ConnString()
	p.Close()
	reopened, err := OpenPostgres(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	got, err := reopened.FeedArchiveSource(ctx, "command", "index-register")
	if err != nil || !reflect.DeepEqual(got, want) || got.Archive != r.Header {
		t.Fatal("upgrade rewrote the source or archive identity", err)
	}
	audit, err := reopened.AuditFeedArchiveSources(ctx)
	if err != nil || audit.Status != "verified" || audit.Archives != 1 || audit.Sources != 1 {
		t.Fatal("schema-100 sources were not backfilled exactly", audit, err)
	}
	duplicate, err := reopened.ArchiveFeed(ctx, owner, func() time.Time { t.Fatal("upgrade lost source-cut retry"); return time.Time{} }, func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil || !duplicate.Duplicate || duplicate.Header != r.Header {
		t.Fatal("upgrade changed source-cut retry identity", duplicate, err)
	}
}

func TestPostgresContractsFeedArchiveRequiresSourceBatch(t *testing.T) {
	ctx := t.Context()
	p := timerPostgres(t, "archivebatchguard")
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
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(a.FeedArchiveHeader)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A schema-100 running writer knows only this insert. The deferred guard
	// must reject its COMMIT rather than expose an unindexed new snapshot.
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_archives(sequence,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5)", a.Sequence, a.SourceSHA256, a.SHA256, header, raw); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("older writer committed an archive without its source index")
	}
	if _, err := p.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("failed COMMIT retained a partial snapshot", err)
	}
	if _, err := p.ArchiveFeed(ctx, source.ReceiverID, time.Now, func(FeedEvidenceSnapshot, time.Time) error { return nil }); err != nil {
		t.Fatal("new writer could not commit the same source after rollback", err)
	}
}
