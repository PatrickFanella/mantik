// Package analyst compiles a retained categorical model view into fixed targets.
// Subscription calls belong to the external runner, never to strategy code.
package analyst

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// Version pins the roles, prompt protocol, long-only mapping and evidence rules.
const Version = "analyst-v1"

// ErrNoTarget prevents the legacy weight-reuse path from turning an outage into orders.
var ErrNoTarget = errors.New("analyst has no target; exposure-hold bridge required")

// Config is owner-reviewed metadata frozen before the prospective declaration.
// Cutoff is the inclusive last training date; no default cutoff is inferred.
type Config struct {
	Provider, Model, Cutoff, Source, ReviewedBy string
	ReviewedAt                                  time.Time
	MetadataSHA256                              string
	Assets                                      []string
	DailyLimit, TimeoutSeconds                  int
}

// MetadataHash binds the exact source-review receipt independently of runtime data.
func (c Config) MetadataHash() string {
	raw, _ := json.Marshal(struct {
		Provider, Model, Cutoff, Source, ReviewedBy string
		ReviewedAt                                  time.Time
	}{c.Provider, c.Model, c.Cutoff, c.Source, c.ReviewedBy, c.ReviewedAt})
	return hash(raw)
}

// Spec supplies the canonical, model-driven identity including all frozen settings.
func (c Config) Spec() (strategy.Spec, error) {
	cutoff, e := time.Parse(time.DateOnly, c.Cutoff)
	if e != nil || cutoff.Year() < 1900 || c.ReviewedAt.IsZero() || c.ReviewedBy == "" || len(c.ReviewedBy) > 128 || c.ReviewedAt.Before(cutoff) || c.MetadataHash() != c.MetadataSHA256 || c.DailyLimit < 1 || c.DailyLimit > 100 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 || len(c.Model) == 0 || len(c.Model) > 128 || len(c.Source) > 2048 || len(c.Assets) < 1 || len(c.Assets) > 8 {
		return strategy.Spec{}, errors.New("invalid frozen analyst configuration")
	}
	for _, r := range c.Model {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", r) {
			return strategy.Spec{}, errors.New("invalid model identity")
		}
	}
	if strings.HasPrefix(c.Model, "-") || (c.Provider != "codex" && c.Provider != "claude") || c.Provider == "codex" && !strings.HasPrefix(c.Source, "https://developers.openai.com/") && !strings.HasPrefix(c.Source, "https://platform.openai.com/") || c.Provider == "claude" && !strings.HasPrefix(c.Source, "https://docs.anthropic.com/") && !strings.HasPrefix(c.Source, "https://platform.claude.com/") {
		return strategy.Spec{}, errors.New("analyst requires a reviewed provider source")
	}
	assets := slices.Clone(c.Assets)
	slices.Sort(assets)
	for i, a := range assets {
		if len(a) == 0 || len(a) > 16 || i > 0 && assets[i-1] == a {
			return strategy.Spec{}, errors.New("invalid analyst assets")
		}
		for _, r := range a {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-", r) {
				return strategy.Spec{}, errors.New("invalid analyst asset")
			}
		}
	}
	c.Assets = assets
	raw, _ := json.Marshal(c)
	s := strategy.Spec{Name: "model_analyst", Version: Version, Params: map[string]string{"config_sha256": hash(raw), "metadata_sha256": c.MetadataSHA256}, Data: []string{strategy.KindDailyBars}, Warmup: 1, ModelDriven: true}
	return s, s.Validate()
}

// Selection is the entire model output vocabulary. It contains no size, weight,
// quantity, stop, order, health or activation fields. Hold has no target.
type Selection struct {
	Action string `json:"action"`
	Asset  string `json:"asset"`
}

// Parse rejects unknown, duplicate, differently cased, missing and null fields,
// trailing JSON, markdown and assets outside the frozen universe.
func Parse(raw string, assets []string) (Selection, error) {
	bad := errors.New("invalid categorical analyst output")
	if len(raw) > 4096 {
		return Selection{}, bad
	}
	d := json.NewDecoder(strings.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return Selection{}, bad
	}
	fields := map[string]string{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return Selection{}, bad
		}
		k, ok := t.(string)
		if !ok || k != "action" && k != "asset" {
			return Selection{}, bad
		}
		if _, exists := fields[k]; exists {
			return Selection{}, bad
		}
		t, e = d.Token()
		v, ok := t.(string)
		if e != nil || !ok {
			return Selection{}, bad
		}
		fields[k] = v
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') || len(fields) != 2 {
		return Selection{}, bad
	}
	if d.Decode(new(any)) != io.EOF {
		return Selection{}, bad
	}
	s := Selection{Action: fields["action"], Asset: fields["asset"]}
	if s.Action == "long" && slices.Contains(assets, s.Asset) || (s.Action == "hold" || s.Action == "flat") && s.Asset == "" {
		return s, nil
	}
	return Selection{}, bad
}

// Receipt binds one completed workflow to its exact view, never a backtest result.
// An empty or unsuccessful receipt means no target, distinct from explicit flat.
type Receipt struct {
	ForwardDeclarationID string `json:",omitempty"`
	CycleID              string `json:",omitempty"`
	RequestSHA256        string `json:",omitempty"`
	ResultSHA256         string `json:",omitempty"`
	At                   time.Time
	InputAt              time.Time
	StrategyID           string
	ViewSHA256           string
	EvidenceClass        string
	SourceSHA256         string
	Completed            bool
	Selection            Selection
}

// Strategy is a deterministic adapter over an immutable receipt.
type Strategy struct {
	spec    strategy.Spec
	receipt Receipt
}

// New validates configuration and copies it into a compiled strategy.
func New(c Config, r Receipt) (*Strategy, error) {
	s, e := c.Spec()
	if e != nil {
		return nil, e
	}
	if r.Completed {
		sid, _ := s.ID()
		if r.StrategyID != sid || r.At.IsZero() || r.InputAt.IsZero() || r.At.Before(r.InputAt) || len(r.ViewSHA256) != 64 || len(r.SourceSHA256) != 64 || (r.EvidenceClass != "synthetic_fixture" && r.EvidenceClass != "prospective_market_unqualified") {
			return nil, errors.New("analyst receipt identity differs")
		}
		raw, _ := json.Marshal(r.Selection)
		if _, e = Parse(string(raw), c.Assets); e != nil {
			return nil, e
		}
	}
	return &Strategy{spec: s, receipt: r}, nil
}

// Spec returns an independent copy of the frozen version.
func (s *Strategy) Spec() strategy.Spec {
	v := s.spec
	v.Params = map[string]string{}
	for k, x := range s.spec.Params {
		v.Params[k] = x
	}
	v.Data = slices.Clone(v.Data)
	return v
}

// Input returns the complete point-in-time model input and its deterministic hash.
// The runner validates raw clocks and cutoff before making subscription calls.
func Input(v strategy.View) ([]byte, string) {
	return input(v, v.At())
}

func input(v strategy.View, at time.Time) ([]byte, string) {
	bars := map[string]any{}
	for _, a := range v.Assets() {
		bars[a] = v.Bars(a)
	}
	raw, _ := json.Marshal(struct {
		At     time.Time
		Assets []string
		Bars   map[string]any
	}{at, v.Assets(), bars})
	return raw, hash(raw)
}

// NoTarget distinguishes an unavailable/hold receipt from explicit flat.
// Service bridges must translate this to allocation.HoldExposure, because the
// legacy KeepPositions path can resize from previous target weights.
func (s *Strategy) NoTarget(v strategy.View) bool {
	_, h := input(v, s.receipt.InputAt)
	return !s.receipt.Completed || !s.receipt.At.Equal(v.At()) || s.receipt.ViewSHA256 != h || s.receipt.Selection.Action == "hold"
}

// Decide maps a selected long to a code-fixed unit sleeve weight. Only the
// capital distributor can turn that weight into money or quantity.
func (s *Strategy) Decide(v strategy.View) (strategy.Decision, error) {
	if s.NoTarget(v) {
		return strategy.Decision{}, ErrNoTarget
	}
	if s.receipt.Selection.Action == "flat" {
		return strategy.Decision{}, nil
	}
	return strategy.Decision{Targets: []strategy.Target{{Asset: s.receipt.Selection.Asset, Weight: "1"}}}, nil
}

func hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

// IncubationDecision consumes a retained runtime receipt after its completion
// clock, with a fixed 60-second lifetime. Holds preserve actual quantities.
// The ordinary Strategy.Decide contract stays unchanged for legacy engines.
func (s *Strategy) IncubationDecision(v strategy.View) (strategy.Decision, bool, error) {
	r := s.receipt
	for _, h := range []string{r.SourceSHA256, r.RequestSHA256, r.ResultSHA256} {
		if b, e := hex.DecodeString(h); e != nil || len(b) != 32 {
			return strategy.Decision{}, false, errors.New("analyst runtime source binding unavailable")
		}
	}
	if r.EvidenceClass != "prospective_market_unqualified" || r.CycleID == "" || r.ForwardDeclarationID == "" || r.At.IsZero() || r.InputAt.IsZero() || v.At().Before(r.At) {
		return strategy.Decision{}, false, errors.New("analyst incubation requires retained prospective post-completion evidence")
	}
	state, _ := json.Marshal(r)
	d := strategy.Decision{State: state}
	_, h := input(v, r.InputAt)
	if !r.Completed || r.Selection.Action == "hold" || v.At().Sub(r.At) > time.Minute || h != r.ViewSHA256 {
		return d, true, nil
	}
	if r.Selection.Action == "long" {
		d.Targets = []strategy.Target{{Asset: r.Selection.Asset, Weight: "1"}}
	}
	return d, false, nil
}

// ForwardDeclarationID identifies the prospective owner of this runtime receipt.
func (s *Strategy) ForwardDeclarationID() string { return s.receipt.ForwardDeclarationID }
