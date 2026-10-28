package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type SocialResearchDispatchCommand = domain.SocialResearchDispatchCommand
type SocialResearchDispatchReceipt struct {
	Record    domain.SocialResearchDispatch `json:"record"`
	Duplicate bool                          `json:"duplicate"`
}
type SocialResearchDispatchCatalog struct {
	ReceiverID string                          `json:"receiver_id"`
	Records    []domain.SocialResearchDispatch `json:"records"`
}
type SocialResearchDispatchStore interface {
	DispatchSocialResearch(context.Context, SocialResearchDispatchCommand, func() time.Time) (SocialResearchDispatchReceipt, error)
	SocialResearchDispatches(context.Context) (SocialResearchDispatchCatalog, error)
}

func validSocialDispatch(c SocialResearchDispatchCommand) bool {
	return ValidReceiverID(c.ReceiverID) && len(c.RequestID) <= 128 && len(c.RequestID) > 0 && decisionPin.MatchString(c.RequestSHA256)
}

// An exact journal prefix binds equal-clock edits and policy changes by retained
// sequence. Later source history never rewrites a committed dispatch receipt.
func socialDispatchPrefix(j socialwatch.Journal, head string) (socialwatch.Journal, error) {
	for i, e := range j.Entries {
		if e.SHA256 == head {
			j.Entries = j.Entries[:i+1]
			return j, nil
		}
	}
	return j, socialwatch.ErrConflict
}

func constructSocialDispatch(j socialwatch.Journal, c SocialResearchDispatchCommand, at time.Time, state *domain.RoutingState) (*domain.RoutingState, domain.SocialResearchDispatch, error) {
	fail := func(err error) (*domain.RoutingState, domain.SocialResearchDispatch, error) {
		return state, domain.SocialResearchDispatch{}, err
	}
	cat, err := socialwatch.Read(j)
	if err != nil {
		return fail(err)
	}
	if !validSocialDispatch(c) || c.ReceiverID != j.ReceiverID || !declarationTime(at) {
		return fail(socialwatch.ErrInvalid)
	}
	if len(j.Entries) == 0 || at.Before(j.Entries[len(j.Entries)-1].At) {
		return fail(socialwatch.ErrConflict)
	}
	var request *socialwatch.ResearchRequest
	for _, candidate := range cat.ResearchRequests {
		if candidate.ID == c.RequestID {
			v := candidate
			request = &v
			break
		}
	}
	if request == nil {
		return fail(socialwatch.ErrNotFound)
	}
	if request.SHA256 != c.RequestSHA256 || request.ReceiverID != c.ReceiverID || at.Before(request.RequestedAt) || request.EvidenceClass == "synthetic_unqualified" {
		return fail(socialwatch.ErrConflict)
	}
	view, err := socialwatch.InspectApplicability(j, request.CaptureID, at)
	if err != nil {
		return fail(err)
	}
	if view.Status != "current_source_assertion_dispatch_unbound" && view.Status != "copy_source_mapping_and_admission_unavailable" {
		return fail(socialwatch.ErrConflict)
	}
	var source *socialwatch.CaptureRecord
	var policy *socialwatch.PolicyRecord
	for _, e := range j.Entries {
		if e.Capture != nil && e.Capture.Command.ID == request.CaptureID {
			source = e.Capture
		}
		if e.Policy != nil && e.Policy.Command.Policy.ID == request.WatchID {
			policy = e.Policy
		}
	}
	if source == nil || policy == nil || policy.SHA256 != request.PolicySHA256 || source.Command.OriginalSHA256 != request.OriginalSHA256 || source.ClaimGroupSHA256 != request.ClaimGroupSHA256 || len(source.Source.Symbols) == 0 {
		return fail(socialwatch.ErrConflict)
	}
	r := domain.SocialResearchDispatch{Schema: "mantik-owned-social-research-dispatch-v1", Command: c, At: at.UTC(), SocialHeadSHA256: cat.HeadSHA256, Links: []domain.SocialResearchDispatchLink{}, Authority: "service_owned_unverified_assertion_research_case_only"}
	next := state
	for _, symbol := range source.Source.Symbols {
		i := domain.RoutingInput{Kind: "event", Source: routing.OwnedSocialResearchSource, Symbol: symbol, EvidenceClass: request.EvidenceClass, OccurredAt: source.Source.OccurredAt, ReceivedAt: at.UTC(), EventName: "social_research_requested", Provenance: &domain.InputProvenance{RecordKey: "social-" + workerplan.Digest([]byte(request.ID+"/"+symbol)), Revision: 1, RawRef: "social-recording:" + source.Command.ID, SHA256: source.Command.OriginalSHA256, AvailableAt: source.RetainedAt, SourceSymbol: symbol, MappingVersion: "social-asserted-symbol-v1", AccessRef: "owner-retained-unverified-social-assertion", Quality: "usable"}}
		i.ID = routing.MarketInputID(i)
		var definition domain.TriggerDefinition
		var receipt domain.RoutingReceipt
		next, definition, receipt, err = routing.DispatchSocialResearch(next, i, request.SHA256, policy.Command.Policy.MaxObservationDelaySeconds)
		if err != nil {
			return fail(err)
		}
		r.Links = append(r.Links, domain.SocialResearchDispatchLink{Symbol: symbol, DefinitionID: definition.ID, InputID: i.ID, CaseID: receipt.Matches[0].CaseID})
	}
	r.SHA256 = workerplan.Hash(r)
	return next, r, nil
}

func socialDispatchCatalog(owner string, j socialwatch.Journal, state *domain.RoutingState) (SocialResearchDispatchCatalog, error) {
	out := SocialResearchDispatchCatalog{ReceiverID: owner, Records: []domain.SocialResearchDispatch{}}
	if _, err := socialwatch.Read(j); err != nil {
		return out, err
	}
	if owner != j.ReceiverID || state == nil || len(state.SocialDispatches) > 32 {
		return out, socialwatch.ErrInvalid
	}
	seen := map[string]bool{}
	inputs, definitions, cases := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var last time.Time
	for _, r := range state.SocialDispatches {
		if r.Command.ReceiverID != owner || seen[r.Command.RequestID] || r.At.Before(last) {
			return out, socialwatch.ErrInvalid
		}
		prefix, err := socialDispatchPrefix(j, r.SocialHeadSHA256)
		if err != nil {
			return out, err
		}
		expectedState, expected, err := constructSocialDispatch(prefix, r.Command, r.At, routing.NewState())
		if err != nil || !reflect.DeepEqual(expected, r) {
			return out, socialwatch.ErrInvalid
		}
		for _, link := range r.Links {
			actual, ok := state.Inputs[link.InputID]
			want := expectedState.Inputs[link.InputID]
			if !ok || !reflect.DeepEqual(actual.Input, want.Input) || actual.Receipt.InputID != link.InputID || actual.Receipt.Duplicate || !reflect.DeepEqual(actual.Receipt.Matches, want.Receipt.Matches) {
				return out, socialwatch.ErrInvalid
			}
			foundDefinition, foundCase := false, false
			for _, d := range state.Definitions {
				if d.ID == link.DefinitionID {
					for _, v := range expectedState.Definitions {
						if v.ID == d.ID {
							foundDefinition = reflect.DeepEqual(v, d)
						}
					}
				}
			}
			for _, c := range state.Cases {
				if c.ID != link.CaseID {
					continue
				}
				if c.Status != "open" && c.Status != "closed" || c.UpdatedAt.Before(r.At) {
					return out, socialwatch.ErrInvalid
				}
				if c.Status == "closed" {
					c.Status, c.UpdatedAt = "open", r.At
				}
				for _, want := range expectedState.Cases {
					if want.ID == c.ID {
						foundCase = reflect.DeepEqual(c, want)
					}
				}
			}
			if !foundDefinition || !foundCase {
				return out, socialwatch.ErrInvalid
			}
			inputs[link.InputID], definitions[link.DefinitionID], cases[link.CaseID] = true, true, true
		}
		seen[r.Command.RequestID], last = true, r.At
		out.Records = append(out.Records, clone(r))
	}
	// Removing only the dispatch marker must not recreate already-issued work.
	for id, i := range state.Inputs {
		if i.Input.Source == routing.OwnedSocialResearchSource && !inputs[id] {
			return out, socialwatch.ErrInvalid
		}
	}
	for _, d := range state.Definitions {
		if (d.Owner == routing.OwnedSocialResearchSource || d.Requirements.Source == routing.OwnedSocialResearchSource) && !definitions[d.ID] {
			return out, socialwatch.ErrInvalid
		}
	}
	for _, c := range state.Cases {
		if c.Owner == routing.OwnedSocialResearchSource && !cases[c.ID] {
			return out, socialwatch.ErrInvalid
		}
	}
	return out, nil
}

func appendSocialDispatch(owner string, j socialwatch.Journal, state *domain.RoutingState, c SocialResearchDispatchCommand, now func() time.Time) (*domain.RoutingState, SocialResearchDispatchReceipt, error) {
	cat, err := socialDispatchCatalog(owner, j, state)
	if err != nil {
		return state, SocialResearchDispatchReceipt{}, err
	}
	if !validSocialDispatch(c) || c.ReceiverID != owner {
		return state, SocialResearchDispatchReceipt{}, socialwatch.ErrInvalid
	}
	for _, r := range cat.Records {
		if r.Command.RequestID == c.RequestID {
			if r.Command != c {
				return state, SocialResearchDispatchReceipt{}, socialwatch.ErrConflict
			}
			return state, SocialResearchDispatchReceipt{Record: r, Duplicate: true}, nil
		}
	}
	if len(cat.Records) >= 32 {
		return state, SocialResearchDispatchReceipt{}, socialwatch.ErrCapacity
	}
	at := now().UTC()
	if len(cat.Records) > 0 && at.Before(cat.Records[len(cat.Records)-1].At) {
		return state, SocialResearchDispatchReceipt{}, socialwatch.ErrConflict
	}
	next, record, err := constructSocialDispatch(j, c, at, state)
	if err != nil {
		return state, SocialResearchDispatchReceipt{}, err
	}
	next.SocialDispatches = append(next.SocialDispatches, record)
	if _, err = socialDispatchCatalog(owner, j, next); err != nil {
		return state, SocialResearchDispatchReceipt{}, err
	}
	return next, SocialResearchDispatchReceipt{Record: record}, nil
}

func (m *Memory) DispatchSocialResearch(ctx context.Context, c SocialResearchDispatchCommand, now func() time.Time) (SocialResearchDispatchReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SocialResearchDispatchReceipt{}, err
	}
	j, err := m.socialJournalLocked()
	if err != nil {
		return SocialResearchDispatchReceipt{}, err
	}
	next, receipt, err := appendSocialDispatch(m.receiverID, j, m.state.Routing, c, now)
	if err != nil {
		return SocialResearchDispatchReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return SocialResearchDispatchReceipt{}, err
	}
	if !receipt.Duplicate {
		m.state.Routing = next
	}
	return clone(receipt), nil
}
func (m *Memory) SocialResearchDispatches(ctx context.Context) (SocialResearchDispatchCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SocialResearchDispatchCatalog{}, err
	}
	j, err := m.socialJournalLocked()
	if err != nil {
		return SocialResearchDispatchCatalog{}, err
	}
	return socialDispatchCatalog(m.receiverID, j, m.state.Routing)
}

func (p *Postgres) socialDispatchAccess(ctx context.Context, c *SocialResearchDispatchCommand, now func() time.Time) (SocialResearchDispatchCatalog, SocialResearchDispatchReceipt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Preserve the collector -> state -> receiver order. The shared state row
	// commits the source-bound receipt, routing inputs and cases atomically.
	feed, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	if owner != feed.ReceiverID {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, ErrReceiverMismatch
	}
	j, err := readSocialJournal(ctx, tx, owner)
	if err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	var state domain.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	var receipt SocialResearchDispatchReceipt
	if c != nil {
		state.Routing, receipt, err = appendSocialDispatch(owner, j, state.Routing, *c, now)
		if err != nil {
			return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
		}
		if !receipt.Duplicate {
			raw, err = json.Marshal(state)
			if err != nil {
				return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
			}
			if _, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
				return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
			}
		}
	}
	cat, err := socialDispatchCatalog(owner, j, state.Routing)
	if err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SocialResearchDispatchCatalog{}, SocialResearchDispatchReceipt{}, err
	}
	return cat, receipt, nil
}
func (p *Postgres) DispatchSocialResearch(ctx context.Context, c SocialResearchDispatchCommand, now func() time.Time) (SocialResearchDispatchReceipt, error) {
	_, r, err := p.socialDispatchAccess(ctx, &c, now)
	return r, err
}
func (p *Postgres) SocialResearchDispatches(ctx context.Context) (SocialResearchDispatchCatalog, error) {
	c, _, err := p.socialDispatchAccess(ctx, nil, nil)
	return c, err
}
