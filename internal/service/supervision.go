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
	tools := []supervisionTool{{"inspect_record", "{kind: decision|review_case|news_record|research_review|watch|copy_watch|social_applicability|copy_applicability, id: string}; owner availability applies", false}}
	if _, ok := store.(storage.ReviewStore); ok {
		tools = append(tools, supervisionTool{"request_research", "news-review start command: receiver_id, request_id, case_id, version, optional type_id", true})
	}
	if _, ok := store.(storage.SocialWatchStore); ok {
		tools = append(tools,
			supervisionTool{"change_watch_policy", "socialwatch.PolicyCommand; behavior restricted to research or decision_review", true},
			supervisionTool{"change_copy_watch_policy", "socialwatch.PolicyCommand; research, decision_review or inactive bounded_paper_follow intent; no admission or execution", true})
	}
	if _, ok := store.(storage.OperationalStore); ok {
		tools = append(tools, supervisionTool{"recheck_exception", "{exception_id: string, request_id: string}; fixed recheck operation", true})
	}
	if _, ok := store.(storage.DecisionReviewStore); ok {
		tools = append(tools, supervisionTool{"review_decision_claims", "storage.DecisionReviewCommand; retained finalized decision and typed claims", true})
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
			paths := map[string]string{"decision": "/v1/paper-decisions/", "review_case": "/v1/decision-review-cases/", "news_record": "/v1/news-recordings/", "research_review": "/v1/news-reviews/", "watch": "/v1/social-watches/", "copy_watch": "/v1/copy-watches/", "social_applicability": "/v1/social-recordings/", "copy_applicability": "/v1/copy-recordings/"}
			prefix, ok := paths[args.Kind]
			if !ok {
				invalid(w)
				return
			}
			method, path = http.MethodGet, prefix+args.ID
			if args.Kind == "social_applicability" || args.Kind == "copy_applicability" {
				path += "/applicability"
			}
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
		payload, err := json.Marshal(body)
		if err != nil {
			invalid(w)
			return
		}
		// Only this fixed in-process route is selected. Preserve cancellation and the
		// owner's exact status/body; no outbound request, response rewriting or retry.
		target := r.Clone(r.Context())
		target.Method, target.URL = method, &url.URL{Path: path}
		target.RequestURI = path
		target.Body = io.NopCloser(bytes.NewReader(payload))
		target.ContentLength = int64(len(payload))
		target.Header = make(http.Header)
		target.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, target)
	})
}
