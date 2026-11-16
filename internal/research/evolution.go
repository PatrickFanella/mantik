package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

const maxVariants = 8

// variant is a bounded edit of reviewed numeric expression operators. Parent
// policy, data and identity cannot be edited by an evolution request.
type variant struct {
	Operator    string     `json:"operator"`
	Parents     []string   `json:"parents"`
	Path        []int      `json:"path,omitempty"`
	Replacement *spec.Expr `json:"replacement,omitempty"`
}

// evolutionEvidence is retained in each immutable start before any backtest.
// Units are a deterministic search preference, never predicted returns.
type evolutionEvidence struct {
	Policy           string `json:"policy"`
	Ordinal          int    `json:"ordinal"`
	ProposalSHA256   string `json:"proposal_sha256"`
	Complexity       int    `json:"complexity"`
	Similarity       int    `json:"similarity"`
	NearestLibrary   string `json:"nearest_library,omitempty"`
	Penalty          int    `json:"penalty"`
	DefinitionSHA256 string `json:"definition_sha256,omitempty"`
	Rejection        string `json:"rejection,omitempty"`
	Selected         bool   `json:"selected"`
}

type evolutionResult struct {
	Total      int                 `json:"total"`
	Candidates []evolutionEvidence `json:"candidates"`
	Selected   string              `json:"selected,omitempty"`
	Evaluation toolResult          `json:"evaluation"`
}

// suppliedVariants scans all variants arrays before strict decoding, including
// duplicate members and complete proposals before a malformed suffix. A partial
// element is itself one failed proposal; its exact bytes stay in the reply.
func suppliedVariants(raw string) []json.RawMessage {
	d := json.NewDecoder(strings.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil
	}
	var out []json.RawMessage
	for d.More() {
		key, e := d.Token()
		if e != nil {
			break
		}
		name, _ := key.(string)
		if !strings.EqualFold(name, "variants") {
			var skip json.RawMessage
			if d.Decode(&skip) != nil {
				break
			}
			continue
		}
		token, e := d.Token()
		if e != nil {
			out = append(out, json.RawMessage(`null`))
			break
		}
		if token != json.Delim('[') {
			out = append(out, json.RawMessage(`null`))
			// A complete malformed value cannot hide later top-level proposals.
			// Token has consumed primitives; consume a compound value as well.
			if token == json.Delim('{') {
				depth := 1
				for depth > 0 {
					next, err := d.Token()
					if err != nil {
						return out
					}
					if next == json.Delim('{') || next == json.Delim('[') {
						depth++
					} else if next == json.Delim('}') || next == json.Delim(']') {
						depth--
					}
				}
			}
			continue
		}
		for d.More() {
			offset := d.InputOffset()
			var v json.RawMessage
			if d.Decode(&v) != nil {
				out = append(out, json.RawMessage(raw[offset:]))
				return out
			}
			out = append(out, v)
		}
		if _, e := d.Token(); e != nil {
			break
		}
	}
	return out
}

func parentDefinition(ctx context.Context, s Trials, d Declaration, id string, at time.Time) (spec.Definition, error) {
	var def spec.Definition
	if !strategytrial.ValidID(id) {
		return def, ErrInvalid
	}
	p, e := s.StrategyTrial(ctx, id)
	if errors.Is(e, strategytrial.ErrNotFound) {
		return def, ErrInvalid
	}
	if e != nil {
		return def, e
	}
	var config struct {
		SessionID string    `json:"session_id"`
		Model     Identity  `json:"model"`
		Trigger   Trigger   `json:"trigger"`
		AsOf      time.Time `json:"as_of"`
	}
	if json.Unmarshal(p.Start.Config, &config) != nil || config.SessionID != d.ID || !config.AsOf.Equal(d.AsOf) || digest(canonical(marshal(config.Model))) != digest(canonical(marshal(d.Identity))) || digest(canonical(marshal(config.Trigger))) != digest(canonical(marshal(d.Trigger))) || p.Start.Family != d.Family || p.Start.DataSHA256 != digest(marshal(d.Data)) || p.Start.StartedAt.After(at) || p.Outcome == nil || p.Outcome.Status != "completed" || p.Outcome.FinishedAt.After(at) {
		return def, ErrInvalid
	}
	var retained struct {
		Definition json.RawMessage `json:"definition"`
		Attempt    string          `json:"attempt"`
	}
	if json.Unmarshal(p.Start.Spec, &retained) != nil {
		return def, ErrInvalid
	}
	if len(retained.Definition) == 0 {
		// Compatibility with reviewed pre-evolution desk starts, which retained the
		// exact bounded tool request rather than a separate definition field.
		var request Request
		if strict([]byte(retained.Attempt), &request) != nil {
			return def, ErrInvalid
		}
		retained.Definition = request.Spec
	}
	if strict(retained.Definition, &def) != nil {
		return def, ErrInvalid
	}
	if _, e := spec.Compile(def); e != nil || def.Score == nil {
		return def, ErrInvalid
	}
	return def, nil
}

func produceVariant(ctx context.Context, s Trials, d Declaration, v variant, at time.Time) (spec.Definition, error) {
	var def spec.Definition
	if v.Operator != "mutate_score" && v.Operator != "recombine_add" {
		return def, ErrInvalid
	}
	want := 1
	if v.Operator == "recombine_add" {
		want = 2
	}
	if len(v.Parents) != want || len(v.Path) > spec.MaxDepth {
		return def, ErrInvalid
	}
	if want == 2 && (v.Parents[0] == v.Parents[1] || len(v.Path) != 0 || v.Replacement != nil) {
		return def, ErrInvalid
	}
	parents := make([]spec.Definition, want)
	for i, id := range v.Parents {
		var e error
		parents[i], e = parentDefinition(ctx, s, d, id, at)
		if e != nil {
			return def, e
		}
	}
	def = parents[0]
	switch v.Operator {
	case "mutate_score":
		if v.Replacement == nil {
			return def, ErrInvalid
		}
		node := &def.Score
		for _, index := range v.Path {
			if index < 0 || index >= len((*node).Args) {
				return def, ErrInvalid
			}
			node = &(*node).Args[index]
		}
		*node = v.Replacement
	case "recombine_add":
		// Policy agreement is mandatory; recombination cannot silently inherit a
		// different universe, gross exposure or side policy from one parent.
		other := parents[1]
		if def.Gross != other.Gross || def.LongOnly != other.LongOnly || def.Universe != other.Universe {
			return def, ErrInvalid
		}
		def.Score = &spec.Expr{Op: "add", Args: []*spec.Expr{def.Score, other.Score}}
	default:
		return def, ErrInvalid
	}
	def.Name = "desk_variant"
	if _, e := spec.Compile(def); e != nil {
		return def, ErrInvalid
	}
	return def, nil
}

func evolve(ctx context.Context, s Trials, d Declaration, r Request, raw string, proposals, specs []json.RawMessage, parseErr error, at time.Time, step int) (toolResult, bool, error) {
	// Stray specs are also attempts; they must not vanish behind an evolution tool.
	proposals = append(proposals, specs...)
	prepared := make([]preparedAttempt, len(proposals))
	evidence := make([]evolutionEvidence, len(proposals))
	seen := map[string]bool{}
	selected := -1
	validEnvelope := parseErr == nil && r.Tool == "evolve_specs" && len(r.Spec) == 0 && len(r.Parents) == 0 && r.ID == "" && r.Asset == "" && len(r.Variants) == len(proposals)
	for i, proposal := range proposals {
		if e := ctx.Err(); e != nil {
			return toolResult{}, false, e
		}
		ev := evolutionEvidence{Policy: "desk-evolution-v1", Ordinal: i + 1, ProposalSHA256: digest(proposal)}
		var v variant
		var def spec.Definition
		var problem error
		if strict(proposal, &v) != nil {
			problem = ErrInvalid
		} else {
			def, problem = produceVariant(ctx, s, d, v, at)
			if problem != nil && !errors.Is(problem, ErrInvalid) {
				return toolResult{}, false, problem
			}
		}
		if problem == nil {
			ev.Complexity, ev.Similarity, ev.NearestLibrary = penalties(def)
			ev.Penalty = ev.Complexity + ev.Similarity
			ev.DefinitionSHA256 = definitionDigest(def)
		}
		switch {
		case !validEnvelope:
			ev.Rejection = "invalid evolution envelope"
		case i >= maxVariants:
			ev.Rejection = "variant limit"
		case problem != nil:
			ev.Rejection = "invalid typed edit or parent"
		case seen[ev.DefinitionSHA256]:
			ev.Rejection = "duplicate definition in batch"
		}
		if ev.DefinitionSHA256 != "" {
			seen[ev.DefinitionSHA256] = true
		}
		if ev.Rejection == "" && (selected < 0 || ev.Penalty < evidence[selected].Penalty) {
			selected = i
		}
		parents := v.Parents
		if len(parents) > 2 {
			parents = nil
		}
		for _, parent := range parents {
			if _, err := parentDefinition(ctx, s, d, parent, at); err != nil {
				if !errors.Is(err, ErrInvalid) {
					return toolResult{}, false, err
				}
				// A rejected sibling cannot acquire a lineage link when this
				// same reply is recovered after earlier starts were retained.
				parents = nil
				break
			}
		}
		req := Request{Tool: "run_backtest", Parents: parents}
		if problem == nil {
			req.Spec = marshal(def)
		}
		// Avoid repeating a 64-KiB reply in every start. The reply journal holds
		// exact bytes; starts bind both the complete reply and individual proposal.
		receipt := string(marshal(map[string]any{"reply_sha256": digest([]byte(raw)), "ordinal": i + 1, "proposal_sha256": ev.ProposalSHA256}))
		p, e := prepareAttempt(ctx, s, d, req, receipt, nil, at, step, i)
		if e != nil {
			return toolResult{}, false, e
		}
		prepared[i], evidence[i] = p, ev
	}
	if selected >= 0 {
		evidence[selected].Selected = true
	}
	created := make([]bool, len(prepared))
	out := toolResult{Code: "evolved_inactive", Stage: "idea"}
	result := evolutionResult{Candidates: evidence[:min(len(evidence), maxVariants)], Total: len(evidence)}
	// Freeze all starts, selection and penalties before evaluating any candidate.
	// If interrupted during this loop, retry fills only the missing starts. Every
	// existing unfinished start is conservative crash evidence, never rerun.
	// The durable family fence stays pending until dispatch reconciles all starts.
	for i := range prepared {
		var declaration map[string]json.RawMessage
		if e := json.Unmarshal(prepared[i].start.Spec, &declaration); e != nil {
			return out, false, e
		}
		declaration["evolution"] = marshal(evidence[i])
		if prepared[i].compileErr == nil {
			declaration["definition"] = prepared[i].request.Spec
		}
		prepared[i].start.Spec = marshal(declaration)
		added, e := s.StartStrategyTrial(ctx, prepared[i].start)
		if e != nil {
			return out, false, e
		}
		created[i] = added
		if len(prepared) <= maxVariants || i < maxVariants {
			out.TrialIDs = append(out.TrialIDs, prepared[i].start.ID)
		}
	}
	for i, p := range prepared {
		if i == selected {
			result.Selected = p.start.ID
			evaluation, _, e := evaluateAttempt(ctx, s, d, p, created[i])
			if e != nil {
				return out, false, e
			}
			result.Evaluation = evaluation
			continue
		}
		// Nonselected proposals never run. Completed means retained inactive data,
		// not that a backtest, gate or qualification has passed.
		status := "completed"
		if evidence[i].Rejection != "" {
			status = "failed"
		}
		if !created[i] {
			old, e := s.StrategyTrial(ctx, p.start.ID)
			if e != nil {
				return out, false, e
			}
			if old.Outcome != nil {
				continue
			}
			status = "crashed"
		}
		if e := s.FinishStrategyTrial(ctx, p.start.ID, strategytrial.Outcome{Status: status, FinishedAt: time.Now().UTC()}); e != nil {
			return out, false, e
		}
	}
	// Detailed evidence for every ordinal is in its immutable start. Limit the
	// model-facing result so even a maximal rejected batch fits one journal event.
	out.Value = result
	return out, false, nil
}

// definitionDigest ignores display names and canonicalizes the reviewed recipe
// lookback order. It detects exact behavior definitions, not profitability.
func definitionDigest(d spec.Definition) string {
	d.Name = "canonical"
	if d.Library != nil {
		c := *d.Library
		c.Lookbacks = slices.Clone(c.Lookbacks)
		slices.Sort(c.Lookbacks)
		d.Library = &c
	}
	return digest(marshal(d))
}

// tokens is a canonical structural multiset. Windows use deterministic 5-session
// buckets so a one-session parameter edit near a recipe retains similarity.
// Constants retain their sign; magnitudes do not buy novelty. Child order is
// retained in subtree tokens, while node tokens also recognize shared signals.
func expressionTokens(n *spec.Expr, out map[string]int) string {
	args := make([]string, len(n.Args))
	for i, a := range n.Args {
		args[i] = expressionTokens(a, out)
	}
	value := n.Op
	if n.Field != "" {
		value += ":" + n.Field
	}
	if n.Window != 0 {
		value += ":" + strconv.Itoa((n.Window+2)/5)
	}
	if n.Op == "const" {
		value += ":" + strconv.Itoa(sign(n.Value))
	}
	out["node:"+value]++
	tree := value + "(" + strings.Join(args, ",") + ")"
	out["tree:"+tree]++
	return tree
}
func sign(v float64) int {
	if v < 0 {
		return -1
	}
	if v > 0 {
		return 1
	}
	return 0
}

// policyTokens makes side, universe and gross weight differences visible. A
// 10-percentage-point bucket is a search heuristic, not a risk approval.
func policyTokens(d spec.Definition, out map[string]int) {
	out[fmt.Sprintf("policy:%t:%t:%d", d.LongOnly, d.Universe, int(d.Gross*10+.5))]++
}

func tokenSimilarity(a, b map[string]int) int {
	intersection, union := 0, 0
	for k, n := range a {
		intersection += min(n, b[k])
		union += max(n, b[k])
	}
	for k, n := range b {
		if a[k] == 0 {
			union += n
		}
	}
	if union == 0 {
		return 0
	}
	return 1000 * intersection / union
}
