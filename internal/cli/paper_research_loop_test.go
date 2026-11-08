package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/decisiondraft"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// A model outage leaves failed attempts on disk and stops after the bound.
// Once the model recovers, a later cycle retains the review and stops asking.
func researchLoopContracts(t *testing.T, base string, d storage.PaperDecisionRecord) {
	t.Helper()
	if d.Evaluation.Action != "entry_candidate" {
		t.Fatal("research loop fixture needs an entry candidate", d.Evaluation.Action)
	}
	var calls atomic.Int32
	var available atomic.Bool
	content, _ := json.Marshal(decisiondraft.Draft{Explanation: "A retained candidate still requires admission.", Claims: []storage.DecisionReviewClaim{{Field: "action", Value: d.Evaluation.Action}}})
	provider := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}}})
		_, _ = w.Write(raw)
	})))
	defer provider.Close()
	root := t.TempDir()
	config := filepath.Join(root, "model.json")
	raw, _ := json.Marshal(decisiondraft.Config{Endpoint: provider.URL, Model: "ollama/manufactured-model", MaxTokens: 512})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	captures := filepath.Join(root, "captures")
	id := automaticReviewID(d)
	attempts := func() int {
		entries, _ := os.ReadDir(captures)
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), id+"-") {
				count++
			}
		}
		return count
	}
	cycle := func() error {
		if err := os.MkdirAll(captures, 0700); err != nil {
			t.Fatal(err)
		}
		return researchCycle(context.Background(), base, d.Command.ReceiverID, captures, config, new(bytes.Buffer), new(bytes.Buffer))
	}
	// The first two attempts fail against the outage and are each retained.
	for want := 1; want <= 2; want++ {
		if err := cycle(); err == nil || attempts() != want || int(calls.Load()) != want {
			t.Fatal("outage attempt", want, err, attempts(), calls.Load())
		}
	}
	available.Store(true)
	if err := cycle(); err != nil || attempts() != 3 || calls.Load() != 3 {
		t.Fatal("recovery", err, attempts(), calls.Load())
	}
	var retained storage.DecisionReviewRecord
	if err := requestBounded(context.Background(), base, "/v1/paper-decision-reviews/"+id, nil, &retained, nil, storage.MaxDecisionReviewBytes, true); err != nil || retained.Command.DecisionID != d.Command.ID {
		t.Fatal("review not retained", err, retained.Command)
	}
	// A retained review takes this decision out of the schedule; the next
	// cycle, if it has anything to do, moves on to another candidate.
	if err := cycle(); err != nil || attempts() != 3 {
		t.Fatal("retained review asked the model again", err, attempts())
	}
}

func TestPaperResearchLoopRequiresItsConfiguration(t *testing.T) {
	root := t.TempDir()
	for name, args := range map[string][]string{
		"no receiver": {"--capture-root", root, "--model-config", "m"},
		"relative":    {"--receiver-id", "receiver-aaaaaaaaaaaaaaaaaaaa", "--capture-root", "captures", "--model-config", "m"},
		"no config":   {"--receiver-id", "receiver-aaaaaaaaaaaaaaaaaaaa", "--capture-root", root},
		"zero period": {"--receiver-id", "receiver-aaaaaaaaaaaaaaaaaaaa", "--capture-root", root, "--model-config", "m", "--interval-seconds", "0"},
	} {
		if err := Run(context.Background(), append([]string{"paper-research-loop"}, args...), new(bytes.Buffer), new(bytes.Buffer)); err == nil {
			t.Fatal(name)
		}
	}
}

// The loop runs until cancelled and returns cleanly.
func TestPaperResearchLoopStopsWhenCancelled(t *testing.T) {
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t.Setenv("MANTIK_API_TOKEN", "research-loop-token")
	if err := Run(ctx, []string{"paper-research-loop", "--api-url", server.URL, "--receiver-id", "receiver-aaaaaaaaaaaaaaaaaaaa", "--capture-root", t.TempDir(), "--model-config", "m", "--interval-seconds", "1"}, new(bytes.Buffer), new(bytes.Buffer)); err != nil {
		t.Fatal(err)
	}
}
