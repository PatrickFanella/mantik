package storage

import (
	"context"
	"encoding/json"
	"testing"
)

// Build a genuine predecessor fixture before any archive closes its identity.
// Damage fixtures must leave the immutable owner hash untouched instead.
func setPredecessorAssessment(t *testing.T, s assessmentBackend, r PaperEvaluationAssessment) {
	t.Helper()
	if m, ok := s.(*Memory); ok {
		if len(m.paperRecordArchives) != 0 {
			t.Fatal("predecessor fixture cannot replace an archived owner")
		}
		m.evaluationAssessments[r.Command.ID] = clone(r)
		m.retainPaperRecordEpoch("assessment", r.Command.ID, r.SHA256, r.AssessedAt)
		return
	}
	p := s.(*Postgres)
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM mantik_paper_record_archives").Scan(&count); err != nil || count != 0 {
		t.Fatal("predecessor fixture cannot replace archived identities", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs"); err != nil {
		t.Fatal(err)
	}
	updated, err := tx.Exec(ctx, "UPDATE mantik_paper_record_epochs SET record_sha256=$1 WHERE kind='assessment' AND record_id=$2 AND created_epoch=0 AND at_seconds=$3 AND at_nanos=$4", r.SHA256, r.Command.ID, r.AssessedAt.Unix(), r.AssessedAt.Nanosecond())
	if err != nil || updated.RowsAffected() != 1 {
		t.Fatal("predecessor fixture changed creation order or clock", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, r.Command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
