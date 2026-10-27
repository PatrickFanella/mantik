package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestServeCorporateJournalRequiresExplicitExistingOwner(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	missing := filepath.Join(t.TempDir(), "not-created")
	for _, flags := range [][]string{
		{"--corporate-source-journal", missing},
		{"--corporate-source-journal-id", "journal-only"},
		{"--corporate-source-journal", "relative", "--corporate-source-journal-id", "journal-test"},
		{"--corporate-source-journal", missing, "--corporate-source-journal-id", "journal-test"},
	} {
		args := append([]string{"serve", "--listen", "127.0.0.1:0"}, flags...)
		if err := Run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid journal configuration accepted", flags)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("reader created a journal", err)
	}
}
