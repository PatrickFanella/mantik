package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const rolloverPending = "rollover-pending.json"

var rolloverKeep = []string{"journal.json", "binding.json", "lock", rolloverPending}

// JournalRollover records one verified export of a journal's working files.
// Records chain by hash and stay in the journal, so a later archive or audit
// can see every earlier one.
type JournalRollover struct {
	Schema         string    `json:"schema"`
	JournalID      string    `json:"journal_id"`
	Sequence       int       `json:"sequence"`
	PreviousSHA256 string    `json:"previous_sha256"`
	ArchiveSHA256  string    `json:"archive_sha256"`
	Files          int       `json:"files"`
	At             time.Time `json:"at"`
	SHA256         string    `json:"sha256"`
}

func rolloverName(n int) string { return fmt.Sprintf("rollover-%06d.json", n) }

// Rollover exports the journal to a new directory under root, verifies the
// export, records it, and removes the exported working files, so a journal
// bound to one registration can keep collecting. It requires every input
// acknowledged. The journal keeps its identity and binding.
func (j *Journal) Rollover(ctx context.Context, root string) (JournalRollover, error) {
	if !filepath.IsAbs(root) {
		return JournalRollover{}, errors.New("rollover requires an absolute archive root")
	}
	status, err := j.Status()
	if err != nil {
		return JournalRollover{}, err
	}
	if status.Pending != 0 || status.Uncertain != 0 {
		return JournalRollover{}, errors.New("rollover requires every input acknowledged")
	}
	previous, sequence, err := j.latestRollover()
	if err != nil {
		return JournalRollover{}, err
	}
	out := JournalRollover{Schema: "mantik-alpaca-journal-rollover-v1", JournalID: j.ID, Sequence: sequence + 1, PreviousSHA256: previous, At: j.stamp()}
	receipt, err := j.Archive(ctx, filepath.Join(root, fmt.Sprintf("%s-%06d", j.ID, out.Sequence)))
	if err != nil {
		return JournalRollover{}, err
	}
	out.ArchiveSHA256, out.Files = receipt.SHA256, receipt.Files
	out.SHA256 = Digest(rolloverCanonical(out))
	raw, err := json.Marshal(out)
	if err != nil {
		return JournalRollover{}, err
	}
	if err := j.writeOnce(rolloverName(out.Sequence), raw); err != nil {
		return JournalRollover{}, err
	}
	if err := j.writeOnce(rolloverPending, raw); err != nil {
		return JournalRollover{}, err
	}
	return out, j.finishRollover()
}

func rolloverCanonical(r JournalRollover) string {
	r.SHA256 = ""
	raw, _ := json.Marshal(r)
	return string(raw)
}

// finishRollover removes exported working files once a rollover is recorded.
// It runs again on open, so a crash partway through completes on the next use.
func (j *Journal) finishRollover() error {
	if _, err := j.read(rolloverPending, 4096); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	names, err := directoryNames(j.root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if slices.Contains(rolloverKeep, name) || strings.HasPrefix(name, "rollover-") {
			continue
		}
		if err := j.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := j.syncDirectory(); err != nil {
		return err
	}
	if err := j.root.Remove(rolloverPending); err != nil {
		return err
	}
	return j.syncDirectory()
}

// latestRollover verifies the record chain and returns its last hash and
// sequence, or an empty hash and zero before the first rollover.
func (j *Journal) latestRollover() (string, int, error) {
	names, err := directoryNames(j.root)
	if err != nil {
		return "", 0, err
	}
	previous, sequence := "", 0
	for _, name := range names {
		if !strings.HasPrefix(name, "rollover-") || name == rolloverPending {
			continue
		}
		raw, err := j.read(name, 4096)
		if err != nil {
			return "", 0, err
		}
		var r JournalRollover
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", 0, ErrJournalConflict
		}
		sequence++
		if name != rolloverName(sequence) || r.Sequence != sequence || r.JournalID != j.ID || r.PreviousSHA256 != previous || r.SHA256 != Digest(rolloverCanonical(r)) {
			return "", 0, ErrJournalConflict
		}
		previous = r.SHA256
	}
	return previous, sequence, nil
}
