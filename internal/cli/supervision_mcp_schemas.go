package cli

func supervisionMCPSchemas() map[string]any {
	str := func() any { return map[string]any{"type": "string"} }
	obj := func(required []string, props map[string]any) any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	enum := func(values ...string) any { return map[string]any{"type": "string", "enum": values} }
	policy := obj([]string{"id", "provider", "account_id", "symbols", "behavior", "rule_id", "max_observation_delay_seconds", "enabled"}, map[string]any{"id": str(), "provider": str(), "account_id": str(), "symbols": map[string]any{"type": "array", "items": str(), "minItems": 1, "maxItems": 16}, "behavior": enum("research", "decision_review"), "rule_id": enum(""), "max_observation_delay_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400}, "enabled": map[string]any{"type": "boolean"}})
	// The copy tool permits bounded intent only when every independent bound is
	// supplied. The service owner validates cross-field limits and policy CAS.
	integer := func(min, max int64) any { return map[string]any{"type": "integer", "minimum": min, "maximum": max} }
	follow := obj([]string{"max_order_notional_cents", "max_exposure_cents", "stop_distance_bps", "max_holding_seconds"}, map[string]any{
		"max_order_notional_cents": integer(1, 1000000000000), "max_exposure_cents": integer(1, 1000000000000),
		"stop_distance_bps": integer(1, 5000), "max_holding_seconds": integer(1, 86400),
	})
	bounded := map[string]any{}
	for k, v := range policy.(map[string]any)["properties"].(map[string]any) {
		bounded[k] = v
	}
	bounded["behavior"], bounded["follow"] = enum("bounded_paper_follow"), follow
	boundedRequired := append([]string{}, policy.(map[string]any)["required"].([]string)...)
	boundedRequired = append(boundedRequired, "follow")
	copyPolicy := map[string]any{"oneOf": []any{policy, obj(boundedRequired, bounded)}}
	claim := obj([]string{"field", "value"}, map[string]any{"field": str(), "value": str()})
	return map[string]any{
		"inspect_record":           obj([]string{"kind", "id"}, map[string]any{"kind": enum("decision", "decision_explanation", "decision_market_evidence", "decision_attribution", "allocation_comparison", "admission_review", "review_case", "news_record", "research_review", "watch", "copy_watch", "social_applicability", "copy_applicability"), "id": str()}),
		"request_research":         obj([]string{"receiver_id", "request_id", "case_id", "version"}, map[string]any{"receiver_id": str(), "request_id": str(), "case_id": str(), "version": map[string]any{"type": "integer", "minimum": 1}, "type_id": str()}),
		"change_watch_policy":      obj([]string{"id", "receiver_id", "previous_sha256", "policy"}, map[string]any{"id": str(), "receiver_id": str(), "previous_sha256": str(), "policy": policy}),
		"change_copy_watch_policy": obj([]string{"id", "receiver_id", "previous_sha256", "policy"}, map[string]any{"id": str(), "receiver_id": str(), "previous_sha256": str(), "policy": copyPolicy}),
		"recheck_exception":        obj([]string{"exception_id", "request_id"}, map[string]any{"exception_id": str(), "request_id": str()}),
		"review_decision_claims":   obj([]string{"id", "receiver_id", "decision_id", "decision_sha256", "claims"}, map[string]any{"id": str(), "receiver_id": str(), "decision_id": str(), "decision_sha256": str(), "explanation": str(), "source": obj([]string{"capture_id", "capture_sha256"}, map[string]any{"capture_id": str(), "capture_sha256": str()}), "claims": map[string]any{"type": "array", "items": claim, "minItems": 1, "maxItems": 16}}),
	}
}
