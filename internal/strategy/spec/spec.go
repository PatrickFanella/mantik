// Package spec compiles a bounded expression vocabulary into pure strategies.
// The vocabulary is informed by qlib Ref/Mean/Std/Rank and Alpha158 features;
// this is a reviewed subset, not a parser for arbitrary Python or qlib code.
package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

const (
	MaxNodes    = 64
	MaxDepth    = 12
	MaxLookback = 504
	MaxWork     = 200000
)

// Expr is a typed-by-validation tree. Each node has only its operator's fields:
// const uses Value, field uses Field, lag/mean/std/return use Window, and all
// compound operators use Args. No symbol strings can name runtime functions.
type Expr struct {
	Op     string  `json:"op"`
	Value  float64 `json:"value,omitempty"`
	Field  string  `json:"field,omitempty"`
	Window int     `json:"window,omitempty"`
	Args   []*Expr `json:"args,omitempty"`
}

// Definition is either an expression recipe or one reviewed library recipe.
// Expression scores normalize to Gross absolute target weight; LongOnly clips
// negative scores to zero. Ineligible/unavailable assets receive zero targets.
type Definition struct {
	Name     string          `json:"name"`
	Score    *Expr           `json:"score,omitempty"`
	Library  *library.Config `json:"library,omitempty"`
	Gross    float64         `json:"gross"`
	LongOnly bool            `json:"long_only"`
	Universe bool            `json:"universe"`
}

// Compiled holds a private copied tree and immutable identity metadata.
type Compiled struct {
	def      Definition
	metadata strategy.Spec
	block    *library.Stock
	cost     int
}

// Compile rejects invalid, cyclic, excessive or ill-typed trees with a reason.
// A successful compilation owns a deep copy, so later caller edits have no effect.
func Compile(d Definition) (*Compiled, error) {
	if !finite(d.Gross) || d.Gross <= 0 || d.Gross > 1 {
		return nil, errors.New("gross must be finite in (0,1]")
	}
	if (d.Score == nil) == (d.Library == nil) {
		return nil, errors.New("choose exactly one expression or library recipe")
	}
	data := []string{strategy.KindDailyBars}
	if d.Universe {
		data = append(data, strategy.KindStockUniverse)
	}
	count := 0
	path := map[*Expr]bool{}
	warm, cost := 0, 1
	var validate func(*Expr, int) (string, int, int, error)
	validate = func(n *Expr, depth int) (string, int, int, error) {
		if n == nil {
			return "", 0, 0, errors.New("nil expression")
		}
		count++
		if depth > MaxDepth || count > MaxNodes || path[n] {
			return "", 0, 0, errors.New("expression exceeds depth/node limit or contains a cycle")
		}
		path[n] = true
		defer delete(path, n)
		arity := 0
		kind := "number"
		window := false
		switch n.Op {
		case "const":
			if !finite(n.Value) || math.Abs(n.Value) > 1e6 {
				return "", 0, 0, errors.New("constant must be finite and bounded")
			}
		case "field":
			if !slices.Contains([]string{"open", "high", "low", "close", "volume"}, n.Field) {
				return "", 0, 0, errors.New("unknown price field")
			}
		case "option_iv", "option_delta":
			data = append(data, strategy.KindOptionChains)
		case "lag":
			arity = 1
			window = true
			if n.Window < 1 {
				return "", 0, 0, errors.New("lag must be positive; forward reads are forbidden")
			}
		case "return":
			arity = 1
			window = true
			if n.Window < 1 {
				return "", 0, 0, errors.New("return window must be positive")
			}
		case "mean", "std":
			arity = 1
			window = true
			if n.Window < 2 {
				return "", 0, 0, errors.New("rolling window must be at least 2")
			}
		case "rank", "sign", "abs":
			arity = 1
		case "add", "sub", "mul", "div":
			arity = 2
		case "gt", "lt":
			arity = 2
			kind = "bool"
		case "where":
			arity = 3
		default:
			return "", 0, 0, fmt.Errorf("unknown operator %q", n.Op)
		}
		if len(n.Args) != arity || n.Op != "const" && n.Value != 0 || n.Op != "field" && n.Field != "" || !window && n.Window != 0 || window && n.Window > 252 {
			return "", 0, 0, fmt.Errorf("invalid fields/arity/window for %s", n.Op)
		}
		look, work := 0, 1
		for i, a := range n.Args {
			k, l, w, e := validate(a, depth+1)
			if e != nil {
				return "", 0, 0, e
			}
			want := "number"
			if n.Op == "where" && i == 0 {
				want = "bool"
			}
			if k != want {
				return "", 0, 0, fmt.Errorf("%s argument %d requires %s", n.Op, i, want)
			}
			look = max(look, l)
			work += w
		}
		switch n.Op {
		case "lag", "return":
			look += n.Window
		case "mean", "std":
			look += n.Window - 1
			work *= n.Window
		}
		if look > MaxLookback || work > MaxWork/strategy.MaxAssets {
			return "", 0, 0, errors.New("expression exceeds lookback or work budget")
		}
		return kind, look, work, nil
	}
	if d.Score != nil {
		k, l, w, e := validate(d.Score, 1)
		if e != nil {
			return nil, e
		}
		if k != "number" {
			return nil, errors.New("root score must be numeric")
		}
		warm = l + 1
		cost = w
	}
	var block *library.Stock
	if d.Library != nil {
		var e error
		block, e = library.New(*d.Library)
		if e != nil {
			return nil, e
		}
		s := block.Spec()
		data = s.Data
		warm = s.Warmup
		if d.Gross != d.Library.Gross {
			return nil, errors.New("library and definition gross must match")
		}
		if d.LongOnly || d.Universe {
			return nil, errors.New("library recipe owns its side and universe policy")
		}
	}
	// Validation above bounds encoding and detects cycles before marshaling.
	raw, e := json.Marshal(d)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(raw)
	var owned Definition
	if e := json.Unmarshal(raw, &owned); e != nil {
		return nil, e
	}
	slices.Sort(data)
	data = slices.Compact(data)
	version := "typed-expr-v2"
	if block != nil {
		version += "-" + library.Version
	}
	s := strategy.Spec{Name: d.Name, Version: version, Params: map[string]string{"definition_sha256": hex.EncodeToString(sum[:])}, Data: data, Warmup: warm}
	if e := s.Validate(); e != nil {
		return nil, e
	}
	return &Compiled{def: owned, metadata: s, block: block, cost: cost}, nil
}

// Spec returns a metadata copy with a content hash of the complete definition.
func (c *Compiled) Spec() strategy.Spec {
	s := c.metadata
	s.Data = slices.Clone(s.Data)
	s.Params = map[string]string{"definition_sha256": c.metadata.Params["definition_sha256"]}
	return s
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type value struct {
	v  float64
	ok bool
}
type key struct {
	node   *Expr
	asset  string
	offset int
}

// Decide evaluates only the supplied view. The runtime budget also bounds rank
// work across the view's assets. Division by zero and missing data stay missing.
func (c *Compiled) Decide(v strategy.View) (strategy.Decision, error) {
	if c.block != nil {
		return c.block.Decide(v)
	}
	assets := v.Assets()
	if c.def.Universe {
		assets = v.Universe()
	}
	if len(assets)*c.cost > MaxWork {
		return strategy.Decision{}, errors.New("view exceeds expression work budget")
	}
	bars := map[string][]bar{}
	for _, a := range assets {
		for _, b := range v.RecentBars(a, c.metadata.Warmup) {
			p := bar{fields: map[string]value{}}
			if b.Raw.AvailableAt.IsZero() || b.Raw.AvailableAt.After(v.At()) || b.Raw.AssetID != a || b.Date > v.At().UTC().Format("2006-01-02") {
				bars[a] = append(bars[a], p)
				continue
			}
			for name, text := range map[string]string{"open": b.Open, "high": b.High, "low": b.Low, "close": b.Close, "volume": b.Volume} {
				n, e := strconv.ParseFloat(text, 64)
				p.fields[name] = value{n, e == nil && finite(n) && (name == "volume" && n >= 0 || name != "volume" && n > 0)}
			}
			bars[a] = append(bars[a], p)
		}
	}
	memo := map[key]value{}
	steps := 0
	var eval func(*Expr, string, int) value
	eval = func(n *Expr, a string, offset int) value {
		steps++
		if steps > MaxWork {
			return value{}
		}
		k := key{n, a, offset}
		if old, ok := memo[k]; ok {
			return old
		}
		out := value{}
		defer func() { memo[k] = out }()
		arg := func(i int) value { return eval(n.Args[i], a, offset) }
		switch n.Op {
		case "const":
			out = value{n.Value, true}
		case "field":
			idx := len(bars[a]) - 1 - offset
			if idx >= 0 && idx < len(bars[a]) {
				out = bars[a][idx].fields[n.Field]
			}
		case "option_iv", "option_delta":
			if offset != 0 {
				break
			}
			chain, ok := v.OptionChain(a)
			if !ok {
				break
			}
			sum, count := 0.0, 0
			for _, e := range chain.Entries {
				if e.EstimateStatus == "ok" {
					x := e.IV
					if n.Op == "option_delta" {
						x = e.Greeks.Delta
					}
					sum += x
					count++
				}
			}
			if count > 0 {
				out = value{sum / float64(count), true}
			}
		case "lag":
			out = eval(n.Args[0], a, offset+n.Window)
		case "mean", "std":
			sum := 0.0
			xs := make([]float64, n.Window)
			for i := range xs {
				x := eval(n.Args[0], a, offset+i)
				if !x.ok {
					return value{}
				}
				xs[i] = x.v
				sum += x.v
			}
			mean := sum / float64(n.Window)
			out = value{mean, true}
			if n.Op == "std" {
				variance := 0.0
				for _, x := range xs {
					variance += (x - mean) * (x - mean)
				}
				out.v = math.Sqrt(variance / float64(n.Window))
			}
		case "return":
			x, y := arg(0), eval(n.Args[0], a, offset+n.Window)
			if x.ok && y.ok && y.v != 0 {
				out = value{x.v/y.v - 1, true}
			}
		case "rank":
			// Evaluate and sort the whole cross-section once. Populate memo for
			// every asset, including unavailable values, so later requests for
			// this node/offset do not rescan all assets.
			type ranked struct {
				asset string
				v     float64
			}
			xs := make([]ranked, 0, len(assets))
			for _, other := range assets {
				y := eval(n.Args[0], other, offset)
				memo[key{n, other, offset}] = value{}
				if y.ok {
					xs = append(xs, ranked{other, y.v})
				}
			}
			slices.SortFunc(xs, func(a, b ranked) int {
				steps++
				if a.v < b.v {
					return -1
				}
				if a.v > b.v {
					return 1
				}
				return strings.Compare(a.asset, b.asset)
			})
			if steps > MaxWork {
				return value{}
			}
			for i := 0; i < len(xs); {
				j := i + 1
				for j < len(xs) && xs[j].v == xs[i].v {
					j++
				}
				rank := (float64(i+1) + float64(j)) / 2 / float64(len(xs))
				for _, x := range xs[i:j] {
					steps++
					memo[key{n, x.asset, offset}] = value{rank, true}
				}
				i = j
			}
			out = memo[k]
		case "sign", "abs":
			out = arg(0)
			if n.Op == "abs" {
				out.v = math.Abs(out.v)
			} else if out.v > 0 {
				out.v = 1
			} else if out.v < 0 {
				out.v = -1
			}
		case "where":
			cond := arg(0)
			if cond.ok {
				if cond.v != 0 {
					out = arg(1)
				} else {
					out = arg(2)
				}
			}
		default:
			x, y := arg(0), arg(1)
			if x.ok && y.ok {
				out = value{ok: true}
				switch n.Op {
				case "add":
					out.v = x.v + y.v
				case "sub":
					out.v = x.v - y.v
				case "mul":
					out.v = x.v * y.v
				case "div":
					if y.v == 0 {
						out.ok = false
					} else {
						out.v = x.v / y.v
					}
				case "gt":
					if x.v > y.v {
						out.v = 1
					}
				case "lt":
					if x.v < y.v {
						out.v = 1
					}
				}
			}
		}
		if !finite(out.v) {
			out = value{}
		}
		return out
	}
	scores := map[string]float64{}
	gross := 0.0
	for _, a := range assets {
		x := eval(c.def.Score, a, 0)
		if x.ok {
			if c.def.LongOnly {
				x.v = max(0, x.v)
			}
			scores[a] = x.v
			gross += math.Abs(x.v)
		}
	}
	if steps > MaxWork {
		return strategy.Decision{}, errors.New("expression work budget exhausted")
	}
	d := strategy.Decision{}
	if gross > 0 && finite(gross) {
		for _, a := range assets {
			w := math.Trunc(scores[a]/gross*c.def.Gross*1e12) / 1e12
			if w != 0 {
				d.Targets = append(d.Targets, strategy.Target{Asset: a, Weight: strconv.FormatFloat(w, 'f', 12, 64)})
			}
		}
	}
	return d, nil
}

type bar struct{ fields map[string]value }

// Parse accepts only a bounded JSON definition with no unknown fields.
func Parse(raw []byte) (*Compiled, error) {
	if len(raw) > strategy.MaxStateBytes {
		return nil, errors.New("spec exceeds 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var d Definition
	if e := decoder.Decode(&d); e != nil {
		return nil, e
	}
	if decoder.More() {
		return nil, errors.New("trailing spec content")
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		return nil, errors.New("trailing or malformed spec content")
	}
	return Compile(d)
}
