package news

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const MaxRecords = 128
const maxJournalArtifacts = 256
const JournalSchema = "mantik-news-journal-v1"
const retainedSchema = "mantik-news-retained-v1"

var ErrNewsBusy = errors.New("news journal is already owned by another handle or process")
var ErrNewsConflict = errors.New("news recording identity conflicts with retained content")
var ErrNewsLineage = errors.New("news predecessor is missing, incompatible, terminal or later than its successor")
var ErrNewsCorrupt = errors.New("news journal artifact or source/result binding is invalid")
var ErrNewsCapacity = errors.New("news journal capacity reached; archive evidence before a separately reviewed continuation")
var recordFile = regexp.MustCompile(`^record-([a-f0-9]{64})\.json$`)
var tempFile = regexp.MustCompile(`^tmp-[A-Z2-7]{16,64}$`)
var journalID = regexp.MustCompile(`^news-[A-Z2-7]{16,64}$`)

type Journal struct {
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
	ID     string
	closed bool
}
type journalManifest struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
}
type Retained struct {
	JournalID string    `json:"journal_id"`
	Schema    string    `json:"schema"`
	Recording Recording `json:"recording"`
	Result    Result    `json:"result"`
}
type RecordSummary struct {
	Key             string       `json:"key"`
	RecordingSHA256 string       `json:"recording_sha256"`
	SourceID        string       `json:"source_id"`
	ArticleID       string       `json:"article_id"`
	EvidenceClass   string       `json:"evidence_class"`
	Revision        int          `json:"revision"`
	Kind            string       `json:"kind"`
	AvailableAt     time.Time    `json:"available_at"`
	ReceivedAt      time.Time    `json:"received_at"`
	Supersedes      *RevisionRef `json:"supersedes,omitempty"`
}
type JournalStatus struct {
	Schema           string          `json:"schema"`
	JournalID        string          `json:"journal_id"`
	Records          []RecordSummary `json:"records"`
	PendingArtifacts []string        `json:"pending_artifacts"`
}
type RetentionReceipt struct {
	JournalID       string `json:"journal_id"`
	Key             string `json:"key"`
	RecordingSHA256 string `json:"recording_sha256"`
	Reference       string `json:"reference"`
	Duplicate       bool   `json:"duplicate"`
}

func recordKey(source, article, class string, revision int) string {
	raw, _ := json.Marshal([]any{source, article, class, revision})
	return SHA256(string(raw))
}
func summary(r Recording, result Result) RecordSummary {
	return RecordSummary{Key: recordKey(r.SourceID, r.ArticleID, r.EvidenceClass, r.Revision), RecordingSHA256: result.RecordingSHA256, SourceID: r.SourceID, ArticleID: r.ArticleID, EvidenceClass: r.EvidenceClass, Revision: r.Revision, Kind: r.Kind, AvailableAt: r.AvailableAt.UTC(), ReceivedAt: r.ReceivedAt.UTC(), Supersedes: r.Supersedes}
}

func OpenJournal(path string) (*Journal, error)         { return openJournal(path, true) }
func OpenExistingJournal(path string) (*Journal, error) { return openJournal(path, false) }
func openJournal(path string, create bool) (*Journal, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("news journal requires an absolute private directory")
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
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("news journal directory must be a real current-user directory with mode 0700")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	leaf, leafErr := root.Lstat("lock")
	if leafErr == nil && !leaf.Mode().IsRegular() || leafErr != nil && !errors.Is(leafErr, os.ErrNotExist) {
		_ = root.Close()
		return nil, ErrNewsCorrupt
	}
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	lockedInfo, err := lock.Stat()
	if err != nil || !lockedInfo.Mode().IsRegular() || lockedInfo.Mode().Perm() != 0600 || leaf != nil && !os.SameFile(leaf, lockedInfo) {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrNewsCorrupt
	}
	lockOwner, ownerOK := lockedInfo.Sys().(*syscall.Stat_t)
	if !ownerOK || lockOwner.Uid != uint32(os.Getuid()) {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrNewsCorrupt
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrNewsBusy
	}
	j := &Journal{root: root, lock: lock}
	fail := func(err error) (*Journal, error) { _ = j.Close(); return nil, err }
	var m journalManifest
	raw, err := j.read("journal.json", 4096)
	if errors.Is(err, os.ErrNotExist) && create {
		// Never adopt pre-existing artifacts as a new logical journal.
		names, listErr := j.names()
		if listErr != nil {
			return fail(listErr)
		}
		if len(names) != 1 || names[0] != "lock" {
			return fail(ErrNewsCorrupt)
		}
		m = journalManifest{Schema: JournalSchema, ID: "news-" + rand.Text()}
		raw, _ = json.Marshal(m)
		err = j.writeOnce("journal.json", raw)
	} else if err == nil {
		err = decodeJournal(raw, &m)
	}
	if err != nil {
		return fail(err)
	}
	if m.Schema != JournalSchema || !journalID.MatchString(m.ID) {
		return fail(ErrNewsCorrupt)
	}
	j.ID = m.ID
	if _, err := j.audit(); err != nil {
		return fail(err)
	}
	return j, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return errors.Join(syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN), j.lock.Close(), j.root.Close())
}
func (j *Journal) names() ([]string, error) {
	f, err := j.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxJournalArtifacts + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxJournalArtifacts {
		return nil, ErrNewsCapacity
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}
func decodeJournal(raw []byte, out any) error {
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return ErrNewsCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrNewsCorrupt
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrNewsCorrupt
	}
	return nil
}
func (j *Journal) readRecord(key string) (Retained, error) {
	var stored Retained
	raw, err := j.read("record-"+key+".json", maxNewsFile)
	if err != nil {
		return stored, err
	}
	if err := decodeJournal(raw, &stored); err != nil {
		return stored, err
	}
	expected, err := Normalize(stored.Recording)
	if err != nil || stored.Schema != retainedSchema || stored.JournalID != j.ID || !reflect.DeepEqual(stored.Result, expected) || recordKey(stored.Recording.SourceID, stored.Recording.ArticleID, stored.Recording.EvidenceClass, stored.Recording.Revision) != key {
		return stored, ErrNewsCorrupt
	}
	return stored, nil
}
func lineage(next RecordSummary, records map[string]RecordSummary) error {
	if next.Kind == "initial" {
		return nil
	}
	p := next.Supersedes
	if p == nil {
		return ErrNewsLineage
	}
	previous, ok := records[recordKey(p.SourceID, p.ArticleID, next.EvidenceClass, p.Revision)]
	if !ok || previous.RecordingSHA256 != p.RecordingSHA256 || previous.Kind == "retraction" || next.AvailableAt.Before(previous.AvailableAt) || next.ReceivedAt.Before(previous.ReceivedAt) {
		return ErrNewsLineage
	}
	return nil
}
func (j *Journal) audit() (JournalStatus, error) {
	out := JournalStatus{Schema: JournalSchema, JournalID: j.ID, Records: []RecordSummary{}, PendingArtifacts: []string{}}
	raw, err := j.read("journal.json", 4096)
	if err != nil {
		return out, err
	}
	var m journalManifest
	if decodeJournal(raw, &m) != nil || m.Schema != JournalSchema || m.ID != j.ID {
		return out, ErrNewsCorrupt
	}
	leaf, err := j.root.Lstat("lock")
	if err != nil {
		return out, err
	}
	held, err := j.lock.Stat()
	if err != nil || !os.SameFile(leaf, held) {
		return out, ErrNewsCorrupt
	}
	names, err := j.names()
	if err != nil {
		return out, err
	}
	index := map[string]RecordSummary{}
	for _, name := range names {
		switch {
		case name == "journal.json": // Already verified above.
		case name == "lock":
			if _, err := j.read(name, 4096); err != nil {
				return out, err
			}
		case recordFile.MatchString(name):
			if len(index) >= MaxRecords {
				return out, ErrNewsCapacity
			}
			key := strings.TrimSuffix(strings.TrimPrefix(name, "record-"), ".json")
			stored, err := j.readRecord(key)
			if err != nil {
				return out, err
			}
			s := summary(stored.Recording, stored.Result)
			index[key] = s
			out.Records = append(out.Records, s)
		case tempFile.MatchString(name):
			if _, err := j.read(name, maxNewsFile); err != nil {
				return out, err
			}
			out.PendingArtifacts = append(out.PendingArtifacts, name)
		default:
			return out, ErrNewsCorrupt
		}
	}
	for _, r := range out.Records {
		if err := lineage(r, index); err != nil {
			return out, err
		}
	}
	return out, nil
}
func (j *Journal) Status() (JournalStatus, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return JournalStatus{}, os.ErrClosed
	}
	return j.audit()
}
func (j *Journal) Retain(r Recording) (RetentionReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return RetentionReceipt{}, os.ErrClosed
	}
	result, err := Normalize(r)
	if err != nil {
		return RetentionReceipt{}, err
	}
	status, err := j.audit()
	if err != nil {
		return RetentionReceipt{}, err
	}
	next := summary(r, result)
	index := map[string]RecordSummary{}
	receipt := RetentionReceipt{JournalID: j.ID, Key: next.Key, RecordingSHA256: result.RecordingSHA256, Reference: "news-journal:" + j.ID + ":record-" + next.Key + ".json"}
	for _, old := range status.Records {
		index[old.Key] = old
		if old.Key == next.Key {
			if old.RecordingSHA256 != next.RecordingSHA256 {
				return receipt, ErrNewsConflict
			}
			if err := j.syncDirectory(); err != nil {
				return receipt, err
			}
			receipt.Duplicate = true
			return receipt, nil
		}
	}
	if len(status.Records) >= MaxRecords || len(status.Records)+len(status.PendingArtifacts)+4 > maxJournalArtifacts {
		return receipt, ErrNewsCapacity
	}
	if err := lineage(next, index); err != nil {
		return receipt, err
	}
	// Store the canonical UTC recording so retries with equivalent instants
	// compare the same bytes; original text and supplied custody refs stay intact.
	r.PublishedAt, r.AvailableAt, r.ReceivedAt = r.PublishedAt.UTC(), r.AvailableAt.UTC(), r.ReceivedAt.UTC()
	raw, err := json.Marshal(Retained{JournalID: j.ID, Schema: retainedSchema, Recording: r, Result: result})
	if err != nil {
		return receipt, err
	}
	if err := j.writeOnce("record-"+next.Key+".json", raw); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func (j *Journal) Inspect(key string) (Retained, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Retained{}, os.ErrClosed
	}
	if !digest.MatchString(key) {
		return Retained{}, errors.New("invalid news record key")
	}
	if _, err := j.audit(); err != nil {
		return Retained{}, err
	}
	return j.readRecord(key)
}
