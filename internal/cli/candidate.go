package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidate(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var source, declaration, data, dataSHA, image, file, expected string
	fs.StringVar(&data, "dataset", "", "exact private dataset artifact; optional when inspecting")
	if command == "candidate-seal" {
		fs.StringVar(&source, "source", "", "caller-supplied Go source; retained without compilation")
		fs.StringVar(&declaration, "hypothesis", "", "predeclared hypothesis JSON; not a service publication")
		fs.StringVar(&dataSHA, "dataset-sha256", "", "expected canonical dataset identity")
		fs.StringVar(&image, "toolchain-image-sha256", "", "declared immutable Go toolchain image; not inspected")
	} else {
		fs.StringVar(&file, "file", "", "sealed candidate JSON")
		fs.StringVar(&expected, "sha256", "", "expected candidate content identity")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || command == "candidate-seal" && (source == "" || declaration == "" || data == "" || dataSHA == "" || image == "") || command == "candidate-inspect" && (file == "" || expected == "") {
		return errors.New("candidate declaration requires all explicit artifact and identity flags")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var p candidate.Package
	if command == "candidate-seal" {
		src, err := externalFile(source, candidate.MaxSourceBytes)
		if err != nil {
			return err
		}
		hRaw, err := externalFile(declaration, hypothesis.MaxBytes)
		if err != nil {
			return err
		}
		h, err := hypothesis.Read(bytes.NewReader(hRaw))
		if err != nil {
			return err
		}
		dRaw, err := externalFile(data, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		p, err = candidate.New(src, h, dRaw, image)
		if err != nil || p.Dataset.CanonicalSHA256 != dataSHA {
			return candidate.ErrInvalid
		}
	} else {
		raw, err := externalFile(file, candidate.MaxPackageBytes)
		if err != nil {
			return err
		}
		p, err = candidate.Read(bytes.NewReader(raw))
		if err != nil || p.SHA256 != expected {
			return candidate.ErrInvalid
		}
		if data != "" {
			raw, err := externalFile(data, workerplan.MaxInputBytes)
			if err != nil || p.VerifyDataset(raw) != nil {
				return candidate.ErrInvalid
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emit(out, p)
}
