-- Fence writers that cannot read windowed v3 decision and outcome bases.
-- Retained v1 and v2 payloads and hashes remain unchanged.
COMMENT ON TABLE mantik_paper_decisions IS 'v3 decisions retain the evaluated window of selected-registration sources and bind all earlier ones by digest; v2 retain them all; legacy v1 completeness is unproven';
COMMENT ON TABLE mantik_paper_outcomes IS 'v3 outcomes retain the evaluated window of selected-registration sources and bind all earlier ones by digest; v2 retain them all; legacy v1 completeness is unproven';
