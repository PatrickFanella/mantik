// Package strategyidentity defines the canonical identity without runtime or storage dependencies.
package strategyidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

var ErrInvalidSpec = errors.New("invalid strategy spec")
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var paramPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var kinds = []string{"daily_bars_adjusted", "option_chains_indicative", "stock_universe"}

// Spec declares what a strategy is and needs. Version is the strategy code's
// version; bump it whenever its behaviour changes. Params are the strategy's
// parameters as text. Data lists the data kinds it reads, and Warmup the
// number of bars it needs before its first decision.
type Spec struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Params      map[string]string `json:"params"`
	Data        []string          `json:"data"`
	Warmup      int               `json:"warmup"`
	ModelDriven bool              `json:"model_driven,omitempty"`
}

// Validate reports whether the spec is well formed.
func (s Spec) Validate() error {
	if !namePattern.MatchString(s.Name) || s.Version == "" || len(s.Version) > 64 || s.Warmup < 0 || len(s.Data) == 0 || len(s.Params) > 64 {
		return ErrInvalidSpec
	}
	for k, v := range s.Params {
		if !paramPattern.MatchString(k) || len(v) > 256 {
			return fmt.Errorf("%w: parameter %q", ErrInvalidSpec, k)
		}
	}
	for _, d := range s.Data {
		if !slices.Contains(kinds, d) {
			return fmt.Errorf("%w: unknown data kind %q", ErrInvalidSpec, d)
		}
	}
	return nil
}

// ID is the content hash of the strategy version: its name, code version,
// parameters, data kinds and warm-up. Map order and data-kind order or
// repetition do not change it.
func (s Spec) ID() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	data := slices.Clone(s.Data)
	slices.Sort(data)
	data = slices.Compact(data)
	params := s.Params
	if params == nil {
		params = map[string]string{}
	}
	// encoding/json writes map keys in sorted order.
	raw, err := json.Marshal(struct {
		Name        string            `json:"name"`
		Version     string            `json:"version"`
		Params      map[string]string `json:"params"`
		Data        []string          `json:"data"`
		Warmup      int               `json:"warmup"`
		ModelDriven bool              `json:"model_driven,omitempty"`
	}{s.Name, s.Version, params, data, s.Warmup, s.ModelDriven})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "strategy-" + hex.EncodeToString(sum[:]), nil
}
