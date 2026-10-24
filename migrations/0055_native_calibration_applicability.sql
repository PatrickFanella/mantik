-- Fence older writers before issuing v3 native assessment recipes.
-- Existing v1/v2 assessment records retain their bytes and replay semantics.
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'Service-owned incomplete native evaluation assessments; v3 derives probability-forecast calibration applicability from exact registered native methods';
