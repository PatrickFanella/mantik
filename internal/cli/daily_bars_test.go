package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestServeDailyBarsRequiresCompleteConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "must-not-open")
	for _, args := range [][]string{
		{"serve", "--daily-bars-start", "2026-01-01"},
		{"serve", "--daily-bars-universe", "missing.json"},
		{"serve", "--daily-bars-start", "bad", "--daily-bars-universe", "missing.json", "--daily-bars-credentials-file", "missing.json", "--daily-bars-journal", "/tmp/unused"},
	} {
		if err := Run(context.Background(), args, new(bytes.Buffer), new(bytes.Buffer)); err == nil {
			t.Fatal("accepted partial or invalid daily collector config", args)
		}
	}
}
