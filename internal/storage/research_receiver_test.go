package storage

import (
	"context"
	"errors"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestMemoryResearchReceiverIdentity(t *testing.T) {
	ctx := context.Background()
	a := NewMemory()
	b := NewMemory()
	left, err := a.ResearchSnapshot(ctx)
	if err != nil || !ValidReceiverID(left.ID) {
		t.Fatal("invalid receiver identity")
	}
	right, _ := b.ResearchSnapshot(ctx)
	if left.ID == right.ID {
		t.Fatal("different stores share identity")
	}
	fixture := workflow.Fixtures()[1]
	i := domain.RoutingInput{ID: fixture.ID, Kind: "quote", Source: fixture.Source, Symbol: fixture.Symbol, EvidenceClass: "synthetic_unqualified", OccurredAt: fixture.OccurredAt, ReceivedAt: fixture.ReceivedAt, PriceCents: fixture.PriceCents}
	if _, err := a.SubmitResearch(ctx, left.ID, i); err == nil {
		t.Fatal("guarded research endpoint admitted fixture authority")
	}
}

func TestPostgresContractsResearchReceiver(t *testing.T) {
	s := timerPostgres(t, "researchreceiver")
	ctx := context.Background()
	config := s.pool.Config().ConnString()
	first, err := s.ResearchSnapshot(ctx)
	if err != nil || !ValidReceiverID(first.ID) {
		t.Fatal("receiver migration did not create identity", err)
	}
	marketRoutingContracts(t, s)
	before, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var input domain.RoutingInput
	for _, old := range before.Inputs {
		if old.Input.Source != "fixture" {
			input = old.Input
			break
		}
	}
	if _, err := s.SubmitResearch(ctx, "receiver-abcdefghijklmnop", input); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal("wrong database receiver accepted mutation")
	}
	if receipt, err := s.SubmitResearch(ctx, first.ID, input); err != nil || !receipt.Duplicate {
		t.Fatal("guarded exact retry lost receipt", err)
	}
	s.Close()
	s, err = OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.ResearchSnapshot(ctx)
	if err != nil || after.ID != first.ID || len(after.Inputs) != len(before.Inputs) {
		t.Fatal("restart changed receiver identity/history", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=7"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("receiver migration checksum drift admitted")
	}
}
