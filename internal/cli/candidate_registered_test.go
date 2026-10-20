package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestRegisteredWorkerCLIRejectsBeforeReservation(t *testing.T) {
	dir := t.TempDir()
	d, err := dataset.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(d)
	image := "sha256:" + strings.Repeat("a", 64)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}"), hypothesis.Fixture(), data, image)
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(c, data, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(p, data)
	if err != nil {
		t.Fatal(err)
	}
	i := candidateattempt.Intent{Schema: candidateattempt.Schema, Command: candidateattempt.Command{ReceiverID: "receiver-abcdefghijklmnop", RequestID: "one", RegistrationID: "root", RegistrationSHA256: strings.Repeat("b", 64), Job: j}, Sequence: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: "intent_retained", Authority: "candidate_worker_intent_only"}
	i.SHA256 = workerplan.Hash(i)
	raw, _ := json.Marshal(i)
	file, datafile, root := filepath.Join(dir, "intent.json"), filepath.Join(dir, "wrong-dataset.json"), filepath.Join(dir, "must-not-exist")
	if err := os.WriteFile(datafile, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"candidate-worker-run", "--journal", root, "--intent", file, "--intent-sha256", i.SHA256, "--receiver-id", i.Command.ReceiverID, "--request-id", "one", "--approved-image-sha256", image, "--dataset", datafile}
	for _, encoded := range [][]byte{raw,
		[]byte(strings.Replace(string(raw), `"schema":`, `"unknown":true,"schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)),
		append(append([]byte{}, raw...), []byte("{}")...),
	} {
		if err := os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		var out, errs bytes.Buffer
		if err := Run(context.Background(), base, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid dataset/receipt reached reservation", err, out.String())
		}
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--intent-sha256", "--receiver-id", "--request-id", "--approved-image-sha256"} {
		args := append([]string{}, base...)
		for n := range args {
			if args[n] == flag {
				args[n+1] = "wrong"
			}
		}
		var out, errs bytes.Buffer
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("wrong independent pin reached reservation", flag)
		}
	}
	for _, args := range [][]string{
		{"candidate-worker-run"}, {"candidate-worker-reconcile"},
		{"candidate-worker-run", "--api-url", "http://127.0.0.1:1"},
		{"candidate-worker-reconcile", "--job", "job.json"},
		append(append([]string{}, base...), "unexpected"),
	} {
		var out, errs bytes.Buffer
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid CLI accepted", args)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("journal created before pins/dataset verified")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errs bytes.Buffer
	if err := Run(ctx, base, &out, &errs); err != context.Canceled || out.Len() != 0 {
		t.Fatal(err)
	}
	for _, command := range []string{"candidate-worker-run", "candidate-worker-reconcile"} {
		if err := Run(context.Background(), []string{command, "--help"}, &out, &errs); err != nil {
			t.Fatal(err)
		}
	}
}
