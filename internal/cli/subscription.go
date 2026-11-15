package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"io"
	"os"
	"time"
)

func runSubscriptionModel(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("subscription-model", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var journal, provider, promptFile string
	var budget, timeoutSeconds int
	fs.StringVar(&journal, "journal", "", "absolute private mode-0700 journal directory")
	fs.StringVar(&provider, "provider", "", "claude or codex")
	fs.StringVar(&promptFile, "prompt-file", "", "private prompt file")
	fs.IntVar(&budget, "daily-budget", 0, "maximum calls per UTC day")
	fs.IntVar(&timeoutSeconds, "timeout-seconds", 60, "call timeout from 1 to 120 seconds")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if journal == "" || provider == "" || promptFile == "" || budget < 1 || timeoutSeconds < 1 || timeoutSeconds > 120 || fs.NArg() != 0 {
		return errors.New("subscription-model requires journal, provider, prompt-file, daily-budget and no positional arguments")
	}
	raw, err := os.ReadFile(promptFile)
	if err != nil || len(raw) > 32768 {
		return errors.New("cannot read bounded model prompt file")
	}
	j, err := model.OpenJournal(journal)
	if err != nil {
		return err
	}
	record, err := j.Call(ctx, model.Call{Provider: model.Provider(provider), Prompt: string(raw), Timeout: time.Duration(timeoutSeconds) * time.Second}, budget)
	if emitErr := emit(out, record); emitErr != nil {
		return emitErr
	}
	return err
}
