package externalplan

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type Comparison struct {
	Native   *int64 `json:"native"`
	External *int64 `json:"external"`
	Delta    *int64 `json:"delta"`
	Matches  *bool  `json:"matches"`
}
type Trade struct {
	Size       *int64 `json:"size"`
	EntryBar   *int64 `json:"entry_bar"`
	EntryPrice *int64 `json:"entry_price_cents"`
	ExitBar    *int64 `json:"exit_bar"`
	ExitPrice  *int64 `json:"exit_price_cents"`
}

func (t *Trade) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return workerplan.ErrInvalid
	}
	for _, name := range []string{"size", "entry_bar", "entry_price_cents", "exit_bar", "exit_price_cents"} {
		if _, ok := fields[name]; !ok {
			return workerplan.ErrInvalid
		}
	}
	type plain Trade
	return json.Unmarshal(raw, (*plain)(t))
}

type MethodResult struct {
	MethodID        string                `json:"method_id"`
	MethodSHA256    string                `json:"method_sha256"`
	Metrics         Metrics               `json:"metrics"`
	SimulatedTrades []Trade               `json:"simulated_trades"`
	Comparisons     map[string]Comparison `json:"comparisons"`
	Noncomparable   []string              `json:"noncomparable_metrics"`
}
type Result struct {
	Schema                string            `json:"schema"`
	Tool                  string            `json:"tool"`
	Library               string            `json:"library"`
	LibraryVersion        string            `json:"library_version"`
	PythonVersion         string            `json:"python_version"`
	RecipeSHA256          string            `json:"recipe_sha256"`
	DatasetSHA256         string            `json:"dataset_sha256"`
	Conversion            string            `json:"conversion"`
	WarmupCopyBars        *int              `json:"warmup_copy_bars"`
	SettlementCopyBars    *int              `json:"settlement_copy_bars"`
	TradeOnClose          *bool             `json:"trade_on_close"`
	FinalizeTrades        *bool             `json:"finalize_trades"`
	Commission            *int              `json:"commission"`
	Spread                *int              `json:"spread"`
	Margin                *int              `json:"margin"`
	Status                string            `json:"status"`
	Results               []MethodResult    `json:"results"`
	Authority             string            `json:"authority"`
	QualificationCredit   *bool             `json:"qualification_credit"`
	ImageIdentityVerified *bool             `json:"image_identity_verified"`
	IsolationVerified     *bool             `json:"isolation_verified"`
	AllMatch              *bool             `json:"all_comparable_metrics_match"`
	ErrorCode             string            `json:"error_code,omitempty"`
	Artifacts             map[string]string `json:"artifacts"`
	AdapterSHA256         string            `json:"adapter_sha256"`
	RequirementsSHA256    string            `json:"requirements_sha256"`
	SHA256                string            `json:"sha256"`
}

func boolIs(v *bool, want bool) bool { return v != nil && *v == want }
func intIs(v *int, want int) bool    { return v != nil && *v == want }
func (r Result) Validate(j Job) error {
	if r.Results == nil {
		return workerplan.ErrInvalid
	}
	if j.Validate() != nil || r.Schema != ResultSchema || r.Tool != j.Tool || r.Library != "backtesting" || r.LibraryVersion != j.LibraryVersion || r.PythonVersion != "3.13.9" || r.RecipeSHA256 != RecipeSHA || r.DatasetSHA256 != DatasetSHA || r.Conversion != "quote_to_flat_ohlc_with_copied_boundary_bars_v1" || !intIs(r.WarmupCopyBars, 1) || !intIs(r.SettlementCopyBars, 1) || !boolIs(r.TradeOnClose, true) || !boolIs(r.FinalizeTrades, false) || !intIs(r.Commission, 0) || !intIs(r.Spread, 0) || !intIs(r.Margin, 1) || r.Authority != "synthetic_external_fixture_comparison_only" || !boolIs(r.QualificationCredit, false) || !boolIs(r.ImageIdentityVerified, false) || !boolIs(r.IsolationVerified, false) || r.AdapterSHA256 != j.AdapterSHA256 || r.RequirementsSHA256 != j.RequirementsSHA256 || len(r.Artifacts) != 3 || r.AllMatch == nil || !hex.MatchString(r.SHA256) {
		return workerplan.ErrInvalid
	}
	for _, a := range j.Inputs {
		name := a.Reference[:len(a.Reference)-len(".json")]
		if r.Artifacts[name] != a.SHA256 {
			return workerplan.ErrInvalid
		}
	}
	if r.Status == "compared" {
		if len(r.Results) != 4 || r.ErrorCode != "" {
			return workerplan.ErrInvalid
		}
	} else if r.Status != "failed" || r.ErrorCode != "library_execution_failed" || *r.AllMatch || len(r.Results) > 4 {
		return workerplan.ErrInvalid
	}
	all := true
	for index, item := range r.Results {
		pin := pins(j)[index]
		baseline := j.NativeReference.Results[index]
		if item.MethodID != pin.ID || item.MethodSHA256 != pin.SHA256 || !item.Metrics.valid() || item.SimulatedTrades == nil || len(item.SimulatedTrades) > 1 || item.Noncomparable == nil {
			return workerplan.ErrInvalid
		}
		expected := []string{}
		if index == 3 {
			expected = []string{"actions"}
		}
		if !slices.Equal(item.Noncomparable, expected) || len(item.Comparisons) != 4-len(expected) {
			return workerplan.ErrInvalid
		}
		for name, value := range item.Metrics.values() {
			if index == 3 && name == "actions" {
				continue
			}
			c, ok := item.Comparisons[name]
			reference := baseline.Metrics.values()[name]
			if !ok || c.Native == nil || c.External == nil || c.Delta == nil || c.Matches == nil || *c.Native != *reference || *c.External != *value || *c.Delta != *value-*reference || *c.Matches != (*value == *reference) {
				return workerplan.ErrInvalid
			}
			all = all && *c.Matches
		}
		for _, trade := range item.SimulatedTrades {
			if trade.Size == nil || *trade.Size < 1 || *trade.Size > 100 || trade.EntryBar == nil || *trade.EntryBar < 1 || *trade.EntryBar > 4 || trade.EntryPrice == nil || *trade.EntryPrice < 1 || *trade.EntryPrice > 1000000 || (trade.ExitBar == nil) != (trade.ExitPrice == nil) {
				return workerplan.ErrInvalid
			}
			if trade.ExitBar != nil && (*trade.ExitBar < *trade.EntryBar || *trade.ExitBar > 4 || *trade.ExitPrice < 1 || *trade.ExitPrice > 1000000) {
				return workerplan.ErrInvalid
			}
		}
	}
	if r.Status == "compared" && *r.AllMatch != all {
		return workerplan.ErrInvalid
	}
	return nil
}

// ReadResult verifies the Python producer's exact compact-byte hash. Reencoding
// with Go would sort metric maps and change this deliberately distinct contract.
func ReadResult(input io.Reader, j Job) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxResultBytes+1))
	if err != nil || len(raw) > MaxResultBytes {
		return Result{}, workerplan.ErrInvalid
	}
	var r Result
	if workerplan.ReadDatasetShape(raw, &r) != nil || r.Validate(j) != nil {
		return Result{}, workerplan.ErrInvalid
	}
	body := bytes.TrimSuffix(raw, []byte("\n"))
	var compact bytes.Buffer
	if json.Compact(&compact, body) != nil || !bytes.Equal(compact.Bytes(), body) {
		return Result{}, workerplan.ErrInvalid
	}
	suffix := []byte(`,"sha256":"` + r.SHA256 + `"}`)
	if !bytes.HasSuffix(body, suffix) {
		return Result{}, workerplan.ErrInvalid
	}
	unsigned := append(bytes.Clone(body[:len(body)-len(suffix)]), '}')
	if workerplan.Digest(unsigned) != r.SHA256 {
		return Result{}, workerplan.ErrInvalid
	}
	return r, nil
}
