package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runAllocationHistoryIndex(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("allocation-history-index", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	command := storage.OwnedAllocationIndexCommand{ReceiverID: *receiver}
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || command.Validate() != nil {
		return storage.ErrOwnedAllocationInvalid
	}
	var receipt storage.OwnedAllocationIndexReceipt
	if err := requestBoundedWithTimeout(ctx, *base, "/v1/allocation-history/index", command, &receipt, nil, storage.MaxOwnedAllocationIndexReceiptBytes, true, 65*time.Second); err != nil {
		return err
	}
	if err := receipt.Validate(command); err != nil {
		return err
	}
	return emit(out, receipt)
}
