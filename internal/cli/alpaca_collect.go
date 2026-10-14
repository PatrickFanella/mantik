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
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

func readCollectionSpec(path string) (alpaca.CollectionSpec, error) {
	var spec alpaca.CollectionSpec
	f, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 {
		return spec, errors.New("collection spec exceeds bound or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, errors.New("invalid collection spec")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return spec, errors.New("expected one collection spec")
	}
	return spec, nil
}
func collectionCredentials(path string) (string, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", errors.New("cannot open collection credentials file")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", "", errors.New("cannot inspect collection credentials file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Getuid()) {
		return "", "", errors.New("credentials require a current-user regular file with mode 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", "", errors.New("credentials file exceeds bound or cannot be read")
	}
	var credentials struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return "", "", errors.New("invalid collection credentials file")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", "", errors.New("expected one credentials object")
	}
	return credentials.Key, credentials.Secret, nil
}
func runAlpacaCollect(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, specPath, credentialsPath, id string
	fs.StringVar(&root, "journal", "", "absolute private journal directory")
	if command == "alpaca-collect" || command == "alpaca-session" {
		fs.StringVar(&specPath, "spec", "", "explicit collection spec JSON (required)")
		fs.StringVar(&credentialsPath, "credentials-file", "", "private mode-0600 JSON credentials file (required)")
	} else {
		if command == "alpaca-session-status" {
			fs.StringVar(&id, "session-id", "", "retained session identity (required)")
		} else {
			fs.StringVar(&id, "collection-id", "", "retained collection identity (required)")
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || fs.NArg() != 0 {
		return errors.New("collection requires a journal and no positional arguments")
	}
	if command == "alpaca-collection" || command == "alpaca-session-status" {
		journal, err := alpaca.OpenExistingJournal(root)
		if err != nil {
			return err
		}
		defer func() { _ = journal.Close() }()
		if command == "alpaca-session-status" {
			status, err := journal.Session(id)
			if err != nil {
				return err
			}
			return emit(out, status)
		}
		status, err := journal.Collection(id)
		if err != nil {
			return err
		}
		return emit(out, status)
	}
	if specPath == "" || credentialsPath == "" {
		return errors.New("collection requires explicit spec and credentials-file")
	}
	if command == "alpaca-session" {
		spec, err := readSessionSpec(specPath)
		if err != nil {
			return err
		}
		key, secret, err := collectionCredentials(credentialsPath)
		if err != nil {
			return err
		}
		client, err := alpaca.NewClient(key, secret, spec.Collection.Feed)
		if err != nil {
			return err
		}
		journal, err := alpaca.OpenJournal(root)
		if err != nil {
			return err
		}
		defer func() { _ = journal.Close() }()
		result, runErr := journal.RunSession(ctx, client, spec)
		return errors.Join(emit(out, result), runErr)
	}
	spec, err := readCollectionSpec(specPath)
	if err != nil {
		return err
	}
	key, secret, err := collectionCredentials(credentialsPath)
	if err != nil {
		return err
	}
	client, err := alpaca.NewClient(key, secret, spec.Feed)
	if err != nil {
		return err
	}
	journal, err := alpaca.OpenJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, collectErr := journal.Collect(ctx, client, spec)
	// A failed run still has useful inspectable status. Never print provider raw
	// bodies or credentials to terminal output.
	return errors.Join(emit(out, result), collectErr)
}

func readSessionSpec(path string) (alpaca.SessionSpec, error) {
	var spec alpaca.SessionSpec
	file, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 {
		return spec, errors.New("session spec exceeds bound or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, errors.New("invalid session spec")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return spec, errors.New("expected one session spec")
	}
	return spec, nil
}
