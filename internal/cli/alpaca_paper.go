package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"syscall"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/mlegpaper"
)

func readPaperJSON(path string, target any, private bool) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("cannot open paper input file")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return errors.New("cannot inspect paper input")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Size() > 1<<20 || private && (!ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0600) {
		return errors.New("invalid paper input; credentials require current-user mode 0600")
	}
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(target); err != nil {
		return errors.New("invalid paper input JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one paper input JSON object")
	}
	return nil
}

func runAlpacaPaper(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("alpaca-paper", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, credentials, account, action, input, clientID, after string
	fs.StringVar(&root, "journal", "", "absolute private paper journal directory")
	fs.StringVar(&credentials, "credentials-file", "", "mode-0600 JSON with key/secret or oauth_token")
	fs.StringVar(&account, "account-id", "", "expected paper account ID")
	fs.StringVar(&action, "action", "", "submit, mleg-open, mleg-close, cancel, reconcile or fills")
	fs.StringVar(&input, "input", "", "order JSON or complete expected asset-ID/quantity map")
	fs.StringVar(&clientID, "client-order-id", "", "original client ID for cancellation")
	fs.StringVar(&after, "after", "", "RFC3339 lower bound for fills")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || root == "" || credentials == "" || account == "" {
		return errors.New("alpaca-paper requires journal, credentials-file and account-id")
	}
	var c alpaca.PaperCredentials
	if err := readPaperJSON(credentials, &c, true); err != nil {
		return err
	}
	j, err := alpaca.OpenJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	p, err := alpaca.NewPaperTrader(c, account, j)
	if err != nil {
		return err
	}
	switch action {
	case "mleg-open":
		var o alpaca.MLegOpen
		if err := readPaperJSON(input, &o, false); err != nil {
			return err
		}
		result, err := mlegpaper.Open(ctx, p, o)
		if err != nil {
			return err
		}
		return emit(out, result)
	case "mleg-close":
		var plan alpaca.MLegClose
		if err := readPaperJSON(input, &plan, false); err != nil {
			return err
		}
		result, operationErr := p.CloseMLeg(ctx, plan)
		if err := emit(out, result); err != nil {
			return err
		}
		return operationErr
	case "submit":
		var o alpaca.StockOrder
		if err := readPaperJSON(input, &o, false); err != nil {
			return err
		}
		result, err := p.Submit(ctx, o)
		if err != nil {
			return err
		}
		return emit(out, result)
	case "cancel":
		if err := p.Cancel(ctx, clientID); err != nil {
			return err
		}
		return emit(out, map[string]string{"client_order_id": clientID, "cancellation": "requested"})
	case "reconcile":
		var expected map[string]string
		if err := readPaperJSON(input, &expected, false); err != nil {
			return err
		}
		if expected == nil {
			return errors.New("expected positions must be a JSON object")
		}
		result, err := p.Reconcile(ctx, expected)
		if err != nil {
			return err
		}
		return emit(out, result)
	case "fills":
		at, err := time.Parse(time.RFC3339, after)
		if err != nil {
			return errors.New("fills requires RFC3339 after")
		}
		result, err := p.Fills(ctx, at)
		if err != nil {
			return err
		}
		return emit(out, result)
	default:
		return errors.New("alpaca-paper action must be submit, mleg-open, mleg-close, cancel, reconcile or fills")
	}
}
