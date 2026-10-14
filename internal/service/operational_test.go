package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestOperationalAPIAndScopedReadiness(t *testing.T) {
	ctx := context.Background()
	store := storage.NewQueuedMemory()
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	now := q.ReceivedAt.Add(31 * time.Second)
	clock := newFakeClock(now)
	if err := RunActionCycle(ctx, store, "worker", clock, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"action_delivery", "protective_monitoring"} {
		if err := store.ReportWorker(ctx, domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: cap, WorkerID: "worker-" + cap, At: now, IntervalMillis: 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	handler := HandlerWithClock(store, "operator-token", clock)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/v1/operations", "", ""); w.Code != 401 {
		t.Fatal("operational inspection bypassed auth")
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/readyz", 200}, {"/readyz?scope=action_delivery", 503}, {"/readyz?scope=feed_coverage", 503}, {"/readyz?scope=unknown", 400}, {"/v1/readiness/action_delivery", 200}} {
		if w := call("GET", test.path, "", "operator-token"); w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
	w := call("GET", "/v1/operations", "", "operator-token")
	var snapshot domain.OperationsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || len(snapshot.Exceptions) != 1 {
		t.Fatal("inspection missing exception", err)
	}
	e := snapshot.Exceptions[0]
	path := "/v1/operations/exceptions/" + e.ID + "/recover"
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"request_id":"force","operation":"force-fill"}`, 422},
		{`{"request_id":"clock","operation":"recheck","at":"2026-01-05T15:01:00Z"}`, 400},
		{`{"request_id":"ack","operation":"reconcile-action"}`, 200},
		{`{"request_id":"ack","operation":"reconcile-action"}`, 200},
		{`{"request_id":"ack","operation":"retry-protection"}`, 409},
	} {
		if w := call("POST", path, test.body, "operator-token"); w.Code != test.status {
			t.Fatalf("recovery %d: %s", w.Code, w.Body.String())
		}
	}
	if w := call("GET", "/readyz?scope=fixture_operation", "", "operator-token"); w.Code != 200 {
		t.Fatal("bounded recovery did not restore readiness", w.Body.String())
	}
	if w := call("POST", "/v1/operations/exceptions/unknown/recover", `{"request_id":"test","operation":"recheck"}`, "operator-token"); w.Code != 404 {
		t.Fatal("unknown exception accepted")
	}
	final, err := store.Snapshot(ctx)
	if err != nil || final.State.Case.Entry != nil {
		t.Fatal("recovery created unauthorized fill")
	}
}

type outageStore struct {
	storage.Store
	down bool
}

func (s *outageStore) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	if s.down {
		return domain.Snapshot{}, errors.New("storage offline")
	}
	return s.Store.Snapshot(ctx)
}
func TestReadinessStorageOutageAndRecovery(t *testing.T) {
	backend := &outageStore{Store: storage.NewMemory(), down: true}
	handler := Handler(backend, "")
	for _, test := range []struct {
		path   string
		status int
	}{{"/healthz", 200}, {"/readyz?scope=process", 200}, {"/readyz", 503}, {"/readyz?scope=fixture_operation", 503}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, test.path, nil))
		if w.Code != test.status {
			t.Fatal(test.path, w.Code)
		}
	}
	backend.down = false
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != 200 {
		t.Fatal("storage recovery not observed")
	}
}

type reportOutage struct {
	*storage.Memory
	down bool
}

func (s *reportOutage) ReportWorker(ctx context.Context, r domain.WorkerReport) error {
	if s.down {
		return errors.New("report storage unavailable")
	}
	return s.Memory.ReportWorker(ctx, r)
}
func TestWorkerRetainsUnrecordableOutage(t *testing.T) {
	ctx := context.Background()
	backend := &reportOutage{Memory: storage.NewQueuedMemory(), down: true}
	clock := newFakeClock(workflow.Fixtures()[0].ReceivedAt)
	pending := pendingFailure{}
	recordWorkerCycle(ctx, backend, "worker", "action_delivery", clock, time.Second, nil, false, &pending)
	if pending.at.IsZero() || pending.code != "storage_unavailable" {
		t.Fatal("unrecordable failure lost")
	}
	backend.down = false
	clock.at = clock.at.Add(time.Second)
	recordWorkerCycle(ctx, backend, "worker", "action_delivery", clock, time.Second, nil, false, &pending)
	if !pending.at.IsZero() {
		t.Fatal("persisted failure still pending")
	}
	snapshot, err := backend.Snapshot(ctx)
	if err != nil || len(snapshot.State.Routing.Timers.Exceptions) != 1 {
		t.Fatal("outage not persisted on recovery", err)
	}
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Status != "resolved" || e.Code != "worker_storage_unavailable" || e.TerminalOutcome == "" {
			t.Fatal("recovery lifecycle incomplete")
		}
	}
}
