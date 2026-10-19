package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runPaperAccount(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var id, receiver string
	var cash, ceiling int64
	if command != "paper-accounts" {
		fs.StringVar(&id, "id", "", "stable paper account ID")
	}
	if command == "paper-account-create" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit service receiver identity")
		fs.Int64Var(&cash, "opening-cash-cents", 0, "declared USD paper opening cash (1..1000000000000)")
		fs.Int64Var(&ceiling, "capital-limit-cents", 0, "positive committed-capital ceiling, no greater than opening cash")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return paperaccount.ErrInvalid
	}
	if command == "paper-accounts" {
		var c paperaccount.Catalog
		if err = requestWithQuery(ctx, *base, "/v1/paper-accounts", nil, &c, nil, 256<<10); err != nil {
			return err
		}
		if c.Validate() != nil {
			return paperaccount.ErrInvalid
		}
		return emit(out, c)
	}
	if !paperaccount.ID.MatchString(id) {
		return paperaccount.ErrInvalid
	}
	if command == "paper-account" {
		var a paperaccount.Account
		if err = requestWithQuery(ctx, *base, "/v1/paper-accounts/"+id, nil, &a, nil, 16<<10); err != nil {
			return err
		}
		if a.Config.ID != id || a.Validate() != nil {
			return paperaccount.ErrInvalid
		}
		return emit(out, a)
	}
	c := paperaccount.Config{ID: id, Currency: "USD", OpeningCashCents: cash, CapitalLimitCents: ceiling}
	if !storage.ValidReceiverID(receiver) || c.Validate() != nil {
		return paperaccount.ErrInvalid
	}
	body := struct {
		ReceiverID string              `json:"receiver_id"`
		Config     paperaccount.Config `json:"config"`
	}{receiver, c}
	var receipt paperaccount.Receipt
	if err = requestWithQuery(ctx, *base, "/v1/paper-accounts", body, &receipt, nil, 16<<10); err != nil {
		return err
	}
	if receipt.Account.Validate() != nil || receipt.Account.ReceiverID != receiver || receipt.Account.Config != c {
		return paperaccount.ErrInvalid
	}
	return emit(out, receipt)
}
