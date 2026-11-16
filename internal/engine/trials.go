package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// ErrTrialCrashed reports a panic while running a registered attempt. The
// panic's value is not retained: source/provider errors can contain secrets.
var ErrTrialCrashed = errors.New("strategy trial execution panicked")

// Trial identifies one search attempt and its input manifest. Family is a
// stable search family across parameter and code variants, not a strategy ID.
// Parents are earlier trial IDs. Custom settlement calendars need a versioned
// CalendarID; the callback itself cannot be serialized into the declaration.
type Trial struct {
	ID            string
	Family        string
	DataSHA256    string
	EvidenceClass string
	Parents       []string
	CalendarID    string
	// Experiment binds fold boundaries, role and candidate to the retained run.
	Experiment json.RawMessage
}

type trialStrategy struct {
	strategy.Strategy
	spec strategy.Spec
}

func (s trialStrategy) Spec() strategy.Spec { return s.spec }

// RunTrial records a start before running the engine, each daily return before
// the next decision, and a terminal outcome. Any registry failure stops the run.
// Use a fresh ID for every execution: an existing ID never reruns the engine.
// A hard process exit can leave no outcome, but its start and saved returns
// remain queryable and count toward the family's search total.
func RunTrial(ctx context.Context, cfg Config, store strategytrial.Store, trial Trial) (Result, error) {
	return runTrial(ctx, cfg, store, trial, time.Now)
}

func runTrial(ctx context.Context, cfg Config, store strategytrial.Store, trial Trial, now func() time.Time) (out Result, err error) {
	if store == nil || cfg.Strategy == nil || cfg.Source == nil || cfg.SettlementDate != nil && trial.CalendarID == "" {
		return out, ErrInvalidConfig
	}
	if len(trial.Experiment) > 16<<10 || len(trial.Experiment) > 0 && !json.Valid(trial.Experiment) {
		return out, ErrInvalidConfig
	}
	spec := cfg.Strategy.Spec()
	id, err := spec.ID()
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return out, err
	}
	// Freeze the declaration so a strategy cannot change its Spec between the
	// recorded identity and the engine's call to Spec.
	spec = strategy.Spec{}
	if err = json.Unmarshal(raw, &spec); err != nil {
		return out, err
	}
	cfg.Strategy = trialStrategy{cfg.Strategy, spec}
	assets := slices.Clone(cfg.Assets)
	slices.Sort(assets)
	cfg.Assets = assets
	calendar := trial.CalendarID
	if cfg.SettlementDate == nil {
		calendar = "us-equity-t1-v1"
	}
	settings, err := json.Marshal(struct {
		Assets        []string         `json:"assets"`
		Start         time.Time        `json:"start"`
		End           time.Time        `json:"end"`
		InitialCash   string           `json:"initial_cash"`
		Cost          CostModel        `json:"cost"`
		Deposits      *DepositSchedule `json:"deposits"`
		Account       string           `json:"account"`
		Calendar      string           `json:"settlement_calendar"`
		Experiment    json.RawMessage  `json:"experiment,omitempty"`
		Unlevered     bool             `json:"unlevered"`
		EngineVersion string           `json:"engine_version"`
	}{assets, cfg.Start, cfg.End, cfg.InitialCash, cfg.Cost, cfg.Deposits, string(cfg.Account), calendar, trial.Experiment, cfg.Unlevered, Version})
	if err != nil {
		return out, err
	}
	start := strategytrial.Start{ID: trial.ID, Family: trial.Family, StrategyID: id, Spec: raw, Config: settings, DataSHA256: trial.DataSHA256, EvidenceClass: trial.EvidenceClass, Parents: trial.Parents, StartedAt: now()}
	created, err := store.StartStrategyTrial(ctx, start)
	if err != nil {
		return out, err
	}
	if !created {
		return out, strategytrial.ErrConflict
	}
	defer func() {
		status := "completed"
		if recovered := recover(); recovered != nil {
			err = ErrTrialCrashed
			status = "crashed"
		} else if err != nil {
			status = "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = "abandoned"
			}
		}
		// Cancellation must not discard the outcome. Bound cleanup independently;
		// if storage is down the unfinished start still counts and remains visible.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		finishErr := store.FinishStrategyTrial(finishCtx, trial.ID, strategytrial.Outcome{Status: status, FinishedAt: now()})
		if finishErr != nil {
			err = errors.Join(err, fmt.Errorf("retain trial outcome: %w", finishErr))
		}
	}()
	return run(ctx, cfg, func(d Day) error {
		if err := store.AppendStrategyTrialReturn(ctx, trial.ID, strategytrial.DailyReturn{Date: d.Date, Return: d.Return, Equity: d.Equity}); err != nil {
			return fmt.Errorf("retain trial return: %w", err)
		}
		return nil
	})
}
