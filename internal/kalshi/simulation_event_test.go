package kalshi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

func TestSimulationEventIsCapturedBeforeUse(t *testing.T) {
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected authorization")
		}
		if r.URL.Path != "/events/KXTEST-EVENT" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"event":{"event_ticker":"KXTEST-EVENT","series_ticker":"KXTEST","fee_multiplier_override":0.5}}`))
	})))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "journal")
	client, e := OpenClient(root)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	client.origin = server.URL
	observation, e := client.FetchSimulationEvent(context.Background(), "KXTEST-EVENT")
	if e != nil || observation.SHA256 != simHash(observation.Original) || observation.ObservedAt.IsZero() {
		t.Fatal(observation, e)
	}
	files, e := filepath.Glob(filepath.Join(root, "capture-*.json"))
	if e != nil || len(files) != 1 {
		t.Fatal("source not retained", files, e)
	}
	if _, e := client.FetchSimulationEvent(context.Background(), "bad/path"); e == nil {
		t.Fatal("invalid event ticker accepted")
	}
}
