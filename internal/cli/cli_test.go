package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestDemoLocalAndRemote(t *testing.T) {
	server := httptest.NewServer(service.Handler(storage.NewMemory(), ""))
	defer server.Close()
	for _, args := range [][]string{{"demo"}, {"demo", "--api-url", server.URL}, {"demo", "--api-url", server.URL}} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		var snapshot domain.Snapshot
		if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.State.Case.Status != "closed" || snapshot.ObservationCount != 4 {
			t.Fatal("CLI lifecycle incomplete")
		}
	}
}

func TestServeRequiresAuthority(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "")
	t.Setenv("DATABASE_URL", "")
	for _, args := range [][]string{{"serve", "--listen", "0.0.0.0:40880"}, {"serve"}, {"demo", "extra"}, {"status", "--format", "csv"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("unexpected acceptance: %v", args)
		}
	}
}
