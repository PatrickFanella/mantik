package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/researchworker"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runResearchWorker(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	input := fs.String("input", "", "sealed synthetic dataset JSON artifact")
	var id, image, recipePath, recipeSHA, jobPath, jobSHA, resultPath string
	if command == "worker-job" {
		fs.StringVar(&id, "id", "", "job identity")
		fs.StringVar(&image, "image-sha256", "", "declared immutable image ID, sha256:hex")
		fs.StringVar(&recipePath, "recipe", "", "sealed native experiment recipe")
		fs.StringVar(&recipeSHA, "recipe-sha256", "", "expected recipe identity")
	} else {
		fs.StringVar(&jobPath, "job", "", "sealed job JSON")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected job identity")
		if command == "worker-result" {
			fs.StringVar(&resultPath, "file", "", "worker result envelope to validate")
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return workerplan.ErrInvalid
	}
	if command == "worker-job" {
		if *input == "" || recipePath == "" || recipeSHA == "" || id == "" || image == "" {
			return workerplan.ErrInvalid
		}
		f, err := os.Open(recipePath)
		if err != nil {
			return errors.New("cannot open recipe")
		}
		recipe, readErr := experiment.ReadRecipe(f)
		err = errors.Join(readErr, f.Close())
		if err != nil {
			return err
		}
		if recipe.SHA256 != recipeSHA {
			return errors.New("recipe identity mismatch")
		}
		f, err = os.Open(*input)
		if err != nil {
			return errors.New("cannot open worker input")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, workerplan.MaxInputBytes+1))
		err = errors.Join(readErr, f.Close())
		if err != nil {
			return err
		}
		job, err := researchworker.Declare(id, image, recipe, raw)
		if err != nil {
			return err
		}
		return emit(out, job)
	}
	if jobPath == "" || jobSHA == "" {
		return workerplan.ErrInvalid
	}
	f, err := os.Open(jobPath)
	if err != nil {
		return errors.New("cannot open worker job")
	}
	job, readErr := workerplan.ReadJob(f)
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return err
	}
	if job.SHA256 != jobSHA {
		return errors.New("worker job identity mismatch")
	}
	if command == "worker-result" {
		if resultPath == "" || *input != "" {
			return workerplan.ErrInvalid
		}
		f, err = os.Open(resultPath)
		if err != nil {
			return errors.New("cannot open worker result")
		}
		result, readErr := workerplan.ReadResult(f, job)
		err = errors.Join(readErr, f.Close())
		if err != nil {
			return err
		}
		return emit(out, result)
	}
	if *input == "" {
		return workerplan.ErrInvalid
	}
	f, err = os.Open(*input)
	if err != nil {
		return errors.New("cannot open worker input")
	}
	result, runErr := researchworker.Execute(ctx, job, f)
	err = errors.Join(runErr, f.Close())
	if err != nil {
		return err
	}
	if err = emit(out, result); err != nil {
		return err
	}
	if result.Status != "completed" || !result.DeadlineMet {
		return fmt.Errorf("worker result %s: %s; deadline met: %t", result.Status, result.ErrorCode, result.DeadlineMet)
	}
	return nil
}
