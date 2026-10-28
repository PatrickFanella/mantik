-- Compatibility fence for immutable v4 declarations and v7 temporal partitions.
COMMENT ON TABLE mantik_paper_evaluation_declarations IS 'Versioned predeclared evaluation plans including prospective purged holdout choices';
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'Versioned source-bound assessments including temporal partition bounds, without statistical independence or financial authority';
