-- Startup fence for v2 outcome observation source membership replay.
-- Retained v1 outcome payloads and hashes remain unchanged.
COMMENT ON TABLE mantik_paper_outcomes IS 'v2 outcomes retain all selected-registration inputs and publications available at evaluation; legacy v1 completeness is unproven';
