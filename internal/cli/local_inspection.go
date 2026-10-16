package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func privateLocalFile(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Getuid() && info.Mode().Perm()&0077 == 0
}

func readLocalReceipt(root *os.Root, name string) (*extraction.LocalReceipt, error) {
	info, err := root.Lstat(name)
	if err != nil {
		if name == "finished.json" && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, extraction.ErrLocalEvidence
	}
	if !info.Mode().IsRegular() || !privateLocalFile(info) || info.Size() > extraction.MaxLocalReceiptBytes {
		return nil, extraction.ErrLocalEvidence
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, extraction.ErrLocalEvidence
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || !privateLocalFile(opened) {
		_ = f.Close()
		return nil, extraction.ErrLocalEvidence
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, extraction.MaxLocalReceiptBytes+1))
	if errors.Join(readErr, f.Close()) != nil || len(raw) > extraction.MaxLocalReceiptBytes {
		return nil, extraction.ErrLocalEvidence
	}
	var receipt extraction.LocalReceipt
	if workerplan.ReadBoundedShape(raw, &receipt, extraction.MaxLocalReceiptBytes) != nil {
		return nil, extraction.ErrLocalEvidence
	}
	return &receipt, nil
}

func runLocalInspection(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-local-inspect", flag.ContinueOnError)
	fs.SetOutput(errOut)
	dir := fs.String("dir", "", "private retained local extraction directory; offline read only")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fs.NArg() != 0 || !filepath.IsAbs(*dir) || filepath.Clean(*dir) != *dir {
		return errors.New("news-local-inspect requires an absolute private directory")
	}
	prepared, finished, err := loadLocalReceipts(*dir)
	if err != nil {
		return err
	}
	report, err := extraction.InspectLocal(ctx, prepared, finished)
	if err != nil {
		return err
	}
	return emit(out, report)
}

// loadLocalReceipts shares private, confined file validation with offline inspection.
func loadLocalReceipts(dir string) (extraction.LocalReceipt, *extraction.LocalReceipt, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return extraction.LocalReceipt{}, nil, extraction.ErrLocalEvidence
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !privateLocalFile(info) {
		return extraction.LocalReceipt{}, nil, extraction.ErrLocalEvidence
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return extraction.LocalReceipt{}, nil, extraction.ErrLocalEvidence
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) || !opened.IsDir() || !privateLocalFile(opened) {
		return extraction.LocalReceipt{}, nil, extraction.ErrLocalEvidence
	}
	prepared, err := readLocalReceipt(root, "prepared.json")
	if err != nil {
		return extraction.LocalReceipt{}, nil, err
	}
	finished, err := readLocalReceipt(root, "finished.json")
	if err != nil {
		return extraction.LocalReceipt{}, nil, err
	}
	return *prepared, finished, nil
}
