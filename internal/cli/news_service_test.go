package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestNewsServiceCLI(t *testing.T) {
	ctx := context.Background()
	backend := storage.NewMemory()
	server := httptest.NewServer(service.Handler(backend, "cli-test-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "cli-test-token")
	path := filepath.Join(t.TempDir(), "journal")
	j, err := news.OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := j.Retain(news.Fixture())
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, err := backend.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run := func(args ...string) []byte {
		t.Helper()
		out.Reset()
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(err)
		}
		return bytes.Clone(out.Bytes())
	}
	var receipt newscase.Receipt
	if err = json.Unmarshal(run("news-submit", "--journal", path, "--record", local.Key, "--receiver-id", catalog.ReceiverID), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.RecordKey != local.Key || receipt.RecordingSHA256 != local.RecordingSHA256 {
		t.Fatal("CLI receipt mismatch")
	}
	_ = json.Unmarshal(run("news-submit", "--journal", path, "--record", local.Key, "--receiver-id", catalog.ReceiverID), &receipt)
	if !receipt.Duplicate {
		t.Fatal("retry not duplicate")
	}
	var view map[string]json.RawMessage
	_ = json.Unmarshal(run("news-record", "--record", local.Key), &view)
	if view["original"] != nil || view["result"] == nil {
		t.Fatal("default record inspection mismatch")
	}
	_ = json.Unmarshal(run("news-record", "--record", local.Key, "--original"), &view)
	var original string
	_ = json.Unmarshal(view["original"], &original)
	if original != news.Fixture().Original {
		t.Fatal("original not preserved")
	}
	_ = json.Unmarshal(run("news-cases"), &catalog)
	if len(catalog.Cases) != 1 {
		t.Fatal("missing case")
	}
	c := catalog.Cases[0]
	var closed newscase.Case
	_ = json.Unmarshal(run("news-close", "--id", c.ID, "--version", strconv.Itoa(c.Version), "--receiver-id", catalog.ReceiverID), &closed)
	if closed.Status != "closed" {
		t.Fatal("case not closed")
	}
	for _, args := range [][]string{{"news-submit", "--journal", path, "--record", local.Key}, {"news-record", "--record", "../bad"}, {"news-close", "--id", c.ID, "--receiver-id", catalog.ReceiverID}} {
		out.Reset()
		if err = Run(ctx, args, &out, &out); err == nil {
			t.Fatal("incomplete arguments accepted", args)
		}
	}
}
