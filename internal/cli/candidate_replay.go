package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatereplay"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateReplay(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var planPath, planSHA, dataset, cutoff, artifact string
	fs.StringVar(&dataset, "dataset", "", "exact original parent dataset")
	fs.StringVar(&planPath, "plan", "", "sealed parent fixture plan or replay plan")
	fs.StringVar(&planSHA, "plan-sha256", "", "expected input plan identity")
	if command == "candidate-replay-plan" {
		fs.StringVar(&cutoff, "cutoff", "", "explicit RFC3339 cutoff; no wall-clock fallback")
	} else {
		fs.StringVar(&artifact, "artifact", "", "verified output: job, dataset or plan")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || dataset == "" || planPath == "" || planSHA == "" {
		return candidatereplay.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := externalFile(dataset, workerplan.MaxInputBytes)
	if err != nil {
		return err
	}
	if command == "candidate-replay-plan" {
		at, err := time.Parse(time.RFC3339Nano, cutoff)
		if err != nil {
			return candidatereplay.ErrInvalid
		}
		raw, err := externalFile(planPath, candidatetest.MaxPlanBytes)
		if err != nil {
			return err
		}
		parent, err := candidatetest.ReadPlan(bytes.NewReader(raw))
		if err != nil || parent.SHA256 != planSHA {
			return candidatereplay.ErrInvalid
		}
		plan, err := candidatereplay.New(parent, data, at)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(out, plan)
	}
	raw, err := externalFile(planPath, candidatereplay.MaxBytes)
	if err != nil {
		return err
	}
	p, err := candidatereplay.Read(bytes.NewReader(raw))
	if err != nil || p.SHA256 != planSHA || p.Verify(data) != nil {
		return candidatereplay.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch artifact {
	case "plan":
		return emit(out, p)
	case "job":
		if p.Job == nil {
			return candidatereplay.ErrNoInputs
		}
		return emit(out, p.Job)
	case "dataset":
		raw, err := p.DatasetBytes(data)
		if err != nil {
			return err
		}
		n, err := out.Write(raw)
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	default:
		return candidatereplay.ErrInvalid
	}
}
