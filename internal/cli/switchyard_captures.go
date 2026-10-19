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

func runSwitchyardCaptures(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var id, receiver, dir string
	if command != "news-switchyard-captures" {
		fs.StringVar(&id, "id", "", "immutable Switchyard capture import ID")
	}
	if command == "news-switchyard-import" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver")
		fs.StringVar(&dir, "dir", "", "private Switchyard extraction directory")
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
	path := "/v1/news-switchyard-captures"
	if command == "news-switchyard-captures" {
		var h extraction.SwitchyardCaptureHistory
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
	var prepared extraction.SwitchyardReceipt
	var finished *extraction.SwitchyardReceipt
	switch command {
	case "news-switchyard-import":
		if !storage.ValidReceiverID(receiver) {
			return extraction.ErrInvalid
		}
		prepared, finished, err = loadSwitchyardReceipts(dir)
		if err != nil {
			return err
		}
		if prepared.Schema != extraction.SwitchyardSchema {
			return errors.New("diagnostic captures are offline-only and cannot be imported")
		}
		if _, err = extraction.InspectSwitchyard(ctx, prepared, finished); err != nil {
			return err
		}
		input = struct {
			ReceiverID string                        `json:"receiver_id"`
			RequestID  string                        `json:"request_id"`
			Prepared   extraction.SwitchyardReceipt  `json:"prepared"`
			Finished   *extraction.SwitchyardReceipt `json:"finished,omitempty"`
		}{receiver, id, prepared, finished}
	case "news-switchyard-capture":
		path += "/" + id
	default:
		return extraction.ErrInvalid
	}
	var r extraction.SwitchyardCapture
	if err = requestWithQuery(ctx, *base, path, input, &r, nil, extraction.MaxSwitchyardCaptureBytes); err != nil {
		return err
	}
	if err = r.ValidateContext(ctx); err != nil {
		return err
	}
	if r.ID != id || receiver != "" && (r.ReceiverID != receiver || !extraction.SameSwitchyardCapture(r, prepared, finished)) {
		return extraction.ErrInvalid
	}
	return emit(out, r)
}

// Share confined private-file reads; schema-specific replay happens before import.
func loadSwitchyardReceipts(dir string) (extraction.SwitchyardReceipt, *extraction.SwitchyardReceipt, error) {
	p, f, err := loadLocalReceipts(dir)
	var finished *extraction.SwitchyardReceipt
	if f != nil {
		value := extraction.SwitchyardReceipt(*f)
		finished = &value
	}
	return extraction.SwitchyardReceipt(p), finished, err
}
