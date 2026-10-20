package workerlaunch

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"os"
	"reflect"
)

const CandidateCaptureSchema = candidateevidence.Schema
const MaxCandidateCaptureBytes = candidateevidence.MaxBytes

type CandidateCapture = candidateevidence.Capture

func ReadCandidateCapture(input io.Reader) (CandidateCapture, error) {
	return candidateevidence.Read(input)
}

// CaptureRegisteredCandidate only reads retained evidence. It does not inspect,
// reconcile, launch or stop Docker handles, and cannot turn a pending row into an outcome.
func CaptureRegisteredCandidate(ctx context.Context, root string, expected candidateattempt.Intent) (CandidateCapture, error) {
	if expected.Validate() != nil || !launchID.MatchString(expected.Command.RequestID) {
		return CandidateCapture{}, ErrConflict
	}
	root, unlock, err := openCandidateJournal(ctx, root, false)
	if err != nil {
		return CandidateCapture{}, err
	}
	defer unlock()
	h, err := candidateHistoryLocked(root)
	if err != nil {
		return CandidateCapture{}, err
	}
	if h.ReceiverID != expected.Command.ReceiverID {
		return CandidateCapture{}, ErrConflict
	}
	for _, attempt := range h.Attempts {
		if attempt.Intent.RequestID != expected.Command.RequestID {
			continue
		}
		if !reflect.DeepEqual(attempt.Intent.ServiceIntent, &expected) {
			return CandidateCapture{}, ErrConflict
		}
		if attempt.Outcome == nil {
			return CandidateCapture{}, ErrUnresolved
		}
		path := attemptPath(root, expected.Command.RequestID)
		a, _, err := loadCandidateBehavior(path, expected.Command.Job.Plan.Candidate.Toolchain.ImageSHA256)
		if err != nil {
			return CandidateCapture{}, err
		}
		pre, err := readFile(path, "pre-start.json", 64<<10)
		if errors.Is(err, os.ErrNotExist) {
			pre, err = []byte{}, nil
		}
		if err != nil {
			return CandidateCapture{}, err
		}
		stdout, err := readFile(path, "stdout.json", workerplan.MaxResultBytes)
		if err != nil {
			return CandidateCapture{}, err
		}
		stderr, err := readFile(path, "stderr.txt", 64<<10)
		if err != nil {
			return CandidateCapture{}, err
		}
		c := CandidateCapture{Schema: CandidateCaptureSchema, JournalRoot: root, Attempt: attempt, Acquisition: a, PreStart: pre, Stdout: stdout, Stderr: stderr, Authority: "caller_reported_candidate_outcome_capture_only"}
		c.SHA256 = workerplan.Hash(c)
		return c, c.VerifyIntent(expected)
	}
	return CandidateCapture{}, os.ErrNotExist
}
