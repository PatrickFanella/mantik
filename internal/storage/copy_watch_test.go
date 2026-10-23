package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryOwnedCopyRecords(t *testing.T) { copyRecordContracts(t, NewMemory()) }
func TestPostgresContractsOwnedCopyRecords(t *testing.T) {
	copyRecordContracts(t, timerPostgres(t, "copyrecords"))
}
func copyRecordContracts(t *testing.T, s socialBackend) {
	ctx := context.Background()
	owner, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	p := socialPolicy(owner.ReceiverID)
	p.Policy.Behavior = "bounded_paper_follow"
	p.Policy.Follow = &socialwatch.FollowBounds{MaxOrderNotionalCents: 10000, MaxExposureCents: 50000, StopDistanceBPS: 100, MaxHoldingSeconds: 3600}
	var calls atomic.Int32
	clock := func() time.Time { calls.Add(1); return at }
	noClock := func() time.Time { panic("historical copy clock") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	policy, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, noClock)
	if err != nil {
		t.Fatal(err)
	}
	original := socialwatch.Original{Schema: socialwatch.CopyRecordingSchema, Source: socialwatch.SourceRef{Provider: p.Policy.Provider, AccountID: p.Policy.AccountID, PostID: "fill-1"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Recorded fill assertion, no provider attestation", Symbols: []string{"SPY"}, Activity: &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "assertion-v1", Side: "buy", QuantityUnits: 10, QuantityUnitClaim: "shares", PriceCents: 10000, OrderRef: "order-1"}}
	raw, _ := json.Marshal(original)
	c := socialwatch.CaptureCommand{ID: "copy", ReceiverID: owner.ReceiverID, WatchID: p.Policy.ID, PolicySHA256: policy.Entry.Policy.SHA256, EvidenceClass: "recorded_copy_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
	for range 8 {
		wg.Go(func() {
			if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	capture, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, noClock)
	if err != nil || !capture.Duplicate || capture.Entry.Capture.Copy == nil || capture.Entry.Capture.Copy.ProviderObservationVerified || capture.Entry.Capture.Copy.FinancialEffectAvailable {
		t.Fatal(capture, err)
	}
	catalog, err := s.SocialHistory(ctx)
	if err != nil || len(catalog.History) != 2 || !reflect.DeepEqual(catalog.History[1].Capture.Source.Activity, original.Activity) {
		t.Fatal(catalog, err)
	}
	capture.Entry.Capture.Copy.ProviderObservationVerified = true
	capture.Entry.Capture.Source.Activity.QuantityUnits = 1000000
	again, err := s.SocialHistory(ctx)
	if err != nil || !reflect.DeepEqual(again, catalog) {
		t.Fatal("detachment", err)
	}
	after, _ := s.Snapshot(ctx)
	accountsAfter, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountsAfter) {
		t.Fatal("copy records mutated operations/capital")
	}
	if pg, ok := s.(*Postgres); ok {
		cfg := pg.pool.Config().ConnString()
		reopened, err := OpenPostgres(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		again, err := reopened.SocialHistory(ctx)
		if err != nil || !reflect.DeepEqual(again, catalog) {
			t.Fatal("copy reconnect", err)
		}
		if _, err := reopened.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, noClock); err != nil {
			t.Fatal(err)
		}
	}
}
