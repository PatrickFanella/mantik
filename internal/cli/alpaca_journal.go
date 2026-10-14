package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func readAlpacaRecording(path, digest string) (alpaca.Recording, error) {
	if path == "" || digest == "" {
		return alpaca.Recording{}, errors.New("recording and expected raw sha256 are required")
	}
	file, err := os.Open(path)
	if err != nil {
		return alpaca.Recording{}, err
	}
	record, readErr := alpaca.Read(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return record, err
	}
	if record.SHA256 != digest {
		return record, errors.New("recording does not match expected raw identity")
	}
	return record, nil
}

type remoteResearchReceiver struct{ base string }

func (r remoteResearchReceiver) Snapshot(ctx context.Context, ids []string) (domain.ResearchSnapshot, error) {
	query := url.Values{}
	for _, id := range ids {
		query.Add("input_id", id)
	}
	var out domain.ResearchSnapshot
	err := requestWithQuery(ctx, r.base, "/v1/research-receiver", nil, &out, query, 1<<20)
	return out, err
}
func (r remoteResearchReceiver) Submit(ctx context.Context, receiver string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	body := struct {
		ReceiverID string              `json:"receiver_id"`
		Input      domain.RoutingInput `json:"input"`
	}{receiver, i}
	var receipt domain.RoutingReceipt
	err := request(ctx, r.base, "/v1/research-inputs", body, &receipt)
	return receipt, err
}

func runAlpacaJournal(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, path, digest, base, receiver, retryMissing string
	var limit int
	fs.StringVar(&root, "journal", "", "absolute private journal directory (required)")
	if command == "alpaca-retain" {
		fs.StringVar(&path, "recording", "", "retained recording JSON file")
		fs.StringVar(&digest, "sha256", "", "expected raw response identity")
	}
	if command == "alpaca-submit" {
		fs.StringVar(&base, "api-url", defaultURL, "exact target service HTTP origin")
		fs.StringVar(&receiver, "receiver-id", "", "expected research receiver identity (required)")
		fs.StringVar(&retryMissing, "retry-missing-id", "", "explicitly retry this pending identity when server history is absent (max 3 attempts)")
		fs.IntVar(&limit, "limit", 16, "maximum pending inputs to process (1–16)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || root == "" {
		return errors.New("journal command requires an absolute journal directory and no positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	open := alpaca.OpenExistingJournal
	if command == "alpaca-retain" {
		open = alpaca.OpenJournal
	}
	journal, err := open(root)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	switch command {
	case "alpaca-retain":
		record, err := readAlpacaRecording(path, digest)
		if err != nil {
			return err
		}
		result, err := journal.Retain(record)
		if err != nil {
			return err
		}
		return emit(out, result)
	case "alpaca-submit":
		result, err := journal.FlushWithRecovery(ctx, alpaca.Binding{APIURL: base, ReceiverID: receiver}, remoteResearchReceiver{base: base}, limit, retryMissing)
		if err != nil {
			return err
		}
		return emit(out, result)
	default:
		status, err := journal.Status()
		if err != nil {
			return err
		}
		return emit(out, status)
	}
}
