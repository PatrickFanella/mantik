// Command capitalcheck derives cold-start recipe targets at a reviewed snapshot
// and reports exact financial capital boundaries. It never changes admissions.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/backtest"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

type sourceRef struct {
	URL, SHA256 string
	ObservedAt  time.Time
}
type sourceDocument struct {
	sourceRef
	Body string
}
type gateReceipt struct {
	StrategyID, DecisionID, Status, Lifecycle, DeclarationSHA256, FamilySHA256 string
	FailedChecks                                                               map[string]string
	Source                                                                     sourceRef
}
type asset struct {
	ID, Symbol, Class string
	Fractionable      bool
	LargeCapReview    string
	Sources           []sourceRef
}
type snapshot struct {
	At, ReviewedAt      time.Time
	ReviewedBy          string
	Assets              []asset
	Bars                map[string][]marketdata.AdjustedBar
	Universe            marketdata.UniverseReport
	Marks, Limits, Fees map[string]string
	Sources             []sourceRef
	MaximumCents        int64
	CalendarDates       []string
	ExpectedSessions    int
	CalendarSource      sourceDocument
	GateSource          sourceDocument
}

func (s snapshot) AdjustedBars(ctx context.Context, id string, at time.Time) (marketdata.Series, error) {
	if err := ctx.Err(); err != nil {
		return marketdata.Series{}, err
	}
	if !at.Equal(s.At) {
		return marketdata.Series{}, strategy.ErrFutureData
	}
	return marketdata.Series{AssetID: id, AsOf: at, Bars: s.Bars[id]}, nil
}
func (s snapshot) StockUniverse(context.Context, time.Time) (marketdata.UniverseReport, error) {
	return s.Universe, nil
}

type recipeResult struct {
	Kind, StrategyID string
	Spec             strategy.Spec
	Assets           []string
	Decision         strategy.Decision
	Capital          backtest.CapitalBoundary
	CashCapital      backtest.CapitalBoundary
	OriginalGate     gateReceipt
}
type report struct {
	EvidenceClass, InputSHA256, ReviewedBy string
	At, ReviewedAt                         time.Time
	QualificationCredit                    bool
	Scope                                  []asset
	Sources                                []sourceRef
	Results                                []recipeResult
	Request                                allocation.RebalanceRequest
	JointPortfolio                         backtest.CapitalBoundary
	ExpectedSessions                       int
	CalendarSource                         sourceRef
}

func evaluate(ctx context.Context, s snapshot) (report, error) {
	out := report{EvidenceClass: "snapshot_financial_diagnostic_unqualified", At: s.At, ReviewedAt: s.ReviewedAt, ReviewedBy: s.ReviewedBy, Scope: s.Assets, Sources: s.Sources, ExpectedSessions: s.ExpectedSessions, CalendarSource: s.CalendarSource.sourceRef}
	if s.At.IsZero() || s.ReviewedAt.IsZero() || s.ReviewedAt.After(s.At) || s.ReviewedBy == "" || len(s.Assets) < 5 || len(s.Assets) > 64 || !s.Universe.AsOf.Equal(s.At) {
		return out, errors.New("capital snapshot requires an explicit bounded reviewed scope")
	}
	validateRef := func(r sourceRef) error {
		decoded, err := hex.DecodeString(r.SHA256)
		if err != nil || len(decoded) != 32 || r.URL == "" || r.ObservedAt.IsZero() || r.ObservedAt.After(s.ReviewedAt) {
			return errors.New("invalid or future reviewed source")
		}
		return nil
	}
	if len(s.Sources) == 0 {
		return out, errors.New("snapshot source references required")
	}
	for _, doc := range []sourceDocument{s.CalendarSource, s.GateSource} {
		if err := validateRef(doc.sourceRef); err != nil {
			return out, err
		}
		sum := sha256.Sum256([]byte(doc.Body))
		if hex.EncodeToString(sum[:]) != doc.SHA256 {
			return out, errors.New("reviewed document source hash differs")
		}
	}
	u, err := url.Parse(s.CalendarSource.URL)
	if err != nil || u.Scheme != "https" || u.Host != "paper-api.alpaca.markets" || u.Path != "/v2/calendar" {
		return out, errors.New("calendar requires captured Alpaca source")
	}
	var calendar []struct{ Date string }
	if json.Unmarshal([]byte(s.CalendarSource.Body), &calendar) != nil || len(calendar) != s.ExpectedSessions || len(calendar) != len(s.CalendarDates) || s.ExpectedSessions < 1 {
		return out, errors.New("retained calendar session count differs")
	}
	for i, d := range calendar {
		if d.Date != s.CalendarDates[i] {
			return out, errors.New("calendar dates differ from original capture")
		}
	}
	for i, date := range s.CalendarDates {
		if _, err := time.Parse(time.DateOnly, date); err != nil || date >= s.At.UTC().Format(time.DateOnly) || i > 0 && date <= s.CalendarDates[i-1] {
			return out, errors.New("invalid retained session coverage")
		}
	}
	for _, r := range s.Sources {
		if err := validateRef(r); err != nil {
			return out, err
		}
	}
	ids, symbols := map[string]asset{}, map[string]bool{}
	for _, a := range s.Assets {
		if a.ID == "" || a.Symbol == "" || ids[a.ID].ID != "" || symbols[a.Symbol] || !a.Fractionable || len(a.Sources) == 0 {
			return out, errors.New("invalid or nonfractionable scoped asset")
		}
		for _, r := range a.Sources {
			if err := validateRef(r); err != nil {
				return out, err
			}
		}
		ids[a.ID] = a
		symbols[a.Symbol] = true
		prior := ""
		if len(s.Bars[a.ID]) != s.ExpectedSessions {
			return out, errors.New("asset complete session count differs from reviewed calendar")
		}
		for i, b := range s.Bars[a.ID] {
			if b.Date != s.CalendarDates[i] || b.Raw.Date != b.Date || b.Date <= prior || b.Date >= s.At.UTC().Format(time.DateOnly) || b.Raw.AssetID != a.ID || b.Raw.Symbol != a.Symbol || b.Raw.ObservedAt.IsZero() || b.Raw.PublishedAt.Before(b.Raw.ObservedAt) || b.Raw.AvailableAt.Before(b.Raw.PublishedAt) || b.Raw.AvailableAt.After(s.ReviewedAt) || b.Raw.AvailableAt.After(s.At) {
				return out, errors.New("invalid, reordered or unavailable snapshot bars")
			}
			prior = b.Date
		}
	}
	req := allocation.RebalanceRequest{ID: "capital-snapshot", At: s.At, Limits: s.Limits, Fees: s.Fees}
	for _, kind := range []string{library.Trend, library.Momentum, library.Reversal, library.LowVol} {
		cfg := library.Default(kind)
		compiled, err := library.New(cfg)
		if err != nil {
			return out, err
		}
		assets := []string{}
		for _, a := range s.Assets {
			if kind == library.Trend && a.Class == "etf" || kind != library.Trend && a.Class == "large_cap_stock" {
				if a.Class == "large_cap_stock" && a.LargeCapReview == "" {
					return out, errors.New("large-cap source review required")
				}
				assets = append(assets, a.ID)
			}
		}
		slices.Sort(assets)
		if len(assets) == 0 || kind != library.Trend && len(assets) < cfg.Count {
			return out, errors.New("reviewed scope lacks the complete configured recipe universe")
		}
		view, err := strategy.Build(ctx, compiled.Spec(), s, s.At, assets, strategy.Book{})
		if err != nil {
			return out, err
		}
		if kind != library.Trend && !slices.Equal(view.Universe(), assets) {
			return out, errors.New("configured asset is absent from eligible universe")
		}
		warm := compiled.Spec().Warmup
		if len(s.CalendarDates) < warm {
			return out, errors.New("calendar coverage shorter than recipe warmup")
		}
		for _, asset := range assets {
			bars := s.Bars[asset]
			if len(bars) < warm {
				return out, errors.New("asset coverage shorter than recipe warmup")
			}
			for j, date := range s.CalendarDates[len(s.CalendarDates)-warm:] {
				if bars[len(bars)-warm+j].Date != date {
					return out, errors.New("asset warmup misses a retained session")
				}
			}
		}
		for _, id := range assets {
			if _, _, err := library.Feature(cfg, view, id); err != nil {
				return out, fmt.Errorf("%s warmup/feature coverage %s: %w", kind, id, err)
			}
		}
		decision, err := strategy.Decide(compiled, view)
		if err != nil {
			return out, err
		}
		id, err := compiled.Spec().ID()
		if err != nil {
			return out, err
		}
		gate, err := publishedGate(s.GateSource, kind, id)
		if err != nil {
			return out, err
		}
		targets := allocation.TargetDecision{}
		for _, t := range decision.Targets {
			targets.Targets = append(targets.Targets, allocation.WeightTarget{Asset: ids[t.Asset].Symbol, Weight: t.Weight})
		}
		req.Sleeves = append(req.Sleeves, allocation.Sleeve{ID: kind, StrategyID: id, Stage: "incubation", Risk: "1", Minimum: "0"})
		req.Targets = append(req.Targets, allocation.SleeveTarget{SleeveID: kind, Decision: targets})
		out.Results = append(out.Results, recipeResult{Kind: kind, StrategyID: id, Spec: compiled.Spec(), Assets: assets, Decision: decision, OriginalGate: gate})
	}
	out.Request = req
	for i := range out.Results {
		var err error
		one := req
		one.Sleeves = req.Sleeves[i : i+1]
		one.Targets = req.Targets[i : i+1]
		out.Results[i].Capital, err = backtest.StrategyMinimum(ctx, account.Margin, one, s.Marks, out.Results[i].Kind, s.MaximumCents)
		if err != nil {
			return out, err
		}
		out.Results[i].CashCapital, err = backtest.StrategyMinimum(ctx, account.Cash, one, s.Marks, out.Results[i].Kind, s.MaximumCents)
		if err != nil {
			return out, err
		}
	}
	// An idle subject reports no_trade before searching the roster. Select an
	// active recipe for the joint check while retaining every sleeve's budget.
	jointSubject := out.Results[0].Kind
	for _, result := range out.Results {
		if len(result.Decision.Targets) > 0 {
			jointSubject = result.Kind
			break
		}
	}
	out.JointPortfolio, err = backtest.StrategyMinimum(ctx, account.Margin, req, s.Marks, jointSubject, s.MaximumCents)
	if err != nil {
		return out, err
	}
	return out, nil
}

func publishedGate(source sourceDocument, kind, id string) (gateReceipt, error) {
	r := gateReceipt{StrategyID: id, DecisionID: "wave9-" + kind, Status: "failed", Lifecycle: "idea", FailedChecks: map[string]string{}, Source: source.sourceRef}
	if source.URL != "https://github.com/subculture-collective/mantik/issues/72#issuecomment-6908455626" {
		return r, errors.New("original gate source is not the published issue 72 receipt")
	}
	if !strings.Contains(source.Body, "all four base recipe versions failed and remain at `idea`") {
		return r, errors.New("original gate failure status absent from published receipt")
	}
	_, section, ok := strings.Cut(source.Body, "**wave9-"+kind+"**")
	if !ok {
		return r, errors.New("original gate receipt section absent")
	}
	section, _, _ = strings.Cut(section, "**wave9-")
	if !strings.Contains(section, "Strategy `"+id+"`") {
		return r, errors.New("original gate strategy identity differs")
	}
	for label, dst := range map[string]*string{"Declaration SHA-256": &r.DeclarationSHA256, "Family snapshot SHA-256": &r.FamilySHA256} {
		pattern := regexp.MustCompile(regexp.QuoteMeta(label) + ": `([a-f0-9]{64})`")
		match := pattern.FindStringSubmatch(section)
		if match == nil {
			return r, errors.New("original gate source pin absent")
		}
		*dst = match[1]
	}
	row := regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([^`]+)` \\|$")
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		r.FailedChecks[m[1]] = m[2]
	}
	checks := expectedGateChecks()
	if len(r.FailedChecks) != len(checks) {
		return r, errors.New("original gate failed-check count differs")
	}
	for name, reason := range checks {
		if r.FailedChecks[name] != reason {
			return r, errors.New("original gate failed-check identity or reason differs")
		}
	}
	return r, nil
}

func expectedGateChecks() map[string]string {
	checks := map[string]string{"source_coverage": "reviewed_universe_and_price_coverage_required", "point_in_time": "historical_availability_evidence_required"}
	for _, fold := range []string{"fold_0", "fold_1"} {
		checks[fold+"/base/test/spy"] = "positive_costed_excess_over_dated_SPY_required"
		checks[fold+"/base/stress/spy"] = "positive_costed_excess_over_dated_SPY_required"
		checks[fold+"/base/test/DSR"] = "deflated_sharpe_at_least_0.95"
	}
	return checks
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("capitalcheck", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("input", "", "private reviewed snapshot JSON")
	pin := fs.String("sha256", "", "exact snapshot SHA-256")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *path == "" || *pin == "" {
		return errors.New("input and sha256 required")
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return errors.New("snapshot exceeds 32 MiB")
	}
	sum := sha256.Sum256(raw)
	actual := hex.EncodeToString(sum[:])
	if actual != *pin {
		return errors.New("snapshot pin differs")
	}
	var input snapshot
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	r, err := evaluate(ctx, input)
	r.InputSHA256 = actual
	return errors.Join(err, json.NewEncoder(out).Encode(r))
}
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
