package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type delayedAllocationIndexStore struct{ *storage.Memory }

func (s delayedAllocationIndexStore) IndexOwnedAllocationHistory(ctx context.Context, c storage.OwnedAllocationIndexCommand, de storage.PaperDecisionEvaluator, oe ...storage.PaperOutcomeEvaluator) (storage.OwnedAllocationIndexReceipt, error) {
	select {
	case <-ctx.Done():
		return storage.OwnedAllocationIndexReceipt{}, ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return s.Memory.IndexOwnedAllocationHistory(ctx, c, de, oe...)
	}
}

func TestAllocationIndexMaintenanceWriteDeadline(t *testing.T) {
	store := delayedAllocationIndexStore{storage.NewMemory()}
	news, err := store.NewsCases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(loopbacktest.GoClientOnly(service.Handler(store, "index-token")))
	// The native maintenance response must survive an ordinary server write
	// deadline shorter than the complete audit, while retaining its own limit.
	server.Config.WriteTimeout = 50 * time.Millisecond
	server.Start()
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "index-token")
	var out, diag bytes.Buffer
	if err := Run(t.Context(), []string{"allocation-history-index", "--api-url", server.URL, "--receiver-id", news.ReceiverID}, &out, &diag); err != nil {
		t.Fatal(err, diag.String())
	}
	var receipt storage.OwnedAllocationIndexReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Validate(storage.OwnedAllocationIndexCommand{ReceiverID: news.ReceiverID}) != nil {
		t.Fatal("maintenance response lost after server write deadline", err)
	}
}
