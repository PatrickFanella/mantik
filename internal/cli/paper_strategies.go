package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var paperStrategyPin = regexp.MustCompile(`^[a-f0-9]{64}$`)

func runPaperStrategy(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "explicit service receiver identity")
	var id, file, pin string
	if command == "paper-strategy-register" {
		fs.StringVar(&file, "file", "", "bounded paper strategy definition JSON")
	}
	if command == "paper-strategy" {
		fs.StringVar(&id, "id", "", "paper strategy definition ID")
		fs.StringVar(&pin, "record-sha256", "", "expected full strategy record SHA-256")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return paperstrategy.ErrInvalid
	}
	if command == "paper-strategies" {
		var c paperstrategy.Catalog
		if err = requestBounded(ctx, *base, "/v1/paper-strategies", nil, &c, nil, paperstrategy.MaxCatalogBytes, true); err != nil {
			return err
		}
		if c.Validate() != nil || c.ReceiverID != *receiver {
			return paperstrategy.ErrInvalid
		}
		return emit(out, c)
	}
	if command == "paper-strategy" {
		if !paperaccount.ID.MatchString(id) || !paperStrategyPin.MatchString(pin) {
			return paperstrategy.ErrInvalid
		}
		var r paperstrategy.Record
		if err = requestBounded(ctx, *base, "/v1/paper-strategies/"+id, nil, &r, nil, paperstrategy.MaxRecordBytes, true); err != nil {
			return err
		}
		if r.Validate() != nil || r.Command.Definition.ID != id || r.Command.ReceiverID != *receiver || r.SHA256 != pin {
			return paperstrategy.ErrInvalid
		}
		return emit(out, r)
	}
	if file == "" {
		return paperstrategy.ErrInvalid
	}
	f, err := os.Open(file)
	if err != nil {
		return paperstrategy.ErrInvalid
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, paperstrategy.MaxCommandBytes+1))
	err = errors.Join(readErr, f.Close())
	var definition paperstrategy.Definition
	if err != nil || workerplan.ReadBoundedShape(raw, &definition, paperstrategy.MaxCommandBytes) != nil {
		return paperstrategy.ErrInvalid
	}
	cmd := paperstrategy.Command{ReceiverID: *receiver, Definition: definition}
	if cmd.Validate() != nil {
		return paperstrategy.ErrInvalid
	}
	var receipt paperstrategy.Receipt
	if err = requestBounded(ctx, *base, "/v1/paper-strategies", cmd, &receipt, nil, paperstrategy.MaxRecordBytes, true); err != nil {
		return err
	}
	if receipt.Record.Validate() != nil || !reflect.DeepEqual(receipt.Record.Command, cmd) {
		return paperstrategy.ErrInvalid
	}
	return emit(out, receipt)
}
