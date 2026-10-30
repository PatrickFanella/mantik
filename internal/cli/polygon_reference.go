package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"syscall"

	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
)

func readPolygonSpec(path string) (polygonref.Spec, error) {
	var spec polygonref.Spec
	f, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return spec, errors.New("polygon reference spec exceeds bound or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, errors.New("invalid polygon reference spec")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return spec, errors.New("expected one polygon reference spec")
	}
	return spec, spec.Validate()
}

// The credential file must be a current-user regular file with mode 0600.
// Its value is never printed or written to the journal.
func polygonCredentials(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("cannot open polygon credentials file")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", errors.New("cannot inspect polygon credentials file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Getuid()) {
		return "", errors.New("polygon credentials require a current-user regular file with mode 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("polygon credentials file exceeds bound or cannot be read")
	}
	var credentials struct {
		APIKey string `json:"api_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil || credentials.APIKey == "" {
		return "", errors.New("invalid polygon credentials file")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", errors.New("expected one polygon credentials object")
	}
	return credentials.APIKey, nil
}

func runPolygonReference(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, specPath, credentialsPath, id string
	fs.StringVar(&root, "journal", "", "absolute private polygon reference journal directory (required)")
	if command == "polygon-corporate-collect" {
		fs.StringVar(&specPath, "spec", "", "polygon reference collection spec JSON (required)")
		fs.StringVar(&credentialsPath, "credentials-file", "", `private mode-0600 JSON {"api_key": "..."} (required)`)
	} else {
		fs.StringVar(&id, "collection-id", "", "retained collection identity (required)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	collect := command == "polygon-corporate-collect"
	if root == "" || fs.NArg() != 0 || collect && (specPath == "" || credentialsPath == "") || !collect && id == "" {
		return errors.New("polygon reference commands require a journal, their identity flags and no positional arguments")
	}
	if !collect {
		journal, err := polygonref.OpenExistingJournal(root)
		if err != nil {
			return err
		}
		defer func() { _ = journal.Close() }()
		status, err := journal.Collection(id)
		if err != nil {
			return err
		}
		return emit(out, status)
	}
	spec, err := readPolygonSpec(specPath)
	if err != nil {
		return err
	}
	key, err := polygonCredentials(credentialsPath)
	if err != nil {
		return err
	}
	journal, err := polygonref.OpenJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	status, collectErr := journal.Collect(ctx, spec, key)
	if status.CollectionID == "" {
		return collectErr
	}
	return errors.Join(emit(out, status), collectErr)
}
