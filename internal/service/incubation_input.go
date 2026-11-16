package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	strategyspec "git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// IncubationInput is a reviewed, bounded input manifest, never generated model
// authority. Bars/actions keep their original knowledge and observation clocks.
type IncubationInput struct {
	Qualification        *forward.Collection `json:",omitempty"`
	Config               *account.CappedConfig
	Calendar             *IncubationCalendar `json:",omitempty"`
	Protective           bool                `json:",omitempty"`
	ID                   string
	DecidedAt, ExecuteAt time.Time
	At                   time.Time
	Quotes               map[string]account.IncubationQuote
	Limits, Fees         map[string]string
	Strategies           []IncubationStrategyInput
}

// IncubationStrategyInput binds executable configuration to original source observations.
type IncubationStrategyInput struct {
	AnalystCycleID         string `json:",omitempty"`
	SleeveID, GateID, Risk string
	ForwardDeclarationID   string `json:",omitempty"`
	Library                *library.Config
	Expression             *strategyspec.Definition
	Assets                 []string
	Bars                   map[string][]storage.DailyBar
	Actions                map[string][]marketdata.CorporateAction
	Universe               *marketdata.UniverseReport
}

// IncubationSource reads only the retained manifest, without provider calls.
type IncubationSource struct{ Input IncubationStrategyInput }

func (s IncubationSource) AdjustedBars(ctx context.Context, asset string, at time.Time) (marketdata.Series, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.Series{}, e
	}
	bars, ok := s.Input.Bars[asset]
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
	return marketdata.Adjust(asset, bars, s.Input.Actions[asset], at)
}
func (s IncubationSource) StockUniverse(ctx context.Context, at time.Time) (marketdata.UniverseReport, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.UniverseReport{}, e
	}
	if s.Input.Universe == nil || !s.Input.Universe.AsOf.Equal(at) {
		return marketdata.UniverseReport{}, errors.New("reviewed point-in-time universe required")
	}
	return *s.Input.Universe, nil
}

// IncubationRequestFromInput compiles a reviewed manifest and binds its full input hash.
func IncubationRequestFromInput(in IncubationInput) (IncubationRequest, error) {
	hash, e := IncubationInputHash(in)
	if e != nil {
		return IncubationRequest{}, e
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return IncubationRequest{}, e
	}
	req := IncubationRequest{Qualification: in.Qualification, ID: in.ID, InputSHA256: hash, DecidedAt: in.DecidedAt, ExecuteAt: in.ExecuteAt, Quotes: in.Quotes, Limits: in.Limits, Fees: in.Fees, Calendar: in.Calendar, Protective: in.Protective}
	req.QualificationInput = raw
	if in.Qualification != nil && (in.Qualification.SearchTotal != nil || len(in.Qualification.DecisionSources) != 0 || len(in.Qualification.Input) != 0) {
		return req, forward.ErrInvalid
	}
	if in.Protective {
		return req, nil
	}
	for _, entry := range in.Strategies {
		if entry.AnalystCycleID != "" {
			return req, errors.New("analyst cycles require the retained analyst bridge")
		}
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
		req.Strategies = append(req.Strategies, IncubatingStrategy{SleeveID: entry.SleeveID, ForwardDeclarationID: entry.ForwardDeclarationID, GateID: entry.GateID, Risk: entry.Risk, Strategy: s, Source: IncubationSource{entry}, Assets: entry.Assets})
	}
	return req, nil
}

// DecodeIncubationInput is the bounded, strict supervised-cycle decoder.
func DecodeIncubationInput(raw []byte) (IncubationRequest, error) {
	var input IncubationInput
	if e := workerplan.ReadBoundedShape(raw, &input, 32<<20); e != nil {
		return IncubationRequest{}, e
	}
	return IncubationRequestFromInput(input)
}
