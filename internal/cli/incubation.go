package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	strategyspec "git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
)

// incubationInput is a reviewed, bounded input manifest, never generated model
// authority. Bars/actions keep their original knowledge and observation clocks.
type incubationInput struct {
	Config               *account.CappedConfig
	Calendar             *service.IncubationCalendar `json:",omitempty"`
	Protective           bool                        `json:",omitempty"`
	ID                   string
	DecidedAt, ExecuteAt time.Time
	At                   time.Time
	Quotes               map[string]account.IncubationQuote
	Limits, Fees         map[string]string
	Strategies           []incubationStrategyInput
}
type incubationStrategyInput struct {
	SleeveID, GateID, Risk string
	ForwardDeclarationID   string `json:",omitempty"`
	Library                *library.Config
	Expression             *strategyspec.Definition
	Assets                 []string
	Bars                   map[string][]storage.DailyBar
	Actions                map[string][]marketdata.CorporateAction
	Universe               *marketdata.UniverseReport
}
type incubationSource struct{ in incubationStrategyInput }

func (s incubationSource) AdjustedBars(ctx context.Context, asset string, at time.Time) (marketdata.Series, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.Series{}, e
	}
	bars, ok := s.in.Bars[asset]
	if !ok {
		return marketdata.Series{}, errors.New("retained bars missing")
	}
	// Never relabel a current fetch as historical knowledge, including a raw bar
	// whose AvailableAt was backdated relative to observation/publication.
	for _, b := range bars {
		if b.AvailableAt.IsZero() || b.ObservedAt.IsZero() || b.PublishedAt.IsZero() || b.AvailableAt.Before(b.ObservedAt) || b.AvailableAt.Before(b.PublishedAt) || b.AvailableAt.After(at) || b.Symbol != asset || b.AssetID != asset {
			return marketdata.Series{}, errors.New("incubation bars require original as-of clocks and symbol binding")
		}
	}
	return marketdata.Adjust(asset, bars, s.in.Actions[asset], at)
}
func (s incubationSource) StockUniverse(ctx context.Context, at time.Time) (marketdata.UniverseReport, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.UniverseReport{}, e
	}
	if s.in.Universe == nil || !s.in.Universe.AsOf.Equal(at) {
		return marketdata.UniverseReport{}, errors.New("reviewed point-in-time universe required")
	}
	return *s.in.Universe, nil
}
func incubationRequest(in incubationInput) (service.IncubationRequest, error) {
	hash, e := service.IncubationInputHash(in)
	if e != nil {
		return service.IncubationRequest{}, e
	}
	req := service.IncubationRequest{ID: in.ID, InputSHA256: hash, DecidedAt: in.DecidedAt, ExecuteAt: in.ExecuteAt, Quotes: in.Quotes, Limits: in.Limits, Fees: in.Fees, Calendar: in.Calendar, Protective: in.Protective}
	if in.Protective {
		return req, nil
	}
	for _, entry := range in.Strategies {
		if (entry.Library == nil) == (entry.Expression == nil) {
			return req, errors.New("choose exactly one compiled library or expression strategy")
		}
		var s strategy.Strategy
		if entry.Library != nil {
			s, e = library.New(*entry.Library)
		} else {
			s, e = strategyspec.Compile(*entry.Expression)
		}
		if e != nil {
			return req, e
		}
		req.Strategies = append(req.Strategies, service.IncubatingStrategy{SleeveID: entry.SleeveID, ForwardDeclarationID: entry.ForwardDeclarationID, GateID: entry.GateID, Risk: entry.Risk, Strategy: s, Source: incubationSource{entry}, Assets: entry.Assets})
	}
	return req, nil
}

func runIncubation(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs, db, id := gateFlags("incubation", errOut)
	action := fs.String("action", "show", "init, run, supervise, sync or show (paper only)")
	input := fs.String("input", "", "reviewed incubation input JSON")
	journal := fs.String("journal", "", "absolute private paper adapter journal directory")
	credentials := fs.String("credentials-file", "", "mode-0600 paper credentials JSON")
	brokerID := fs.String("account-id", "", "expected paper account ID")
	safetyConfig := fs.String("safety-config", "", "separate owner safety policy and quote-file configuration for supervise")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || *id == "" || os.Getenv(*db) == "" {
		return errors.New("incubation requires id and explicit database-env")
	}
	if *action != "init" && *action != "run" && *action != "sync" && *action != "show" && *action != "supervise" {
		return errors.New("unknown incubation action")
	}
	var in incubationInput
	if *action != "show" {
		if e := readGateJSON(*input, &in, 32<<20); e != nil {
			return e
		}
	}
	var req service.IncubationRequest
	if *action == "supervise" && (*safetyConfig == "" || *safetyConfig == *input) {
		return errors.New("supervise requires a separate owner safety-config")
	}
	if *action == "run" {
		var e error
		req, e = incubationRequest(in)
		if e != nil {
			return e
		}
	}
	s, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer s.Close()
	switch *action {
	case "show":
		state, e := s.CappedBook(ctx, *id)
		if e != nil {
			return e
		}
		return emit(out, state)
	case "init":
		if in.Config == nil || in.Config.ID != *id {
			return errors.New("init requires matching capped book configuration")
		}
		if in.Config.OpeningCash != "2000" || in.Config.DepositAmount != "500" || in.Config.EveryWeeks != 2 {
			return errors.New("incubation init requires the fixed 2000 opening cash and 500 biweekly schedule")
		}
		state, e := s.CreateCappedBook(ctx, *in.Config)
		if e != nil {
			return e
		}
		return emit(out, state)
	}
	if *journal == "" || *credentials == "" || *brokerID == "" {
		return errors.New("incubation run/sync require journal, credentials-file and account-id")
	}
	var creds alpaca.PaperCredentials
	if e = readPaperJSON(*credentials, &creds, true); e != nil {
		return e
	}
	j, e := alpaca.OpenJournal(*journal)
	if e != nil {
		return e
	}
	defer func() { _ = j.Close() }()
	broker, e := alpaca.NewPaperTrader(creds, *brokerID, j)
	if e != nil {
		return e
	}
	runner := service.IncubationRunner{Store: s, Broker: broker, BookID: *id}
	if *action == "supervise" {
		return runIncubationSupervision(ctx, runner, *input, *safetyConfig, out)
	}
	if *action == "run" {
		result, e := runner.Run(ctx, req)
		if e != nil {
			return errors.Join(e, emit(out, result))
		}
		return emit(out, result)
	}
	result, e := runner.Sync(ctx, in.At, in.Quotes)
	if e != nil {
		return e
	}
	return emit(out, result)
}
