-- Match native Go byte ordering for punctuation, case and timestamp ties.
-- The previous scope index used the database's default text collation.
DROP INDEX IF EXISTS mantik_paper_sample_window;
CREATE INDEX mantik_paper_sample_window ON mantik_paper_record_epochs
    (strategy_id,strategy_sha256,decision_at_seconds,decision_at_nanos,
     decision_id COLLATE "C",kind COLLATE "C",at_seconds,at_nanos,record_id COLLATE "C")
    WHERE kind IN ('decision','outcome');
COMMENT ON INDEX mantik_paper_sample_window IS 'Bounded owner sample windows with native byte ordering; metadata alone grants no evaluation authority';
