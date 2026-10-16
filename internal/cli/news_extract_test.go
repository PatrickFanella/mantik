package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestRecordedArticleExtractionCLI(t *testing.T) {
	dir := t.TempDir()
	article, model := filepath.Join(dir, "article.json"), filepath.Join(dir, "model.json")
	ctx := context.Background()
	var out, errs bytes.Buffer
	for _, tc := range []struct{ flag, path string }{{"--export-article-fixture", article}, {"--export-model-fixture", model}} {
		out.Reset()
		if err := Run(ctx, []string{"news-extract", tc.flag}, &out, &errs); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tc.path, out.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"news-extract", "--article", article, "--model-fixture", model, "--event-key", "operator-event"}
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Mode   string            `json:"mode"`
		Result extraction.Result `json:"result"`
	}
	if json.Unmarshal(out.Bytes(), &r) != nil || r.Mode != "recorded_model_fixture" || r.Result.Outcome != "extracted" || r.Result.Claims[0].EventKey != "operator-event" || r.Result.Article.Schema != news.ArticleSchema || r.Result.QualificationCredit {
		t.Fatal(out.String())
	}
	first := append([]byte(nil), out.Bytes()...)
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("recorded replay changed receipt", err)
	}
	// Malformed model JSON is a retained abstention, not an empty CLI failure.
	raw := []byte(`{"outcome":"extracted","outcome":"abstain","private":"retained only in raw capture"}`)
	if err := os.WriteFile(model, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(out.Bytes(), &r) != nil || r.Result.Outcome != "abstain" || r.Result.ReasonCode != "malformed_extractor_artifact" || len(r.Result.Claims) != 0 {
		t.Fatal(out.String())
	}
	retained, _ := base64.StdEncoding.DecodeString(r.Result.Artifact.RawBase64)
	if !bytes.Equal(retained, raw) || bytes.Contains(errs.Bytes(), []byte("retained only")) {
		t.Fatal("lost raw artifact or echoed private error")
	}
	for _, flags := range [][]string{{}, {"--article", article}, {"unexpected"}, {"--export-contract", "--article", article}, {"--export-model-fixture", "--export-article-fixture"}, {"--api-url", "http://127.0.0.1:41890"}, {"--article", article, "--model-fixture", dir, "--event-key", "event"}} {
		out.Reset()
		errs.Reset()
		if err := Run(ctx, append([]string{"news-extract"}, flags...), &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid args produced output", flags, err)
		}
	}
	for _, content := range []string{strings.Repeat("x", extraction.MaxArtifactBytes+1)} {
		if err := os.WriteFile(model, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := Run(ctx, args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("oversized input admitted as full capture")
		}
	}
	if err := os.WriteFile(article, []byte(`{"secret":"do not echo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if err := Run(ctx, args, &out, &errs); err == nil || out.Len() != 0 || strings.Contains(errs.String(), "do not echo") {
		t.Fatal("invalid source echoed or accepted")
	}
}

func TestExtractionInputRejectsFIFOBeforeBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input-pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readExtractionFile(path, extraction.MaxArtifactBytes)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted nonregular extraction input")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO open blocked before the regular-file check")
	}
}
