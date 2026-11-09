package storage

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

// An index hit is a historical retry only when the owner explicitly retired
// that identity. Missing active/kept evidence remains corruption.
func retiredArchiveSource(retirement *FeedRetention, source FeedArchivedSource) error {
	if retirement == nil || retirement.Validate() != nil || source.Archive.ReceiverID != retirement.ReceiverID || source.Archive.Sequence > retirement.Checkpoint.Archive.Sequence {
		return ErrFeedArchiveConflict
	}
	key := archivedSourceKey(source.Kind, source.ID)
	_, kept := slices.BinarySearchFunc(retirement.Kept, key, func(p FeedKeptSource, k string) int { return strings.Compare(archivedSourceKey(p.Kind, p.ID), k) })
	if kept {
		return ErrFeedArchiveConflict
	}
	return nil
}

func retiredCollectorCommand(r *FeedRetention, source FeedArchivedSource, c feedcollector.Command) (feedcollector.Receipt, error) {
	if err := retiredArchiveSource(r, source); err != nil {
		return feedcollector.Receipt{}, err
	}
	var old feedcollector.RetainedCommand
	if json.Unmarshal(source.Payload, &old) != nil || !reflect.DeepEqual(old.Command, c) {
		return feedcollector.Receipt{}, feedcollector.ErrConflict
	}
	return clone(old.Receipt), nil
}

func retiredCollectorAdmission(r *FeedRetention, source FeedArchivedSource, original FeedEvidenceSnapshot, f feedcollector.Fence, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	if err := retiredArchiveSource(r, source); err != nil {
		return domain.RoutingReceipt{}, err
	}
	replay, err := feedcollector.CheckSubmission(original.Collectors, f, i, time.Time{})
	if err != nil {
		return domain.RoutingReceipt{}, err
	}
	if !replay {
		return domain.RoutingReceipt{}, feedcollector.ErrConflict
	}
	return collectorReplay(original.Collectors, i, original.Routing)
}

func retiredPublication(r *FeedRetention, source FeedArchivedSource, original, current FeedEvidenceSnapshot, registration, key string, f feedcollector.Fence, raw json.RawMessage, now func() time.Time, check EvidenceCheck) (FeedEvidenceReceipt, error) {
	if err := retiredArchiveSource(r, source); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	previous, err := evidencePrevious(original.Records, registration, key, raw)
	if err != nil {
		return FeedEvidenceReceipt{}, err
	}
	if previous == nil || check == nil {
		return FeedEvidenceReceipt{}, ErrEvidenceConflict
	}
	at := previous.PublishedAt
	if previous.Fence != f {
		// A different current fence still needs a live lease check; it cannot
		// change the old publication's original fence or receipt.
		if now == nil {
			return FeedEvidenceReceipt{}, ErrEvidenceInvalid
		}
		at = now().UTC()
		original.Collectors.Entries[registration] = current.Collectors.Entries[registration]
		original.Collectors.LastClock = current.Collectors.LastClock
	}
	if err := check(original, at, previous); err != nil {
		return FeedEvidenceReceipt{}, err
	}
	return evidenceReceipt(*previous, true), nil
}
