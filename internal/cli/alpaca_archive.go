package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"io"
	"time"
)

func runAlpacaArchive(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var journalPath, archivePath, destination, digest string
	if command == "alpaca-archive" {
		fs.StringVar(&journalPath, "journal", "", "existing private source journal (required)")
		fs.StringVar(&destination, "destination", "", "new private archive directory (required)")
	} else {
		fs.StringVar(&archivePath, "archive", "", "private archive directory (required)")
		fs.StringVar(&digest, "sha256", "", "expected archive manifest SHA-256 (required)")
		if command == "alpaca-restore" {
			fs.StringVar(&destination, "destination", "", "new isolated journal directory (required)")
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("archive commands do not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if command == "alpaca-archive" {
		if journalPath == "" || destination == "" {
			return errors.New("archive requires source journal and new destination")
		}
		journal, err := alpaca.OpenExistingJournal(journalPath)
		if err != nil {
			return err
		}
		defer func() { _ = journal.Close() }()
		result, err := journal.Archive(ctx, destination)
		return errors.Join(emit(out, result), err)
	}
	if archivePath == "" || digest == "" || command == "alpaca-restore" && destination == "" {
		return errors.New("verify/restore requires archive identity and restore requires a new destination")
	}
	if command == "alpaca-restore" {
		result, err := alpaca.RestoreArchive(ctx, archivePath, digest, destination)
		return errors.Join(emit(out, result), err)
	}
	result, err := alpaca.VerifyArchive(ctx, archivePath, digest)
	return errors.Join(emit(out, result), err)
}
