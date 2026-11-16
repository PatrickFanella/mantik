package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/backtest"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

func gateFlags(name string, errOut io.Writer) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	db := fs.String("database-env", "", "required environment variable holding the explicit PostgreSQL URL")
	id := fs.String("id", "", "immutable gate or book identifier")
	return fs, db, id
}

func runStrategyGate(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs, db, id := gateFlags("strategy-gate", errOut)
	action := fs.String("action", "show", "declare, evaluate or show")
	input := fs.String("input", "", "predeclared experiment JSON for declare")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || os.Getenv(*db) == "" {
		return errors.New("strategy-gate requires an explicit database environment and flags only")
	}
	s, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer s.Close()
	var r strategygate.Record
	switch *action {
	case "declare":
		var d strategygate.Declaration
		if e = readGateJSON(*input, &d, 2<<20); e != nil {
			return e
		}
		if *id != "" && *id != d.ID {
			return errors.New("gate ID differs from input")
		}
		r, e = backtest.DeclareGate(ctx, s, d)
	case "evaluate":
		r, e = backtest.EvaluateGate(ctx, s, *id)
	case "show":
		r, e = s.StrategyGate(ctx, *id)
	default:
		return errors.New("unknown strategy-gate action")
	}
	if e != nil {
		return e
	}
	return emit(out, r)
}

func readGateJSON(path string, v any, limit int64) (err error) {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("input must be a bounded regular JSON file")
	}
	d := json.NewDecoder(io.LimitReader(f, limit+1))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func runCappedRisk(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs, db, id := gateFlags("capped-risk", errOut)
	action := fs.String("action", "show", "show or an explicit owner risk control")
	command := fs.String("command-id", "", "idempotent owner command ID")
	strategy := fs.String("strategy-id", "", "content-addressed strategy for breaker controls")
	clock := fs.String("at", "", "RFC3339 observation/control clock")
	marksFile := fs.String("marks", "", "reviewed current mark JSON, symbol to price")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	at, e := time.Parse(time.RFC3339, *clock)
	if e != nil || *id == "" || fs.NArg() != 0 || os.Getenv(*db) == "" {
		return errors.New("capped-risk requires id, at and explicit database environment")
	}
	s, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer s.Close()
	state, e := s.CappedBook(ctx, *id)
	if e != nil {
		return e
	}
	marks := map[string]string{}
	if *marksFile != "" {
		if e = readGateJSON(*marksFile, &marks, 1<<20); e != nil {
			return e
		}
	}
	if _, e = account.SnapshotCapped(state, marks, at); e != nil {
		return e
	}
	if *action != "show" {
		if *command == "" {
			return errors.New("owner risk control requires command-id")
		}
		control := risk.Control{Action: *action, StrategyID: *strategy}
		cmd := account.BookCommand{ID: *command, Kind: "risk_control", At: at, RiskControl: &control, Marks: marks}
		if *action == "observe" {
			cmd.Kind, cmd.RiskControl = "risk_observe", nil
		}
		state, e = s.ApplyCappedBook(ctx, *id, state.Version, cmd)
		if e != nil {
			return e
		}
	}
	snap, e := account.SnapshotCapped(state, marks, at)
	if e != nil {
		return e
	}
	return emit(out, snap)
}
