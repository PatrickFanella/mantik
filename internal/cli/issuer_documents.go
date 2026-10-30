package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
)

func readIssuerDocumentSpec(path string) (issuerdoc.Spec, error) {
	var spec issuerdoc.Spec
	f, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return spec, errors.New("issuer document spec exceeds bound or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, errors.New("invalid issuer document spec")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return spec, errors.New("expected one issuer document spec")
	}
	return spec, spec.Validate()
}

// Collection performs one method-bound GET into a private journal. Inspection
// rederives the parse from retained bytes and never prints the original file.
func runIssuerDocuments(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, specPath, id string
	fs.StringVar(&root, "journal", "", "absolute private issuer document journal directory (required)")
	if command == "issuer-document-collect" {
		fs.StringVar(&specPath, "spec", "", "issuer document collection spec JSON (required)")
	} else {
		fs.StringVar(&id, "collection-id", "", "retained collection identity (required)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || fs.NArg() != 0 || (command == "issuer-document-collect") == (specPath == "") || (command == "issuer-document") == (id == "") {
		return errors.New("issuer document commands require a journal, their identity flag and no positional arguments")
	}
	if command == "issuer-document" {
		journal, err := issuerdoc.OpenExistingJournal(root)
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
	spec, err := readIssuerDocumentSpec(specPath)
	if err != nil {
		return err
	}
	journal, err := issuerdoc.OpenJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	status, collectErr := journal.Collect(ctx, spec)
	if status.CollectionID == "" {
		return collectErr
	}
	return errors.Join(emit(out, status), collectErr)
}
