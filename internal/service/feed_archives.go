package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Validate normalized publication/admission custody at the archived cut. Lease
// status may be expired or stopped; archiving never revives ownership/readiness.
func validateFeedArchiveSource(s storage.FeedEvidenceSnapshot, at time.Time) error {
	if !storage.ValidReceiverID(s.ReceiverID) || at.IsZero() || s.Collectors.LastClock.After(at) {
		return storage.ErrFeedArchiveInvalid
	}
	for id, entry := range s.Collectors.Entries {
		registration := entry.Registration
		if entry.Registration.ID != id || entry.RegisteredAt.IsZero() || entry.RegisteredAt.After(at) || feedcollector.Validate(feedcollector.Command{RequestID: "archive-validation", Operation: "register", RegistrationID: id, Registration: &registration}) != nil {
			return storage.ErrFeedArchiveConflict
		}
		if _, err := inspectFeedCoverage(s, id, at); err != nil {
			return err
		}
	}
	for id, command := range s.Collectors.Commands {
		if command.Command.RequestID != id || command.Receipt.RequestID != id || command.Receipt.Operation != command.Command.Operation || feedcollector.Validate(command.Command) != nil || command.Receipt.At.IsZero() || command.Receipt.At.After(at) {
			return storage.ErrFeedArchiveConflict
		}
	}
	for id, admission := range s.Collectors.Admissions {
		if feedcollector.ValidateFence(admission.Fence) != nil || admission.At.IsZero() || admission.At.After(at) || s.Routing == nil {
			return storage.ErrFeedArchiveConflict
		}
		input, ok := s.Routing.Inputs[id]
		if _, registered := s.Collectors.Entries[admission.Fence.RegistrationID]; !registered || !ok || input.Input.ID != id || admission.InputSHA256 != feedcollector.InputDigest(input.Input) || !reflect.DeepEqual(admission.Receipt, input.Receipt) {
			return storage.ErrFeedArchiveConflict
		}
	}
	for key, publication := range s.Records {
		if _, ok := s.Collectors.Entries[publication.RegistrationID]; !ok || key != publication.RegistrationID+":"+publication.Key || publication.PublishedAt.After(at) {
			return storage.ErrFeedArchiveConflict
		}
	}
	return nil
}

func feedArchiveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrFeedArchiveNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrFeedArchiveInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrFeedArchiveConflict), errors.Is(err, storage.ErrFeedArchiveCapacity),
		errors.Is(err, storage.ErrPaperDecisionInvalid), errors.Is(err, storage.ErrPaperDecisionConflict),
		errors.Is(err, storage.ErrPaperOutcomeInvalid), errors.Is(err, storage.ErrPaperOutcomeConflict):
		write(w, 409, map[string]string{"error": err.Error()})
	default:
		feedEvidenceError(w, err)
	}
}

func feedArchiveRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.FeedArchiveStore)
	if !ok {
		return
	}
	create := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		defer func() { _ = r.Body.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		var command struct {
			ReceiverID string `json:"receiver_id"`
		}
		if err != nil || workerplan.ReadBoundedShape(raw, &command, 4096) != nil {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var receipt storage.FeedArchiveReceipt
		if r.Pattern == "POST /v1/feed-archives/rotate" {
			retention := store.(storage.FeedRetentionStore)
			receipt, err = retention.RotateFeed(ctx, command.ReceiverID, clock.Now, validateFeedArchiveSource, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		} else if paper, ok := store.(storage.PaperFeedArchiveStore); ok {
			receipt, err = paper.ArchivePaperFeed(ctx, command.ReceiverID, clock.Now, validateFeedArchiveSource, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		} else {
			receipt, err = backend.ArchiveFeed(ctx, command.ReceiverID, clock.Now, validateFeedArchiveSource)
		}
		if err != nil {
			feedArchiveError(w, err)
			return
		}
		status := http.StatusCreated
		if receipt.Duplicate {
			status = http.StatusOK
		}
		write(w, status, receipt)
	}
	mux.HandleFunc("POST /v1/feed-archives", create)
	if _, ok := store.(storage.FeedRetentionStore); ok {
		mux.HandleFunc("POST /v1/feed-archives/rotate", create)
	}
	mux.HandleFunc("GET /v1/feed-archives", func(w http.ResponseWriter, r *http.Request) {
		values, queryErr := url.ParseQuery(r.URL.RawQuery)
		if queryErr != nil || r.ContentLength != 0 {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		for key, value := range values {
			if (key != "after" && key != "limit") || len(value) != 1 {
				feedArchiveError(w, storage.ErrFeedArchiveInvalid)
				return
			}
		}
		after, limit := int64(0), 16
		var err error
		if values.Has("after") {
			after, err = strconv.ParseInt(values.Get("after"), 10, 64)
		}
		if err == nil && values.Has("limit") {
			limit, err = strconv.Atoi(values.Get("limit"))
		}
		if err != nil {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		page, err := backend.FeedArchives(ctx, after, limit)
		if err != nil {
			feedArchiveError(w, err)
			return
		}
		write(w, 200, page)
	})
	if audit, ok := store.(storage.PaperFeedAuditStore); ok {
		mux.HandleFunc("GET /v1/feed-archives/paper-custody-audit", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
				feedArchiveError(w, storage.ErrFeedArchiveInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
			defer cancel()
			result, err := audit.AuditPaperFeed(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				feedArchiveError(w, err)
				return
			}
			write(w, 200, result)
		})
	}
	if paper, ok := store.(storage.PaperFeedArchiveStore); ok {
		mux.HandleFunc("GET /v1/feed-archives/paper-custody/{sequence}", func(w http.ResponseWriter, r *http.Request) {
			sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
			if err != nil || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
				feedArchiveError(w, storage.ErrFeedArchiveInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
			defer cancel()
			checkpoint, err := paper.PaperFeedCheckpoint(ctx, sequence, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				feedArchiveError(w, err)
				return
			}
			write(w, 200, checkpoint)
		})
	}
	mux.HandleFunc("GET /v1/feed-archives/{sequence}", func(w http.ResponseWriter, r *http.Request) {
		sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
		if err != nil || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		archive, err := backend.FeedArchive(ctx, sequence)
		if err == nil {
			err = validateFeedArchiveSource(archive.Source, archive.ArchivedAt)
		}
		if err != nil {
			feedArchiveError(w, err)
			return
		}
		write(w, 200, archive)
	})
}
