package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

type ownerConfirmationPrompt struct {
	once  sync.Once
	ready chan struct{}
}

func (w *ownerConfirmationPrompt) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "Type the exact Confirmation pin") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), nil
}
func TestOwnerBookContextCancelsBlockedConfirmation(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "isolated-owner")
	var requests atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected mutation", 500)
	})))
	defer server.Close()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prompt := &ownerConfirmationPrompt{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runOwnerBook(ctx, []string{"--api-url", server.URL, "--id", "book", "--action", "owner_clear_halt", "--action-id", "clear", "--expected-version", "0"}, input, prompt, io.Discard)
	}()
	select {
	case <-prompt.ready:
	case <-time.After(time.Second):
		t.Fatal("confirmation prompt missing")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled confirmation still blocks on input")
	}
	if requests.Load() != 0 {
		t.Fatal("cancelled prompt sent an owner request")
	}
}
