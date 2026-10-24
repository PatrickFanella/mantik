-- Fence writers that cannot bind source reviews or preserve their source cut.
-- Existing v1 decision review payloads remain unchanged.
COMMENT ON TABLE mantik_decision_reviews IS 'v2 social/copy source-bound deterministic claim reviews; source and claims remain unverified';
COMMENT ON TABLE mantik_social_watch_journal IS 'new source writes follow retained source-bound decision review cutoffs; exact historical retries preserved';
