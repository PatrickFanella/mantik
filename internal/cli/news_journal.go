package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func runNewsJournal(ctx context.Context, command string, args []string, out, errOut io.Writer) (retErr error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("journal", "", "absolute private news journal directory")
	file, key, claim := new(string), new(string), new(string)
	original := new(bool)
	if command == "news-retain" {
		fs.StringVar(file, "file", "", "recorded news JSON file")
	}
	if command == "news-inspect" {
		fs.StringVar(key, "record", "", "record key returned by retention/status")
		fs.StringVar(claim, "claim", "", "select one claim ID instead of the full normalized result")
		fs.BoolVar(original, "original", false, "explicitly include the retained original article text")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *path == "" || command == "news-retain" && *file == "" || command == "news-inspect" && *key == "" {
		return errors.New("news journal command requires explicit journal and command-specific arguments")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Validate source bytes before creating a new journal or taking its lock.
	var r news.Recording
	if command == "news-retain" {
		f, err := os.Open(*file)
		if err != nil {
			return errors.New("cannot open news recording file")
		}
		var readErr error
		r, readErr = news.Read(f)
		if err := errors.Join(readErr, f.Close()); err != nil {
			return err
		}
	}
	var j *news.Journal
	var err error
	if command == "news-retain" {
		j, err = news.OpenJournal(*path)
	} else {
		j, err = news.OpenExistingJournal(*path)
	}
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, j.Close()) }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch command {
	case "news-retain":
		receipt, err := j.Retain(r)
		if err != nil {
			return err
		}
		return emit(out, receipt)
	case "news-journal":
		status, err := j.Status()
		if err != nil {
			return err
		}
		return emit(out, status)
	case "news-inspect":
		stored, err := j.Inspect(*key)
		if err != nil {
			return err
		}
		view := struct {
			JournalID string           `json:"journal_id"`
			Key       string           `json:"key"`
			Result    *news.Result     `json:"result,omitempty"`
			Claim     *news.BoundClaim `json:"claim,omitempty"`
			Original  string           `json:"original,omitempty"`
		}{JournalID: j.ID, Key: *key}
		if *claim == "" {
			view.Result = &stored.Result
		} else {
			for _, c := range stored.Result.Claims {
				if c.ID == *claim {
					copy := c
					view.Claim = &copy
					break
				}
			}
			if view.Claim == nil {
				return errors.New("claim not found in retained news record")
			}
		}
		if *original {
			view.Original = stored.Recording.Original
		}
		return emit(out, view)
	}
	return errors.New("unsupported news journal command")
}
