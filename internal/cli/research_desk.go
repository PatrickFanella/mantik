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

	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func runResearchDesk(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("research-desk", flag.ContinueOnError)
	fs.SetOutput(errOut)
	action := fs.String("action", "show", "run, show or trials")
	db := fs.String("database-env", "", "explicit PostgreSQL URL environment variable")
	id := fs.String("id", "", "immutable session ID")
	input := fs.String("input", "", "operator-reviewed private declaration JSON")
	expected := fs.String("input-sha256", "", "independently approved exact declaration file hash")
	journal := fs.String("journal", "", "stable private mode-0700 subscription budget journal")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || !strategytrial.ValidID(*id) || os.Getenv(*db) == "" || (*action != "show" && *action != "run" && *action != "trials") {
		return errors.New("research-desk requires id, explicit database-env and run/show/trials action")
	}
	var d research.Declaration
	if *action == "run" {
		file, e := os.Open(*input)
		if e != nil {
			return errors.New("cannot read desk declaration")
		}
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return errors.New("desk declaration requires a bounded regular file")
		}
		raw, e := io.ReadAll(io.LimitReader(file, (2<<20)+1))
		closeErr := file.Close()
		if e != nil || closeErr != nil || len(raw) > 2<<20 {
			return errors.New("cannot read bounded desk declaration")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&d); e != nil {
			return errors.New("invalid desk declaration JSON")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("expected one desk declaration")
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != *expected || d.ID != *id {
			return errors.New("desk declaration does not match external approval pin")
		}
		// Check cutoff, trigger and data before opening storage or launching a model.
		if e = d.Validate(time.Now().UTC()); e != nil {
			return e
		}
	}
	store, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer store.Close()
	evidence, e := research.OpenPostgresJournal(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer evidence.Close()
	if *action == "run" {
		caller, e := model.OpenJournal(*journal)
		if e != nil {
			return e
		}
		summary, runErr := research.Run(ctx, evidence, store, store, caller, d)
		if e = emit(out, summary); e != nil {
			return e
		}
		return runErr
	}
	events, e := evidence.Events(ctx, *id)
	if e != nil {
		return e
	}
	if len(events) == 0 {
		return errors.New("research desk session not found")
	}
	summary := research.Inspect(*id, events)
	if *action == "show" {
		return emit(out, summary)
	}
	records := []strategytrial.Record{}
	for _, id := range research.AttemptIDs(*id, events) {
		r, e := store.StrategyTrial(ctx, id)
		if errors.Is(e, strategytrial.ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		records = append(records, r)
	}
	// Trials includes exact attempted specs. Redirect this explicit private export
	// to an evidence file; ordinary show emits only hashes, statuses and IDs.
	return emit(out, records)
}
