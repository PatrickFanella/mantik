// Package privatejournal provides the private write-once directory used by
// local acquisition journals: an absolute current-user directory with mode
// 0700, regular 0600 leaves, hard-link publication and one exclusive owner.
package privatejournal

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var ErrBusy = errors.New("private journal is already owned by another process")
var ErrConflict = errors.New("private journal identity conflicts with retained content")
var idSuffix = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Journal struct {
	root *os.Root
	lock *os.File
	ID   string
}

// Open validates or creates the directory and its manifest. The manifest's
// schema and ID prefix name the journal kind; another kind's journal fails.
func Open(dir string, create bool, schema, idPrefix string) (*Journal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("journal requires an absolute private directory")
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
		return nil, errors.New("journal directory must be owned by the current user with mode 0700")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
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
		return nil, ErrBusy
	}
	j := &Journal{root: root, lock: lock}
	var m struct {
		Schema string `json:"schema"`
		ID     string `json:"id"`
	}
	raw, err := j.Read("journal.json", 4096)
	if errors.Is(err, os.ErrNotExist) && create {
		m.Schema, m.ID = schema, idPrefix+rand.Text()
		raw, _ = json.Marshal(m)
		err = j.WriteOnce("journal.json", raw, 4096)
	} else if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil || m.Schema != schema || !strings.HasPrefix(m.ID, idPrefix) || !idSuffix.MatchString(strings.TrimPrefix(m.ID, idPrefix)) {
		_ = j.Close()
		return nil, errors.New("invalid or unavailable journal manifest")
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

// Read inspects the leaf, then verifies the opened inode matches it, so a link
// swapped in after the check cannot be read.
func (j *Journal) Read(name string, max int64) ([]byte, error) {
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

// WriteOnce publishes by hard link after fsync. An existing leaf is never
// overwritten; identical content is an idempotent retry.
func (j *Journal) WriteOnce(name string, raw []byte, max int64) error {
	if int64(len(raw)) > max {
		return errors.New("journal artifact exceeds bound")
	}
	if old, err := j.Read(name, max); err == nil {
		if string(old) != string(raw) {
			return ErrConflict
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
