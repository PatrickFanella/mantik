package service

import (
	"context"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"log/slog"
	"time"
)

const SocialResearchWorkerCapability = "social_research_dispatch"
const SocialResearchWorkerInterval = 30 * time.Second
const MaxSocialResearchDispatchesPerCycle = 8

type SocialResearchWorkerStore interface {
	storage.SocialWatchStore
	storage.SocialResearchDispatchStore
}

// The process opt-in consumes only already-requested research work. Every
// dispatch rechecks current source applicability in the source owner's atomic
// transaction. No content, case, provider attestation or financial grant is supplied.
func RunSocialResearchCycle(ctx context.Context, store SocialResearchWorkerStore, clock Clock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if clock == nil {
		return socialwatch.ErrInvalid
	}
	at := clock.Now().UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return socialwatch.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	retained, err := store.SocialHistory(ctx)
	if err != nil {
		return err
	}
	journal := socialwatch.Journal{Schema: socialwatch.Schema, ReceiverID: retained.ReceiverID, Entries: retained.History}
	history, err := socialwatch.Read(journal)
	if err != nil {
		return err
	}
	dispatched, err := store.SocialResearchDispatches(ctx)
	if err != nil {
		return err
	}
	if dispatched.ReceiverID != history.ReceiverID {
		return storage.ErrReceiverMismatch
	}
	done := map[string]string{}
	for _, r := range dispatched.Records {
		done[r.Command.RequestID] = r.Command.RequestSHA256
	}
	// Rotate the bounded attempt window each polling interval. A permanently
	// failing prefix cannot monopolize every cycle ahead of unrelated requests.
	requests := history.ResearchRequests
	if len(requests) > 0 {
		slot := at.Unix() / int64(SocialResearchWorkerInterval/time.Second)
		start := int((slot * MaxSocialResearchDispatchesPerCycle) % int64(len(requests)))
		if start < 0 {
			start += len(requests)
		}
		ordered := make([]socialwatch.ResearchRequest, 0, len(requests))
		ordered = append(ordered, requests[start:]...)
		ordered = append(ordered, requests[:start]...)
		requests = ordered
	}
	var failures []error
	attempts := 0
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if pin, ok := done[request.ID]; ok {
			if pin != request.SHA256 {
				return socialwatch.ErrConflict
			}
			continue
		}
		if request.EvidenceClass == "synthetic_unqualified" {
			continue
		}
		view, err := socialwatch.InspectApplicability(journal, request.CaptureID, at)
		if err != nil {
			return err
		}
		if view.Status != "current_source_assertion_dispatch_unbound" && view.Status != "copy_source_mapping_and_admission_unavailable" {
			continue
		}
		if attempts >= MaxSocialResearchDispatchesPerCycle {
			break
		}
		attempts++
		c := storage.SocialResearchDispatchCommand{ReceiverID: history.ReceiverID, RequestID: request.ID, RequestSHA256: request.SHA256}
		receipt, err := store.DispatchSocialResearch(ctx, c, func() time.Time {
			now := clock.Now().UTC()
			if now.Before(at) {
				return time.Time{}
			}
			return now
		})
		if err == nil {
			r := receipt.Record
			if r.Command != c || r.At.IsZero() || len(r.Links) == 0 || r.ProviderAccessVerified || r.IndependentEvidenceCredit || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
				err = socialwatch.ErrConflict
			}
		}
		if err != nil {
			ref := "social-research-request:" + request.ID
			failures = append(failures, &workerFailure{cause: fmt.Errorf("%s: %w", ref, err), ref: ref})
		}
	}
	return errors.Join(failures...)
}

// Recover immediately on start. Health describes dispatch only; it does not
// assert provider availability, completed research or financial protection.
func RunSocialResearchWorker(ctx context.Context, store SocialResearchWorkerStore, worker string, clock Clock) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, SocialResearchWorkerCapability, clock, SocialResearchWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := RunSocialResearchCycle(ctx, store, clock)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, SocialResearchWorkerCapability, clock, SocialResearchWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("social research dispatch cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, SocialResearchWorkerInterval); err != nil {
			return
		}
	}
}
