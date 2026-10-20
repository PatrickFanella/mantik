package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestCandidateGenerationCLIExactArtifactsAndRejectedOutput(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "unusable-candidate-generation-canary")
	t.Setenv("DATABASE_URL", "invalid-generation-database")
	dir := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	put := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	h := put("hypothesis.json", run("hypothesis-declare", "--export-fixture"))
	d := run("replay", "--export-dataset")
	data := put("dataset.json", d)
	var meta struct {
		SHA256 string `json:"sha256"`
	}
	_ = json.Unmarshal(d, &meta)
	need := put("requirement.txt", []byte("Propose a finite synthetic threshold example.\n"))
	planArgs := []string{"candidate-generation-plan", "--hypothesis", h, "--dataset", data, "--dataset-sha256", meta.SHA256, "--toolchain-image-sha256", "sha256:" + strings.Repeat("a", 64), "--model-label", "gpt-6.1-sol", "--requirement", need, "--requested-at", "2026-01-01T00:00:00Z"}
	pRaw := run(planArgs...)
	var p candidategeneration.Plan
	if json.Unmarshal(pRaw, &p) != nil || p.Validate() != nil {
		t.Fatal("invalid CLI plan")
	}
	pf := put("plan.json", pRaw)
	source := "package candidate\r\nfunc Entry(p int64)bool{return p<=10000}\r\n"
	payload, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: source})
	for _, test := range []struct {
		name      string
		output    []byte
		candidate bool
	}{{"source", payload, true}, {"binary", []byte{0xff, 0, 0xfe}, false}, {"missing", []byte{}, false}, {"partial", []byte(`{"schema":"mantik-candidate-generation-output-v1"`), false}} {
		t.Run(test.name, func(t *testing.T) {
			of := put(test.name+"-output.json", test.output)
			recordArgs := []string{"candidate-generation-record", "--plan", pf, "--plan-sha256", p.SHA256, "--dataset", data, "--output", of, "--output-sha256", workerplan.Digest(test.output), "--captured-at", "2026-01-01T00:00:01Z"}
			rRaw := run(recordArgs...)
			var r candidategeneration.Recording
			if json.Unmarshal(rRaw, &r) != nil || r.Validate() != nil || r.VerifyDataset(d) != nil || (r.Candidate != nil) != test.candidate {
				t.Fatal("invalid recording")
			}
			rf := put(test.name+"-recording.json", rRaw)
			inspect := []string{"candidate-generation-inspect", "--recording", rf, "--recording-sha256", r.SHA256, "--plan-sha256", p.SHA256, "--dataset", data}
			var roundtrip candidategeneration.Recording
			if json.Unmarshal(run(inspect...), &roundtrip) != nil || !reflect.DeepEqual(roundtrip, r) {
				t.Fatal("recording changed")
			}
			for artifact, want := range map[string][]byte{"output": test.output, "prompt": []byte(p.Prompt)} {
				if !bytes.Equal(run(append(append([]string{}, inspect...), "--artifact", artifact)...), want) {
					t.Fatal("exact artifact changed", artifact)
				}
			}
			if test.candidate {
				if !bytes.Equal(run(append(append([]string{}, inspect...), "--artifact", "source")...), []byte(source)) {
					t.Fatal("source CRLF bytes changed")
				}
				cf := put("exported-candidate.json", run(append(append([]string{}, inspect...), "--artifact", "candidate")...))
				_ = run("candidate-inspect", "--file", cf, "--sha256", r.Candidate.SHA256, "--dataset", data)
			} else {
				for _, artifact := range []string{"candidate", "source"} {
					var out, diag bytes.Buffer
					if err := Run(ctx, append(append([]string{}, inspect...), "--artifact", artifact), &out, &diag); err == nil || out.Len() != 0 {
						t.Fatal("rejection invented source", artifact)
					}
				}
			}
			for _, flag := range []string{"--recording-sha256", "--plan-sha256"} {
				args := append([]string{}, inspect...)
				for n := range args {
					if args[n] == flag {
						args[n+1] = strings.Repeat("b", 64)
					}
				}
				var out, diag bytes.Buffer
				if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
					t.Fatal("changed pin emitted data", flag)
				}
			}
			for _, bad := range [][]byte{append(append([]byte{}, rRaw...), []byte("{}")...), []byte(strings.Replace(string(rRaw), `"schema":`, `"schema":"duplicate","schema":`, 1)), []byte(strings.Replace(string(rRaw), `"schema":`, `"Schema":`, 1)), []byte(strings.Replace(string(rRaw), `"schema":`, `"extra":true,"schema":`, 1))} {
				put(test.name+"-recording.json", bad)
				var out, diag bytes.Buffer
				if err := Run(ctx, inspect, &out, &diag); err == nil || out.Len() != 0 {
					t.Fatal("ambiguous recording emitted data")
				}
			}
			put(test.name+"-recording.json", rRaw)
			// Wrong raw-output identity and changed dataset fail before any record output.
			for _, which := range []string{"output", "dataset", "time"} {
				args := append([]string{}, recordArgs...)
				for n := range args {
					if which == "output" && args[n] == "--output-sha256" {
						args[n+1] = strings.Repeat("c", 64)
					}
					if which == "dataset" && args[n] == "--dataset" {
						args[n+1] = put("changed-dataset.json", append(append([]byte{}, d...), ' '))
					}
					if which == "time" && args[n] == "--captured-at" {
						args[n+1] = "2025-01-01T00:00:00Z"
					}
				}
				var out, diag bytes.Buffer
				if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
					t.Fatal("changed recording input accepted", which)
				}
			}
			if raw, err := os.ReadFile(of); err != nil || !bytes.Equal(raw, test.output) {
				t.Fatal("input output file changed", err)
			}
		})
	}
	for _, args := range [][]string{{"candidate-generation-plan"}, {"candidate-generation-record"}, {"candidate-generation-inspect"}, append(append([]string{}, planArgs...), "--api-url", "http://127.0.0.1:1"), append(append([]string{}, planArgs...), "unexpected")} {
		var out, diag bytes.Buffer
		if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("unsupported command accepted", args)
		}
	}
	for _, command := range []string{"candidate-generation-plan", "candidate-generation-record", "candidate-generation-inspect"} {
		var out, diag bytes.Buffer
		if err := Run(ctx, []string{command, "--help"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
	}
}
