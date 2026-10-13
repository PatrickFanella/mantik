package worker

import (
	"context"
	"testing"
)

func TestRunOnce(t *testing.T) {
	if got := RunOnce(context.Background(), "template"); got != "template: ok" {
		t.Fatalf("unexpected result: %s", got)
	}
}
