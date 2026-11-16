// Package research runs an advisory desk with fixed, bounded tools. Its stores
// expose trial writes and report reads, never gate writes or money controls.
package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

var ErrInvalid = errors.New("invalid research desk declaration")
var ErrConflict = errors.New("research desk history conflicts")
var ErrInterrupted = errors.New("desk model call interrupted; retained intent is not relaunched")

// Identity is operator-reviewed configuration, never supplied by the model.
// Cutoff is the inclusive last training date. Metadata is the retained provider
// document or owner configuration; its externally pinned hash is verified.
type Identity struct {
	Provider       model.Provider  `json:"provider"`
	Model          string          `json:"model"`
	Cutoff         string          `json:"cutoff"`
	Source         string          `json:"source"`
	MetadataSHA256 string          `json:"metadata_sha256"`
	Metadata       json.RawMessage `json:"metadata"`
}

// Metadata is a source-review receipt made outside the desk. The operator must
// check the provider source before approving its exact bytes. It is not a model
// assertion or a gate decision.
type Metadata struct {
	Provider   model.Provider `json:"provider"`
	Model      string         `json:"model"`
	Cutoff     string         `json:"cutoff"`
	Source     string         `json:"source"`
	ReviewedBy string         `json:"reviewed_by"`
	ReviewedAt time.Time      `json:"reviewed_at"`
}

// Trigger retains the external scheduler/owner/overseer receipt verbatim.
// ID identifies a schedule occurrence, owner request or finalized finding.
type Trigger struct {
	Kind         string          `json:"kind"`
	ID           string          `json:"id"`
	At           time.Time       `json:"at"`
	SourceSHA256 string          `json:"source_sha256"`
	Receipt      json.RawMessage `json:"receipt"`
}

// TriggerReceipt is supplied by the invoking authority, outside model tools.
// ScheduledFor is mandatory only for a schedule occurrence. A finding must be
// finalized and request research; owner requests name the authenticated actor.
type TriggerReceipt struct {
	Kind         string     `json:"kind"`
	ID           string     `json:"id"`
	At           time.Time  `json:"at"`
	Actor        string     `json:"actor"`
	ScheduledFor *time.Time `json:"scheduled_for,omitempty"`
	Finalized    bool       `json:"finalized,omitempty"`
	Action       string     `json:"action"`
}

// Manifest retains the complete bounded source selection. All raw dates,
// revisions and adjustment inputs must be strictly after the inclusive cutoff.
// Coverage is explicitly unqualified; a hash is not source/gate approval.
type Manifest struct {
	Source        string                                  `json:"source"`
	SourceSHA256  string                                  `json:"source_sha256"`
	EvidenceClass string                                  `json:"evidence_class"`
	Bars          []storage.DailyBar                      `json:"bars"`
	Actions       map[string][]marketdata.CorporateAction `json:"actions"`
}

// Declaration freezes the trust boundary and budgets before the first call.
type Declaration struct {
	ID             string    `json:"id"`
	Family         string    `json:"family"`
	Identity       Identity  `json:"identity"`
	Trigger        Trigger   `json:"trigger"`
	Data           Manifest  `json:"data"`
	AsOf           time.Time `json:"as_of"`
	MaxCalls       int       `json:"max_calls"`
	DailyBudget    int       `json:"daily_budget"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	Goal           string    `json:"goal"`
	// GateIDs scopes reads to separately authored reports; never creates them.
	GateIDs []string `json:"gate_ids"`
}

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func validDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func marshal(v any) []byte { b, _ := json.Marshal(v); return b }
func strict(raw []byte, v any) error {
	if e := uniqueJSON(json.NewDecoder(bytes.NewReader(raw))); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

// uniqueJSON rejects duplicate object members before decoding can discard them.
func uniqueJSON(d *json.Decoder) error {
	token, e := d.Token()
	if e != nil {
		return e
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrInvalid
			}
			seen[name] = true
		}
		if e := uniqueJSON(d); e != nil {
			return e
		}
	}
	_, e = d.Token()
	return e
}

// Validate fails closed on unknown cutoffs, unbound triggers and data clocks.
func (d Declaration) Validate(now time.Time) error {
	i := d.Identity
	var meta Metadata
	var receipt TriggerReceipt
	cutoff, e := time.Parse(time.DateOnly, i.Cutoff)
	u, ue := url.Parse(i.Source)
	if e != nil || cutoff.Year() < 1900 || ue != nil || u.Scheme != "https" || u.User != nil || (i.Provider != model.Codex && i.Provider != model.Claude) || !strategytrial.ValidID(i.Model) || len(i.Model) > 128 || len(i.Source) > 2048 || len(i.Metadata) > 4096 || len(d.Trigger.Receipt) > 4096 || digest(i.Metadata) != i.MetadataSHA256 || strict(i.Metadata, &meta) != nil || meta.Provider != i.Provider || meta.Model != i.Model || meta.Cutoff != i.Cutoff || meta.Source != i.Source || meta.ReviewedBy == "" || meta.ReviewedAt.IsZero() || meta.ReviewedAt.After(d.AsOf) {
		return ErrInvalid
	}
	if i.Provider == model.Codex && u.Host != "developers.openai.com" && u.Host != "platform.openai.com" || i.Provider == model.Claude && u.Host != "docs.anthropic.com" && u.Host != "platform.claude.com" {
		return ErrInvalid
	}
	t := d.Trigger
	if !strategytrial.ValidID(d.ID) || len(d.ID) > 80 || !strategytrial.ValidFamily(d.Family) || t.At.IsZero() || t.At.After(d.AsOf) || d.AsOf.After(now) || !d.AsOf.After(cutoff.AddDate(0, 0, 1)) || !strategytrial.ValidID(t.ID) || digest(t.Receipt) != t.SourceSHA256 || strict(t.Receipt, &receipt) != nil || receipt.Kind != t.Kind || receipt.ID != t.ID || !receipt.At.Equal(t.At) || receipt.Actor == "" || receipt.Action != "research" {
		return ErrInvalid
	}
	switch t.Kind {
	case "schedule":
		if receipt.ScheduledFor == nil || !receipt.ScheduledFor.Equal(t.At) || !receipt.Finalized {
			return ErrInvalid
		}
	case "owner_request":
		if receipt.ScheduledFor != nil {
			return ErrInvalid
		}
	case "overseer_finding":
		if !receipt.Finalized || receipt.ScheduledFor != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if d.MaxCalls < 1 || d.MaxCalls > 12 || d.DailyBudget < 1 || d.DailyBudget > 100 || d.TimeoutSeconds < 1 || d.TimeoutSeconds > 120 || len(d.Goal) == 0 || len(d.Goal) > 2048 || len(d.GateIDs) > 16 || len(d.Data.Bars) == 0 || len(d.Data.Bars) > 2000 || len(d.Data.Source) > 2048 || d.Data.Source == "" || !validDigest(d.Data.SourceSHA256) || (d.Data.EvidenceClass != "synthetic_fixture" && d.Data.EvidenceClass != "historical_market_unqualified") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, b := range d.Data.Bars {
		day, e := time.Parse(time.DateOnly, b.Date)
		if e != nil || !day.After(cutoff) || b.Date > d.AsOf.UTC().Format(time.DateOnly) || b.PublishedAt.IsZero() || b.ObservedAt.IsZero() || b.AvailableAt.IsZero() || b.PublishedAt.Before(day) || b.AvailableAt.Before(b.PublishedAt) || b.AvailableAt.Before(b.ObservedAt) || b.ObservedAt.Before(b.PublishedAt) || b.ObservedAt.After(d.AsOf) || b.AvailableAt.After(d.AsOf) || b.PublishedAt.After(d.AsOf) || !b.PublishedAt.After(cutoff.AddDate(0, 0, 1)) {
			return ErrInvalid
		}
		key := b.AssetID + "/" + b.Date
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	assets := d.assets()
	if len(assets) > 8 {
		return ErrInvalid
	}
	for asset, actions := range d.Data.Actions {
		if !slices.Contains(assets, asset) {
			return ErrInvalid
		}
		for _, a := range actions {
			ex, e := time.Parse(time.DateOnly, a.ExDate)
			if e != nil || !ex.After(cutoff) || !a.KnownAt.After(cutoff.AddDate(0, 0, 1)) || a.KnownAt.After(d.AsOf) {
				return ErrInvalid
			}
		}
	}
	for _, asset := range assets {
		if _, e := d.Data.AdjustedBars(context.Background(), asset, d.AsOf); e != nil {
			return ErrInvalid
		}
	}
	if len(marshal(d)) > 2<<20 {
		return ErrInvalid
	}
	return nil
}
func (d Declaration) assets() []string {
	out := []string{}
	for _, b := range d.Data.Bars {
		out = append(out, b.AssetID)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// AdjustedBars never queries a network or database. The engine can only see the
// frozen post-cutoff bars and adjustment inputs, including its warm-up reads.
func (m Manifest) AdjustedBars(ctx context.Context, asset string, at time.Time) (marketdata.Series, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.Series{}, e
	}
	bars := []storage.DailyBar{}
	for _, b := range m.Bars {
		if b.AssetID == asset && !b.AvailableAt.After(at) && b.Date <= at.UTC().Format(time.DateOnly) {
			bars = append(bars, b)
		}
	}
	return marketdata.Adjust(asset, bars, m.Actions[asset], at)
}

// Trials contains only research history writes. It has no promotion interface.
type Trials interface {
	strategytrial.Store
}

// Gates is read-only, including on the concrete dispatcher.
type Gates interface {
	StrategyGate(context.Context, string) (strategygate.Record, error)
}

// Caller is satisfied by the existing subscription journal.
type Caller interface {
	Call(context.Context, model.Call, int) (model.Record, error)
}

// Event is immutable retained session evidence. Replies are private raw data;
// CLI show returns hashes and trial IDs rather than printing model text.
type Event struct {
	Kind string          `json:"kind"`
	At   time.Time       `json:"at"`
	Body json.RawMessage `json:"body"`
}

// Journal locks one session across processes and appends immutable events.
type Journal interface {
	Lock(context.Context, string) (func(), error)
	Events(context.Context, string) ([]Event, error)
	Append(context.Context, string, int, Event) error
}
