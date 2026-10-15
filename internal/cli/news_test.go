package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestRecordedNewsCLI(t *testing.T) {
	var out bytes.Buffer
	ctx := context.Background()
	if err := Run(ctx, []string{"news-normalize", "--export-fixture"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	r, err := news.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"news-normalize", "--file", p}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var actual news.Result
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	want, _ := news.Normalize(r)
	if !reflect.DeepEqual(actual, want) {
		t.Fatal("CLI altered source bindings")
	}
	for _, args := range [][]string{{}, {"--file", p, "--export-fixture"}, {"unexpected"}, {"--file", p + "-missing"}} {
		if err := Run(ctx, append([]string{"news-normalize"}, args...), &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := os.WriteFile(p, []byte(`{"secret_source":"do not echo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"news-normalize", "--file", p}, &out, &out); err == nil || bytes.Contains(out.Bytes(), []byte("do not echo")) {
		t.Fatal("malformed source echoed or accepted")
	}
}
