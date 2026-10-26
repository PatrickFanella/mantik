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

func TestMemoryRetainedCopyResearch(t *testing.T) { retainedCopyResearchContracts(t, NewMemory()) }
func TestPostgresContractsRetainedCopyResearch(t *testing.T) {
	retainedCopyResearchContracts(t, timerPostgres(t, "copyresearch"))
}
func retainedCopyResearchContracts(t *testing.T, s socialBackend) {
	t.Helper()
	ctx := context.Background()
	h, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	now := func() time.Time { return at }
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	policy := socialPolicy(h.ReceiverID)
	registered, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	original := socialwatch.Original{Schema: socialwatch.CopyRecordingSchema, Source: socialwatch.SourceRef{Provider: policy.Policy.Provider, AccountID: policy.Policy.AccountID, PostID: "copy-research-source"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Recorded unverified fill", Symbols: []string{"SPY"}, Activity: &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "asserted-v1", Side: "buy", QuantityUnits: 5, QuantityUnitClaim: "shares", PriceCents: 10000}}
	raw, _ := json.Marshal(original)
	capture := socialwatch.CaptureCommand{ID: "retained-copy", ReceiverID: h.ReceiverID, WatchID: policy.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: "recorded_copy_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
	source, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, now)
	if err != nil {
		t.Fatal(err)
	}
	command := socialwatch.ResearchCommand{ID: "select-copy", ReceiverID: h.ReceiverID, CaptureID: capture.ID, CaptureSHA256: source.Entry.Capture.SHA256}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, e := s.ApplySocialCommand(ctx, socialwatch.Command{Research: &command}, func() time.Time { calls.Add(1); return at }); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("copy request issued twice", calls.Load())
	}
	receipt, err := s.ApplySocialCommand(ctx, socialwatch.Command{Research: &command}, func() time.Time { panic("copy research retry clock") })
	if err != nil || !receipt.Duplicate || receipt.Entry.Research == nil {
		t.Fatal(receipt, err)
	}
	request := receipt.Entry.Research.Request
	if request.Schema != "mantik-owned-copy-research-request-v1" || request.EvidenceClass != "recorded_copy_unqualified" || request.OriginalSHA256 != capture.OriginalSHA256 || request.DispatchAvailable || request.AdmissionAvailable || request.ExecutionAvailable || request.QualificationCredit {
		t.Fatal(request)
	}
	alias := command
	alias.ID = "copy-research-alias"
	duplicate, e := s.ApplySocialCommand(ctx, socialwatch.Command{Research: &alias}, now)
	if e != nil || !duplicate.Entry.Research.ResearchRequestDuplicate || !reflect.DeepEqual(duplicate.Entry.Research.Request, request) {
		t.Fatal(duplicate, e)
	}
	history, e := s.SocialHistory(ctx)
	if e != nil || len(history.ResearchRequests) != 1 || !reflect.DeepEqual(history.History[1], source.Entry) {
		t.Fatal(history, e)
	}
	after, _ := s.Snapshot(ctx)
	accountsAfter, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountsAfter) {
		t.Fatal("copy research mutated capital or operations")
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		got, e := reopened.SocialHistory(ctx)
		if e != nil || !reflect.DeepEqual(got, history) {
			t.Fatal("copy research not durable", e)
		}
	}
}
