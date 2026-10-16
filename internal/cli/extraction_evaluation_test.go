package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
)

func TestExtractionEvaluationCLI(t *testing.T) {
	ctx := context.Background()
	var out, errs bytes.Buffer
	if err := Run(ctx, []string{"news-extraction-evaluate", "--export-fixture"}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"news-extraction-evaluate", "--corpus", path}
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var r extraction.Evaluation
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Counts.Cases != 2 || r.Counts.ExactCases != 1 || r.Counts.MatchedClaims != 1 || r.Counts.MissingClaims != 1 || r.Counts.UnexpectedClaims != 1 || r.QualificationCredit {
		t.Fatal("wrong evaluation", err, out.String())
	}
	// Mismatches are successful report production, not a reason to discard cases.
	first := append([]byte(nil), out.Bytes()...)
	out.Reset()
	if err := Run(ctx, args, &out, &errs); err != nil || !bytes.Equal(first, out.Bytes()) {
		t.Fatal("unstable report", err)
	}
	for _, flags := range [][]string{{}, {"--export-fixture", "--corpus", path}, {"--corpus", path, "extra"}, {"--api-url", "http://127.0.0.1:41890"}, {"--corpus", filepath.Dir(path)}, {"--corpus", path, "--only-passed"}} {
		out.Reset()
		errs.Reset()
		if err := Run(ctx, append([]string{"news-extraction-evaluate"}, flags...), &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid flags emitted report", flags, err)
		}
	}
	fixture, _ := json.Marshal(extraction.FixtureCorpus())
	for _, raw := range [][]byte{[]byte(`{"schema":"private-input-do-not-echo","cases":[]}`), []byte(`{"schema":"x","schema":"y"}`), []byte(`{"Schema":"x"}`), append(append([]byte(nil), fixture...), []byte(` {}`)...), bytes.Replace(fixture, []byte(`"cases":`), []byte(`"unknown":0,"cases":`), 1), bytes.Repeat([]byte("x"), extraction.MaxEvaluationBytes+1)} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errs.Reset()
		err := Run(ctx, args, &out, &errs)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "private-input-do-not-echo") || strings.Contains(errs.String(), "private-input-do-not-echo") {
			t.Fatal("bad corpus accepted or echoed", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	out.Reset()
	if err := Run(cancelled, []string{"news-extraction-evaluate", "--export-fixture"}, &out, &errs); err != context.Canceled || out.Len() != 0 {
		t.Fatal("cancelled export emitted result")
	}
}
