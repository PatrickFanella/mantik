package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaperLedgerOfflineCLI(t *testing.T) {
	ctx := context.Background()
	var exported bytes.Buffer
	if err := Run(ctx, []string{"paper-ledger-audit", "--export-fixture"}, &exported, &exported); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "recording.json")
	if err := os.WriteFile(path, exported.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(ctx, []string{"paper-ledger-audit", "--file", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var result paperledger.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.AvailableCashCents != 55000 || result.ReservedCashCents != 30000 || result.SettledDebitsCents != 15000 || result.UnresolvedReservations != 1 || result.QualificationCredit || result.ExecutionAttested || result.AllocationAvailable {
		t.Fatal(result, err)
	}
	var again bytes.Buffer
	if err := Run(ctx, []string{"paper-ledger-audit", "--file", path}, &again, &again); err != nil || !bytes.Equal(out.Bytes(), again.Bytes()) {
		t.Fatal("unstable replay", err)
	}
	before, _ := os.ReadFile(path)
	if !bytes.Equal(before, exported.Bytes()) {
		t.Fatal("rewrote input")
	}
	for _, args := range [][]string{{"paper-ledger-audit"}, {"paper-ledger-audit", "--file", path, "--export-fixture"}, {"paper-ledger-audit", "--api-url", "http://example.invalid", "--file", path}, {"paper-ledger-audit", "--export-fixture", "extra"}, {"paper-ledger-audit", "--file", path + "-missing"}} {
		var stdout, stderr bytes.Buffer
		if err := Run(ctx, args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("invalid command emitted result", args, err)
		}
	}
	r := paperledger.Fixture()
	r.Entries = append(r.Entries, paperledger.Entry{ID: "unsafe-release", Kind: "release", ReservationID: "b", AmountCents: 1, OccurredAt: r.Entries[7].OccurredAt})
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"paper-ledger-audit", "--file", path}, &out, &out); err == nil || out.Len() != 0 {
		t.Fatal("partial invalid ledger emitted")
	}
	var help bytes.Buffer
	if err := Run(ctx, []string{"paper-ledger-audit", "--help"}, &help, &help); err != nil || !strings.Contains(help.String(), "export-fixture") {
		t.Fatal(err)
	}
}
