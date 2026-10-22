// Package storage commits observations, decisions, actions, and state together.
package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

var ErrConflict = errors.New("observation id already exists with different content")

type Store interface {
	Ingest(context.Context, domain.Observation) (domain.Receipt, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	Ping(context.Context) error
	Close()
}

func canonical(o domain.Observation) (domain.Observation, []byte, error) {
	o.OccurredAt, o.ReceivedAt = o.OccurredAt.UTC(), o.ReceivedAt.UTC()
	if err := workflow.Validate(o); err != nil {
		return o, nil, err
	}
	raw, err := json.Marshal(o)
	return o, raw, err
}

type Memory struct {
	ownedAllocations         ownedAllocationJournal
	paperDecisions           map[string]retainedPaperDecision
	paperStrategies          map[string]paperstrategy.Record
	allocationPolicies       map[string]allocation.PolicyRecord
	generationOutcomes       map[string]candidategeneration.Outcome
	generationRecordings     map[string][]byte
	generatedLinks           map[string]generatedcandidate.Link
	generatedMarkers         map[string]string
	generationIntents        map[string]candidategeneration.Intent
	generationDatasets       map[string][]byte
	candidateWorkerOutcomes  map[string]candidateevidence.OutcomeRecord
	candidateOutcomeCaptures map[string][]byte
	candidateWorkerIntents   map[string]candidateattempt.Intent
	candidates               map[string]candidatelineage.Registration
	candidateDatasets        map[string][]byte
	fixtureCapital           *fixturecapital.Book
	paperLedgers             map[string]paperledger.Rehearsal
	paperAccounts            map[string]paperaccount.Account
	switchyardAdmissions     map[string]extraction.SwitchyardAdmission
	localCaptureAdmissions   map[string]extraction.LocalAdmission
	switchyardCaptures       map[string]extraction.SwitchyardCapture
	localCaptures            map[string]extraction.LocalCapture
	extractionAdmissions     map[string]extraction.Admission
	externalAttempts         map[string]workertrial.Attempt
	externalOutputs          map[string]map[string]string
	externalJobs             map[string]workertrial.Record
	externalInputs           map[string]map[string][]byte
	trialRuns                map[string]trial.Run
	trialInputs              map[string]string
	trialResults             map[string]string
	trialAttempts            map[string]trial.Attempt
	trials                   map[string]trial.Registration
	publications             map[string]hypothesis.Publication
	reviews                  map[string]review.Attempt
	extractions              map[string]extraction.Attempt
	news                     newscase.State
	evidence                 map[string]FeedEvidenceRecord
	collectors               feedcollector.State
	receiverID               string
	mu                       sync.Mutex
	queued                   bool
	effects                  map[string]domain.ActionEffect
	state                    domain.State
	observations             map[string][]byte
	receipts                 map[string]domain.Receipt
	events                   []domain.Event
}

func NewMemory() *Memory {
	return &Memory{ownedAllocations: ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, paperDecisions: map[string]retainedPaperDecision{}, paperStrategies: map[string]paperstrategy.Record{}, allocationPolicies: map[string]allocation.PolicyRecord{}, generatedLinks: map[string]generatedcandidate.Link{}, generatedMarkers: map[string]string{}, generationOutcomes: map[string]candidategeneration.Outcome{}, generationRecordings: map[string][]byte{}, generationIntents: map[string]candidategeneration.Intent{}, generationDatasets: map[string][]byte{}, candidateWorkerOutcomes: map[string]candidateevidence.OutcomeRecord{}, candidateOutcomeCaptures: map[string][]byte{}, candidateWorkerIntents: map[string]candidateattempt.Intent{}, candidates: map[string]candidatelineage.Registration{}, candidateDatasets: map[string][]byte{}, paperLedgers: map[string]paperledger.Rehearsal{}, paperAccounts: map[string]paperaccount.Account{}, switchyardAdmissions: map[string]extraction.SwitchyardAdmission{}, switchyardCaptures: map[string]extraction.SwitchyardCapture{}, localCaptureAdmissions: map[string]extraction.LocalAdmission{}, localCaptures: map[string]extraction.LocalCapture{}, extractionAdmissions: map[string]extraction.Admission{}, extractions: map[string]extraction.Attempt{}, externalAttempts: map[string]workertrial.Attempt{}, externalOutputs: map[string]map[string]string{}, externalJobs: map[string]workertrial.Record{}, externalInputs: map[string]map[string][]byte{}, trialRuns: map[string]trial.Run{}, trialInputs: map[string]string{}, trialResults: map[string]string{}, trialAttempts: map[string]trial.Attempt{}, trials: map[string]trial.Registration{}, publications: map[string]hypothesis.Publication{}, reviews: map[string]review.Attempt{}, news: newscase.InitialState(), collectors: feedcollector.InitialState(), receiverID: "receiver-" + rand.Text(), state: workflow.InitialState(), effects: map[string]domain.ActionEffect{}, observations: map[string][]byte{}, receipts: map[string]domain.Receipt{}, events: []domain.Event{}}
}

// NewQueuedMemory exercises the persistent delivery contract without a database.
func NewQueuedMemory() *Memory { m := NewMemory(); m.queued = true; return m }

// NewMemoryFor selects a registered immutable method for an isolated replay.
// Service/PostgreSQL startup continues to select the original demo explicitly.
func NewMemoryFor(id string) (*Memory, error) {
	state, err := workflow.InitialStateFor(id)
	if err != nil {
		return nil, err
	}
	m := NewMemory()
	m.state = state
	return m, nil
}

func (m *Memory) Ingest(ctx context.Context, o domain.Observation) (domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	o, raw, err := canonical(o)
	if err != nil {
		return domain.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.observations[o.ID]; ok {
		if !bytes.Equal(previous, raw) {
			return domain.Receipt{}, ErrConflict
		}
		r := clone(m.receipts[o.ID])
		r.Duplicate = true
		return r, nil
	}
	apply := workflow.Apply
	if m.queued {
		apply = workflow.Queue
	}
	state, events, err := apply(m.state, o)
	if err != nil {
		return domain.Receipt{}, err
	}
	r := domain.Receipt{ObservationID: o.ID, Events: events}
	m.state, m.observations[o.ID], m.receipts[o.ID] = state, raw, clone(r)
	m.events = append(m.events, events...)
	return clone(r), nil
}

func (m *Memory) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(domain.Snapshot{Mode: "paper_fixture", ObservationCount: int64(len(m.observations)), State: m.state, Events: m.events}), nil
}

func clone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(raw, &result)
	return result
}

func (m *Memory) Ping(ctx context.Context) error { return ctx.Err() }
func (m *Memory) Close()                         {}
