-- Version new comparison readiness without rewriting historical owner journals.
COMMENT ON TABLE mantik_owned_allocations IS 'Owner comparisons v14 retain known excluded-candidate assessment failures; older journals retain original semantics';
