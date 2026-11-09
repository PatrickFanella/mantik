package storage

import "testing"

// Exact predecessor construction is confined to disposable upgrade fixtures.
// Normal startup or rollback must never remove these identity reservations.
func dropPaperRecordArchiveSchema(t *testing.T, p *Postgres) {
	t.Helper()
	for _, sql := range []string{
		"DROP TRIGGER IF EXISTS mantik_paper_custody_epoch_required ON mantik_paper_feed_checkpoints",
		"DROP FUNCTION IF EXISTS mantik_require_paper_custody_epoch()",
		"DROP TRIGGER IF EXISTS mantik_decision_creation_epoch ON mantik_paper_decisions",
		"DROP TRIGGER IF EXISTS mantik_outcome_creation_epoch ON mantik_paper_outcomes",
		"DROP TRIGGER IF EXISTS mantik_assessment_creation_epoch ON mantik_paper_evaluation_assessments",
		"DROP TRIGGER IF EXISTS mantik_review_creation_epoch ON mantik_decision_reviews",
		"DROP FUNCTION IF EXISTS mantik_record_paper_epoch()",
		"DROP TRIGGER IF EXISTS mantik_paper_archive_batch_required ON mantik_paper_record_archives",
		"DROP FUNCTION IF EXISTS mantik_require_paper_archive_batch()",
		"DROP TABLE mantik_paper_archive_members",
		"DROP TABLE mantik_paper_archive_batches",
		"DROP TABLE mantik_paper_record_epochs",
		"DROP TABLE mantik_paper_record_archives",
		"DROP FUNCTION IF EXISTS mantik_immutable_paper_archive()",
	} {
		if _, err := p.pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}
