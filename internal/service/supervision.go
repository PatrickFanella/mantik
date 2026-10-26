package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const maxSupervisionCallBytes = 20 << 10

// Arguments are decoded into the owning route's command type before dispatch.
// The adapter has no independent journal, clock, authority or effect issuer.
type researchRequestArguments struct {
	ReceiverID string `json:"receiver_id"`
	RequestID  string `json:"request_id"`
	CaseID     string `json:"case_id"`
	Version    int    `json:"version"`
	TypeID     string `json:"type_id,omitempty"`
}
type inspectArguments struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type recheckArguments struct {
	ExceptionID string `json:"exception_id"`
	RequestID   string `json:"request_id"`
}
type supervisionCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}
type supervisionTool struct {
	Name             string `json:"name"`
	ArgumentContract string `json:"argument_contract"`
	Mutates          bool   `json:"mutates"`
}

// IDs cannot become paths, queries, fragments, authorities or encoded separators.
var supervisionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,159}$`)

func supervisionRoutes(mux *http.ServeMux, store storage.Store) {
	tools := []supervisionTool{{"inspect_record", "{kind: outcome|outcome_market_evidence|evaluation_declaration|evaluation_evidence|evaluation_assessment|decision|decision_explanation|decision_market_evidence|decision_attribution|allocation_comparison|admission_review|review_case|news_record|research_review|watch|copy_watch|social_record|copy_record|social_research_request|social_applicability|copy_applicability, id: string}; owner availability applies", false}}
	if _, ok := store.(storage.ReviewStore); ok {
		tools = append(tools, supervisionTool{"request_research", "news-review start command: receiver_id, request_id, case_id, version, optional type_id", true})
	}
	if _, ok := store.(storage.SocialWatchStore); ok {
		tools = append(tools,
			supervisionTool{"request_social_research", "socialwatch.ResearchCommand; select retained capture id/hash; pending research only, no dispatch", true},
			supervisionTool{"change_watch_policy", "socialwatch.PolicyCommand; behavior restricted to research or decision_review", true},
			supervisionTool{"change_copy_watch_policy", "socialwatch.PolicyCommand; research, decision_review or inactive bounded_paper_follow intent; no admission or execution", true})
	}
	if _, ok := store.(storage.OperationalStore); ok {
		tools = append(tools, supervisionTool{"recheck_exception", "{exception_id: string, request_id: string}; fixed recheck operation", true})
	}
	if _, ok := store.(storage.DecisionReviewStore); ok {
		tools = append(tools, supervisionTool{"review_decision_claims", "storage.DecisionReviewCommand; retained finalized decision, typed claims, optional owned social/copy recording pin; no extraction attestation", true})
	}
	invalid := func(w http.ResponseWriter) { write(w, 422, map[string]string{"error": "invalid supervision call"}) }
	mux.HandleFunc("GET /v1/supervision/tools", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w)
			return
		}
		write(w, 200, struct {
			Schema              string            `json:"schema"`
			Tools               []supervisionTool `json:"tools"`
			ModelUsed           bool              `json:"model_used"`
			QualificationCredit bool              `json:"qualification_credit"`
		}{"mantik-supervision-tools-v1", tools, false, false})
	})
	mux.HandleFunc("POST /v1/supervision/calls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			invalid(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSupervisionCallBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var call supervisionCall
		if err != nil || workerplan.ReadBoundedShape(raw, &call, maxSupervisionCallBytes) != nil {
			invalid(w)
			return
		}
		available := false
		for _, tool := range tools {
			if tool.Name == call.Tool {
				available = true
				break
			}
		}
		if !available {
			invalid(w)
			return
		}
		method, path := http.MethodPost, ""
		var body any
		decode := func(out any, limit int) bool {
			args := bytes.TrimSpace(call.Arguments)
			return len(args) > 0 && args[0] == '{' && workerplan.ReadBoundedShape(args, out, limit) == nil
		}
		switch call.Tool {
		case "inspect_record":
			var args inspectArguments
			if !decode(&args, 4096) || !supervisionID.MatchString(args.ID) {
				invalid(w)
				return
			}
			paths := map[string]string{"outcome": "/v1/paper-outcomes/", "outcome_market_evidence": "/v1/paper-outcomes/", "evaluation_declaration": "/v1/paper-evaluation-declarations/", "evaluation_evidence": "/v1/paper-evaluation-declarations/", "evaluation_assessment": "/v1/paper-evaluation-assessments/", "decision_attribution": "/v1/paper-attribution/", "allocation_comparison": "/v1/allocation-comparisons/", "admission_review": "/v1/admission-reviews/", "decision": "/v1/paper-decisions/", "decision_explanation": "/v1/paper-decisions/", "decision_market_evidence": "/v1/paper-decisions/", "review_case": "/v1/decision-review-cases/", "news_record": "/v1/news-recordings/", "research_review": "/v1/news-reviews/", "watch": "/v1/social-watches/", "copy_watch": "/v1/copy-watches/", "social_research_request": "/v1/social-research-requests/", "social_record": "/v1/social-recordings/", "copy_record": "/v1/copy-recordings/", "social_applicability": "/v1/social-recordings/", "copy_applicability": "/v1/copy-recordings/"}
			prefix, ok := paths[args.Kind]
			if !ok {
				invalid(w)
				return
			}
			method, path = http.MethodGet, prefix+args.ID
			switch args.Kind {
			case "social_applicability", "copy_applicability":
				path += "/applicability"
			case "evaluation_evidence":
				path += "/evidence"
			case "decision_explanation":
				path += "/explanation"
			case "decision_market_evidence", "outcome_market_evidence":
				path += "/market-evidence"
			}
		case "request_social_research":
			var args socialwatch.ResearchCommand
			if !decode(&args, 4096) || args.Validate() != nil {
				invalid(w)
				return
			}
			path, body = "/v1/social-research-requests", args
		case "request_research":
			var args researchRequestArguments
			if !decode(&args, 4096) {
				invalid(w)
				return
			}
			path, body = "/v1/news-reviews", args
		case "change_watch_policy":
			var args socialwatch.PolicyCommand
			if !decode(&args, 4096) || args.Validate() != nil || (args.Policy.Behavior != "research" && args.Policy.Behavior != "decision_review") {
				invalid(w)
				return
			}
			path, body = "/v1/social-watches", args
		case "change_copy_watch_policy":
			var args socialwatch.PolicyCommand
			if !decode(&args, 4096) || args.Validate() != nil || args.Policy.Behavior == "configured_rule" {
				invalid(w)
				return
			}
			path, body = "/v1/copy-watches", args
		case "recheck_exception":
			var args recheckArguments
			if !decode(&args, 4096) || !supervisionID.MatchString(args.ExceptionID) {
				invalid(w)
				return
			}
			path = "/v1/operations/exceptions/" + args.ExceptionID + "/recover"
			body = struct {
				RequestID string `json:"request_id"`
				Operation string `json:"operation"`
			}{args.RequestID, "recheck"}
		case "review_decision_claims":
			var args storage.DecisionReviewCommand
			if !decode(&args, storage.MaxDecisionReviewCommandBytes) || args.Validate() != nil {
				invalid(w)
				return
			}
			path, body = "/v1/paper-decision-reviews", args
		}
		var payload []byte
		if method != http.MethodGet {
			payload, err = json.Marshal(body)
			if err != nil {
				invalid(w)
				return
			}
		}
		// Only this fixed in-process route is selected. Preserve cancellation and the
		// owner's exact status/body; no outbound request, response rewriting or retry.
		target := r.Clone(r.Context())
		target.Method, target.URL = method, &url.URL{Path: path}
		target.RequestURI = path
		target.Body = io.NopCloser(bytes.NewReader(payload))
		target.ContentLength = int64(len(payload))
		target.TransferEncoding, target.Trailer, target.GetBody = nil, nil, nil
		target.Header = make(http.Header)
		target.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, target)
	})
}
