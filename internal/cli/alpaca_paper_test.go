package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

func TestPaperCLIRequiresExplicitPrivateInputs(t *testing.T) {
	for _, args := range [][]string{{"alpaca-paper"}, {"alpaca-paper", "--action", "submit"}, {"alpaca-paper", "--action", "mleg-open"}, {"alpaca-paper", "--action", "mleg-close"}, {"alpaca-paper", "--help"}} {
		var out, errs bytes.Buffer
		err := Run(context.Background(), args, &out, &errs)
		if args[len(args)-1] == "--help" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("missing inputs accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"oauth_token":"fixture"}`), 0644); err != nil {
		t.Fatal(err)
	}
	var credentials struct {
		OAuthToken string `json:"oauth_token"`
	}
	if readPaperJSON(path, &credentials, true) == nil {
		t.Fatal("public credential accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := readPaperJSON(path, &credentials, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"oauth_token":"fixture"} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if readPaperJSON(path, &credentials, true) == nil {
		t.Fatal("trailing object accepted")
	}
}

func TestMLegCLIRejectsExtraBrokerAuthorityFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.json")
	for _, extra := range []string{`"symbol":"TST"`, `"side":"sell"`, `"notional":"1"`, `"extended_hours":true`} {
		if err := os.WriteFile(path, []byte(`{"Order":{`+extra+`}}`), 0600); err != nil {
			t.Fatal(err)
		}
		var input alpaca.MLegOpen
		if readPaperJSON(path, &input, false) == nil {
			t.Fatal("unsupported wire authority accepted", extra)
		}
	}
}
