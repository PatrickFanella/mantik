package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
)

func TestLibrarySupervisionJourney(t *testing.T) {
	c, err := experiment.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context) (experiment.Catalog, error) {
		calls++
		if calls == 2 {
			return experiment.Catalog{}, errors.New("offline")
		}
		return c, nil
	}
	var out bytes.Buffer
	commands := "d " + c.Methods[1].ID + "\nd " + c.Methods[0].ID + "\nn\nd " + c.Methods[1].ID + "\np\nr\nn\nd " + c.Methods[0].ID + "\nr\nn\nn\nn\nn\nrun\nq\n"
	if err := superviseLibrary(context.Background(), io.NopCloser(strings.NewReader(commands)), &out, 1, fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("navigation/details caused requests: %d", calls)
	}
	for _, want := range []string{c.Methods[0].SHA256, c.Methods[1].SHA256, "SafetyRequirements", "Required", "STALE LIBRARY PAGE", "STALE: navigation and details", "no request sent", "End of compiled catalogue", "not active admission"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}

func TestLibrarySupervisionRejectsInvalidCatalog(t *testing.T) {
	c, err := experiment.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c.Methods[0].Definition.Safety.StopCents++
	calls := 0
	var out bytes.Buffer
	fetch := func(context.Context) (experiment.Catalog, error) { calls++; return c, nil }
	if err := superviseLibrary(context.Background(), io.NopCloser(strings.NewReader("n\nd "+c.Methods[0].ID+"\nr\n")), &out, 1, fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Contains(out.String(), "READBACK OK") || strings.Contains(out.String(), "DISPLAYED COMPILED METHOD PIN") {
		t.Fatal("invalid catalogue enabled inspection")
	}
}

func TestLibrarySupervisionCancellation(t *testing.T) {
	c, err := experiment.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, w := io.Pipe()
	defer w.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- superviseLibrary(ctx, in, io.Discard, 10, func(context.Context) (experiment.Catalog, error) { close(ready); return c, nil })
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled library viewer blocked input")
	}
}
