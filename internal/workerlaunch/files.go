package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func writeOnce(root, name string, raw []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(root, ".pending-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	_, err = f.Write(raw)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = os.Link(f.Name(), filepath.Join(root, name)); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, err := readFile(root, name, int64(len(raw)))
		if err != nil || !bytes.Equal(existing, raw) {
			return ErrConflict
		}
	}
	return syncDir(root)
}
func writeJSON(root, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeOnce(root, name, raw, 0600)
}
func readFile(root, name string, limit int64) ([]byte, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrConflict
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	err = errors.Join(err, f.Close())
	if int64(len(raw)) > limit {
		return nil, ErrConflict
	}
	return raw, err
}
func directory(root string, create bool) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil || strings.ContainsAny(abs, ",\n\r") || root == "" {
		return "", ErrConflict
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	if create {
		if err = os.Mkdir(abs, 0700); err != nil {
			return "", ErrConflict
		}
		if err = syncDir(parent); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return "", ErrConflict
	}
	return abs, nil
}
func lock(ctx context.Context, root string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrBusy
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
