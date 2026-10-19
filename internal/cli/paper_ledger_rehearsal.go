package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/url"
	"os"
)

func runPaperLedgerRehearsal(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("account", "", "exact paper account ID")
	var file string
	if command == "paper-ledger-record" {
		fs.StringVar(&file, "file", "", "private single append command JSON; explicit receiver, account digest, head and rehearsal scope required")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !paperaccount.ID.MatchString(*id) {
		return paperledger.ErrInvalid
	}
	path := "/v1/paper-accounts/" + *id + "/ledger-rehearsal"
	if command == "paper-ledger-rehearsal" {
		var r paperledger.Rehearsal
		if err = requestWithQuery(ctx, *base, path, nil, &r, nil, paperledger.MaxRehearsalBytes); err != nil {
			return err
		}
		if r.Account.Config.ID != *id || r.Validate(ctx) != nil {
			return paperledger.ErrInvalid
		}
		return emit(out, r)
	}
	if file == "" {
		return errors.New("paper-ledger-record requires one explicit append command file")
	}
	f, err := os.Open(file)
	if err != nil {
		return errors.New("cannot open paper ledger rehearsal command")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	var c paperledger.AppendCommand
	if workerplan.ReadBoundedShape(raw, &c, 16<<10) != nil || !storage.ValidReceiverID(c.ReceiverID) || c.Validate() != nil {
		return paperledger.ErrInvalid
	}
	var receipt paperledger.AppendReceipt
	if err = requestWithQuery(ctx, *base, path+"/entries", c, &receipt, nil, 16<<10); err != nil {
		return err
	}
	if receipt.Validate(*id, c) != nil {
		return paperledger.ErrInvalid
	}
	return emit(out, receipt)
}
