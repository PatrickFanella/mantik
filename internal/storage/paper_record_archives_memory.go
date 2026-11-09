package storage

import (
	"context"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func (m *Memory) paperArchiveOwnersLocked() paperArchiveOwners {
	return paperArchiveOwners{m.paperDecisions, m.paperOutcomes, m.evaluationAssessments, m.decisionReviews}
}

func (m *Memory) ArchivePaperRecords(ctx context.Context, owner string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchiveReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(owner) || de == nil || oe == nil {
		return PaperRecordArchiveReceipt{}, ErrPaperRecordArchiveInvalid
	}
	if owner != m.receiverID {
		return PaperRecordArchiveReceipt{}, ErrReceiverMismatch
	}
	if err := ctx.Err(); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	sources, err := m.paperEvaluationSourcesLocked(de, oe)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	owners := m.paperArchiveOwnersLocked()
	social, err := m.decisionReviewSocialSourcesLocked(DecisionReviewCommand{}, owners.reviews)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	c := paperArchiveContext(sources, social)
	if err := verifyPaperArchiveOwners(ctx, c, owners, de, oe, m.portfolioEvaluator, m.feedWindowReaderLocked(ctx, owner)); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	records, err := owners.records(m.paperRecordEpochs)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	var previous PaperRecordArchiveHeader
	if len(m.paperRecordArchives) > 0 {
		old, err := m.paperRecordArchiveLocked(ctx, int64(len(m.paperRecordArchives)), de, oe)
		if err != nil {
			return PaperRecordArchiveReceipt{}, err
		}
		previous = old.PaperRecordArchiveHeader
		if err := paperArchiveExtends(old, records); err != nil {
			return PaperRecordArchiveReceipt{}, err
		}
		if workerplan.Hash(paperArchivePins(records)) == previous.RecordsSHA256 {
			return PaperRecordArchiveReceipt{previous, true}, nil
		}
	}
	a, err := newPaperRecordArchive(c, records, previous, now)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if err := checkPaperArchiveEpochs(a.Sequence, records, m.paperRecordEpochs, m.paperRecordRetiredEpochs); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	// All replay and construction can fail before these owner mutations.
	m.paperRecordArchives = append(m.paperRecordArchives, clone(a))
	m.paperArchiveMembers = append(m.paperArchiveMembers, clone(paperArchivePins(records)))
	m.paperArchiveSeals = append(m.paperArchiveSeals, a.PaperRecordArchiveHeader)
	if m.paperArchiveOriginals == nil {
		m.paperArchiveOriginals = map[string]int64{}
	}
	for _, r := range records {
		key := r.Kind + ":" + r.ID
		if _, exists := m.paperArchiveOriginals[key]; !exists {
			m.paperArchiveOriginals[key] = a.Sequence
		}
	}
	return PaperRecordArchiveReceipt{a.PaperRecordArchiveHeader, false}, nil
}

func (m *Memory) paperRecordArchivePayloadLocked(sequence int64) (PaperRecordArchive, error) {
	if sequence < 1 {
		return PaperRecordArchive{}, ErrPaperRecordArchiveInvalid
	}
	if sequence > int64(len(m.paperRecordArchives)) {
		return PaperRecordArchive{}, ErrPaperRecordArchiveNotFound
	}
	a := clone(m.paperRecordArchives[sequence-1])
	if a.Validate() != nil || a.Sequence != sequence || a.ReceiverID != m.receiverID || len(m.paperArchiveMembers) < int(sequence) || len(m.paperArchiveSeals) < int(sequence) || m.paperArchiveSeals[sequence-1] != a.PaperRecordArchiveHeader || !reflect.DeepEqual(paperArchivePins(a.Records), m.paperArchiveMembers[sequence-1]) {
		return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
	}
	if sequence > 1 {
		previous := m.paperRecordArchives[sequence-2].PaperRecordArchiveHeader
		if previous.Validate() != nil || previous.SHA256 != a.PreviousSHA256 {
			return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
		}
	}
	if err := checkPaperArchiveEpochs(sequence, a.Records, m.paperRecordEpochs, m.paperRecordRetiredEpochs); err != nil {
		return PaperRecordArchive{}, err
	}
	for _, r := range a.Records {
		key := r.Kind + ":" + r.ID
		original := m.paperArchiveOriginals[key]
		if m.paperRecordEpochs[key] != r.PaperRecordEpoch || original != r.CreatedEpoch+1 || original > sequence {
			return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
		}
	}
	return a, nil
}

func (m *Memory) paperRecordArchiveLocked(ctx context.Context, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchive, error) {
	if de == nil || oe == nil {
		return PaperRecordArchive{}, ErrPaperRecordArchiveInvalid
	}
	a, err := m.paperRecordArchivePayloadLocked(sequence)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	view, err := m.feedCustodyLocked(a.Context.Feed.Retention)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	a.Context.Feed.custody = view
	owners, err := decodePaperArchiveOwners(a.Records)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	if err := verifyPaperArchiveOwners(ctx, a.Context, owners, de, oe, m.portfolioEvaluator, m.feedWindowReaderLocked(ctx, m.receiverID)); err != nil {
		return PaperRecordArchive{}, err
	}
	return a, nil
}

func (m *Memory) PaperRecordArchive(ctx context.Context, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchive, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperRecordArchive{}, err
	}
	a, err := m.paperRecordArchiveLocked(ctx, sequence, de, oe)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	return clone(a), nil
}

func (m *Memory) PaperArchivedRecord(ctx context.Context, kind, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperArchivedRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !paperRecordKind(kind) || !paperaccount.ID.MatchString(id) {
		return PaperArchivedRecord{}, ErrPaperRecordArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return PaperArchivedRecord{}, err
	}
	sequence, ok := m.paperArchiveOriginals[kind+":"+id]
	if !ok {
		if e, reserved := m.paperRecordEpochs[kind+":"+id]; reserved && e.CreatedEpoch < int64(len(m.paperRecordArchives)) {
			return PaperArchivedRecord{}, ErrPaperRecordArchiveConflict
		}
		return PaperArchivedRecord{}, ErrPaperRecordArchiveNotFound
	}
	a, err := m.paperRecordArchiveLocked(ctx, sequence, de, oe)
	if err != nil {
		if errors.Is(err, ErrPaperRecordArchiveNotFound) {
			err = ErrPaperRecordArchiveConflict
		}
		return PaperArchivedRecord{}, err
	}
	for _, r := range a.Records {
		if r.Kind == kind && r.ID == id {
			return clone(PaperArchivedRecord{a.PaperRecordArchiveHeader, r}), nil
		}
	}
	return PaperArchivedRecord{}, ErrPaperRecordArchiveConflict
}

func (m *Memory) PaperRecordArchives(ctx context.Context, after int64, limit int) (PaperRecordArchivePage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if after < 0 || limit < 1 || limit > 32 {
		return PaperRecordArchivePage{}, ErrPaperRecordArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return PaperRecordArchivePage{}, err
	}
	out := PaperRecordArchivePage{ReceiverID: m.receiverID, Records: []PaperRecordArchiveHeader{}}
	for n := after; n < int64(len(m.paperRecordArchives)) && len(out.Records) < limit; n++ {
		h := m.paperRecordArchives[n].PaperRecordArchiveHeader
		if h.Validate() != nil || h.ReceiverID != m.receiverID || h.Sequence != n+1 || n >= int64(len(m.paperArchiveSeals)) || h != m.paperArchiveSeals[n] || len(out.Records) > 0 && h.PreviousSHA256 != out.Records[len(out.Records)-1].SHA256 {
			return PaperRecordArchivePage{}, ErrPaperRecordArchiveConflict
		}
		out.Records = append(out.Records, h)
	}
	if len(out.Records) > 0 && out.Records[len(out.Records)-1].Sequence < int64(len(m.paperRecordArchives)) {
		out.NextAfter = out.Records[len(out.Records)-1].Sequence
	}
	return out, nil
}
