package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
)

func TestHypothesisDeclarationCLI(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	if err := Run(ctx, []string{"hypothesis-declare", "--export-fixture"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var declaration hypothesis.Declaration
	if err := json.Unmarshal(out.Bytes(), &declaration); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Clone(out.Bytes())
	file := filepath.Join(t.TempDir(), "declaration.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"hypothesis-declare", "--file", file}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var result hypothesis.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	expected, err := hypothesis.Seal(declaration)
	if err != nil || result.ID != expected.ID || result.DeclarationSHA256 != expected.DeclarationSHA256 || result.OriginsVerified || result.QualificationCredit {
		t.Fatal(result, err)
	}
	if actual, err := os.ReadFile(file); err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("declaration file changed")
	}
	out.Reset()
	if err := Run(ctx, []string{"hypothesis-declare", "--export-method", "demo-half-size-v1"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var method struct {
		Pin hypothesis.MethodRef `json:"pin"`
	}
	_ = json.Unmarshal(out.Bytes(), &method)
	if method.Pin.ID != "demo-half-size-v1" || method.Pin.Schema != hypothesis.MethodSchema {
		t.Fatal(method)
	}
	for _, args := range [][]string{{"hypothesis-declare"}, {"hypothesis-declare", "--export-method", "unknown"}, {"hypothesis-declare", "--export-method", "demo-half-size-v1", "--file", file}, {"hypothesis-declare", "--export-fixture", "--file", file}, {"hypothesis-declare", "--file", file, "extra"}} {
		out.Reset()
		if err := Run(ctx, args, &out, &out); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	if err := os.WriteFile(file, []byte(`{"schema":"mantik-hypothesis-declaration-v1","qualified":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"hypothesis-declare", "--file", file}, &out, &out); err == nil {
		t.Fatal("promoted incomplete declaration accepted")
	}
}
