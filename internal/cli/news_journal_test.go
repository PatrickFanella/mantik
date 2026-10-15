package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestNewsJournalCLI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal")
	file := filepath.Join(root, "source.json")
	r := news.Fixture()
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out bytes.Buffer
	run := func(args ...string) []byte {
		t.Helper()
		out.Reset()
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(err)
		}
		return bytes.Clone(out.Bytes())
	}
	var first news.RetentionReceipt
	if err := json.Unmarshal(run("news-retain", "--journal", path, "--file", file), &first); err != nil {
		t.Fatal(err)
	}
	var second news.RetentionReceipt
	_ = json.Unmarshal(run("news-retain", "--journal", path, "--file", file), &second)
	if !second.Duplicate || first.Key != second.Key {
		t.Fatal("CLI retry changed identity")
	}
	var status news.JournalStatus
	_ = json.Unmarshal(run("news-journal", "--journal", path), &status)
	if len(status.Records) != 1 || status.JournalID != first.JournalID {
		t.Fatal("CLI journal audit mismatch")
	}
	var groups news.EventGroups
	_ = json.Unmarshal(run("news-events", "--journal", path, "--as-of", r.ReceivedAt.Format("2006-01-02T15:04:05Z07:00")), &groups)
	if len(groups.Groups) != 1 || groups.Groups[0].EventKey != r.Claims[0].EventKey {
		t.Fatal("CLI lost event group")
	}
	for _, args := range [][]string{{"news-events", "--journal", path}, {"news-events", "--journal", path, "--as-of", "bad"}} {
		out.Reset()
		if err := Run(ctx, args, &out, &out); err == nil {
			t.Fatal("missing or invalid cutoff accepted")
		}
	}
	var view map[string]json.RawMessage
	_ = json.Unmarshal(run("news-inspect", "--journal", path, "--record", first.Key, "--claim", r.Claims[0].ID), &view)
	if view["original"] != nil || view["claim"] == nil {
		t.Fatal("targeted view leaked full original or lost claim")
	}
	_ = json.Unmarshal(run("news-inspect", "--journal", path, "--record", first.Key, "--original"), &view)
	var original string
	_ = json.Unmarshal(view["original"], &original)
	if original != r.Original {
		t.Fatal("explicit original inspection changed source")
	}
	out.Reset()
	if err := Run(ctx, []string{"news-inspect", "--journal", path, "--record", first.Key, "--claim", "missing"}, &out, &out); err == nil {
		t.Fatal("missing claim accepted")
	}
	missing := filepath.Join(root, "missing")
	if err := Run(ctx, []string{"news-journal", "--journal", missing}, &out, &out); err == nil {
		t.Fatal("inspection created journal")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection mutated missing path")
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, raw) {
		t.Fatal("retention altered caller source file")
	}
}
