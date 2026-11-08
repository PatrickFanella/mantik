package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperDecisionWorkerInterval = 15 * time.Second
const PaperDecisionWorkerCapability = "paper_decision"
const AutomaticPaperDecisionPrefix = "automatic-decision-"
const AutomaticPaperComparisonPrefix = "automatic-comparison-"

type PaperDecisionWorkerStore interface {
	storage.PaperDecisionStore
	storage.PaperStrategyStore
	storage.FeedEvidenceStore
	storage.PaperEvaluationDeclarationStore
	storage.OwnedAllocationStore
}

func reservedPaperDecisionID(id string) bool {
	return strings.HasPrefix(id, AutomaticPaperDecisionPrefix)
}

func reservedPaperComparisonID(id string) bool {
	return strings.HasPrefix(id, AutomaticPaperComparisonPrefix)
}

// AutomaticPaperDecisionID binds one decision to a strategy record, a feed
// registration and the latest quote that registration published. A cycle
// that sees no newer quote derives the same identity and retains nothing.
func AutomaticPaperDecisionID(strategySHA256, registrationID string, latest storage.FeedEvidenceRecord) string {
	return AutomaticPaperDecisionPrefix + workerplan.Hash(struct {
		StrategySHA256, RegistrationID, Key string
		PublishedAt                         time.Time
	}{strategySHA256, registrationID, latest.Key, latest.PublishedAt})
}

// AutomaticPaperComparisonID binds one comparison to one finalized decision.
func AutomaticPaperComparisonID(decision storage.PaperDecisionRecord) string {
	return AutomaticPaperComparisonPrefix + workerplan.Hash(decision.SHA256)
}

// RunPaperDecisionCycle finalizes and compares without a caller sequencing
// either stage. Every strategy a retained evaluation declaration names, at
// its current record, is paired with each feed registration that matches its
// symbol, feed and evidence class. When such a registration has published a
// quote no decision has seen, the cycle finalizes one decision against it:
// an entry candidate, an abstention or a rejection, all retained. Every entry
// candidate that no comparison pins then gets its own comparison, which the
// allocation owner reserves, defers or rejects. Identities derive from the
// inputs, so a retried or concurrent cycle reaches the same records. Each
// commit is atomic; a later failure keeps the earlier ones.
func RunPaperDecisionCycle(ctx context.Context, store PaperDecisionWorkerStore, clock Clock) error {
	if clock == nil {
		return storage.ErrPaperDecisionInvalid
	}
	// Each store call has its own deadline, so many pairs do not share one.
	bounded := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 20*time.Second) }
	at := clock.Now().UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return storage.ErrPaperDecisionInvalid
	}
	call, cancel := bounded()
	defer cancel()
	strategies, err := store.PaperStrategies(call)
	if err != nil {
		return err
	}
	declarations, err := store.PaperEvaluationDeclarations(call)
	if err != nil {
		return err
	}
	feed, err := store.FeedEvidence(call)
	if err != nil {
		return err
	}
	if strategies.ReceiverID != declarations.ReceiverID || feed.ReceiverID != strategies.ReceiverID {
		return storage.ErrReceiverMismatch
	}
	owner := strategies.ReceiverID
	declared := []paperstrategy.Record{}
	seen := map[string]bool{}
	for _, d := range declarations.Records {
		for _, v := range d.Command.Plan.Variants {
			s, err := paperstrategy.Select(strategies, v.ID)
			// A superseded strategy record no longer decides.
			if err != nil || s.SHA256 != v.SHA256 || seen[s.SHA256] {
				continue
			}
			seen[s.SHA256] = true
			declared = append(declared, s)
		}
	}
	slices.SortFunc(declared, func(a, b paperstrategy.Record) int {
		return strings.Compare(a.Command.Definition.ID, b.Command.Definition.ID)
	})
	latest := latestQuotes(feed)
	registrations := []string{}
	for id := range feed.Collectors.Entries {
		registrations = append(registrations, id)
	}
	slices.Sort(registrations)
	var failures []error
	for _, s := range declared {
		def := s.Command.Definition
		for _, id := range registrations {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			r := feed.Collectors.Entries[id].Registration
			quote, ok := latest[id]
			if r.Symbol != def.Symbol || r.Feed != def.Feed || r.EvidenceClass != def.InputEvidenceClass || !ok {
				continue
			}
			c := storage.PaperDecisionCommand{ID: AutomaticPaperDecisionID(s.SHA256, id, quote), ReceiverID: owner, StrategyID: def.ID, StrategySHA256: s.SHA256, RegistrationID: id}
			call, cancel := bounded()
			_, err := store.FinalizePaperDecision(call, c, clock.Now, evaluateOwnedPaperDecision)
			cancel()
			if err != nil {
				failures = append(failures, &workerFailure{cause: err, ref: "paper-decision:" + c.ID})
			}
		}
	}
	read, cancelRead := bounded()
	defer cancelRead()
	decisions, err := store.PaperDecisions(read, evaluateOwnedPaperDecision)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	allocations, err := store.OwnedAllocations(read, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	pinned := map[string]bool{}
	for _, r := range allocations.Records {
		for _, p := range r.Command.Decisions {
			pinned[p.ID] = true
		}
	}
	for _, d := range decisions.Records {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if d.Evaluation.Action != "entry_candidate" || pinned[d.Command.ID] {
			continue
		}
		c := storage.OwnedAllocationCommand{ID: AutomaticPaperComparisonID(d), ReceiverID: owner, Decisions: []storage.OwnedDecisionPin{{ID: d.Command.ID, SHA256: d.SHA256}}}
		call, cancel := bounded()
		_, err := store.CompareOwnedAllocation(call, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		cancel()
		if err != nil {
			failures = append(failures, &workerFailure{cause: err, ref: "paper-comparison:" + c.ID})
		}
	}
	return errors.Join(failures...)
}

// latestQuotes returns each registration's most recently published quote
// recording, by publication clock and then key.
func latestQuotes(feed storage.FeedEvidenceSnapshot) map[string]storage.FeedEvidenceRecord {
	out := map[string]storage.FeedEvidenceRecord{}
	for _, r := range feed.Records {
		var recording struct {
			Capability string `json:"capability"`
		}
		if json.Unmarshal(r.Recording, &recording) != nil || recording.Capability != "latest_quotes" {
			continue
		}
		old, ok := out[r.RegistrationID]
		if !ok || r.PublishedAt.After(old.PublishedAt) || r.PublishedAt.Equal(old.PublishedAt) && r.Key > old.Key {
			out[r.RegistrationID] = r
		}
	}
	return out
}

// RunPaperDecisionWorker decides immediately, then every 15 seconds. Its
// health is reported under the paper_decision capability and grants no
// qualification.
func RunPaperDecisionWorker(ctx context.Context, store PaperDecisionWorkerStore, worker string, clock Clock) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, PaperDecisionWorkerCapability, clock, PaperDecisionWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := RunPaperDecisionCycle(ctx, store, clock)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, PaperDecisionWorkerCapability, clock, PaperDecisionWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("paper decision cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, PaperDecisionWorkerInterval); err != nil {
			return
		}
	}
}
