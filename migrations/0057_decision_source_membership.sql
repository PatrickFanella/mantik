-- Startup fence for v2 finalized-decision source membership replay.
-- Retained v1 decision payloads and hashes remain unchanged.
COMMENT ON TABLE mantik_paper_decisions IS 'v2 decisions retain all selected-registration inputs and publications available at finalization; legacy v1 completeness is unproven';
