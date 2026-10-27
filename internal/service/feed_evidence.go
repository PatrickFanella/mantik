package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func recordingKey(r alpaca.Recording) string { return fmt.Sprintf("%s-r%d", r.CaptureID, r.Revision) }

// Validate raw capture and normalized history independently of a client's
// coverage boolean. A calendar with no sessions still has a validated range.
func validateFeedRecording(s storage.FeedEvidenceSnapshot, registration string, r alpaca.Recording) error {
	// This publication owner is limited to admitted quote/calendar evidence.
	// New source-custody normalizers must not implicitly widen its contract.
	if r.Capability != "latest_quotes" && r.Capability != "market_calendar" {
		return storage.ErrEvidenceInvalid
	}
	e, exists := s.Collectors.Entries[registration]
	if !exists {
		return feedcollector.ErrNotFound
	}
	spec := e.Registration
	if len(r.Symbols) != 1 || r.Symbols[0] != spec.Symbol || r.EvidenceClass != spec.EvidenceClass || r.AccessRef != spec.AccessRef || r.RawRef != fmt.Sprintf("journal:%s:capture-%s-r%d.json", spec.JournalID, r.CaptureID, r.Revision) || (r.Capability == "latest_quotes" && r.Feed != spec.Feed) {
		return storage.ErrEvidenceInvalid
	}
	normalized, err := alpaca.Normalize(r)
	if err != nil {
		return errors.Join(storage.ErrEvidenceInvalid, err)
	}
	for _, i := range normalized.Inputs {
		if s.Routing == nil {
			return storage.ErrEvidenceConflict
		}
		old, ok := s.Routing.Inputs[i.ID]
		if !ok || !reflect.DeepEqual(old.Input, i) {
			return storage.ErrEvidenceConflict
		}
		a, ok := s.Collectors.Admissions[i.ID]
		if !ok || a.Fence.RegistrationID != registration || a.InputSHA256 != feedcollector.InputDigest(i) || !reflect.DeepEqual(old.Receipt, a.Receipt) {
			return storage.ErrEvidenceConflict
		}
	}
	return nil
}

func publicationCheck(registration string, f feedcollector.Fence, r alpaca.Recording) storage.EvidenceCheck {
	return func(s storage.FeedEvidenceSnapshot, at time.Time, previous *storage.FeedEvidenceRecord) error {
		if err := validateFeedRecording(s, registration, r); err != nil {
			return err
		}
		if previous != nil && previous.Fence == f {
			return nil
		}
		if at.IsZero() || at.Before(s.Collectors.LastClock) {
			return feedcollector.ErrClockBackwards
		}
		if r.ReceivedAt.After(at) {
			return storage.ErrEvidenceInvalid
		}
		e := s.Collectors.Entries[registration]
		if e.Lease == nil || e.Lease.Stopped || e.Lease.OwnerID != f.OwnerID || e.Lease.Generation != f.Generation || !at.Before(e.Lease.ExpiresAt) {
			return feedcollector.ErrLeaseLost
		}
		return nil
	}
}

type serverFeedCoverage struct {
	ReceiverID                      string                        `json:"receiver_id"`
	RegistrationID                  string                        `json:"registration_id"`
	Evidence                        alpaca.CoverageReport         `json:"evidence"`
	LeaseStatus                     string                        `json:"lease_status"`
	QuoteAdmissionCurrentGeneration bool                          `json:"quote_admission_current_generation"`
	OperationalEvidenceSatisfied    bool                          `json:"operational_evidence_satisfied"`
	CollectorLivenessVerified       bool                          `json:"collector_liveness_verified"`
	RequiredByReadinessPolicy       bool                          `json:"required_by_readiness_policy"`
	GlobalReadinessConfigured       bool                          `json:"global_readiness_configured"`
	QualificationCredit             bool                          `json:"qualification_credit"`
	Publications                    []storage.FeedEvidenceReceipt `json:"publications"`
}

func inspectFeedCoverage(s storage.FeedEvidenceSnapshot, id string, at time.Time) (serverFeedCoverage, error) {
	out := serverFeedCoverage{ReceiverID: s.ReceiverID, RegistrationID: id, Publications: []storage.FeedEvidenceReceipt{}}
	e, ok := s.Collectors.Entries[id]
	if !ok {
		return out, feedcollector.ErrNotFound
	}
	records := []alpaca.Recording{}
	publishedInputs := map[string]bool{}
	keys := make([]string, 0, len(s.Records))
	for key := range s.Records {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		published := s.Records[key]
		if published.RegistrationID != id {
			continue
		}
		var r alpaca.Recording
		if err := json.Unmarshal(published.Recording, &r); err != nil {
			return out, storage.ErrEvidenceInvalid
		}
		if published.Key != recordingKey(r) || published.Fence.RegistrationID != id || feedcollector.ValidateFence(published.Fence) != nil || published.PublishedAt.IsZero() || published.PublishedAt.Before(r.ReceivedAt) {
			return out, storage.ErrEvidenceConflict
		}
		if err := validateFeedRecording(s, id, r); err != nil {
			return out, err
		}
		records = append(records, r)
		normalized, _ := alpaca.Normalize(r)
		for _, input := range normalized.Inputs {
			publishedInputs[input.ID] = true
		}
		var value any
		_ = json.Unmarshal(published.Recording, &value)
		canonical, _ := json.Marshal(value)
		out.Publications = append(out.Publications, storage.FeedEvidenceReceipt{RegistrationID: id, Key: published.Key, Fence: published.Fence, PublishedAt: published.PublishedAt, RecordingSHA256: alpaca.Digest(string(canonical))})
	}
	spec := alpaca.CoverageSpec{Feed: e.Registration.Feed, Symbol: e.Registration.Symbol, EvidenceClass: e.Registration.EvidenceClass, MaxQuoteAgeSeconds: 60, MaxCaptureAgeSeconds: 300, MaxCalendarAgeSeconds: 86400}
	report, err := alpaca.CoverageFromRecordings(spec, at, records)
	if err != nil {
		return out, err
	}
	report.Scope = "server_validated_regular_session_evidence"
	// Newer admitted evidence whose raw capture is not yet published must not
	// allow an older published good quote/calendar to receive coverage credit.
	if s.Routing != nil {
		for inputID, admission := range s.Collectors.Admissions {
			if admission.Fence.RegistrationID != id || publishedInputs[inputID] {
				continue
			}
			retained, exists := s.Routing.Inputs[inputID]
			if !exists {
				return out, storage.ErrEvidenceConflict
			}
			if admission.InputSHA256 != feedcollector.InputDigest(retained.Input) || !reflect.DeepEqual(admission.Receipt, retained.Receipt) {
				return out, storage.ErrEvidenceConflict
			}
			input := retained.Input
			if input.Symbol != e.Registration.Symbol || input.EvidenceClass != e.Registration.EvidenceClass {
				continue
			}
			if input.Source == "alpaca-regular-session" && !input.ReceivedAt.Before(report.Session.ReceivedAt) {
				report.Session.Status = "unknown"
				report.Session.Code = "calendar_publication_pending"
				report.Quote.RegularSessionEligible = false
				report.RequiredEvidenceSatisfied = false
				report.PriceRequired = false
			}
			if input.Source == "alpaca-"+strings.ReplaceAll(e.Registration.Feed, "_", "-") && !input.ReceivedAt.Before(report.Quote.ReceivedAt) {
				report.Quote.Usable = false
				report.Quote.RegularSessionEligible = false
				report.Quote.Code = "quote_publication_pending"
				if report.PriceRequired {
					report.RequiredEvidenceSatisfied = false
				}
			}
		}
	}
	out.Evidence = report
	out.LeaseStatus = feedcollector.LeaseStatus(e, s.Collectors.LastClock, at)
	if a, ok := s.Collectors.Admissions[report.Quote.InputID]; ok && e.Lease != nil {
		out.QuoteAdmissionCurrentGeneration = a.Fence.RegistrationID == id && a.Fence.OwnerID == e.Lease.OwnerID && a.Fence.Generation == e.Lease.Generation
	}
	clockOK := !at.Before(s.Collectors.LastClock)
	for _, p := range out.Publications {
		if p.PublishedAt.After(at) {
			clockOK = false
		}
	}
	out.OperationalEvidenceSatisfied = clockOK && out.LeaseStatus == "lease_current" && report.RequiredEvidenceSatisfied && (!report.PriceRequired || out.QuoteAdmissionCurrentGeneration)
	return out, nil
}

func feedEvidenceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, feedcollector.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrReceiverMismatch), errors.Is(err, storage.ErrEvidenceConflict), errors.Is(err, storage.ErrEvidenceCapacity), errors.Is(err, feedcollector.ErrClockBackwards), errors.Is(err, feedcollector.ErrLeaseLost):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrEvidenceInvalid), errors.Is(err, feedcollector.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	default:
		failed(w, err)
	}
}

func feedEvidenceRoutes(mux *http.ServeMux, store storage.Store, clock Clock, policy *FeedReadinessPolicy) {
	backend, ok := store.(storage.FeedEvidenceStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/feed-evidence", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2*alpaca.MaxBytes)
		defer func() { _ = r.Body.Close() }()
		var body struct {
			ReceiverID string              `json:"receiver_id"`
			Fence      feedcollector.Fence `json:"fence"`
			Recording  alpaca.Recording    `json:"recording"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			write(w, 400, map[string]string{"error": "invalid feed evidence JSON"})
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			write(w, 400, map[string]string{"error": "expected one feed evidence object"})
			return
		}
		if _, err := alpaca.Normalize(body.Recording); err != nil {
			feedEvidenceError(w, errors.Join(storage.ErrEvidenceInvalid, err))
			return
		}
		raw, err := json.Marshal(body.Recording)
		if err != nil {
			feedEvidenceError(w, storage.ErrEvidenceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.PublishFeedEvidence(ctx, body.ReceiverID, body.Fence.RegistrationID, recordingKey(body.Recording), body.Fence, raw, clock.Now, publicationCheck(body.Fence.RegistrationID, body.Fence, body.Recording))
		if err != nil {
			feedEvidenceError(w, err)
			return
		}
		write(w, 200, receipt)
	})
	mux.HandleFunc("GET /v1/feed-collectors/{id}/coverage", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			write(w, 400, map[string]string{"error": "coverage uses fixed server age limits and the current service clock"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		snapshot, err := backend.FeedEvidence(ctx)
		if err != nil {
			feedEvidenceError(w, err)
			return
		}
		report, err := inspectFeedCoverage(snapshot, r.PathValue("id"), clock.Now())
		if err != nil {
			feedEvidenceError(w, err)
			return
		}
		report.GlobalReadinessConfigured = policy != nil
		report.RequiredByReadinessPolicy = policy != nil && policy.ReceiverID == snapshot.ReceiverID && slices.Contains(policy.RequiredRegistrations, r.PathValue("id"))
		write(w, 200, report)
	})
}
