package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveCLIPrivateRetainVerifyRestoreAndStatus(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "journal")
	archive := filepath.Join(base, "archive")
	destination := filepath.Join(base, "copy")
	j, err := alpaca.OpenJournal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"alpaca-archive", "--journal", source, "--destination", archive}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var receipt alpaca.ArchiveReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.SHA256 == "" || receipt.Files != 1 {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-archive-verify", "--archive", archive, "--sha256", receipt.SHA256}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-restore", "--archive", archive, "--sha256", receipt.SHA256, "--destination", destination}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"contracts_verified": true`) || !strings.Contains(out.String(), `"bytes_verified": true`) {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-journal", "--journal", destination}, &out, &errOut); err != nil || !strings.Contains(out.String(), receipt.JournalID) {
		t.Fatal("restored journal identity changed", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-restore", "--archive", archive, "--sha256", receipt.SHA256, "--destination", destination}, &out, &errOut); err == nil {
		t.Fatal("existing restore overwritten")
	}
}
func TestArchiveCLIRequiresExplicitIdentityAndDoesNotRecreateSource(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	for _, args := range [][]string{{"alpaca-archive", "--journal", missing, "--destination", filepath.Join(base, "archive")}, {"alpaca-archive-verify", "--archive", missing}, {"alpaca-restore", "--archive", missing, "--sha256", strings.Repeat("0", 64)}, {"alpaca-archive", "extra"}} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), args, &out, &errOut); err == nil {
			t.Fatal("invalid archive command admitted", args)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing source recreated")
	}
	for _, command := range []string{"alpaca-archive", "alpaca-archive-verify", "alpaca-restore"} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), []string{command, "--help"}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
	}
}
