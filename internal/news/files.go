package news

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"syscall"
)

const maxNewsFile = 4 << 20

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
// partial writes remain unreferenced temporary evidence, not published news records.
func (j *Journal) writeOnce(name string, raw []byte) error {
	if old, err := j.read(name, maxNewsFile); err == nil {
		if string(old) != string(raw) {
			return ErrNewsConflict
		}
		return j.syncDirectory()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(raw) > maxNewsFile {
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
