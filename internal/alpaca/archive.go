package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const MaxArchiveBytes int64 = 512 << 20

var archiveHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var archiveLeaf = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var archiveCollection = regexp.MustCompile(`^collection-([A-Za-z0-9_-]{1,40})-(?:intent|complete|start-[123]|response-[123])\.json$`)
var archiveSession = regexp.MustCompile(`^session-([A-Za-z0-9_-]{1,32})-(?:intent|complete)\.json$`)
var ErrArchiveIntegrity = errors.New("archive content or expected identity does not match")
var ErrRestoreContract = errors.New("restored bytes preserved but journal contracts failed; inspect isolated destination")

type ArchiveFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type ArchiveManifest struct {
	Schema     string        `json:"schema"`
	JournalID  string        `json:"journal_id"`
	At         time.Time     `json:"at"`
	TotalBytes int64         `json:"total_bytes"`
	Files      []ArchiveFile `json:"files"`
}
type ArchiveReceipt struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	JournalID string `json:"journal_id"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
}
type JournalAudit struct {
	JournalStatus
	Collections           int `json:"collections"`
	FailedCollections     int `json:"failed_collections"`
	IncompleteCollections int `json:"incomplete_collections"`
	Sessions              int `json:"sessions"`
	StoppedSessions       int `json:"stopped_sessions"`
	IncompleteSessions    int `json:"incomplete_sessions"`
	TemporaryFiles        int `json:"temporary_files"`
}
type RestoreReceipt struct {
	Archive           ArchiveReceipt `json:"archive"`
	Destination       string         `json:"destination"`
	BytesVerified     bool           `json:"bytes_verified"`
	ContractsVerified bool           `json:"contracts_verified"`
	Audit             *JournalAudit  `json:"audit,omitempty"`
}

func privateRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private artifact directory requires an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("artifact directory requires current-user ownership and mode 0700")
	}
	return os.OpenRoot(path)
}
func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	// Journal's reader already enforces regular files, owner, 0600, no leaf
	// symlink, and bounded reads. Reuse exactly that physical contract.
	return (&Journal{root: root}).read(name, limit)
}
func directoryNames(root *os.Root) ([]string, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := file.ReadDir(20001)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > 20000 {
		return nil, errors.New("archive artifact count exceeds bound")
	}
	names := []string{}
	for _, entry := range entries {
		if !archiveLeaf.MatchString(entry.Name()) {
			return nil, errors.New("archive requires bounded ASCII leaf names")
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}
func syncRoot(root *os.Root) error { return (&Journal{root: root}).syncDirectory() }
func publishPrivate(root *os.Root, name string, raw []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	n, writeErr := file.Write(raw)
	if writeErr == nil && n != len(raw) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
func syncParent(path string) error {
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
func disjointDestination(source, destination string) error {
	if !filepath.IsAbs(destination) {
		return errors.New("destination requires a new absolute directory")
	}
	actualSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	actualDestination := filepath.Join(parent, filepath.Base(destination))
	relative, err := filepath.Rel(actualSource, actualDestination)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))) {
		return errors.New("destination must be outside the source directory")
	}
	return nil
}

// Archive owns the source journal lock throughout the coherent copy. Every
// regular private file is retained, including orphan/partial evidence. The
// process lock is the sole exclusion. Completion manifest is published last.
func (j *Journal) Archive(ctx context.Context, destination string) (ArchiveReceipt, error) {
	out := ArchiveReceipt{Path: destination, JournalID: j.ID}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := disjointDestination(j.root.Name(), destination); err != nil {
		return out, err
	}
	names, err := directoryNames(j.root)
	if err != nil {
		return out, err
	}
	// Preflight all bytes and privacy before creating any destination.
	total := int64(0)
	for _, name := range names {
		if name == "lock" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		raw, err := j.read(name, maxJournalFile)
		if err != nil {
			return out, err
		}
		total += int64(len(raw))
		if total > MaxArchiveBytes {
			return out, errors.New("archive exceeds 512 MiB retention bound")
		}
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return out, err
	}
	root, err := privateRoot(destination)
	if err != nil {
		return out, err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("files", 0700); err != nil {
		return out, err
	}
	files, err := root.OpenRoot("files")
	if err != nil {
		return out, err
	}
	defer func() { _ = files.Close() }()
	manifest := ArchiveManifest{Schema: "mantik-alpaca-archive-v1", JournalID: j.ID, At: time.Now().UTC(), Files: []ArchiveFile{}}
	for _, name := range names {
		if name == "lock" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		raw, err := j.read(name, maxJournalFile)
		if err != nil {
			return out, err
		}
		manifest.TotalBytes += int64(len(raw))
		if manifest.TotalBytes > MaxArchiveBytes {
			return out, errors.New("archive size changed beyond bound")
		}
		if err := publishPrivate(files, name, raw); err != nil {
			return out, err
		}
		manifest.Files = append(manifest.Files, ArchiveFile{name, int64(len(raw)), Digest(string(raw))})
	}
	if err := syncRoot(files); err != nil {
		return out, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := publishPrivate(root, "archive.json", raw); err != nil {
		return out, err
	}
	if err := syncRoot(root); err != nil {
		return out, err
	}
	if err := syncParent(destination); err != nil {
		return out, err
	}
	out.SHA256, out.Files, out.Bytes = Digest(string(raw)), len(manifest.Files), manifest.TotalBytes
	// Read back the completed archive against its separately returned identity.
	return VerifyArchive(ctx, destination, out.SHA256)
}
func openArchive(path, expected string) (*os.Root, *os.Root, ArchiveManifest, error) {
	var manifest ArchiveManifest
	if !archiveHash.MatchString(expected) {
		return nil, nil, manifest, errors.New("archive requires an explicit SHA-256 identity")
	}
	root, err := privateRoot(path)
	if err != nil {
		return nil, nil, manifest, err
	}
	fail := func(err error) (*os.Root, *os.Root, ArchiveManifest, error) {
		_ = root.Close()
		return nil, nil, manifest, err
	}
	names, err := directoryNames(root)
	if err != nil || !slices.Equal(names, []string{"archive.json", "files"}) {
		return fail(ErrArchiveIntegrity)
	}
	raw, err := readPrivate(root, "archive.json", maxJournalFile)
	if err != nil {
		return fail(err)
	}
	if Digest(string(raw)) != expected {
		return fail(ErrArchiveIntegrity)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fail(ErrArchiveIntegrity)
	}
	if manifest.Schema != "mantik-alpaca-archive-v1" || manifest.JournalID == "" || !validTime(manifest.At) || len(manifest.Files) < 1 || len(manifest.Files) > 20000 || manifest.TotalBytes < 0 || manifest.TotalBytes > MaxArchiveBytes {
		return fail(ErrArchiveIntegrity)
	}
	total := int64(0)
	previous := ""
	hasManifest := false
	expectedNames := []string{}
	for _, entry := range manifest.Files {
		if !archiveLeaf.MatchString(entry.Name) || (entry.Name == "lock" || entry.Name == "restore-pending") || entry.Name <= previous || entry.Bytes < 0 || entry.Bytes > maxJournalFile || !archiveHash.MatchString(entry.SHA256) {
			return fail(ErrArchiveIntegrity)
		}
		previous = entry.Name
		total += entry.Bytes
		expectedNames = append(expectedNames, entry.Name)
		if entry.Name == "journal.json" {
			hasManifest = true
		}
	}
	if total != manifest.TotalBytes || !hasManifest {
		return fail(ErrArchiveIntegrity)
	}
	// Reject symlinked files directory and preserve 0700/current-user contract.
	info, err := root.Lstat("files")
	if err != nil {
		return fail(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fail(ErrArchiveIntegrity)
	}
	files, err := root.OpenRoot("files")
	if err != nil {
		return fail(err)
	}
	actualNames, err := directoryNames(files)
	if err != nil || !slices.Equal(actualNames, expectedNames) {
		_ = files.Close()
		return fail(ErrArchiveIntegrity)
	}
	return root, files, manifest, nil
}
func verifiedArchiveBytes(root *os.Root, entry ArchiveFile) ([]byte, error) {
	raw, err := readPrivate(root, entry.Name, maxJournalFile)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != entry.Bytes || Digest(string(raw)) != entry.SHA256 {
		return nil, ErrArchiveIntegrity
	}
	return raw, nil
}
func VerifyArchive(ctx context.Context, path, expected string) (ArchiveReceipt, error) {
	out := ArchiveReceipt{Path: path, SHA256: expected}
	root, files, manifest, err := openArchive(path, expected)
	if err != nil {
		return out, err
	}
	defer func() { _ = files.Close(); _ = root.Close() }()
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if _, err := verifiedArchiveBytes(files, entry); err != nil {
			return out, err
		}
	}
	out.JournalID, out.Files, out.Bytes = manifest.JournalID, len(manifest.Files), manifest.TotalBytes
	return out, nil
}

// Audit checks active contracts while counting expected failed/incomplete
// histories separately. Unknown and temporary files are retained, not executed.
func (j *Journal) Audit() (JournalAudit, error) {
	var out JournalAudit
	status, err := j.Status()
	if err != nil {
		return out, err
	}
	out.JournalStatus = status
	names, err := directoryNames(j.root)
	if err != nil {
		return out, err
	}
	captures, err := j.captures()
	if err != nil {
		return out, err
	}
	validDelivery := map[string]bool{}
	for _, capture := range captures {
		for _, input := range capture.Result.Inputs {
			validDelivery[ackName(input)] = true
			for n := 1; n <= 3; n++ {
				validDelivery[attemptName(input, n)] = true
			}
		}
	}
	collections, sessions := map[string]bool{}, map[string]bool{}
	for _, name := range names {
		if strings.HasPrefix(name, "tmp-") {
			out.TemporaryFiles++
			continue
		}
		if strings.HasPrefix(name, "ack-") || strings.HasPrefix(name, "attempt-") {
			if !validDelivery[name] {
				return out, ErrJournalConflict
			}
		}
		if strings.HasPrefix(name, "collection-") {
			match := archiveCollection.FindStringSubmatch(name)
			if match == nil {
				return out, ErrJournalConflict
			}
			collections[match[1]] = true
		}
		if strings.HasPrefix(name, "session-") {
			match := archiveSession.FindStringSubmatch(name)
			if match == nil {
				return out, ErrJournalConflict
			}
			sessions[match[1]] = true
		}
	}
	for id := range collections {
		status, err := j.Collection(id)
		if err != nil {
			return out, err
		}
		out.Collections++
		if status.State == "failed" {
			out.FailedCollections++
		}
		if status.State == "incomplete" {
			out.IncompleteCollections++
		}
	}
	for id := range sessions {
		status, err := j.Session(id)
		if err != nil {
			return out, err
		}
		out.Sessions++
		if status.State == "stopped" {
			out.StoppedSessions++
		}
		if status.State == "incomplete" {
			out.IncompleteSessions++
		}
	}
	return out, nil
}

// Restore copies only into a newly created isolated directory. It preserves the
// logical journal and receiver identity; target binding is never rewritten.
// A contract failure retains the copied destination for inspection, not use.
func RestoreArchive(ctx context.Context, path, expected, destination string) (RestoreReceipt, error) {
	out := RestoreReceipt{Destination: destination}
	receipt, err := VerifyArchive(ctx, path, expected)
	if err != nil {
		return out, err
	}
	out.Archive = receipt
	if err := disjointDestination(path, destination); err != nil {
		return out, err
	}
	root, files, archiveManifest, err := openArchive(path, expected)
	if err != nil {
		return out, err
	}
	defer func() { _ = files.Close(); _ = root.Close() }()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return out, err
	}
	target, err := privateRoot(destination)
	if err != nil {
		return out, err
	}
	defer func() { _ = target.Close() }()
	if err := publishPrivate(target, "restore-pending", []byte("isolated restore incomplete\n")); err != nil {
		return out, err
	}
	if err := syncRoot(target); err != nil {
		return out, err
	}
	if err := syncParent(destination); err != nil {
		return out, err
	}
	for _, entry := range archiveManifest.Files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		raw, err := verifiedArchiveBytes(files, entry)
		if err != nil {
			return out, err
		}
		if err := publishPrivate(target, entry.Name, raw); err != nil {
			return out, err
		}
		copied, err := readPrivate(target, entry.Name, maxJournalFile)
		if err != nil || Digest(string(copied)) != entry.SHA256 {
			return out, ErrArchiveIntegrity
		}
	}
	if err := syncRoot(target); err != nil {
		return out, err
	}
	if err := syncParent(destination); err != nil {
		return out, err
	}
	out.BytesVerified = true
	raw, err := readPrivate(target, "journal.json", 4096)
	if err != nil {
		return out, ErrRestoreContract
	}
	var metadata manifest
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.Schema != "mantik-alpaca-journal-v1" || metadata.ID != archiveManifest.JournalID || !strings.HasPrefix(metadata.ID, "journal-") || !capturePattern.MatchString(metadata.ID) {
		return out, ErrRestoreContract
	}
	// The pending guard blocks every public journal opener. Audit the owned,
	// isolated directory directly before making it eligible to open.
	restored := &Journal{root: target, ID: metadata.ID}
	audit, err := restored.Audit()
	if err != nil {
		return out, ErrRestoreContract
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := target.Remove("restore-pending"); err != nil {
		return out, err
	}
	if err := syncRoot(target); err != nil {
		return out, err
	}
	out.ContractsVerified = true
	out.Audit = &audit
	return out, nil
}
