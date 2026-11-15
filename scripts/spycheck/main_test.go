package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

func TestAuctionCommandCannotPassWithoutEvidenceAndManifest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
		args  []string
		want  string
	}{
		{name: "missing_manifest", args: []string{"-close-basis", "primary-close"}, want: "requires manifest"},
		{name: "missing_closes", clear: true, args: []string{"-close-basis", "primary-close", "-manifest", "unused"}, want: "complete auction evidence"},
		{name: "implicit_basis", want: "requires explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := closeBasisInput()
			// Make raw comparison pass so only the auction requirement can stop this.
			in.Reference = []marketdata.ReferenceClose{{Date: in.Calendar[0], Close: in.Raw[0].Close}}
			in.ReferencePayDateReturn = "1"
			if tc.clear {
				in.SessionCloses = nil
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "input.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			previousFlags, previousArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine = previousFlags; os.Args = previousArgs }()
			flag.CommandLine = flag.NewFlagSet("spycheck", flag.ContinueOnError)
			os.Args = append([]string{"spycheck", "-input", path}, tc.args...)
			if err = run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("auction gate bypassed", err)
			}
		})
	}
}
