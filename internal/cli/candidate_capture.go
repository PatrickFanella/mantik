package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
)

func runCandidateCapture(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("candidate-capture-inspect", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("capture", "", "portable caller-reported outcome capture")
	sha := fs.String("capture-sha256", "", "required expected capture identity")
	receiver := fs.String("receiver-id", "", "required expected receiver identity")
	id := fs.String("request-id", "", "required expected request identity")
	intentSHA := fs.String("intent-sha256", "", "required expected service intent identity")
	artifact := fs.String("artifact", "capture", "capture JSON or exact stdout|stderr bytes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *file == "" || *receiver == "" || *id == "" || !candidateRegistrySHA.MatchString(*sha) || !candidateRegistrySHA.MatchString(*intentSHA) || *artifact != "capture" && *artifact != "stdout" && *artifact != "stderr" {
		return workerlaunch.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := externalFile(*file, workerlaunch.MaxCandidateCaptureBytes)
	if err != nil {
		return err
	}
	c, err := workerlaunch.ReadCandidateCapture(bytes.NewReader(raw))
	if err != nil || c.SHA256 != *sha || c.Attempt.Intent.ServiceIntent.SHA256 != *intentSHA || c.Attempt.Intent.ServiceIntent.Command.ReceiverID != *receiver || c.Attempt.Intent.RequestID != *id {
		return workerlaunch.ErrConflict
	}
	switch *artifact {
	case "stdout":
		_, err = io.Copy(out, bytes.NewReader(c.Stdout))
	case "stderr":
		_, err = io.Copy(out, bytes.NewReader(c.Stderr))
	default:
		return emit(out, c)
	}
	return err
}
