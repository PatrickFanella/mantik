package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func runModelAnalyst(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("model-analyst", flag.ContinueOnError)
	fs.SetOutput(errOut)
	action := fs.String("action", "show", "run or show (advisory targets only)")
	id := fs.String("id", "", "immutable runtime cycle ID")
	db := fs.String("database-env", "", "explicit PostgreSQL URL environment variable")
	input := fs.String("input", "", "private owner-reviewed runtime input JSON")
	pin := fs.String("input-sha256", "", "independently approved exact input file hash")
	journal := fs.String("journal", "", "stable shared private subscription daily-budget journal")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || !strategytrial.ValidID(*id) || len(*id) > 64 || os.Getenv(*db) == "" || (*action != "show" && *action != "run") {
		return errors.New("model-analyst requires id, explicit database-env and run/show action")
	}
	var q analyst.Request
	if *action == "run" {
		f, e := os.Open(*input)
		if e != nil {
			return errors.New("cannot read analyst input")
		}
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			_ = f.Close()
			return errors.New("analyst input requires a mode-0600 regular file")
		}
		raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		ce := f.Close()
		if e != nil || ce != nil || len(raw) > 1<<20 {
			return errors.New("cannot read bounded analyst input")
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != *pin {
			return errors.New("analyst input does not match external approval pin")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&q); e != nil {
			return errors.New("invalid analyst input JSON")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("expected one analyst input")
		}
		if q.ID != *id {
			return errors.New("analyst cycle ID differs from approved input")
		}
		if e = q.Validate(time.Now().UTC()); e != nil {
			return e
		}
	}
	s, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer s.Close()
	j, e := research.OpenPostgresJournal(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer j.Close()
	if *action == "run" {
		caller, e := model.OpenJournal(*journal)
		if e != nil {
			return e
		}
		result, e := (analyst.Runner{Store: s, Journal: j, Caller: caller}).Run(ctx, q)
		return errors.Join(e, emit(out, result))
	}
	events, e := j.Events(ctx, "analyst-"+*id)
	if e != nil {
		return e
	}
	return showModelAnalyst(events, *id, out)
}

func showModelAnalyst(events []research.Event, id string, out io.Writer) error {
	if len(events) == 0 {
		return errors.New("analyst cycle not found")
	}
	last := events[len(events)-1]
	if last.Kind != "done" {
		return emit(out, analyst.Result{ID: id, Status: "no_target", Reason: "interrupted_no_relaunch", Target: allocation.TargetDecision{HoldExposure: true}})
	}
	var result analyst.Result
	if json.Unmarshal(last.Body, &result) != nil || result.ID != id {
		return errors.New("invalid retained analyst result")
	}
	return emit(out, result)
}
