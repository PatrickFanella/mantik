package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runPaperDecision(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	var id, pin, strategy, strategyPin, registration string
	if command != "paper-decisions" {
		fs.StringVar(&id, "id", "", "immutable decision request ID")
	}
	if command == "paper-decision" {
		fs.StringVar(&pin, "record-sha256", "", "expected complete record hash")
	}
	if command == "paper-decision-finalize" {
		fs.StringVar(&strategy, "strategy-id", "", "owned strategy ID")
		fs.StringVar(&strategyPin, "strategy-sha256", "", "expected owned strategy record hash")
		fs.StringVar(&registration, "registration-id", "", "owned price/calendar collector ID")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return storage.ErrPaperDecisionInvalid
	}
	if command == "paper-decisions" {
		var c storage.PaperDecisionCatalog
		if err := requestBounded(ctx, *base, "/v1/paper-decisions", nil, &c, nil, 4<<20, true); err != nil {
			return err
		}
		if c.ReceiverID != *receiver || c.Records == nil || len(c.Records) > storage.MaxPaperDecisions {
			return storage.ErrPaperDecisionInvalid
		}
		previous := ""
		for _, r := range c.Records {
			if r.Validate() != nil || r.Command.ReceiverID != *receiver || r.Command.ID <= previous {
				return storage.ErrPaperDecisionInvalid
			}
			previous = r.Command.ID
		}
		return emit(out, c)
	}
	if command == "paper-decision" {
		if !paperaccount.ID.MatchString(id) || !paperStrategyPin.MatchString(pin) {
			return storage.ErrPaperDecisionInvalid
		}
		var r storage.PaperDecisionRecord
		if err := requestBounded(ctx, *base, "/v1/paper-decisions/"+url.PathEscape(id), nil, &r, nil, 32768, true); err != nil {
			return err
		}
		if r.Validate() != nil || r.Command.ID != id || r.Command.ReceiverID != *receiver || r.SHA256 != pin {
			return storage.ErrPaperDecisionInvalid
		}
		return emit(out, r)
	}
	c := storage.PaperDecisionCommand{ID: id, ReceiverID: *receiver, StrategyID: strategy, StrategySHA256: strategyPin, RegistrationID: registration}
	if c.Validate() != nil {
		return storage.ErrPaperDecisionInvalid
	}
	var receipt storage.PaperDecisionReceipt
	if err := requestBounded(ctx, *base, "/v1/paper-decisions", c, &receipt, nil, 32768, true); err != nil {
		return err
	}
	if receipt.Record.Validate() != nil || !reflect.DeepEqual(receipt.Record.Command, c) {
		return storage.ErrPaperDecisionInvalid
	}
	return emit(out, receipt)
}
