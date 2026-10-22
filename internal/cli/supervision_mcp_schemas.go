package cli

func supervisionMCPSchemas() map[string]any {
	str := func() any { return map[string]any{"type": "string"} }
	obj := func(required []string, props map[string]any) any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	enum := func(values ...string) any { return map[string]any{"type": "string", "enum": values} }
	policy := obj([]string{"id", "provider", "account_id", "symbols", "behavior", "rule_id", "max_observation_delay_seconds", "enabled"}, map[string]any{"id": str(), "provider": str(), "account_id": str(), "symbols": map[string]any{"type": "array", "items": str(), "minItems": 1, "maxItems": 16}, "behavior": enum("research", "decision_review"), "rule_id": enum(""), "max_observation_delay_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400}, "enabled": map[string]any{"type": "boolean"}})
	claim := obj([]string{"field", "value"}, map[string]any{"field": str(), "value": str()})
	return map[string]any{
		"inspect_record":         obj([]string{"kind", "id"}, map[string]any{"kind": enum("decision", "review_case", "news_record", "research_review", "watch"), "id": str()}),
		"request_research":       obj([]string{"receiver_id", "request_id", "case_id", "version"}, map[string]any{"receiver_id": str(), "request_id": str(), "case_id": str(), "version": map[string]any{"type": "integer", "minimum": 1}, "type_id": str()}),
		"change_watch_policy":    obj([]string{"id", "receiver_id", "previous_sha256", "policy"}, map[string]any{"id": str(), "receiver_id": str(), "previous_sha256": str(), "policy": policy}),
		"recheck_exception":      obj([]string{"exception_id", "request_id"}, map[string]any{"exception_id": str(), "request_id": str()}),
		"review_decision_claims": obj([]string{"id", "receiver_id", "decision_id", "decision_sha256", "claims"}, map[string]any{"id": str(), "receiver_id": str(), "decision_id": str(), "decision_sha256": str(), "explanation": str(), "claims": map[string]any{"type": "array", "items": claim, "minItems": 1, "maxItems": 16}}),
	}
}
