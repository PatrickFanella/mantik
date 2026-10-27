-- JSON payload v6 adds owner-replayed observed portfolio accounting. Older
-- binaries cannot validate its compiled evaluator or write its source clock.
-- No retained declaration, assessment, source or allocation payload is rewritten.
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'Owner-pinned evaluation assessments including v6 observed counterfactual portfolio accounting; replay requires the compiled portfolio evaluator; no total-return, admission or execution grant';
