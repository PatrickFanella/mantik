package storage

import "testing"

// Upgrade fixtures deliberately construct a predecessor only in disposable
// databases. Remove 104's guards as well as its tables before running an older
// writer; normal startup and rollback never remove custody in place.
func dropFeedRetentionSchema(t *testing.T, p *Postgres) {
	t.Helper()
	for _, sql := range []string{
		"DROP TRIGGER IF EXISTS mantik_collector_retention_root ON mantik_feed_collectors",
		"DROP TRIGGER IF EXISTS mantik_archive_retention_root ON mantik_feed_archives",
		"DROP TRIGGER IF EXISTS mantik_paper_decision_source_seal ON mantik_paper_decisions",
		"DROP TRIGGER IF EXISTS mantik_paper_outcome_source_seal ON mantik_paper_outcomes",
		"DROP TRIGGER IF EXISTS mantik_immutable_feed_sources ON mantik_feed_archive_sources",
		"DROP TRIGGER IF EXISTS mantik_immutable_feed_prefixes ON mantik_feed_archive_prefixes",
		"DROP FUNCTION IF EXISTS mantik_require_feed_retention_root()",
		"DROP FUNCTION IF EXISTS mantik_require_archive_retention_root()",
		"DROP FUNCTION IF EXISTS mantik_require_paper_feed_source_seal()",
		"DROP FUNCTION IF EXISTS mantik_immutable_feed_archive_index()",
		"DROP TABLE mantik_paper_feed_source_seals",
		"DROP TABLE mantik_feed_retention",
	} {
		if _, err := p.pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}
