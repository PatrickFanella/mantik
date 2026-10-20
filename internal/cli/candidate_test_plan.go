package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateTest(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var source, expected, dataset, annotations, planPath, planSHA, recording string
	fs.StringVar(&dataset, "dataset", "", "exact retained synthetic dataset artifact")
	if command == "candidate-test-plan" {
		fs.StringVar(&source, "candidate", "", "sealed candidate declaration JSON")
		fs.StringVar(&expected, "candidate-sha256", "", "expected candidate identity")
		fs.StringVar(&annotations, "expectations", "", "explicit Boolean array in retained dataset order")
	} else {
		fs.StringVar(&planPath, "plan", "", "sealed fixture test plan")
		fs.StringVar(&planSHA, "plan-sha256", "", "expected plan identity")
		fs.StringVar(&recording, "recording", "", "caller-reported outputs; not execution attestation")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || dataset == "" {
		return candidatetest.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dRaw, err := externalFile(dataset, workerplan.MaxInputBytes)
	if err != nil {
		return err
	}
	if command == "candidate-test-plan" {
		if source == "" || expected == "" || annotations == "" {
			return candidatetest.ErrInvalid
		}
		raw, err := externalFile(source, candidate.MaxPackageBytes)
		if err != nil {
			return err
		}
		p, err := candidate.Read(bytes.NewReader(raw))
		if err != nil || p.SHA256 != expected {
			return candidatetest.ErrInvalid
		}
		raw, err = externalFile(annotations, 4096)
		if err != nil {
			return err
		}
		values, err := candidatetest.ReadExpectations(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		plan, err := candidatetest.New(p, dRaw, values)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(out, plan)
	}
	if planPath == "" || planSHA == "" || recording == "" {
		return candidatetest.ErrInvalid
	}
	raw, err := externalFile(planPath, candidatetest.MaxPlanBytes)
	if err != nil {
		return err
	}
	plan, err := candidatetest.ReadPlan(bytes.NewReader(raw))
	if err != nil || plan.SHA256 != planSHA {
		return candidatetest.ErrInvalid
	}
	raw, err = externalFile(recording, candidatetest.MaxRecordingBytes)
	if err != nil {
		return err
	}
	report, err := candidatetest.Evaluate(plan, dRaw, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emit(out, report)
}
