package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
)

func TestModelAnalystRejectsBeforeStorageOrSubscription(t *testing.T) {
	t.Setenv("MANTIK_ANALYST_FLAG_TEST_URL", "postgres://invalid.invalid/mantik_test_flags")
	p := filepath.Join(t.TempDir(), "input.json")
	if e := os.WriteFile(p, []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{
		{"--help"},
		{"--action", "activate", "--id", "cycle", "--database-env", "MANTIK_ANALYST_FLAG_TEST_URL"},
		{"--action", "run", "--id", "cycle", "--database-env", "MANTIK_ANALYST_FLAG_TEST_URL", "--input", p, "--input-sha256", "wrong-pin"},
		{"--action", "show"},
	} {
		var out, errOut bytes.Buffer
		e := Run(context.Background(), append([]string{"model-analyst"}, args...), &out, &errOut)
		if args[0] == "--help" {
			if e != nil {
				t.Fatal(e)
			}
		} else if e == nil {
			t.Fatal("invalid action admitted", args)
		}
	}
}

func TestModelAnalystInterruptedInspectionIsExposureHold(t *testing.T) {
	var out bytes.Buffer
	e := showModelAnalyst([]research.Event{{Kind: "start"}, {Kind: "call"}}, "fixture-cycle", &out)
	var result analyst.Result
	if e != nil || json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "no_target" || !result.Target.HoldExposure || allocation.FlatTarget(result.Target) {
		t.Fatal(out.String(), e)
	}
}
