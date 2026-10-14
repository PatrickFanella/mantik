package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

func TestAlpacaNormalizeCLI(t *testing.T) {
	raw := `{"quotes":{"SPY":{"t":"2026-10-14T15:00:00Z","ap":100.01,"bp":100.00,"as":10,"bs":12}}}`
	record := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "synthetic-cli", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: time.Date(2026, 10, 14, 15, 0, 1, 0, time.UTC), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-fixture", RawRef: "artifact:manufactured-quote", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	path := filepath.Join(t.TempDir(), "recording.json")
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	out := new(bytes.Buffer)
	if err := Run(context.Background(), []string{"alpaca-normalize", "--recording", path, "--sha256", record.SHA256}, out, new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
	var result alpaca.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Inputs) != 1 || result.Inputs[0].PriceCents != 10001 {
		t.Fatal("CLI normalization lost contract")
	}
	for _, args := range [][]string{{"alpaca-normalize"}, {"alpaca-normalize", "--recording", path, "--sha256", "wrong"}, {"alpaca-normalize", "--recording", path, "--sha256", record.SHA256, "extra"}} {
		if err := Run(context.Background(), args, new(bytes.Buffer), new(bytes.Buffer)); err == nil {
			t.Fatal("invalid recording CLI arguments admitted")
		}
	}
}
