package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

func runRouting(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var apiURL, filePath, id, owner, atText string
	var deriveMarketID bool
	if command == "route-input" {
		fs.BoolVar(&deriveMarketID, "derive-market-id", false, "derive immutable identity from market provenance")
	}
	fs.StringVar(&apiURL, "api-url", defaultURL, "service HTTP origin")
	if command == "close-routing-case" {
		fs.StringVar(&id, "id", "", "routing case id")
		fs.StringVar(&owner, "owner", "", "recorded case owner")
		fs.StringVar(&atText, "at", "", "explicit RFC3339 closure time")
	} else {
		fs.StringVar(&filePath, "file", "", "definition or attributed input JSON file (required)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected routing positional arguments")
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("api-url must be an HTTP origin without credentials or a path")
	}
	if command == "close-routing-case" {
		at, err := time.Parse(time.RFC3339Nano, atText)
		if err != nil || id == "" || owner == "" {
			return errors.New("case closure requires id, owner, and explicit RFC3339 time")
		}
		if url.PathEscape(id) != id {
			return errors.New("case id must be one path segment")
		}
		body := struct {
			Owner string    `json:"owner"`
			At    time.Time `json:"at"`
		}{owner, at}
		var result map[string]string
		if err := request(ctx, apiURL, "/v1/routing-cases/"+id+"/close", body, &result); err != nil {
			return err
		}
		return emit(out, result)
	}
	if filePath == "" {
		return errors.New("routing command requires a JSON file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 16385))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(raw) > 16384 {
		return errors.New("routing JSON exceeds 16 KiB")
	}
	// The API performs strict typed validation; retaining raw JSON here does not
	// interpret its content as commands or supply execution authority.
	var body json.RawMessage = raw
	if !json.Valid(body) {
		return errors.New("invalid routing JSON file")
	}
	if command == "trigger-register" {
		var result domain.TriggerDefinition
		if err := request(ctx, apiURL, "/v1/triggers", body, &result); err != nil {
			return err
		}
		return emit(out, result)
	}
	if deriveMarketID {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var input domain.RoutingInput
		if err := decoder.Decode(&input); err != nil {
			return errors.New("invalid attributed input JSON")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("expected one attributed input")
		}
		expected := routing.MarketInputID(input)
		if expected == "" || input.ID != "" && input.ID != expected {
			return errors.New("market identity conflicts with source record revision")
		}
		input.ID = expected
		if err := routing.ValidateInput(input); err != nil {
			return err
		}
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	var result domain.RoutingReceipt
	if err := request(ctx, apiURL, "/v1/routing-inputs", body, &result); err != nil {
		return err
	}
	return emit(out, result)
}
