package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateproperty"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateProperty(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var dataset, jobPath, jobSHA, properties, planPath, planSHA, file string
	fs.StringVar(&dataset, "dataset", "", "exact original synthetic dataset")
	if command == "candidate-property-plan" {
		fs.StringVar(&jobPath, "job", "", "sealed behavior job")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected behavior job identity")
		fs.StringVar(&properties, "properties", "", "explicit array of supported sample properties")
	} else {
		fs.StringVar(&planPath, "plan", "", "sealed property plan")
		fs.StringVar(&planSHA, "plan-sha256", "", "expected property plan identity")
		fs.StringVar(&file, "file", "", "retained behavior worker result")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || dataset == "" {
		return candidateproperty.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := externalFile(dataset, workerplan.MaxInputBytes)
	if err != nil {
		return err
	}
	if command == "candidate-property-plan" {
		if jobPath == "" || jobSHA == "" || properties == "" {
			return candidateproperty.ErrInvalid
		}
		raw, err := externalFile(jobPath, candidateexec.MaxJobBytes)
		if err != nil {
			return err
		}
		j, err := candidateexec.ReadJob(bytes.NewReader(raw))
		if err != nil || j.SHA256 != jobSHA {
			return candidateproperty.ErrInvalid
		}
		raw, err = externalFile(properties, 4096)
		if err != nil {
			return err
		}
		checks, err := candidateproperty.ReadProperties(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		plan, err := candidateproperty.New(j, data, checks)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(out, plan)
	}
	if planPath == "" || planSHA == "" || file == "" {
		return candidateproperty.ErrInvalid
	}
	raw, err := externalFile(planPath, candidateproperty.MaxPlanBytes)
	if err != nil {
		return err
	}
	p, err := candidateproperty.ReadPlan(bytes.NewReader(raw))
	if err != nil || p.SHA256 != planSHA {
		return candidateproperty.ErrInvalid
	}
	raw, err = externalFile(file, candidateexec.MaxResultBytes)
	if err != nil {
		return err
	}
	report, err := candidateproperty.Evaluate(p, data, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emit(out, report)
}
