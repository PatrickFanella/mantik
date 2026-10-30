package issuerdoc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const journalSchema = "mantik-issuer-document-journal-v1"
const maxReceiptBytes = 64 << 10
const fetchTimeout = 30 * time.Second

var ErrJournalBusy = errors.New("issuer document journal is already owned by another process")
var ErrJournalConflict = errors.New("issuer document journal identity conflicts with retained content")
var ErrCollectionUnresolved = errors.New("issuer document collection has an interrupted or failed attempt; use a new collection identity")
var ErrCollectionNotFound = errors.New("issuer document collection not found")
var collectionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Spec selects a method-bound document and the instrument to parse from it.
// It cannot choose a URL, supply bytes or assert coverage.
type Spec struct {
	ID        string `json:"id"`
	Method    string `json:"method"`
	Symbol    string `json:"symbol"`
	CUSIP     string `json:"cusip"`
	AccessRef string `json:"access_ref"`
}

func (s Spec) Validate() error {
	if !collectionID.MatchString(s.ID) || s.Method != MethodSPDRWorkbook || !Symbol.MatchString(s.Symbol) || !CUSIP.MatchString(s.CUSIP) || len(s.AccessRef) == 0 || len(s.AccessRef) > 256 || !utf8.ValidString(s.AccessRef) || strings.ContainsFunc(s.AccessRef, unicode.IsControl) {
		return errors.New("invalid issuer document spec")
	}
	return nil
}

type intent struct {
	Schema      string    `json:"schema"`
	JournalID   string    `json:"journal_id"`
	Spec        Spec      `json:"spec"`
	URL         string    `json:"url"`
	RequestedAt time.Time `json:"requested_at"`
	SHA256      string    `json:"sha256"`
}

// Completion binds the original bytes to the intent. FinalURL is the request
// URL because redirects are refused rather than followed.
type completion struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	CompletedAt    time.Time `json:"completed_at"`
	HTTPStatus     int       `json:"http_status"`
	ContentType    string    `json:"content_type"`
	FinalURL       string    `json:"final_url"`
	DocumentSHA256 string    `json:"document_sha256"`
	DocumentBytes  int       `json:"document_bytes"`
	SHA256         string    `json:"sha256"`
}
type failure struct {
	Schema       string    `json:"schema"`
	IntentSHA256 string    `json:"intent_sha256"`
	FailedAt     time.Time `json:"failed_at"`
	HTTPStatus   int       `json:"http_status"`
	Reason       string    `json:"reason"`
	SHA256       string    `json:"sha256"`
}

// Status is derived from retained artifacts. The parse result is rederived from
// the original bytes on every read, never stored as an independent claim.
type Status struct {
	Schema                   string    `json:"schema"`
	JournalID                string    `json:"journal_id"`
	CollectionID             string    `json:"collection_id"`
	State                    string    `json:"state"`
	Spec                     Spec      `json:"spec"`
	URL                      string    `json:"url"`
	RequestedAt              time.Time `json:"requested_at"`
	IntentSHA256             string    `json:"intent_sha256"`
	CompletedAt              time.Time `json:"completed_at,omitzero"`
	FailedAt                 time.Time `json:"failed_at,omitzero"`
	HTTPStatus               int       `json:"http_status,omitempty"`
	FailureReason            string    `json:"failure_reason,omitempty"`
	ContentType              string    `json:"content_type,omitempty"`
	DocumentSHA256           string    `json:"document_sha256,omitempty"`
	DocumentBytes            int       `json:"document_bytes,omitempty"`
	CompletionSHA256         string    `json:"completion_sha256,omitempty"`
	ParseStatus              string    `json:"parse_status,omitempty"`
	ParseError               string    `json:"parse_error,omitempty"`
	Document                 *Document `json:"document,omitempty"`
	Authority                string    `json:"authority"`
	ProviderAttested         bool      `json:"provider_attested"`
	CoverageComplete         bool      `json:"coverage_complete"`
	EconomicApplicationReady bool      `json:"economic_application_ready"`
}

type Journal struct {
	root *os.Root
	lock *os.File
	ID   string
	url  string
	now  func() time.Time
}

func OpenJournal(path string) (*Journal, error)         { return openJournal(path, true) }
func OpenExistingJournal(path string) (*Journal, error) { return openJournal(path, false) }

// The journal follows the Alpaca journal's private-directory rules: an absolute
// current-user directory with mode 0700, regular 0600 leaves and one writer.
func openJournal(dir string, create bool) (*Journal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("issuer document journal requires an absolute private directory")
	}
	if create {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("issuer document journal must be owned by the current user with mode 0700")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	lockLeaf, leafErr := root.Lstat("lock")
	if leafErr == nil && !lockLeaf.Mode().IsRegular() || leafErr != nil && !errors.Is(leafErr, os.ErrNotExist) {
		_ = root.Close()
		return nil, errors.New("invalid issuer document journal lock leaf")
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
		return nil, errors.New("invalid issuer document journal lock artifact")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrJournalBusy
	}
	j := &Journal{root: root, lock: lock, url: SPDRWorkbookURL, now: time.Now}
	var m struct {
		Schema string `json:"schema"`
		ID     string `json:"id"`
	}
	raw, err := j.read("journal.json", 4096)
	if errors.Is(err, os.ErrNotExist) && create {
		m.Schema, m.ID = journalSchema, "issuer-journal-"+rand.Text()
		raw, _ = json.Marshal(m)
		err = j.writeOnce("journal.json", raw)
	} else if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil || m.Schema != journalSchema || !strings.HasPrefix(m.ID, "issuer-journal-") || !collectionID.MatchString(strings.TrimPrefix(m.ID, "issuer-journal-")) {
		_ = j.Close()
		return nil, errors.New("invalid or unavailable issuer document journal manifest")
	}
	j.ID = m.ID
	return j, nil
}

func (j *Journal) Close() error {
	a := syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	b := j.lock.Close()
	c := j.root.Close()
	return errors.Join(a, b, c)
}

func (j *Journal) read(name string, max int64) ([]byte, error) {
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

// Publish by hard link after fsync; an existing leaf is never overwritten.
func (j *Journal) writeOnce(name string, raw []byte) error {
	if old, err := j.read(name, MaxDocumentBytes); err == nil {
		if string(old) != string(raw) {
			return ErrJournalConflict
		}
		return j.syncDirectory()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
	if err := errors.Join(writeErr, f.Close()); err != nil {
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

func readJSON[T any](j *Journal, name string, value *T) (bool, error) {
	raw, err := j.read(name, maxReceiptBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if workerplan.ReadBoundedShape(raw, value, maxReceiptBytes) != nil {
		return false, errors.New("invalid issuer document journal artifact " + name)
	}
	return true, nil
}

func (r intent) seal() string     { r.SHA256 = ""; return workerplan.Hash(r) }
func (r completion) seal() string { r.SHA256 = ""; return workerplan.Hash(r) }
func (r failure) seal() string    { r.SHA256 = ""; return workerplan.Hash(r) }

// Collection rederives status from the retained intent, terminal receipt and
// original bytes, rejecting any artifact that no longer matches its hash chain.
func (j *Journal) Collection(id string) (Status, error) {
	if !collectionID.MatchString(id) {
		return Status{}, errors.New("invalid issuer document collection identity")
	}
	var in intent
	found, err := readJSON(j, "intent-"+id+".json", &in)
	if err != nil {
		return Status{}, err
	}
	if !found {
		return Status{}, ErrCollectionNotFound
	}
	if in.Schema != "mantik-issuer-document-intent-v1" || in.JournalID != j.ID || in.Spec.ID != id || in.Spec.Validate() != nil || in.URL != SPDRWorkbookURL || in.RequestedAt.IsZero() || in.seal() != in.SHA256 {
		return Status{}, ErrJournalConflict
	}
	s := Status{Schema: "mantik-issuer-document-collection-v1", JournalID: j.ID, CollectionID: id, State: "interrupted", Spec: in.Spec, URL: in.URL, RequestedAt: in.RequestedAt, IntentSHA256: in.SHA256, Authority: "local_journal_acquisition_of_issuer_document_only"}
	var done completion
	completed, err := readJSON(j, "complete-"+id+".json", &done)
	if err != nil {
		return Status{}, err
	}
	var failed failure
	isFailed, err := readJSON(j, "failed-"+id+".json", &failed)
	if err != nil {
		return Status{}, err
	}
	if completed && isFailed {
		return Status{}, ErrJournalConflict
	}
	if isFailed {
		if failed.Schema != "mantik-issuer-document-failure-v1" || failed.IntentSHA256 != in.SHA256 || failed.FailedAt.Before(in.RequestedAt) || failed.Reason == "" || failed.seal() != failed.SHA256 {
			return Status{}, ErrJournalConflict
		}
		s.State, s.FailedAt, s.HTTPStatus, s.FailureReason = "failed", failed.FailedAt, failed.HTTPStatus, failed.Reason
		return s, nil
	}
	if !completed {
		return s, nil
	}
	if done.Schema != "mantik-issuer-document-completion-v1" || done.IntentSHA256 != in.SHA256 || done.CompletedAt.Before(in.RequestedAt) || done.HTTPStatus != http.StatusOK || done.FinalURL != in.URL || done.seal() != done.SHA256 {
		return Status{}, ErrJournalConflict
	}
	raw, err := j.read("document-"+id+".bin", MaxDocumentBytes)
	if err != nil || len(raw) != done.DocumentBytes || workerplan.Digest(raw) != done.DocumentSHA256 {
		return Status{}, ErrJournalConflict
	}
	s.State, s.CompletedAt, s.HTTPStatus, s.ContentType, s.DocumentSHA256, s.DocumentBytes, s.CompletionSHA256 = "retained", done.CompletedAt, done.HTTPStatus, done.ContentType, done.DocumentSHA256, done.DocumentBytes, done.SHA256
	doc, err := ParseSPDRWorkbook(raw, in.Spec.Symbol, in.Spec.CUSIP)
	if err != nil {
		s.ParseStatus, s.ParseError = "unparseable", err.Error()
		return s, nil
	}
	s.ParseStatus, s.Document = "parsed", &doc
	return s, nil
}

// Document returns the exact retained bytes of a completed collection.
func (j *Journal) Document(id string) (Status, []byte, error) {
	s, err := j.Collection(id)
	if err != nil {
		return Status{}, nil, err
	}
	if s.State != "retained" {
		return Status{}, nil, ErrCollectionUnresolved
	}
	raw, err := j.read("document-"+id+".bin", MaxDocumentBytes)
	return s, raw, err
}

// Collect performs at most one GET per collection identity. A completed
// identity returns its retained status without fetching. An interrupted or
// failed identity is never retried; it needs a new identity.
func (j *Journal) Collect(ctx context.Context, s Spec) (Status, error) {
	if err := s.Validate(); err != nil {
		return Status{}, err
	}
	old, err := j.Collection(s.ID)
	if err == nil {
		if old.Spec != s {
			return Status{}, ErrJournalConflict
		}
		if old.State != "retained" {
			return old, ErrCollectionUnresolved
		}
		return old, nil
	}
	if !errors.Is(err, ErrCollectionNotFound) {
		return Status{}, err
	}
	in := intent{Schema: "mantik-issuer-document-intent-v1", JournalID: j.ID, Spec: s, URL: SPDRWorkbookURL, RequestedAt: j.now().UTC()}
	in.SHA256 = in.seal()
	raw, _ := json.Marshal(in)
	if err := j.writeOnce("intent-"+s.ID+".json", raw); err != nil {
		return Status{}, err
	}
	fail := func(status int, reason string) (Status, error) {
		f := failure{Schema: "mantik-issuer-document-failure-v1", IntentSHA256: in.SHA256, FailedAt: j.now().UTC(), HTTPStatus: status, Reason: reason}
		if f.FailedAt.Before(in.RequestedAt) {
			f.FailedAt = in.RequestedAt
		}
		f.SHA256 = f.seal()
		raw, _ := json.Marshal(f)
		if err := j.writeOnce("failed-"+s.ID+".json", raw); err != nil {
			return Status{}, err
		}
		st, err := j.Collection(s.ID)
		return st, errors.Join(errors.New("issuer document acquisition failed: "+reason), err)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, j.url, nil)
	if err != nil {
		return fail(0, "request_construction_failed")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return fail(0, "transport_error")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fail(response.StatusCode, fmt.Sprintf("http_status_%d", response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxDocumentBytes+1))
	if err != nil {
		return fail(response.StatusCode, "response_read_failed")
	}
	if len(body) == 0 || len(body) > MaxDocumentBytes {
		return fail(response.StatusCode, "document_size_outside_bounds")
	}
	if err := j.writeOnce("document-"+s.ID+".bin", body); err != nil {
		return Status{}, err
	}
	contentType := response.Header.Get("Content-Type")
	if len(contentType) > 256 || !utf8.ValidString(contentType) || strings.ContainsFunc(contentType, unicode.IsControl) {
		contentType = ""
	}
	done := completion{Schema: "mantik-issuer-document-completion-v1", IntentSHA256: in.SHA256, CompletedAt: j.now().UTC(), HTTPStatus: response.StatusCode, ContentType: contentType, FinalURL: in.URL, DocumentSHA256: workerplan.Digest(body), DocumentBytes: len(body)}
	if done.CompletedAt.Before(in.RequestedAt) {
		done.CompletedAt = in.RequestedAt
	}
	done.SHA256 = done.seal()
	raw, _ = json.Marshal(done)
	if err := j.writeOnce("complete-"+s.ID+".json", raw); err != nil {
		return Status{}, err
	}
	return j.Collection(s.ID)
}
