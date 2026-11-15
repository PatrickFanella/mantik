package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBarsBackfillValidatesBeforeOpeningStore(t *testing.T) {
	t.Setenv("DATABASE_URL", "must-not-use-ambient-database")
	assets := filepath.Join(t.TempDir(), "assets.json")
	if err := os.WriteFile(assets, []byte(`[{"symbol":"SPY","asset_id":"asset-spy"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"bars-backfill"},
		{"bars-backfill", "--help"},
		{"bars-backfill", "--database-url", "invalid", "--journal", "/tmp/unused", "--credentials-file", "/tmp/unused", "--assets", assets, "--end", "2015-01-01"},
	} {
		var out bytes.Buffer
		err := Run(context.Background(), args, &out, &out)
		if len(args) > 1 && args[1] == "--help" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatal("invalid backfill was accepted", args)
		}
	}
}
