package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"io"
	"net/url"
)

func runFixtureCapital(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var c storage.FixtureCapitalBinding
	if command == "fixture-capital-bind" {
		fs.StringVar(&c.ReceiverID, "receiver-id", "", "explicit service receiver identity")
		fs.StringVar(&c.AccountID, "account", "", "explicit synthetic paper account ID")
		fs.StringVar(&c.AccountSHA256, "account-sha256", "", "exact immutable funding receipt SHA-256")
		fs.StringVar(&c.ScopeCode, "scope-code", "", "explicit bind_synthetic_fixture_capital_v1 scope")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return storage.ErrFixtureCapitalBinding
	}
	path := "/v1/fixture-capital"
	var body any
	if command == "fixture-capital-bind" {
		if err := storage.ValidateFixtureCapitalBinding(c); err != nil {
			return err
		}
		path += "/binding"
		body = c
	}
	var v storage.FixtureCapitalReadback
	if err := requestWithQuery(ctx, *base, path, body, &v, nil, 256<<10); err != nil {
		return err
	}
	if v.Schema != storage.FixtureCapitalReadbackSchema || v.Authority != storage.FixtureCapitalReadbackAuthority || !v.WorkerBound || v.QualificationCredit || !paperaccount.ID.MatchString(v.Capital.AccountID) {
		return fixturecapital.ErrInvalid
	}
	if command == "fixture-capital-bind" && (v.Capital.ReceiverID != c.ReceiverID || v.Capital.AccountID != c.AccountID || v.Capital.AccountSHA256 != c.AccountSHA256) {
		return fixturecapital.ErrConflict
	}
	var a paperaccount.Account
	if err := requestWithQuery(ctx, *base, "/v1/paper-accounts/"+v.Capital.AccountID, nil, &a, nil, 16<<10); err != nil {
		return err
	}
	if err := v.Validate(a); err != nil {
		return err
	}
	return emit(out, v)
}
