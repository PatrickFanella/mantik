package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

type submissionBackend interface {
	Store
	FeedCollectorStore
	CollectorSubmissionStore
	ResearchReceiverStore
}

func collectorInput(at time.Time, key string) domain.RoutingInput {
	i := domain.RoutingInput{Kind: "quote", Source: "alpaca-iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", OccurredAt: at.Add(-time.Second), ReceivedAt: at, PriceCents: 10000, Provenance: &domain.InputProvenance{RecordKey: key, Revision: 1, RawRef: "journal:journal-one:capture-test-r1.json", SHA256: strings.Repeat("a", 64), AvailableAt: at, SourceSymbol: "SPY", MappingVersion: "alpaca-us-equities-v1", AccessRef: "access-one", Quality: "usable"}}
	i.ID = routing.MarketInputID(i)
	return i
}

func TestMemoryCollectorSubmission(t *testing.T) { submissionContracts(t, NewMemory()) }
func TestPostgresContractsCollectorSubmission(t *testing.T) {
	submissionContracts(t, timerPostgres(t, "collectorsubmission"))
}

func submissionContracts(t *testing.T, s submissionBackend) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	now := func() time.Time { return at }
	initial, _ := s.FeedCollectors(ctx)
	receiver := initial.ReceiverID
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 30}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, {RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}} {
		if _, err := s.FeedCollectorCommand(ctx, receiver, c, now); err != nil {
			t.Fatal(err)
		}
	}
	f := feedcollector.Fence{RegistrationID: r.ID, OwnerID: "worker-one", Generation: 1}
	i := collectorInput(at, "first")
	if _, err := s.SubmitCollectorResearch(ctx, "receiver-abcdefghijklmnop", f, i, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.RoutingInput){func(i *domain.RoutingInput) { i.Source = "alpaca-sip"; i.ID = routing.MarketInputID(*i) }, func(i *domain.RoutingInput) { i.Symbol = "QQQ"; i.ID = routing.MarketInputID(*i) }, func(i *domain.RoutingInput) { i.Provenance.AccessRef = "other" }, func(i *domain.RoutingInput) { i.Provenance.RawRef = "journal:other:capture-test-r1.json" }, func(i *domain.RoutingInput) {
		i.EvidenceClass = "synthetic_unqualified"
		i.ID = routing.MarketInputID(*i)
	}} {
		bad := clone(i)
		mutate(&bad)
		if _, err := s.SubmitCollectorResearch(ctx, receiver, f, bad, now); !errors.Is(err, feedcollector.ErrInvalid) {
			t.Fatal("mismatched registration admitted", err)
		}
	}
	var wg sync.WaitGroup
	var first atomic.Int32
	for range 8 {
		wg.Go(func() {
			receipt, err := s.SubmitCollectorResearch(ctx, receiver, f, i, now)
			if err != nil {
				t.Error(err)
			} else if !receipt.Duplicate {
				first.Add(1)
			}
		})
	}
	wg.Wait()
	if first.Load() != 1 {
		t.Fatal("duplicate input effects", first.Load())
	}
	accepted, _ := s.FeedCollectors(ctx)
	research, _ := s.ResearchSnapshot(ctx)
	if len(accepted.State.Admissions) != 1 || len(research.Inputs) != 1 || accepted.State.Admissions[i.ID].Fence != f {
		t.Fatal("missing atomic admission")
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		after, _ := s.FeedCollectors(ctx)
		if !reflect.DeepEqual(accepted, after) {
			t.Fatal("restart lost admission")
		}
	}
	at = at.Add(30 * time.Second)
	if receipt, err := s.SubmitCollectorResearch(ctx, receiver, f, i, now); err != nil || !receipt.Duplicate {
		t.Fatal("expired exact retry lost receipt", err)
	}
	newInput := collectorInput(at, "new")
	if _, err := s.SubmitCollectorResearch(ctx, receiver, f, newInput, now); !errors.Is(err, feedcollector.ErrLeaseLost) {
		t.Fatal("expired collector submitted", err)
	}
	if _, err := s.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "takeover", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-two"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCollectorResearch(ctx, receiver, f, newInput, now); !errors.Is(err, feedcollector.ErrLeaseLost) {
		t.Fatal("replaced collector submitted", err)
	}
	changed := clone(i)
	changed.PriceCents++
	if _, err := s.SubmitCollectorResearch(ctx, receiver, f, changed, now); !errors.Is(err, feedcollector.ErrConflict) {
		t.Fatal("changed replay admitted", err)
	}
	newFence := feedcollector.Fence{RegistrationID: r.ID, OwnerID: "worker-two", Generation: 2}
	if receipt, err := s.SubmitCollectorResearch(ctx, receiver, newFence, i, now); err != nil || !receipt.Duplicate {
		t.Fatal("new owner could not read prior delivery", err)
	}
	// Input previously retained by a manual operator keeps its original receipt.
	manual := collectorInput(at, "manual")
	if _, err := s.SubmitResearch(ctx, receiver, manual); err != nil {
		t.Fatal(err)
	}
	if receipt, err := s.SubmitCollectorResearch(ctx, receiver, newFence, manual, now); err != nil || !receipt.Duplicate {
		t.Fatal(err)
	}
	if receipt, err := s.SubmitCollectorResearch(ctx, receiver, newFence, manual, now); err != nil || !receipt.Duplicate {
		t.Fatal("manual original receipt lost", err)
	}
	manualAdmission, _ := s.FeedCollectors(ctx)
	if !manualAdmission.State.Admissions[manual.ID].InputAlreadyRetained {
		t.Fatal("manual origin obscured")
	}
	if pg, ok := s.(*Postgres); ok {
		before, _ := s.Snapshot(ctx)
		beforeCollectors, _ := s.FeedCollectors(ctx)
		// The collector write occurs after the aggregate write; reject it to prove
		// the entire two-row transaction rolls back.
		// PostgreSQL utility statements cannot bind parameters. The ID is a
		// fixed hex-derived market identity from routing.MarketInputID.
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors ADD CONSTRAINT reject_admission CHECK(NOT (payload->'admissions' ? '"+newInput.ID+"'))"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SubmitCollectorResearch(ctx, receiver, newFence, newInput, now); err == nil {
			t.Fatal("forced persistence failure admitted")
		}
		after, _ := s.Snapshot(ctx)
		afterCollectors, _ := s.FeedCollectors(ctx)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeCollectors, afterCollectors) {
			t.Fatal("partial admission commit")
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors DROP CONSTRAINT reject_admission"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SubmitCollectorResearch(ctx, receiver, newFence, newInput, now); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot(ctx)
	if state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 || state.State.Case.Status != "waiting" {
		t.Fatal("collector research produced economics")
	}
	if _, err := s.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "stop", Operation: "stop", RegistrationID: r.ID, OwnerID: "worker-two", Generation: 2}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCollectorResearch(ctx, receiver, newFence, collectorInput(at, "stopped"), now); !errors.Is(err, feedcollector.ErrLeaseLost) {
		t.Fatal("stopped collector admitted", err)
	}
}
