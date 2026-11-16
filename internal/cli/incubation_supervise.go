package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

// This configuration belongs to the credentialed operator process. It is read
// once, separately from the changing strategy input and its execution quotes.
type incubationSafetyInput struct {
	QuotesFile            string
	Symbols               []string
	Fees                  map[string]string
	MaximumHoldingSeconds int64
	IntervalSeconds       int64
}

type incubationSafetyFile string

func (path incubationSafetyFile) Quotes(ctx context.Context, symbols []string, at time.Time) (map[string]account.IncubationQuote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var quotes map[string]account.IncubationQuote
	if err := readPaperJSON(string(path), &quotes, true); err != nil {
		return nil, err
	}
	out := make(map[string]account.IncubationQuote, len(symbols))
	for _, symbol := range symbols {
		q, ok := quotes[symbol]
		if !ok || q.Symbol != symbol {
			return nil, errors.New("independent safety quote missing or misbound")
		}
		if err := account.ValidateIncubationQuote(q, at.Add(-time.Minute-time.Nanosecond), at); err != nil {
			return nil, err
		}
		out[symbol] = q
	}
	return out, nil
}

type incubationSupervisionEvent struct {
	Kind    string
	At      time.Time
	Health  *workhealth.Check         `json:",omitempty"`
	CycleID string                    `json:",omitempty"`
	Result  *service.IncubationResult `json:",omitempty"`
	Error   string                    `json:",omitempty"`
}

func runIncubationSupervision(ctx context.Context, runner service.IncubationRunner, cyclePath, configPath string, out io.Writer, journal analyst.EvidenceReader) error {
	var cfg incubationSafetyInput
	if err := readPaperJSON(configPath, &cfg, true); err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.QuotesFile) || filepath.Clean(cfg.QuotesFile) == filepath.Clean(cyclePath) || filepath.Clean(cfg.QuotesFile) == filepath.Clean(configPath) || cfg.MaximumHoldingSeconds < 1 || cfg.MaximumHoldingSeconds > 90*86400 || cfg.IntervalSeconds < 1 || cfg.IntervalSeconds > 60 {
		return errors.New("supervise requires an independent absolute quote path, a holding duration from 1 second through 90 days and a 1–60 second interval")
	}
	source := incubationSafetyFile(cfg.QuotesFile)
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	worker, err := service.NewIncubationSafetyWorker(runner, source, service.IncubationSafetyPolicy{Symbols: cfg.Symbols, Fees: cfg.Fees, MaximumHolding: time.Duration(cfg.MaximumHoldingSeconds) * time.Second}, interval)
	if err != nil {
		return err
	}
	runner.Safety = worker
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(workerCtx, service.WallClock{}) }()
	joined := false
	defer func() {
		stop()
		if !joined {
			<-done
		}
	}()
	var lastInput, lastHealth string
	for ctx.Err() == nil {
		at := time.Now().UTC()
		quotes, readErr := source.Quotes(ctx, cfg.Symbols, at)
		marks := make(map[string]string, len(quotes))
		for symbol, q := range quotes {
			marks[symbol] = q.Bid
		}
		health := worker.Readiness(ctx, at, marks)
		if readErr != nil {
			health = workhealth.Check{Status: "unavailable", Code: "independent_safety_quotes_unavailable", At: at}
		}
		// A status transition is inspection only. The runner and adapter repeat
		// current entry admission; this report is never restored as authority.
		key := health.Status + "/" + health.Code
		if key != lastHealth {
			if err := emit(out, incubationSupervisionEvent{Kind: "safety", At: at, Health: &health}); err != nil {
				return err
			}
			lastHealth = key
		}
		if health.Ready {
			var input incubationInput
			err := readGateJSON(cyclePath, &input, 32<<20)
			var req service.IncubationRequest
			if err == nil {
				var h string
				h, err = service.IncubationInputHash(input)
				req.InputSHA256 = h
				if err == nil && h != lastInput {
					fs, _ := runner.Store.(forward.Store)
					req, err = incubationRequestFromJournal(ctx, input, journal, fs)
				}
			}
			if err != nil {
				key := "invalid:" + err.Error()
				if key != lastInput {
					if e := emit(out, incubationSupervisionEvent{Kind: "cycle_refused", At: at, Error: err.Error()}); e != nil {
						return e
					}
					lastInput = key
				}
			} else if req.InputSHA256 != lastInput {
				result, runErr := runner.Run(ctx, req)
				event := incubationSupervisionEvent{Kind: "cycle", At: time.Now().UTC(), CycleID: req.ID, Result: &result}
				if runErr != nil {
					event.Error = runErr.Error()
				}
				if e := emit(out, event); e != nil {
					return e
				}
				lastInput = req.InputSHA256
			}
		}
		if err := (service.WallClock{}).Wait(ctx, 250*time.Millisecond); err != nil {
			break
		}
	}
	stop()
	<-done
	joined = true
	// Shutdown does not claim that venue exposure was flattened. The durable
	// ledger and adapter intents remain available to a restarted supervisor.
	if err := emit(out, incubationSupervisionEvent{Kind: "supervision_stopped", At: time.Now().UTC()}); err != nil {
		return err
	}
	return ctx.Err()
}

// The supervised path uses the same strict shape and full-input hash as run.
func readIncubationCycle(path string) (service.IncubationRequest, error) {
	var raw json.RawMessage
	if err := readGateJSON(path, &raw, 32<<20); err != nil {
		return service.IncubationRequest{}, err
	}
	return service.DecodeIncubationInput(raw)
}
