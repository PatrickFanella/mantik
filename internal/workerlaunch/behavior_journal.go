package workerlaunch

import (
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

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const CandidateJournalSchema = "mantik-candidate-attempt-journal-v1"
const RegisteredCandidateJournalSchema = "mantik-registered-candidate-attempt-journal-v1"
const MaxCandidateAttempts = 64
const maxIntentBytes = candidateevidence.MaxIntentBytes

var ErrCapacity = errors.New("candidate attempt journal capacity reached; preserve all evidence before a separately reviewed continuation")
var ErrUnresolved = errors.New("candidate attempt execution or collection is unresolved; inspect its original handle without relaunching")
var journalIdentity = regexp.MustCompile(`^candidate-journal-[A-Z2-7]{16,64}$`)

type candidateManifest struct {
	Schema     string `json:"schema"`
	ID         string `json:"id"`
	ReceiverID string `json:"receiver_id,omitempty"`
}
type CandidateIntent = candidateevidence.Intent
type CandidateOutcome = candidateevidence.Outcome
type CandidateAttempt = candidateevidence.Attempt

type CandidateHistory struct {
	Schema                string             `json:"schema"`
	JournalID             string             `json:"journal_id"`
	ReceiverID            string             `json:"receiver_id,omitempty"`
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
	return openCandidateJournalFor(ctx, root, create, "")
}
func validCandidateManifest(m candidateManifest) bool {
	return journalIdentity.MatchString(m.ID) && (m.Schema == CandidateJournalSchema && m.ReceiverID == "" || m.Schema == RegisteredCandidateJournalSchema && journalReceiver.MatchString(m.ReceiverID))
}

var journalReceiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)

func openCandidateJournalFor(ctx context.Context, root string, create bool, receiver string) (string, func(), error) {
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
		m = candidateManifest{Schema: CandidateJournalSchema, ID: "candidate-journal-" + rand.Text()}
		if receiver != "" {
			m.Schema, m.ReceiverID = RegisteredCandidateJournalSchema, receiver
		}
		if !validCandidateManifest(m) {
			return fail(ErrConflict)
		}
		if err = writeJSON(canonical, "journal.json", m); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	}
	if !validCandidateManifest(m) || create && m.ReceiverID != receiver {
		return fail(ErrConflict)
	}
	return canonical, unlock, nil
}

func retainedCandidateOutcome(root string, i CandidateIntent, o CandidateOutcome) error {
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
	stdout, err := readFile(path, "stdout.json", workerplan.MaxResultBytes)
	if err != nil {
		return err
	}
	stderr, err := readFile(path, "stderr.txt", 64<<10)
	if err != nil {
		return err
	}
	return candidateevidence.ValidateOutcomeEvidence(i, o, a, stdout, stderr)
}

func candidateHistoryLocked(root string) (CandidateHistory, error) {
	var m candidateManifest
	if err := journalDecode(root, "journal.json", 4096, &m); err != nil {
		return CandidateHistory{}, err
	}
	if !validCandidateManifest(m) {
		return CandidateHistory{}, ErrConflict
	}
	h := CandidateHistory{Schema: m.Schema, JournalID: m.ID, ReceiverID: m.ReceiverID, Attempts: []CandidateAttempt{}, PendingArtifacts: []string{}, RecordedStatuses: map[string]int{}, Authority: "local_candidate_attempt_history_only"}
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
			if journalDecode(root, name, maxIntentBytes, &i) != nil || i.Validate() != nil || i.JournalID != m.ID || name != "intent-"+i.RequestID+".json" {
				return h, ErrConflict
			}
			if i.ServiceIntent == nil && m.ReceiverID != "" || i.ServiceIntent != nil && i.ServiceIntent.Command.ReceiverID != m.ReceiverID {
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
	return reserveRegisteredCandidate(ctx, root, id, j, data, approved, nil)
}
func reserveRegisteredCandidate(ctx context.Context, root, id string, j candidateexec.Job, data []byte, approved string, service *candidateattempt.Intent) (string, CandidateAttempt, bool, error) {
	if ctx.Err() != nil {
		return "", CandidateAttempt{}, false, ctx.Err()
	}
	if !launchID.MatchString(id) || j.Validate() != nil || j.Plan.VerifyDataset(data) != nil || approved != j.Plan.Candidate.Toolchain.ImageSHA256 {
		return "", CandidateAttempt{}, false, ErrConflict
	}
	receiver := ""
	if service != nil {
		if service.Validate() != nil || service.Command.RequestID != id || !reflect.DeepEqual(service.Command.Job, j) {
			return "", CandidateAttempt{}, false, ErrConflict
		}
		receiver = service.Command.ReceiverID
	}
	root, unlock, err := openCandidateJournalFor(ctx, root, true, receiver)
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
			if a.Intent.Job.SHA256 != j.SHA256 || !reflect.DeepEqual(a.Intent.ServiceIntent, service) {
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
	if service != nil {
		i.Schema, i.ServiceIntent = "mantik-registered-candidate-attempt-intent-v1", service
	}
	i.SHA256 = workerplan.Hash(i)
	if i.Validate() != nil {
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
	return runRegisteredCandidateBehavior(ctx, root, id, j, data, approved, nil, d)
}

// RunRegisteredCandidateBehavior binds an exact service receipt before launching.
// The local journal is the owner namespace; this is not a service execution lease.
func RunRegisteredCandidateBehavior(ctx context.Context, root string, service candidateattempt.Intent, data []byte, approved string) (CandidateAttempt, error) {
	return runRegisteredCandidateBehavior(ctx, root, service.Command.RequestID, service.Command.Job, data, approved, &service, nil)
}
func runRegisteredCandidateBehavior(ctx context.Context, root, id string, j candidateexec.Job, data []byte, approved string, service *candidateattempt.Intent, d driver) (CandidateAttempt, error) {
	root, a, fresh, err := reserveRegisteredCandidate(ctx, root, id, j, data, approved, service)
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
	return reconcileRegisteredCandidateRequest(ctx, root, id, approved, cancel, nil, d)
}

func ReconcileRegisteredCandidateRequest(ctx context.Context, root string, service candidateattempt.Intent, approved string, cancel bool) (CandidateAttempt, error) {
	return reconcileRegisteredCandidateRequest(ctx, root, service.Command.RequestID, approved, cancel, &service, nil)
}
func reconcileRegisteredCandidateRequest(ctx context.Context, root, id, approved string, cancel bool, service *candidateattempt.Intent, d driver) (CandidateAttempt, error) {
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
			if approved != a.Intent.Job.Plan.Candidate.Toolchain.ImageSHA256 || !reflect.DeepEqual(a.Intent.ServiceIntent, service) {
				return CandidateAttempt{}, ErrConflict
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
