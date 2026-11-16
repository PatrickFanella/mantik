package analyst

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

func config() Config {
	c := Config{Provider: "codex", Model: "fixture-model", Cutoff: "2026-04-30", Source: "https://developers.openai.com/api/docs/models/fixture-model", ReviewedBy: "fixture-owner", ReviewedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Assets: []string{"SPY", "QQQ"}, DailyLimit: 10, TimeoutSeconds: 60}
	c.MetadataSHA256 = c.MetadataHash()
	return c
}

type bars struct{}

func (bars) AdjustedBars(_ context.Context, a string, at time.Time) (marketdata.Series, error) {
	return marketdata.Series{AssetID: a, AsOf: at, Bars: []marketdata.AdjustedBar{{Date: "2026-05-04", Close: "100", TotalReturn: "1"}}}, nil
}
func TestCompiledTargetsAndNoTargetBoundary(t *testing.T) {
	c := config()
	spec, e := c.Spec()
	if e != nil {
		t.Fatal(e)
	}
	sid, _ := spec.ID()
	v, e := strategy.Build(context.Background(), spec, bars{}, time.Date(2026, 5, 5, 22, 0, 0, 0, time.UTC), c.Assets, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	_, h := Input(v)
	for _, action := range []string{"long", "flat", "hold", "unavailable", "wrong_view"} {
		t.Run(action, func(t *testing.T) {
			r := Receipt{StrategyID: sid, At: v.At(), InputAt: v.At(), ViewSHA256: h, EvidenceClass: "synthetic_fixture", SourceSHA256: strings.Repeat("b", 64), Completed: true, Selection: Selection{Action: action}}
			switch action {
			case "long":
				r.Selection.Asset = "SPY"
			case "unavailable":
				r.Completed = false
			case "wrong_view":
				r.Selection.Action = "long"
				r.Selection.Asset = "SPY"
				r.ViewSHA256 = strings.Repeat("a", 64)
			}
			s, e := New(c, r)
			if e != nil {
				t.Fatal(e)
			}
			d, e := strategy.Decide(s, v)
			if action == "long" {
				if e != nil || !reflect.DeepEqual(d.Targets, []strategy.Target{{Asset: "SPY", Weight: "1"}}) {
					t.Fatal(d, e)
				}
				again, e := strategy.Decide(s, v)
				if e != nil || !reflect.DeepEqual(again, d) {
					t.Fatal("nondeterministic", again, e)
				}
			} else if action == "flat" {
				if e != nil || len(d.Targets) != 0 || d.KeepPositions {
					t.Fatal(d, e)
				}
			} else if !errors.Is(e, ErrNoTarget) || d.KeepPositions || len(d.Targets) != 0 {
				t.Fatal("unsafe legacy weight reuse", d, e)
			}
		})
	}
	if e := strategy.Audit("."); e != nil {
		t.Fatal(e)
	}
}
func TestStrictCategoricalOutput(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `{"action":"flat"}`, `{"action":"flat","asset":null}`, `{"Action":"flat","asset":""}`,
		`{"action":"long","action":"flat","asset":"SPY"}`, `{"action":"long","asset":"SPY","asset":"QQQ"}`,
		`{"action":"long","asset":"SPY","weight":"1"}`, `{"action":"long","asset":"SPY","stop":"90"}`,
		`{"action":"long","asset":"SPY","orders":[]}`, `{"action":"long","asset":"SPY","health":"ready"}`,
		`{"action":"long","asset":"UNKNOWN"}`, `{"action":"short","asset":"SPY"}`, `{"action":"hold","asset":"SPY"}`,
		`{"action":"flat","asset":""} {}`, "```json\n{}\n```", strings.Repeat("x", 4097),
	} {
		if _, e := Parse(raw, []string{"SPY"}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{`{"action":"long","asset":"SPY"}`, `{"asset":"","action":"hold"}`, `{"action":"flat","asset":""}`} {
		if _, e := Parse(raw, []string{"SPY"}); e != nil {
			t.Fatal(raw, e)
		}
	}
}
func TestFrozenIdentityAndCopies(t *testing.T) {
	c := config()
	spec, e := c.Spec()
	if e != nil {
		t.Fatal(e)
	}
	id, _ := spec.ID()
	c.Assets = []string{"QQQ", "SPY"}
	reordered, _ := c.Spec()
	rid, _ := reordered.ID()
	if id != rid {
		t.Fatal("asset order changed identity")
	}
	for name, change := range map[string]func(*Config){
		"model":   func(c *Config) { c.Model = "another-model"; c.MetadataSHA256 = c.MetadataHash() },
		"cutoff":  func(c *Config) { c.Cutoff = "2026-04-29"; c.MetadataSHA256 = c.MetadataHash() },
		"limit":   func(c *Config) { c.DailyLimit = 11 },
		"timeout": func(c *Config) { c.TimeoutSeconds = 90 },
		"assets":  func(c *Config) { c.Assets = []string{"SPY"} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := c
			change(&copy)
			s, e := copy.Spec()
			if e != nil {
				t.Fatal(e)
			}
			other, _ := s.ID()
			if other == id {
				t.Fatal("identity omitted frozen configuration")
			}
		})
	}
	s, e := New(c, Receipt{})
	if e != nil {
		t.Fatal(e)
	}
	copy := s.Spec()
	copy.Params["config_sha256"] = "changed"
	copy.Data[0] = "changed"
	actual, _ := s.Spec().ID()
	if actual != id {
		t.Fatal("spec mutation changed compiled strategy")
	}
	r := Receipt{StrategyID: "wrong", At: time.Now(), Completed: true, ViewSHA256: strings.Repeat("a", 64), Selection: Selection{Action: "flat"}}
	if _, e := New(c, r); e == nil {
		t.Fatal("accepted mismatched receipt")
	}
}
