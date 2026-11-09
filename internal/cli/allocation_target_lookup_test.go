package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type targetOnlyAllocationStore struct {
	*storage.Memory
	blocked atomic.Bool
	lists   atomic.Int32
}

func (s *targetOnlyAllocationStore) OwnedAllocations(ctx context.Context, de storage.PaperDecisionEvaluator, oe ...storage.PaperOutcomeEvaluator) (storage.OwnedAllocationCatalog, error) {
	s.lists.Add(1)
	if s.blocked.Load() {
		return storage.OwnedAllocationCatalog{}, errors.New("complete history unavailable in target-only contract")
	}
	return s.Memory.OwnedAllocations(ctx, de, oe...)
}

func TestAllocationTargetHTTPDoesNotLoadCatalogue(t *testing.T) {
	m := storage.NewMemory()
	ownedPaperDecisionHTTP(t, m)
	s := &targetOnlyAllocationStore{Memory: m}
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.HandlerWithClock(s, "owned-decision-token", &ownedDecisionClock{panicRead: true})))
	defer server.Close()
	ctx := t.Context()
	var all storage.OwnedAllocationCatalog
	if err := requestBounded(ctx, server.URL, "/v1/allocation-comparisons", nil, &all, nil, storage.MaxOwnedAllocationBytes, true); err != nil || len(all.Records) == 0 {
		t.Fatal("native lifecycle comparison source", err)
	}
	s.blocked.Store(true)
	for _, expected := range all.Records {
		var got storage.OwnedAllocationRecord
		if err := requestBounded(ctx, server.URL, "/v1/allocation-comparisons/"+expected.Command.ID, nil, &got, nil, storage.MaxOwnedAllocationBytes, true); err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatal("target route loaded complete history or changed native result", err)
		}
		call := map[string]any{"tool": "inspect_record", "arguments": map[string]string{"kind": "allocation_comparison", "id": expected.Command.ID}}
		if err := requestBounded(ctx, server.URL, "/v1/supervision/calls", call, &got, nil, storage.MaxOwnedAllocationBytes, true); err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatal("supervision loaded complete history or changed native result", err)
		}
	}
	if s.lists.Load() != 1 {
		t.Fatal("targeted inspection requested the complete catalogue", s.lists.Load())
	}
	var got storage.OwnedAllocationRecord
	var failure *httpFailure
	err := requestBounded(ctx, server.URL, "/v1/allocation-comparisons/absent", nil, &got, nil, storage.MaxOwnedAllocationBytes, true)
	if !errors.As(err, &failure) || failure.Status != 404 {
		t.Fatal("missing target did not retain its 404 contract", err)
	}
	err = requestBounded(ctx, server.URL, "/v1/allocation-comparisons/"+all.Records[0].Command.ID, nil, &got, url.Values{"unexpected": {"1"}}, storage.MaxOwnedAllocationBytes, true)
	if !errors.As(err, &failure) || failure.Status != 422 {
		t.Fatal("target query parameters accepted", err)
	}
}
