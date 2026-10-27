package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// FeedReadinessPolicy is immutable startup configuration, not trading authority.
// Every registration is required, and IDs are scoped to the pinned receiver.
type FeedReadinessPolicy struct {
	Schema                string   `json:"schema"`
	ReceiverID            string   `json:"receiver_id"`
	RequiredRegistrations []string `json:"required_registrations"`
}

func (p FeedReadinessPolicy) validate() error {
	if p.Schema != "mantik-feed-readiness-policy-v1" || !storage.ValidReceiverID(p.ReceiverID) || len(p.RequiredRegistrations) < 1 || len(p.RequiredRegistrations) > feedcollector.MaxRegistrations {
		return errors.New("feed readiness policy requires schema v1, receiver identity and 1–16 registrations")
	}
	seen := map[string]bool{}
	for _, id := range p.RequiredRegistrations {
		if seen[id] || feedcollector.ValidateFence(feedcollector.Fence{RegistrationID: id, OwnerID: "policy", Generation: 1}) != nil {
			return errors.New("feed readiness policy requires unique valid registration identities")
		}
		seen[id] = true
	}
	return nil
}

// ReadFeedReadinessPolicy rejects unknown fields, trailing values and oversized files.
func ReadFeedReadinessPolicy(r io.Reader) (*FeedReadinessPolicy, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 16385))
	if err != nil {
		return nil, err
	}
	if len(raw) > 16384 {
		return nil, errors.New("feed readiness policy exceeds 16 KiB")
	}
	var p FeedReadinessPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, errors.New("invalid feed readiness policy JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("expected one feed readiness policy")
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// HandlerWithFeedReadiness freezes a validated policy for the handler lifetime.
// Missing registrations and receiver mismatches are reported as not ready.
func HandlerWithFeedReadiness(store storage.Store, token string, clock Clock, policy *FeedReadinessPolicy) (http.Handler, error) {
	return HandlerWithCorporateAcquisitions(store, token, clock, policy, nil)
}

// HandlerWithCorporateAcquisitions retains normal token authentication and a
// frozen feed policy; the caller owns the optional journal reader's lifetime.
func HandlerWithCorporateAcquisitions(store storage.Store, token string, clock Clock, policy *FeedReadinessPolicy, acquisitions *CorporateAcquisitionReader) (http.Handler, error) {
	if policy == nil {
		return handlerWithAcquisitions(store, token, clock, nil, acquisitions), nil
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	frozen := *policy
	frozen.RequiredRegistrations = slices.Clone(policy.RequiredRegistrations)
	slices.Sort(frozen.RequiredRegistrations)
	return handlerWithAcquisitions(store, token, clock, &frozen, acquisitions), nil
}

func feedReadiness(ctx context.Context, store storage.Store, at time.Time, policy *FeedReadinessPolicy, storageOK bool) domain.CapabilityReadiness {
	cap := domain.CapabilityReadiness{Status: "not_configured", Code: "real_feed_not_configured", EvidenceRefs: []string{}}
	if policy == nil {
		return cap
	}
	cap.Status = "unavailable"
	cap.Code = "feed_evidence_unavailable"
	cap.EvidenceRefs = append(cap.EvidenceRefs, "receiver:"+policy.ReceiverID)
	for _, id := range policy.RequiredRegistrations {
		cap.EvidenceRefs = append(cap.EvidenceRefs, "collector:"+id)
	}
	if !storageOK {
		return cap
	}
	backend, ok := store.(storage.FeedEvidenceStore)
	if !ok {
		return cap
	}
	snapshot, err := backend.FeedEvidence(ctx)
	if err != nil || ctx.Err() != nil {
		return cap
	}
	if snapshot.ReceiverID != policy.ReceiverID {
		cap.Status = "degraded"
		cap.Code = "feed_receiver_mismatch"
		return cap
	}
	for _, id := range policy.RequiredRegistrations {
		if ctx.Err() != nil {
			return cap
		}
		report, err := inspectFeedCoverage(snapshot, id, at)
		if err != nil {
			cap.Status = "degraded"
			cap.EvidenceRefs = append(cap.EvidenceRefs, "feed_requirement:"+id)
			cap.Code = "feed_evidence_invalid"
			if errors.Is(err, feedcollector.ErrNotFound) {
				cap.Code = "feed_registration_missing"
			}
			return cap
		}
		if !report.OperationalEvidenceSatisfied {
			cap.EvidenceRefs = append(cap.EvidenceRefs, "feed_requirement:"+id)
			cap.Status = "degraded"
			switch {
			case report.LeaseStatus != "lease_current":
				cap.Code = report.LeaseStatus
			case !report.Evidence.RequiredEvidenceSatisfied:
				cap.Code = report.Evidence.Session.Code
				if report.Evidence.PriceRequired {
					cap.Code = report.Evidence.Quote.Code
				}
			case report.Evidence.PriceRequired && !report.QuoteAdmissionCurrentGeneration:
				cap.Code = "quote_admission_previous_generation"
			default:
				cap.Code = "feed_clock_uncertain"
			}
			return cap
		}
	}
	if ctx.Err() != nil {
		return cap
	}
	cap.Ready = true
	cap.Status = "ready"
	cap.Code = "validated_feed_evidence_current"
	return cap
}

func applyFeedReadiness(ctx context.Context, store storage.Store, at time.Time, policy *FeedReadinessPolicy, storageOK bool, report *domain.ReadinessReport) {
	report.Capabilities["feed_coverage"] = feedReadiness(ctx, store, at, policy, storageOK)
	if report.Scope == "feed_coverage" {
		report.Ready = report.Capabilities["feed_coverage"].Ready
		report.Status = "not_ready"
		if report.Ready {
			report.Status = "ready"
		}
	}
}
