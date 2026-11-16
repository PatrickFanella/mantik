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
	action := fs.String("action", "show", "show or protective observe; owner controls require authenticated book")
	command := fs.String("command-id", "", "immutable protective observation ID (observe only)")
	_ = fs.String("strategy-id", "", "legacy flag; owner strategy controls now require book")
	clock := fs.String("at", "", "RFC3339 observation/control clock")
	marksFile := fs.String("marks", "", "reviewed current mark JSON, symbol to price")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if *action != "show" && *action != "observe" {
		return errors.New("capped-risk refuses direct-database owner controls; use book with the configured owner MANTIK_API_TOKEN, explicit version and confirmation")
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

	if *action == "observe" {
		if *command == "" {
			return errors.New("protective observation requires command-id")
		}
		state, e = s.ApplyCappedBook(ctx, *id, state.Version, account.BookCommand{ID: *command, Kind: "risk_observe", At: at, Marks: marks})
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
