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
	"time"
)

func TestCoverageCLIUsesActualTimeAndDoesNotReadCredentialsOrRecreateJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, err := alpaca.OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	date := now.In(location).Format("2006-01-02")
	record := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "manufactured-calendar", Capability: "market_calendar", Feed: "regular_session", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: now, Start: date, End: date, EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-access", RawRef: "artifact:manufactured-calendar", Original: "[]", SHA256: alpaca.Digest("[]"), MaxQuoteAgeSeconds: 60}
	if _, err := j.Retain(record); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	var out, errOut bytes.Buffer
	args := []string{"alpaca-coverage", "--journal", root, "--feed", "iex", "--symbol", "SPY", "--evidence-class", "synthetic_unqualified"}
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var report alpaca.CoverageReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Session.Code != "calendar_no_session" || report.Session.Date != date || report.At.Before(now) || report.At.After(time.Now().UTC()) || report.GlobalReadinessConfigured || report.CollectorLivenessVerified || report.QualificationCredit {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-coverage", "--journal", root, "--feed", "sip", "--symbol", "SPY"}, &out, &errOut); err != nil || !strings.Contains(out.String(), "calendar_capture_missing") {
		t.Fatal("synthetic capture promoted to prospective", out.String(), err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := Run(context.Background(), []string{"alpaca-coverage", "--journal", missing, "--feed", "iex", "--symbol", "SPY"}, &out, &errOut); err == nil {
		t.Fatal("missing journal opened")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("coverage recreated journal")
	}
	out.Reset()
	errOut.Reset()
	if err := Run(context.Background(), []string{"alpaca-coverage", "--help"}, &out, &errOut); err != nil || !strings.Contains(errOut.String(), "max-calendar-age") {
		t.Fatal("coverage help unavailable", err)
	}
	if err := Run(context.Background(), append(args, "--at", "2026-10-14T15:00:00Z"), &out, &errOut); err == nil {
		t.Fatal("CLI allowed simulated current time")
	}
}
