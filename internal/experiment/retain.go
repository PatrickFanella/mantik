package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

// RunAndRetain writes the immutable declaration before execution, then a
// terminal attempt. No timestamp/current/best pointer makes identical retries
// diverge. A process crash can leave a recipe without a terminal attempt.
func RunAndRetain(ctx context.Context, root, id string, r Recipe, d replay.Dataset) (Attempt, error) {
	if err := r.Validate(); err != nil {
		return Attempt{}, err
	}
	if !identifier.MatchString(id) {
		return Attempt{}, errors.New("invalid attempt id")
	}
	if err := writeOnce(root, "recipes", r.SHA256, r); err != nil {
		return Attempt{}, err
	}
	inputHash, err := digest(d)
	if err != nil {
		return Attempt{}, err
	}
	if err := writeOnce(root, "inputs", inputHash, d); err != nil {
		return Attempt{}, err
	}
	a, err := Execute(ctx, id, r, d)
	if err != nil {
		return Attempt{}, err
	}
	if err := writeOnce(root, "attempts", id, a); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

func writeOnce(root, group, name string, value any) error {
	if root == "" {
		return errors.New("artifact directory is required")
	}
	for _, directory := range []string{root, filepath.Join(root, group)} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact directory must be a real directory")
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	directory := filepath.Join(root, group)
	temporary, err := os.CreateTemp(directory, ".pending-")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err = temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	target := filepath.Join(directory, name+".json")
	if err = os.Link(tempPath, target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("retained artifact must be a regular file")
		}
		existing, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, raw) {
			return errors.New("retained artifact identity conflicts; choose a new attempt id")
		}
	}
	// Persist directory entries as well as file bytes before reporting retention.
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func ReadRecipe(input io.Reader) (Recipe, error) { return experimentplan.ReadRecipe(input) }
