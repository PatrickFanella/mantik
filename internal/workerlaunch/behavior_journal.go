package workerlaunch

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const CandidateJournalSchema = "mantik-candidate-attempt-journal-v1"
const MaxCandidateAttempts = 64
const maxIntentBytes = candidateexec.MaxJobBytes + 8192

var ErrCapacity = errors.New("candidate attempt journal capacity reached; preserve all evidence before a separately reviewed continuation")
var ErrUnresolved = errors.New("candidate attempt execution or collection is unresolved; inspect its original handle without relaunching")
var journalIdentity = regexp.MustCompile(`^candidate-journal-[A-Z2-7]{16,64}$`)

type candidateManifest struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
}
type CandidateIntent struct {
	Schema              string            `json:"schema"`
	JournalID           string            `json:"journal_id"`
	RequestID           string            `json:"request_id"`
	Sequence            int               `json:"sequence"`
	Job                 candidateexec.Job `json:"job"`
	CreatedAt           time.Time         `json:"created_at"`
	Authority           string            `json:"authority"`
	ExecutionStarted    bool              `json:"execution_started"`
	QualificationCredit bool              `json:"qualification_credit"`
	SHA256              string            `json:"sha256"`
}

func (i CandidateIntent) validate() error {
	if i.Schema != "mantik-candidate-attempt-intent-v1" || !journalIdentity.MatchString(i.JournalID) || !launchID.MatchString(i.RequestID) || i.Sequence < 1 || i.Sequence > MaxCandidateAttempts || i.Job.Validate() != nil || i.CreatedAt.IsZero() || i.CreatedAt.Year() < 1 || i.CreatedAt.Year() > 9999 || i.Authority != "local_candidate_attempt_intent_only" || i.ExecutionStarted || i.QualificationCredit {
		return ErrConflict
	}
	expected := i.SHA256
	i.SHA256 = ""
	if expected != workerplan.Hash(i) {
		return ErrConflict
	}
	return nil
}

type CandidateOutcome struct {
	Schema                string    `json:"schema"`
	IntentSHA256          string    `json:"intent_sha256"`
	CapturedAt            time.Time `json:"captured_at"`
	Receipt               Receipt   `json:"receipt"`
	Authority             string    `json:"authority"`
	CurrentHostStateKnown bool      `json:"current_host_state_known"`
	QualificationCredit   bool      `json:"qualification_credit"`
	SHA256                string    `json:"sha256"`
}
type CandidateAttempt struct {
	Intent  CandidateIntent   `json:"intent"`
	Outcome *CandidateOutcome `json:"outcome"`
}
type CandidateHistory struct {
	Schema                string             `json:"schema"`
	JournalID             string             `json:"journal_id"`
	Attempts              []CandidateAttempt `json:"attempts"`
	PendingArtifacts      []string           `json:"pending_artifacts"`
	Pending               int                `json:"pending"`
	RecordedStatuses      map[string]int     `json:"recorded_statuses"`
	Authority             string             `json:"authority"`
	CurrentHostStateKnown bool               `json:"current_host_state_known"`
	QualificationCredit   bool               `json:"qualification_credit"`
}

func attemptPath(root, id string) string { return filepath.Join(root, "attempt-"+id) }
func journalNames(root string) ([]string, error) {
	f, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(3*MaxCandidateAttempts + 18)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 3*MaxCandidateAttempts+16 {
		return nil, ErrCapacity
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
func journalDecode(root, name string, limit int, v any) error {
	raw, err := readFile(root, name, int64(limit))
	if err != nil {
		return err
	}
	if workerplan.ReadBoundedShape(raw, v, limit) != nil {
		return ErrConflict
	}
	return nil
}

// openCandidateJournal holds only the short metadata lock, not a worker wait.
func openCandidateJournal(ctx context.Context, root string, create bool) (string, func(), error) {
	if !filepath.IsAbs(root) {
		return "", nil, ErrConflict
	}
	if create {
		if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	canonical, err := directory(root, false)
	if err != nil {
		return "", nil, err
	}
	if create {
		if err := syncDir(filepath.Dir(canonical)); err != nil {
			return "", nil, err
		}
	}
	unlock, err := lock(ctx, canonical)
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) { unlock(); return "", nil, err }
	info, err := os.Lstat(canonical + "/.lock")
	if err != nil {
		return fail(err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || owner.Uid != uint32(os.Getuid()) {
		return fail(ErrConflict)
	}
	var m candidateManifest
	err = journalDecode(canonical, "journal.json", 4096, &m)
	if errors.Is(err, os.ErrNotExist) && create {
		names, e := journalNames(canonical)
		if e != nil {
			return fail(e)
		}
		if len(names) != 1 || names[0] != ".lock" {
			return fail(ErrConflict)
		}
		m = candidateManifest{CandidateJournalSchema, "candidate-journal-" + rand.Text()}
		if err = writeJSON(canonical, "journal.json", m); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	}
	if m.Schema != CandidateJournalSchema || !journalIdentity.MatchString(m.ID) {
		return fail(ErrConflict)
	}
	return canonical, unlock, nil
}

func retainedCandidateOutcome(root string, i CandidateIntent, o CandidateOutcome) error {
	if i.validate() != nil || o.Schema != "mantik-candidate-attempt-outcome-v1" || o.IntentSHA256 != i.SHA256 || o.CapturedAt.Before(i.CreatedAt) || o.CapturedAt.Before(o.Receipt.CollectedAt) || o.CapturedAt.Year() < 1 || o.CapturedAt.Year() > 9999 || o.Authority != "local_candidate_host_receipt_capture_only" || o.CurrentHostStateKnown || o.QualificationCredit {
		return ErrConflict
	}
	expected := o.SHA256
	o.SHA256 = ""
	if expected != workerplan.Hash(o) {
		return ErrConflict
	}
	path := attemptPath(root, i.RequestID)
	canonical, err := directory(path, false)
	if err != nil || canonical != path {
		return ErrConflict
	}
	a, j, err := loadCandidateBehavior(path, i.Job.Plan.Candidate.Toolchain.ImageSHA256)
	if err != nil || j.SHA256 != i.Job.SHA256 {
		return ErrConflict
	}
	var stored Receipt
	if journalDecode(path, "receipt.json", 16384, &stored) != nil || !reflect.DeepEqual(stored, o.Receipt) {
		return ErrConflict
	}
	r := o.Receipt
	sha := r.SHA256
	r.SHA256 = ""
	if sha != workerplan.Hash(r) || r.Schema != "mantik-candidate-behavior-host-receipt-v1" || r.Authority != "synthetic_candidate_behavior_host_receipt_only" || r.QualificationCredit || r.AcquisitionSHA256 != a.SHA256 || r.ImageSHA256 != a.ImageSHA256 || !containerID.MatchString(r.ContainerID) {
		return ErrConflict
	}
	switch r.Status {
	case "completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted":
	default:
		return ErrConflict
	}
	if r.StdoutBytes < 0 || r.StdoutBytes > workerplan.MaxResultBytes || r.StderrBytes < 0 || r.StderrBytes > 64<<10 {
		return ErrConflict
	}
	for _, artifact := range []struct {
		name, sha string
		n         int
	}{{"stdout.json", r.StdoutSHA256, r.StdoutBytes}, {"stderr.txt", r.StderrSHA256, r.StderrBytes}} {
		raw, err := readFile(path, artifact.name, int64(artifact.n))
		if err != nil || len(raw) != artifact.n || workerplan.Digest(raw) != artifact.sha {
			return ErrConflict
		}
	}
	raw, err := readFile(path, "stdout.json", int64(r.StdoutBytes))
	if err != nil {
		return err
	}
	result, resultErr := candidateexec.ReadResult(bytes.NewReader(raw), j)
	if resultErr == nil {
		if r.WorkerResultSHA256 != result.SHA256 || r.WorkerStatus != result.Status {
			return ErrConflict
		}
	} else if r.WorkerResultSHA256 != "" || r.WorkerStatus != "" {
		return ErrConflict
	}
	if r.Status == "completed" || r.Status == "mismatch" || r.Status == "failed" {
		if resultErr != nil || result.Status != r.Status || result.ReasonCode != r.ReasonCode || !r.OutputComplete || !r.ProfileVerified || !r.HostDeadlineMet || r.ExitCode != 1 && r.Status != "completed" || r.Status == "completed" && r.ExitCode != 0 {
			return ErrConflict
		}
	}
	return nil
}
func candidateHistoryLocked(root string) (CandidateHistory, error) {
	var m candidateManifest
	if err := journalDecode(root, "journal.json", 4096, &m); err != nil {
		return CandidateHistory{}, err
	}
	if m.Schema != CandidateJournalSchema || !journalIdentity.MatchString(m.ID) {
		return CandidateHistory{}, ErrConflict
	}
	h := CandidateHistory{Schema: CandidateJournalSchema, JournalID: m.ID, Attempts: []CandidateAttempt{}, PendingArtifacts: []string{}, RecordedStatuses: map[string]int{}, Authority: "local_candidate_attempt_history_only"}
	names, err := journalNames(root)
	if err != nil {
		return h, err
	}
	intents := map[string]CandidateIntent{}
	outcomes := map[string]bool{}
	directories := map[string]bool{}
	for _, name := range names {
		switch {
		case name == "journal.json" || name == ".lock":
		case strings.HasPrefix(name, ".pending-"):
			if _, err := readFile(root, name, int64(maxIntentBytes)); err != nil {
				return h, err
			}
			h.PendingArtifacts = append(h.PendingArtifacts, name)
		case strings.HasPrefix(name, "intent-") && strings.HasSuffix(name, ".json"):
			var i CandidateIntent
			if journalDecode(root, name, maxIntentBytes, &i) != nil || i.validate() != nil || i.JournalID != m.ID || name != "intent-"+i.RequestID+".json" {
				return h, ErrConflict
			}
			intents[i.RequestID] = i
		case strings.HasPrefix(name, "outcome-") && strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(strings.TrimPrefix(name, "outcome-"), ".json")
			if !launchID.MatchString(id) {
				return h, ErrConflict
			}
			outcomes[id] = true
		case strings.HasPrefix(name, "attempt-"):
			id := strings.TrimPrefix(name, "attempt-")
			if !launchID.MatchString(id) {
				return h, ErrConflict
			}
			path := attemptPath(root, id)
			canonical, err := directory(path, false)
			if err != nil || canonical != path {
				return h, ErrConflict
			}
			directories[id] = true
		default:
			return h, ErrConflict
		}
	}
	if len(intents) > MaxCandidateAttempts {
		return h, ErrCapacity
	}
	for id := range outcomes {
		if _, ok := intents[id]; !ok {
			return h, ErrConflict
		}
	}
	for id := range directories {
		if _, ok := intents[id]; !ok {
			return h, ErrConflict
		}
	}
	for id, i := range intents {
		attempt := CandidateAttempt{Intent: i}
		if outcomes[id] {
			var o CandidateOutcome
			if journalDecode(root, "outcome-"+id+".json", 32768, &o) != nil || retainedCandidateOutcome(root, i, o) != nil {
				return h, ErrConflict
			}
			attempt.Outcome = &o
			h.RecordedStatuses[o.Receipt.Status]++
		} else {
			h.Pending++
		}
		h.Attempts = append(h.Attempts, attempt)
	}
	sort.Slice(h.Attempts, func(i, j int) bool { return h.Attempts[i].Intent.Sequence < h.Attempts[j].Intent.Sequence })
	for index, a := range h.Attempts {
		if a.Intent.Sequence != index+1 {
			return h, ErrConflict
		}
	}
	return h, nil
}
func CandidateBehaviorHistory(ctx context.Context, root string) (CandidateHistory, error) {
	root, unlock, err := openCandidateJournal(ctx, root, false)
	if err != nil {
		return CandidateHistory{}, err
	}
	defer unlock()
	return candidateHistoryLocked(root)
}
func reserveCandidate(ctx context.Context, root, id string, j candidateexec.Job, data []byte, approved string) (string, CandidateAttempt, bool, error) {
	if ctx.Err() != nil {
		return "", CandidateAttempt{}, false, ctx.Err()
	}
	if !launchID.MatchString(id) || j.Validate() != nil || j.Plan.VerifyDataset(data) != nil || approved != j.Plan.Candidate.Toolchain.ImageSHA256 {
		return "", CandidateAttempt{}, false, ErrConflict
	}
	root, unlock, err := openCandidateJournal(ctx, root, true)
	if err != nil {
		return "", CandidateAttempt{}, false, err
	}
	defer unlock()
	history, err := candidateHistoryLocked(root)
	if err != nil {
		return "", CandidateAttempt{}, false, err
	}
	for _, a := range history.Attempts {
		if a.Intent.RequestID == id {
			if a.Intent.Job.SHA256 != j.SHA256 {
				return "", CandidateAttempt{}, false, ErrConflict
			}
			return root, a, false, syncDir(root)
		}
	}
	if len(history.PendingArtifacts) > 0 {
		return "", CandidateAttempt{}, false, ErrUnresolved
	}
	if len(history.Attempts) >= MaxCandidateAttempts {
		return "", CandidateAttempt{}, false, ErrCapacity
	}
	i := CandidateIntent{Schema: "mantik-candidate-attempt-intent-v1", JournalID: history.JournalID, RequestID: id, Sequence: len(history.Attempts) + 1, Job: j, CreatedAt: time.Now().UTC(), Authority: "local_candidate_attempt_intent_only"}
	i.SHA256 = workerplan.Hash(i)
	if i.validate() != nil {
		return "", CandidateAttempt{}, false, ErrConflict
	}
	if err := writeJSON(root, "intent-"+id+".json", i); err != nil {
		return "", CandidateAttempt{}, false, err
	}
	return root, CandidateAttempt{Intent: i}, true, nil
}
func finishCandidate(ctx context.Context, root string, a CandidateAttempt, r Receipt) (CandidateAttempt, error) {
	root, unlock, err := openCandidateJournal(ctx, root, false)
	if err != nil {
		return a, err
	}
	defer unlock()
	history, err := candidateHistoryLocked(root)
	if err != nil {
		return a, err
	}
	found := false
	for _, existing := range history.Attempts {
		if existing.Intent.RequestID == a.Intent.RequestID {
			if existing.Intent.SHA256 != a.Intent.SHA256 {
				return a, ErrConflict
			}
			found = true
			if existing.Outcome != nil {
				if !reflect.DeepEqual(existing.Outcome.Receipt, r) {
					return a, ErrConflict
				}
				return existing, syncDir(root)
			}
		}
	}
	if !found {
		return a, ErrConflict
	}
	o := CandidateOutcome{Schema: "mantik-candidate-attempt-outcome-v1", IntentSHA256: a.Intent.SHA256, CapturedAt: time.Now().UTC(), Receipt: r, Authority: "local_candidate_host_receipt_capture_only"}
	o.SHA256 = workerplan.Hash(o)
	if retainedCandidateOutcome(root, a.Intent, o) != nil {
		return a, ErrConflict
	}
	if err := writeJSON(root, "outcome-"+a.Intent.RequestID+".json", o); err != nil {
		return a, err
	}
	a.Outcome = &o
	return a, nil
}

// RunCandidateBehavior retains intent before launch. Reusing an ID reconciles
// only; an intent without an acquisition is never interpreted as permission to run.
func RunCandidateBehavior(ctx context.Context, root, id string, j candidateexec.Job, data []byte, approved string) (CandidateAttempt, error) {
	return runCandidateBehavior(ctx, root, id, j, data, approved, nil)
}
func runCandidateBehavior(ctx context.Context, root, id string, j candidateexec.Job, data []byte, approved string, d driver) (CandidateAttempt, error) {
	root, a, fresh, err := reserveCandidate(ctx, root, id, j, data, approved)
	if err != nil {
		return a, err
	}
	var r Receipt
	if fresh {
		r, err = launchCandidateBehavior(ctx, attemptPath(root, id), j, data, approved, d)
	} else {
		r, err = reconcileCandidateBehavior(ctx, attemptPath(root, id), approved, false, d)
	}
	if err != nil {
		return a, errors.Join(ErrUnresolved, err)
	}
	// Retain completion even if the caller's context expired during worker stop.
	work, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return finishCandidate(work, root, a, r)
}
func ReconcileCandidateRequest(ctx context.Context, root, id, approved string, cancel bool) (CandidateAttempt, error) {
	return reconcileCandidateRequest(ctx, root, id, approved, cancel, nil)
}
func reconcileCandidateRequest(ctx context.Context, root, id, approved string, cancel bool, d driver) (CandidateAttempt, error) {
	if !launchID.MatchString(id) {
		return CandidateAttempt{}, ErrConflict
	}
	canonical, unlock, err := openCandidateJournal(ctx, root, false)
	if err != nil {
		return CandidateAttempt{}, err
	}
	history, err := candidateHistoryLocked(canonical)
	unlock()
	if err != nil {
		return CandidateAttempt{}, err
	}
	for _, a := range history.Attempts {
		if a.Intent.RequestID == id {
			if approved != a.Intent.Job.Plan.Candidate.Toolchain.ImageSHA256 {
				return a, ErrConflict
			}
			r, err := reconcileCandidateBehavior(ctx, attemptPath(canonical, id), approved, cancel, d)
			if err != nil {
				return a, errors.Join(ErrUnresolved, err)
			}
			return finishCandidate(ctx, canonical, a, r)
		}
	}
	return CandidateAttempt{}, os.ErrNotExist
}
