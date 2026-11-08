package alpaca

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

const MaxCaptures = 128
const maxJournalFile = 8 << 20

var ErrRestorePending = errors.New("journal restore is incomplete or unverified; inspect isolated restore evidence")

var ErrJournalBusy = errors.New("capture journal is already owned by another process")
var ErrJournalConflict = errors.New("capture journal identity conflicts with retained content")
var journalReceiverPattern = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var journalCollectorPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var ErrResearchOutcomeUnknown = errors.New("research input outcome is unavailable; inspect receiver history or explicitly retry the retained input identity")
var ErrTargetChanged = errors.New("research receiver or retained delivery history changed")

type Journal struct {
	root *os.Root
	lock *os.File
	ID   string

	clock     func() time.Time
	stampMu   sync.Mutex
	lastStamp time.Time
}
type manifest struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
}
type capture struct {
	Schema      string    `json:"schema"`
	OriginalRef string    `json:"original_ref"`
	Recording   Recording `json:"recording"`
	Result      Result    `json:"result"`
	SHA256      string    `json:"sha256"`
}
type Binding struct {
	APIURL                  string `json:"api_url"`
	ReceiverID              string `json:"receiver_id"`
	CollectorRegistrationID string `json:"collector_registration_id,omitempty"`
}
type acknowledgement struct {
	Binding     Binding               `json:"binding"`
	InputSHA256 string                `json:"input_sha256"`
	Receipt     domain.RoutingReceipt `json:"receipt"`
	At          time.Time             `json:"at"`
	SHA256      string                `json:"sha256"`
}
type PendingInput struct {
	ID       string `json:"id"`
	Attempts int    `json:"attempts"`
	State    string `json:"state"`
}
type JournalStatus struct {
	PendingInputs []PendingInput `json:"pending_inputs"`
	JournalID     string         `json:"journal_id"`
	Captures      int            `json:"captures"`
	Inputs        int            `json:"inputs"`
	Acknowledged  int            `json:"acknowledged"`
	Pending       int            `json:"pending"`
	Uncertain     int            `json:"uncertain"`
	Binding       *Binding       `json:"binding,omitempty"`
}
type DeliveryResult struct {
	JournalStatus
	Processed  int `json:"processed"`
	Reconciled int `json:"reconciled"`
}
type ResearchReceiver interface {
	Snapshot(context.Context, []string) (domain.ResearchSnapshot, error)
	Submit(context.Context, string, domain.RoutingInput) (domain.RoutingReceipt, error)
}

// OpenJournal owns a private directory for the lifetime of the returned handle.
// Linux flock is deliberately non-blocking; callers never queue journal writers.
func OpenJournal(path string) (*Journal, error)         { return openJournal(path, true) }
func OpenExistingJournal(path string) (*Journal, error) { return openJournal(path, false) }
func openJournal(path string, create bool) (*Journal, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("journal requires an absolute private directory")
	}
	if create {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("journal directory must be owned by the current user with mode 0700")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if _, err := root.Lstat("restore-pending"); err == nil {
		_ = root.Close()
		return nil, ErrRestorePending
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = root.Close()
		return nil, err
	}
	lockLeaf, leafErr := root.Lstat("lock")
	if leafErr == nil && !lockLeaf.Mode().IsRegular() || leafErr != nil && !errors.Is(leafErr, os.ErrNotExist) {
		_ = root.Close()
		return nil, errors.New("invalid journal lock leaf")
	}
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	lockInfo, statErr := lock.Stat()
	if statErr != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 || lockLeaf != nil && !os.SameFile(lockLeaf, lockInfo) {
		_ = lock.Close()
		_ = root.Close()
		return nil, errors.New("invalid journal lock artifact")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrJournalBusy
	}
	j := &Journal{root: root, lock: lock}
	var m manifest
	raw, err := j.read("journal.json", 4096)
	if errors.Is(err, os.ErrNotExist) && create {
		m = manifest{Schema: "mantik-alpaca-journal-v1", ID: "journal-" + rand.Text()}
		raw, _ = json.Marshal(m)
		err = j.writeOnce("journal.json", raw)
	} else if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil || m.Schema != "mantik-alpaca-journal-v1" || !strings.HasPrefix(m.ID, "journal-") || !capturePattern.MatchString(m.ID) {
		_ = j.Close()
		return nil, errors.New("invalid or unavailable journal manifest")
	}
	j.ID = m.ID
	if err := j.finishRollover(); err != nil {
		_ = j.Close()
		return nil, err
	}
	return j, nil
}

// stamp reads the wall clock for a record whose order the journal later
// verifies. time.Now().UTC() drops the monotonic reading, so a backward step
// would otherwise make the journal write a record it then rejects as a
// conflict, permanently. A reading behind the latest stamp holds at that
// stamp until the clock catches up.
func (j *Journal) stamp() time.Time {
	j.stampMu.Lock()
	defer j.stampMu.Unlock()
	now := time.Now
	if j.clock != nil {
		now = j.clock
	}
	at := now().UTC()
	if at.Before(j.lastStamp) {
		return j.lastStamp
	}
	j.lastStamp = at
	return at
}

func (j *Journal) Close() error {
	a := syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	b := j.lock.Close()
	c := j.root.Close()
	return errors.Join(a, b, c)
}
func (j *Journal) read(name string, max int64) ([]byte, error) {
	// Root.OpenFile may resolve a link inside its root before the final open.
	// Inspect the leaf itself, then verify the opened inode matches it. The
	// journal owner holds the writer lock throughout this operation.
	leaf, err := j.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !leaf.Mode().IsRegular() {
		return nil, errors.New("journal artifact must be a regular non-symlink leaf")
	}
	f, err := j.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !os.SameFile(leaf, info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Getuid()) || info.Size() > max {
		return nil, errors.New("invalid journal artifact permissions, type or size")
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if len(raw) > int(max) {
		return nil, errors.New("journal artifact exceeds bound")
	}
	return raw, err
}
func (j *Journal) syncDirectory() error {
	f, err := j.root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// Publish by hard link after fsync: the destination is never overwritten and
// partial writes remain unreferenced temporary evidence, not valid captures.
func (j *Journal) writeOnce(name string, raw []byte) error {
	if old, err := j.read(name, maxJournalFile); err == nil {
		if string(old) != string(raw) {
			return ErrJournalConflict
		}
		return j.syncDirectory()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(raw) > maxJournalFile {
		return errors.New("journal artifact exceeds bound")
	}
	temp := "tmp-" + rand.Text()
	f, err := j.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(raw)
	if writeErr == nil && n != len(raw) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := j.root.Link(temp, name); err != nil {
		return err
	}
	if err := j.syncDirectory(); err != nil {
		return err
	}
	if err := j.root.Remove(temp); err != nil {
		return err
	}
	return j.syncDirectory()
}
func captureName(r Recording) string {
	return fmt.Sprintf("capture-%s-r%d.json", r.CaptureID, r.Revision)
}
func (j *Journal) names() ([]string, error) {
	f, err := j.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(20001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 20000 {
		return nil, errors.New("journal artifact count exceeds bound")
	}
	names := []string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "capture-") {
			names = append(names, entry.Name())
		}
	}
	if len(names) > MaxCaptures {
		return nil, errors.New("journal capture capacity exceeded")
	}
	slices.Sort(names)
	return names, nil
}
func captureDigest(c capture) (string, error) {
	c.SHA256 = ""
	raw, err := json.Marshal(c)
	return Digest(string(raw)), err
}
func (j *Journal) captures() ([]capture, error) {
	names, err := j.names()
	if err != nil {
		return nil, err
	}
	out := []capture{}
	for _, name := range names {
		raw, err := j.read(name, maxJournalFile)
		if err != nil {
			return nil, err
		}
		var c capture
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		hash, err := captureDigest(c)
		if err != nil || c.Schema != "mantik-alpaca-capture-v1" || hash != c.SHA256 || name != captureName(c.Recording) || c.Recording.RawRef != "journal:"+j.ID+":"+name {
			return nil, ErrJournalConflict
		}
		normalized, err := Normalize(c.Recording)
		if err != nil || !reflect.DeepEqual(normalized, c.Result) {
			return nil, ErrJournalConflict
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b capture) int {
		if n := a.Recording.ReceivedAt.Compare(b.Recording.ReceivedAt); n != 0 {
			return n
		}
		return strings.Compare(captureName(a.Recording), captureName(b.Recording))
	})
	return out, nil
}
func (j *Journal) Retain(r Recording) (Result, error) {
	if _, err := Normalize(r); err != nil {
		return Result{}, err
	}
	existing, err := j.captures()
	if err != nil {
		return Result{}, err
	}
	name := captureName(r)
	found := false
	for _, c := range existing {
		if captureName(c.Recording) == name {
			found = true
		}
	}
	if !found && len(existing) >= MaxCaptures {
		return Result{}, errors.New("journal capture capacity reached; retain or archive under a reviewed policy")
	}
	originalRef := r.RawRef
	r.RawRef = "journal:" + j.ID + ":" + name
	result, err := Normalize(r)
	if err != nil {
		return result, err
	}
	c := capture{Schema: "mantik-alpaca-capture-v1", OriginalRef: originalRef, Recording: r, Result: result}
	c.SHA256, err = captureDigest(c)
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return result, err
	}
	return result, j.writeOnce(name, raw)
}
func inputDigest(i domain.RoutingInput) string { raw, _ := json.Marshal(i); return Digest(string(raw)) }
func ackName(i domain.RoutingInput) string     { return "ack-" + i.ID + ".json" }
func (j *Journal) binding() (*Binding, error) {
	raw, err := j.read("binding.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b Binding
	err = json.Unmarshal(raw, &b)
	return &b, err
}
func ackDigest(a acknowledgement) (string, error) {
	a.SHA256 = ""
	raw, err := json.Marshal(a)
	return Digest(string(raw)), err
}
func (j *Journal) ack(i domain.RoutingInput) (*acknowledgement, error) {
	raw, err := j.read(ackName(i), 65536)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a acknowledgement
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	hash, hashErr := ackDigest(a)
	if hashErr != nil || hash != a.SHA256 || a.InputSHA256 != inputDigest(i) || a.Receipt.InputID != i.ID || a.At.IsZero() {
		return nil, ErrJournalConflict
	}
	return &a, nil
}
func (j *Journal) status(captures []capture) (JournalStatus, error) {
	out := JournalStatus{JournalID: j.ID, Captures: len(captures), PendingInputs: []PendingInput{}}
	b, err := j.binding()
	if err != nil {
		return out, err
	}
	out.Binding = b
	for _, c := range captures {
		for _, i := range c.Result.Inputs {
			out.Inputs++
			a, err := j.ack(i)
			if err != nil {
				return out, err
			}
			if a != nil {
				if b == nil || a.Binding != *b {
					return out, ErrJournalConflict
				}
				out.Acknowledged++
			}
		}
	}
	out.Pending = out.Inputs - out.Acknowledged
	for _, c := range captures {
		for _, i := range c.Result.Inputs {
			a, err := j.ack(i)
			if err != nil {
				return out, err
			}
			attempts, err := j.attempts(i, b)
			if err != nil {
				return out, err
			}
			if a == nil {
				state := "pending"
				if attempts > 0 {
					state = "uncertain"
					out.Uncertain++
				}
				if len(out.PendingInputs) < 16 {
					out.PendingInputs = append(out.PendingInputs, PendingInput{ID: i.ID, Attempts: attempts, State: state})
				}
			}
		}
	}
	return out, nil
}
func (j *Journal) Status() (JournalStatus, error) {
	captures, err := j.captures()
	if err != nil {
		return JournalStatus{}, err
	}
	return j.status(captures)
}
func validateBinding(b Binding) error {
	u, err := url.Parse(b.APIURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !journalReceiverPattern.MatchString(b.ReceiverID) {
		return errors.New("journal requires a receiver identity and exact HTTP origin")
	}
	if b.CollectorRegistrationID != "" && !journalCollectorPattern.MatchString(b.CollectorRegistrationID) {
		return errors.New("invalid collector registration binding")
	}
	return nil
}
func retained(snapshot domain.ResearchSnapshot, i domain.RoutingInput) (*domain.RetainedRoutingInput, error) {
	old, ok := snapshot.Inputs[i.ID]
	if !ok {
		return nil, nil
	}
	if !reflect.DeepEqual(old.Input, i) || old.Receipt.InputID != i.ID {
		return nil, ErrTargetChanged
	}
	return &old, nil
}

// Flush fences every mutation at the receiver and verifies its readback before
// retaining an acknowledgement. A lost reply is reconciled from server state.
func (j *Journal) Flush(ctx context.Context, b Binding, receiver ResearchReceiver, limit int) (DeliveryResult, error) {
	return j.FlushWithRecovery(ctx, b, receiver, limit, "")
}

// Explicit recovery admits only the same retained input, with at most three
// total attempted submissions. It cannot change attribution or rebind a target.
func (j *Journal) FlushWithRecovery(ctx context.Context, b Binding, receiver ResearchReceiver, limit int, retryMissingID string) (DeliveryResult, error) {
	out := DeliveryResult{}
	if err := validateBinding(b); err != nil {
		return out, err
	}
	if limit < 1 || limit > 16 {
		return out, errors.New("submission limit must be 1–16")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	captures, err := j.captures()
	if err != nil {
		return out, err
	}
	out.JournalStatus, err = j.status(captures)
	if err != nil {
		return out, err
	}
	if retryMissingID != "" {
		found := false
		for _, c := range captures {
			for _, i := range c.Result.Inputs {
				if i.ID == retryMissingID {
					a, err := j.ack(i)
					if err != nil {
						return out, err
					}
					found = a == nil
				}
			}
		}
		if !found {
			return out, errors.New("explicit recovery requires a pending retained input identity")
		}
	}
	if out.Binding != nil && *out.Binding != b {
		return out, ErrTargetChanged
	}
	snapshot, err := receiver.Snapshot(ctx, nil)
	if err != nil {
		return out, err
	}
	if snapshot.ID != b.ReceiverID {
		return out, ErrTargetChanged
	}
	// Read prior deliveries in bounded pages, checking each identity and receipt.
	acknowledged := []domain.RoutingInput{}
	pending := []domain.RoutingInput{}
	for _, c := range captures {
		for _, i := range c.Result.Inputs {
			a, err := j.ack(i)
			if err != nil {
				return out, err
			}
			if a != nil {
				acknowledged = append(acknowledged, i)
			} else if len(pending) < limit {
				pending = append(pending, i)
			}
		}
	}
	for start := 0; start < len(acknowledged); start += 16 {
		end := min(start+16, len(acknowledged))
		ids := []string{}
		for _, i := range acknowledged[start:end] {
			ids = append(ids, i.ID)
		}
		page, err := receiver.Snapshot(ctx, ids)
		if err != nil {
			return out, err
		}
		if page.ID != b.ReceiverID {
			return out, ErrTargetChanged
		}
		for _, i := range acknowledged[start:end] {
			a, err := j.ack(i)
			if err != nil {
				return out, err
			}
			old, err := retained(page, i)
			if err != nil {
				return out, err
			}
			if old == nil || !reflect.DeepEqual(old.Receipt, a.Receipt) {
				return out, ErrTargetChanged
			}
		}
	}
	ids := []string{}
	for _, i := range pending {
		ids = append(ids, i.ID)
	}
	snapshot, err = receiver.Snapshot(ctx, ids)
	if err != nil {
		return out, err
	}
	if snapshot.ID != b.ReceiverID {
		return out, ErrTargetChanged
	}
	if snapshot.Inputs == nil {
		snapshot.Inputs = map[string]domain.RetainedRoutingInput{}
	}

	raw, _ := json.Marshal(b)
	if err := j.writeOnce("binding.json", raw); err != nil {
		return out, err
	}
	for _, c := range captures {
		for _, i := range c.Result.Inputs {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			a, err := j.ack(i)
			if err != nil {
				return out, err
			}
			if a != nil {
				continue
			}
			if out.Processed == limit {
				out.JournalStatus, err = j.status(captures)
				return out, err
			}
			old, err := retained(snapshot, i)
			if err != nil {
				return out, err
			}
			if old == nil {
				attempts, err := j.attempts(i, &b)
				if err != nil {
					return out, err
				}
				if attempts > 0 && retryMissingID != i.ID {
					return out, ErrResearchOutcomeUnknown
				}
				if attempts >= 3 {
					return out, errors.New("research submission attempt bound reached")
				}
				attempt := submissionAttempt{Binding: b, InputSHA256: inputDigest(i), Number: attempts + 1, At: j.stamp()}
				attempt.SHA256, err = attemptDigest(attempt)
				if err != nil {
					return out, err
				}
				raw, err := json.Marshal(attempt)
				if err != nil {
					return out, err
				}
				if err := j.writeOnce(attemptName(i, attempt.Number), raw); err != nil {
					return out, err
				}
				receipt, err := receiver.Submit(ctx, b.ReceiverID, i)
				if err != nil {
					return out, err
				}
				if receipt.InputID != i.ID {
					return out, ErrTargetChanged
				}
				fresh, readErr := receiver.Snapshot(ctx, []string{i.ID})
				if readErr != nil {
					return out, readErr
				}
				if fresh.ID != b.ReceiverID {
					return out, ErrTargetChanged
				}
				snapshot.Inputs[i.ID] = fresh.Inputs[i.ID]
				old, err = retained(snapshot, i)
				if err != nil {
					return out, err
				}
				if old == nil {
					return out, ErrTargetChanged
				}
			} else {
				out.Reconciled++
			}
			ack := acknowledgement{Binding: b, InputSHA256: inputDigest(i), Receipt: old.Receipt, At: j.stamp()}
			ack.SHA256, err = ackDigest(ack)
			if err != nil {
				return out, err
			}
			raw, err := json.Marshal(ack)
			if err != nil {
				return out, err
			}
			if err := j.writeOnce(ackName(i), raw); err != nil {
				return out, err
			}
			out.Processed++
		}
	}
	out.JournalStatus, err = j.status(captures)
	return out, err
}

type submissionAttempt struct {
	Binding     Binding   `json:"binding"`
	InputSHA256 string    `json:"input_sha256"`
	Number      int       `json:"number"`
	At          time.Time `json:"at"`
	SHA256      string    `json:"sha256"`
}

func attemptDigest(a submissionAttempt) (string, error) {
	a.SHA256 = ""
	raw, err := json.Marshal(a)
	return Digest(string(raw)), err
}
func attemptName(i domain.RoutingInput, number int) string {
	return fmt.Sprintf("attempt-%s-%d.json", i.ID, number)
}
func (j *Journal) attempts(i domain.RoutingInput, b *Binding) (int, error) {
	count := 0
	missing := false
	for number := 1; number <= 3; number++ {
		raw, err := j.read(attemptName(i, number), 4096)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return count, err
		}
		var a submissionAttempt
		if err := json.Unmarshal(raw, &a); err != nil {
			return count, err
		}
		digest, err := attemptDigest(a)
		if err != nil || missing || b == nil || a.Binding != *b || a.Number != number || a.At.IsZero() || a.InputSHA256 != inputDigest(i) || digest != a.SHA256 {
			return count, ErrJournalConflict
		}
		count++
	}
	return count, nil
}
