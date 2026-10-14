package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type operationalClock struct{ at time.Time }

func (c operationalClock) Now() time.Time                                  { return c.at }
func (c operationalClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestOperationalCLIInspectionAndRecovery(t *testing.T) {
	ctx := context.Background()
	store := storage.NewQueuedMemory()
	q := workflow.Fixtures()[1]
	now := q.ReceivedAt.Add(31 * time.Second)
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimActions(ctx, "worker", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileAction(ctx, claims[0], now); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"action_delivery", "protective_monitoring"} {
		if err := store.ReportWorker(ctx, domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: cap, WorkerID: "worker-" + cap, At: now, IntervalMillis: 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MANTIK_API_TOKEN", "operator-token")
	server := httptest.NewServer(service.HandlerWithClock(store, "operator-token", operationalClock{now}))
	defer server.Close()
	run := func(args ...string) string {
		var out, errOut bytes.Buffer
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	raw := run("operations", "--format", "json")
	var snapshot domain.OperationsSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot.Exceptions) != 1 {
		t.Fatal("CLI lost exception", err)
	}
	if table := run("operations"); !strings.Contains(table, "action_expired") {
		t.Fatal("table omitted exception")
	}
	raw = run("readiness", "--scope", "action_delivery", "--format", "json")
	var ready domain.ReadinessReport
	if err := json.Unmarshal([]byte(raw), &ready); err != nil || ready.Ready {
		t.Fatal("CLI readiness hid failure", err)
	}
	e := snapshot.Exceptions[0]
	raw = run("recover", "--exception", e.ID, "--request-id", "cli-ack", "--operation", "reconcile-action")
	var receipt domain.RecoveryReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.Attempt.Outcome != "terminal_action_acknowledged" {
		t.Fatal("CLI recovery failed", err)
	}
	if table := run("readiness", "--scope", "fixture_operation"); !strings.Contains(table, "READY  true") {
		t.Fatal("CLI did not show recovery")
	}
	for _, args := range [][]string{{"recover"}, {"recover", "--exception", "bad/path", "--request-id", "test"}, {"readiness", "--scope", "unknown"}, {"readiness", "--operation", "force"}, {"operations", "--api-url", "https://example.com"}} {
		var out, errOut bytes.Buffer
		if err := Run(ctx, args, &out, &errOut); err == nil {
			t.Fatalf("invalid CLI request accepted: %v", args)
		}
	}
}
