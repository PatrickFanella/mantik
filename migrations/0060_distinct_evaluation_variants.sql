-- Fence older declaration writers without rewriting retained v1 payloads.
COMMENT ON TABLE mantik_paper_evaluation_declarations IS 'v2 declarations bind distinct native configurations from actual strategy owners; v1 records remain unchanged and unqualified';
