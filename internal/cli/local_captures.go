package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"io"
	"net/url"
)

func runLocalCaptures(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var id, receiver, dir string
	if command != "news-local-captures" {
		fs.StringVar(&id, "id", "", "immutable local capture import ID")
	}
	if command == "news-local-import" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver")
		fs.StringVar(&dir, "dir", "", "private local extraction directory")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return extraction.ErrInvalid
	}
	path := "/v1/news-local-captures"
	if command == "news-local-captures" {
		var h extraction.LocalCaptureHistory
		if err = requestWithQuery(ctx, *base, path, nil, &h, nil, 1<<20); err != nil {
			return err
		}
		if err = h.Validate(); err != nil {
			return err
		}
		return emit(out, h)
	}
	if !extraction.ID.MatchString(id) {
		return extraction.ErrInvalid
	}
	var input any
	var prepared extraction.LocalReceipt
	var finished *extraction.LocalReceipt
	switch command {
	case "news-local-import":
		if !storage.ValidReceiverID(receiver) {
			return extraction.ErrInvalid
		}
		prepared, finished, err = loadLocalReceipts(dir)
		if err != nil {
			return err
		}
		if _, err = extraction.InspectLocal(ctx, prepared, finished); err != nil {
			return err
		}
		input = struct {
			ReceiverID string                   `json:"receiver_id"`
			RequestID  string                   `json:"request_id"`
			Prepared   extraction.LocalReceipt  `json:"prepared"`
			Finished   *extraction.LocalReceipt `json:"finished,omitempty"`
		}{receiver, id, prepared, finished}
	case "news-local-capture":
		path += "/" + id
	default:
		return extraction.ErrInvalid
	}
	var r extraction.LocalCapture
	if err = requestWithQuery(ctx, *base, path, input, &r, nil, extraction.MaxLocalCaptureBytes); err != nil {
		return err
	}
	if err = r.ValidateContext(ctx); err != nil {
		return err
	}
	if r.ID != id || receiver != "" && (r.ReceiverID != receiver || !extraction.SameLocalCapture(r, prepared, finished)) {
		return extraction.ErrInvalid
	}
	return emit(out, r)
}
