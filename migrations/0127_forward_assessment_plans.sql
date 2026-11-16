-- Additive: never backfill qualification credit from incubation or threshold data.
CREATE TABLE IF NOT EXISTS mantik_forward_assessment_plans (
 id text PRIMARY KEY,
 declaration_id text NOT NULL UNIQUE REFERENCES mantik_forward_declarations(id),
 payload json NOT NULL CHECK (octet_length(payload::text)<=131072 AND json_typeof(payload)='object' AND payload->'Plan'->>'ID'=id AND payload->'Plan'->>'DeclarationID'=declaration_id)
);
DROP TRIGGER IF EXISTS assessment_plan_immutable ON mantik_forward_assessment_plans;
CREATE TRIGGER assessment_plan_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_forward_assessment_plans FOR EACH STATEMENT EXECUTE FUNCTION mantik_forward_immutable();
