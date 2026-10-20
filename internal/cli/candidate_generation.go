package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateGeneration(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var declaration, dataset, dataSHA, image, model, requirement, requestedAt, planFile, planSHA, outputFile, outputSHA, capturedAt, recordFile, recordSHA, artifact string
	fs.StringVar(&dataset, "dataset", "", "exact private synthetic dataset; optional for inspection")
	switch command {
	case "candidate-generation-plan":
		fs.StringVar(&declaration, "hypothesis", "", "predeclared hypothesis JSON")
		fs.StringVar(&dataSHA, "dataset-sha256", "", "required expected canonical dataset identity")
		fs.StringVar(&image, "toolchain-image-sha256", "", "declared immutable Go image identity")
		fs.StringVar(&model, "model-label", "", "caller-declared model label; not verified or contacted")
		fs.StringVar(&requirement, "requirement", "", "UTF-8 file describing the bounded synthetic requirement")
		fs.StringVar(&requestedAt, "requested-at", "", "explicit RFC3339Nano request time")
	case "candidate-generation-record":
		fs.StringVar(&planFile, "plan", "", "sealed offline generation plan")
		fs.StringVar(&planSHA, "plan-sha256", "", "required expected plan identity")
		fs.StringVar(&outputFile, "output", "", "exact recorded output file, including rejected/incomplete bytes")
		fs.StringVar(&outputSHA, "output-sha256", "", "required SHA-256 of exact recorded output bytes")
		fs.StringVar(&capturedAt, "captured-at", "", "explicit RFC3339Nano capture time")
	default:
		fs.StringVar(&recordFile, "recording", "", "retained recorded generation envelope")
		fs.StringVar(&recordSHA, "recording-sha256", "", "required expected recording identity")
		fs.StringVar(&planSHA, "plan-sha256", "", "required expected generation plan identity")
		fs.StringVar(&artifact, "artifact", "recording", "recording|plan JSON or exact prompt|output|candidate|source")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return candidategeneration.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if command == "candidate-generation-plan" {
		if declaration == "" || dataset == "" || !candidateRegistrySHA.MatchString(dataSHA) || image == "" || model == "" || requirement == "" {
			return candidategeneration.ErrInvalid
		}
		at, err := time.Parse(time.RFC3339Nano, requestedAt)
		if err != nil {
			return candidategeneration.ErrInvalid
		}
		raw, err := externalFile(declaration, hypothesis.MaxBytes)
		if err != nil {
			return err
		}
		h, err := hypothesis.Read(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		data, err := externalFile(dataset, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		need, err := externalFile(requirement, candidategeneration.MaxRequirementBytes)
		if err != nil {
			return err
		}
		p, err := candidategeneration.NewPlan(h, data, image, model, string(need), at)
		if err != nil || p.Dataset.CanonicalSHA256 != dataSHA {
			return candidategeneration.ErrInvalid
		}
		return emit(out, p)
	}
	if !candidateRegistrySHA.MatchString(planSHA) {
		return candidategeneration.ErrInvalid
	}
	if command == "candidate-generation-record" {
		if planFile == "" || dataset == "" || outputFile == "" || !candidateRegistrySHA.MatchString(outputSHA) {
			return candidategeneration.ErrInvalid
		}
		at, err := time.Parse(time.RFC3339Nano, capturedAt)
		if err != nil {
			return candidategeneration.ErrInvalid
		}
		raw, err := externalFile(planFile, candidategeneration.MaxPlanBytes)
		if err != nil {
			return err
		}
		p, err := candidategeneration.ReadPlan(bytes.NewReader(raw))
		if err != nil || p.SHA256 != planSHA {
			return candidategeneration.ErrInvalid
		}
		data, err := externalFile(dataset, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		output, err := externalFile(outputFile, candidategeneration.MaxOutputBytes)
		if err != nil {
			return err
		}
		if workerplan.Digest(output) != outputSHA {
			return candidategeneration.ErrInvalid
		}
		r, err := candidategeneration.NewRecording(p, data, output, at)
		if err != nil {
			return err
		}
		return emit(out, r)
	}
	if recordFile == "" || !candidateRegistrySHA.MatchString(recordSHA) || artifact != "recording" && artifact != "plan" && artifact != "prompt" && artifact != "output" && artifact != "candidate" && artifact != "source" {
		return candidategeneration.ErrInvalid
	}
	raw, err := externalFile(recordFile, candidategeneration.MaxRecordingBytes)
	if err != nil {
		return err
	}
	r, err := candidategeneration.ReadRecording(bytes.NewReader(raw))
	if err != nil || r.SHA256 != recordSHA || r.Plan.SHA256 != planSHA {
		return candidategeneration.ErrInvalid
	}
	if dataset != "" {
		data, err := externalFile(dataset, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		if r.VerifyDataset(data) != nil {
			return candidategeneration.ErrInvalid
		}
	}
	switch artifact {
	case "plan":
		return emit(out, r.Plan)
	case "prompt":
		_, err = io.WriteString(out, r.Plan.Prompt)
	case "output":
		_, err = out.Write(r.Output)
	case "candidate", "source":
		if r.Candidate == nil {
			return candidategeneration.ErrInvalid
		}
		if artifact == "candidate" {
			return emit(out, r.Candidate)
		}
		_, err = io.WriteString(out, r.Candidate.Source.Text)
	default:
		return emit(out, r)
	}
	return err
}
