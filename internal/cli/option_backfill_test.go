package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestOptionsBackfillValidatesBeforeCredentialsOrDatabase(t *testing.T) {
	for _, command := range []string{"options-backfill", "stock-assets", "stock-universe"} {
		var out bytes.Buffer
		if err := Run(context.Background(), []string{command, "--help"}, &out, &out); err != nil {
			t.Fatal(err)
		}
		if err := Run(context.Background(), []string{command}, &out, &out); err == nil {
			t.Fatal("missing configuration accepted", command)
		}
	}
	var out bytes.Buffer
	err := Run(context.Background(), []string{"options-backfill", "--database-url", "invalid", "--journal", "/tmp/unused", "--credentials-file", "/tmp/unused", "--underlyings", "SPY", "--start", "2023-01-01", "--end", "2024-02-03", "--expiry-end", "2024-02-02"}, &out, &out)
	if err == nil || err.Error() != "options require 1–10 underlyings, start since February 2024, fixed end at least 15 minutes old" {
		t.Fatal("invalid range reached credential/store access", err)
	}
}
