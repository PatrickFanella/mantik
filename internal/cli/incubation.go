package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	analyststrategy "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	strategyspec "git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
)

type incubationInput = service.IncubationInput
type incubationStrategyInput = service.IncubationStrategyInput
type incubationSource = service.IncubationSource

func incubationRequest(in incubationInput) (service.IncubationRequest, error) {
	return incubationRequestFromJournal(context.Background(), in, nil, nil)
}
func incubationHasAnalyst(in incubationInput) bool {
	for _, entry := range in.Strategies {
		if entry.AnalystCycleID != "" {
			return true
		}
	}
	return false
}
func incubationRequestFromJournal(ctx context.Context, in incubationInput, journal analyst.EvidenceReader, fs forward.Store) (service.IncubationRequest, error) {
	if in.Qualification != nil {
		return service.IncubationRequestFromInput(in)
	}
	hash, e := service.IncubationInputHash(in)
	if e != nil {
		return service.IncubationRequest{}, e
	}
	req := service.IncubationRequest{Qualification: in.Qualification, ID: in.ID, InputSHA256: hash, DecidedAt: in.DecidedAt, ExecuteAt: in.ExecuteAt, Quotes: in.Quotes, Limits: in.Limits, Fees: in.Fees, Calendar: in.Calendar, Protective: in.Protective}
	if in.Protective {
		return req, nil
	}
	for _, entry := range in.Strategies {
		choices := 0
		if entry.Library != nil {
			choices++
		}
		if entry.Expression != nil {
			choices++
		}
		if entry.AnalystCycleID != "" {
			choices++
		}
		if choices != 1 {
			return req, errors.New("choose exactly one compiled library, expression or retained analyst cycle")
		}
		if entry.AnalystCycleID != "" {
			if journal == nil || fs == nil {
				return req, errors.New("retained analyst registry required")
			}
			if len(entry.Bars) != 0 || len(entry.Actions) != 0 || entry.Universe != nil {
				return req, errors.New("analyst source must come from its retained request")
			}
			original, result, err := analyst.Retained(ctx, journal, entry.AnalystCycleID)
			if err != nil {
				return req, err
			}
			if result.EvidenceClass != "prospective_market_unqualified" || in.DecidedAt.Before(result.Receipt.At) {
				return req, errors.New("analyst requires prospective evidence known before the decision")
			}
			record, err := fs.ForwardDeclaration(ctx, original.ForwardDeclarationID)
			if err != nil {
				return req, err
			}
			if entry.GateID != "" && entry.GateID != record.Declaration.GateID || entry.ForwardDeclarationID != "" && entry.ForwardDeclarationID != record.Declaration.ID || entry.SleeveID != record.Declaration.SleeveID {
				return req, forward.ErrConflict
			}
			assets := slices.Clone(entry.Assets)
			slices.Sort(assets)
			expected := slices.Clone(original.Config.Assets)
			slices.Sort(expected)
			if len(assets) > 0 && !slices.Equal(assets, expected) {
				return req, errors.New("analyst assets differ from retained configuration")
			}
			compiled, err := analyststrategy.New(original.Config, result.Receipt)
			if err != nil {
				return req, err
			}
			risk := entry.Risk
			if risk == "" {
				risk = "1"
			}
			req.Strategies = append(req.Strategies, service.IncubatingStrategy{SleeveID: entry.SleeveID, GateID: record.Declaration.GateID, ForwardDeclarationID: record.Declaration.ID, Risk: risk, Strategy: compiled, Source: original.Data, Assets: expected})
			continue
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
		req.Strategies = append(req.Strategies, service.IncubatingStrategy{SleeveID: entry.SleeveID, ForwardDeclarationID: entry.ForwardDeclarationID, GateID: entry.GateID, Risk: entry.Risk, Strategy: s, Source: incubationSource{Input: entry}, Assets: entry.Assets})
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
	if *action == "run" && !incubationHasAnalyst(in) {
		var e error
		req, e = incubationRequest(in)
		if e != nil {
			return e
		}
	}
	s, e := storage.OpenPostgresWithQualificationEvaluator(ctx, os.Getenv(*db), service.EvaluateQualificationCollection)
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
	analystJournal := incubationAnalystJournal{database: os.Getenv(*db)}
	if *action == "run" && incubationHasAnalyst(in) {
		req, e = incubationRequestFromJournal(ctx, in, analystJournal, s)
		if e != nil {
			return e
		}
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
		return runIncubationSupervision(ctx, runner, *input, *safetyConfig, out, analystJournal)
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

// Open the model evidence reader only when a changed cycle actually selects an
// analyst. Missing model evidence cannot prevent the safety worker from starting.
type incubationAnalystJournal struct{ database string }

func (j incubationAnalystJournal) Events(ctx context.Context, id string) ([]research.Event, error) {
	records, err := research.OpenPostgresJournal(ctx, j.database)
	if err != nil {
		return nil, err
	}
	defer records.Close()
	return records.Events(ctx, id)
}
