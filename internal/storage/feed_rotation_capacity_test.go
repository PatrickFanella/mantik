package storage

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func TestMemoryFeedRotationCrossesActiveCapacity(t *testing.T) {
	feedRotationCapacityContract(t, NewMemory())
}

func TestPostgresContractsFeedRotationCrossesActiveCapacity(t *testing.T) {
	feedRotationCapacityContract(t, timerPostgres(t, "feedrotationcapacity"))
}

// Cross the real limits without increasing them or using a test-only capacity.
// Calendar refreshes keep each current decision window small; finalized paper
// owners remain below their separate limit, which physical paper archival must
// address in a subsequent unit.
func feedRotationCapacityContract(t *testing.T, s rotationBackend) {
	t.Helper()
	ctx := t.Context()
	base := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	at := base
	now := func() time.Time { return at }
	feed, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := feed.ReceiverID
	def := strategyDefinition("rotation-capacity")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, now)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "capacity-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "capacity-journal", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, command := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, command, now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "capacity_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "capacity_fixture_only"}, nil
	}
	check := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	var originalRaw json.RawMessage
	var originalInput domain.RoutingInput
	var originalReceipt feedcollector.Receipt
	publications, commands := 0, 2
	write := func(n int, calendar bool) {
		t.Helper()
		key := fmt.Sprintf("quote-%d", n)
		r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: key, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: at, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, MaxQuoteAgeSeconds: 60}
		if calendar {
			key = fmt.Sprintf("calendar-%d", n)
			r.CaptureID, r.Capability, r.Feed = key, "market_calendar", "regular_session"
			r.Start, r.End = "2026-11-09", "2026-11-09"
			r.Original = `[{"date":"2026-11-09","open":"09:30","close":"16:00"}]`
		} else {
			r.Original = fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99.99,"as":10,"bs":10}}}`, at.Format(time.RFC3339Nano))
		}
		r.RawRef = fmt.Sprintf("journal:%s:capture-%s-r1.json", reg.JournalID, key)
		r.SHA256 = alpaca.Digest(r.Original)
		normalized, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range normalized.Inputs {
			if _, err := s.SubmitCollectorResearch(ctx, owner, fence, input, now); err != nil {
				t.Fatal("admission capacity before rotation", n, err)
			}
		}
		raw, _ := json.Marshal(r)
		if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, key+"-r1", fence, raw, now, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
			t.Fatal("publication capacity before rotation", n, err)
		}
		if n == 0 && !calendar {
			originalRaw, originalInput = raw, normalized.Inputs[0]
		}
		publications++
	}
	for n := range 2100 {
		at = base.Add(time.Duration(n) * time.Second)
		for retry := range 2 {
			command := feedcollector.Command{RequestID: fmt.Sprintf("renew-%d-%d", n, retry), Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
			receipt, err := s.FeedCollectorCommand(ctx, owner, command, now)
			if err != nil {
				t.Fatal("command capacity before rotation", n, err)
			}
			if n == 0 && retry == 0 {
				originalReceipt = receipt
			}
			commands++
		}
		if n%16 == 0 {
			write(n, true)
		}
		write(n, false)
		if (n+1)%64 == 0 || n == 2099 {
			at = at.Add(time.Nanosecond)
			if _, err := s.RotateFeed(ctx, owner, now, check, de, oe); err != nil {
				t.Fatal("capacity rotation", n, err)
			}
			active, err := s.FeedEvidence(ctx)
			if err != nil || len(active.Records) > 17 || len(active.Collectors.Commands) != 1 || len(active.Collectors.Admissions) > 20 || len(active.Routing.Inputs) > 24 {
				t.Fatal("active working set grew with archive history", n, len(active.Records), len(active.Collectors.Commands), len(active.Collectors.Admissions), err)
			}
			// Sample paper replay at early, middle and final source cuts. Every
			// feed write also validates all active paper clocks; growing that
			// separate catalogue on each cut obscures this collector-bound test.
			if n == 63 || n == 1087 || n == 2099 {
				at = at.Add(time.Nanosecond)
				if _, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: fmt.Sprintf("decision-%d", n), ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, now, de); err != nil {
					t.Fatal("new paper owner after long-running rotation", n, err)
				}
			}
		}
	}
	if publications <= MaxFeedEvidence || commands <= 4096 {
		t.Fatal("fixture did not cross real active bounds", publications, commands)
	}
	noClock := func() time.Time { t.Fatal("historical retry sampled new time"); return time.Time{} }
	first := feedcollector.Command{RequestID: "renew-0-0", Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	if receipt, err := s.FeedCollectorCommand(ctx, owner, first, noClock); err != nil || !reflect.DeepEqual(receipt, originalReceipt) {
		t.Fatal("long archive lost original command receipt", err)
	}
	if receipt, err := s.SubmitCollectorResearch(ctx, owner, fence, originalInput, noClock); err != nil || !receipt.Duplicate {
		t.Fatal("long archive lost original admission", err)
	}
	if receipt, err := s.PublishFeedEvidence(ctx, owner, reg.ID, "quote-0-r1", fence, originalRaw, noClock, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil || !receipt.Duplicate {
		t.Fatal("long archive lost original publication", err)
	}
	if audit, err := s.AuditPaperFeed(ctx, de, oe); err != nil || audit.Decisions != 3 || audit.Checkpoints != 33 {
		t.Fatal("long archive original-source audit", audit, err)
	}
	t.Logf("crossed active bounds: publications=%d admissions=%d commands=%d checkpoints=33 paper_observations=3; active publications<=17 commands=1 admissions<=20", publications, publications, commands)
}
