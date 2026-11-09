package storage

import (
	"context"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// Source writers use validated retained envelopes only as a causal clock floor.
// This is not actual source replay, a completeness proof or evaluation authority.
func sourceObservationClock(owner string, decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, assessments map[string]PaperEvaluationAssessment, at time.Time) error {
	if !ValidReceiverID(owner) || !declarationTime(at) || len(decisions) > MaxPaperDecisions || len(outcomes) > MaxPaperOutcomes {
		return feedcollector.ErrInvalid
	}
	for id, v := range decisions {
		r := v.Record
		if id != r.Command.ID || r.Command.ReceiverID != owner || r.Validate() != nil || workerplan.Hash(v.Basis) != r.BasisSHA256 {
			return ErrPaperDecisionInvalid
		}
		if !at.After(r.FinalizedAt) {
			return feedcollector.ErrClockBackwards
		}
	}
	for id, v := range outcomes {
		r := v.Record
		if id != r.Command.ID || r.Command.ReceiverID != owner || r.Validate() != nil || workerplan.Hash(v.Basis) != r.BasisSHA256 {
			return ErrPaperOutcomeInvalid
		}
		if !at.After(r.EvaluatedAt) {
			return feedcollector.ErrClockBackwards
		}
	}
	if err := paperEvaluationSourceClock(owner, assessments, at); err != nil {
		if errors.Is(err, ErrPaperEvaluationAssessmentConflict) {
			return feedcollector.ErrClockBackwards
		}
		return err
	}
	return nil
}
func (m *Memory) sourceObservationClockLocked(at time.Time) error {
	if len(m.feedArchives) > 0 {
		h := m.feedArchives[len(m.feedArchives)-1].FeedArchiveHeader
		if err := feedArchiveSourceClock(m.receiverID, h, at); err != nil {
			return err
		}
	}
	if err := m.paperFeedCheckpointClockLocked(at); err != nil {
		return err
	}
	return sourceObservationClock(m.receiverID, m.paperDecisions, m.paperOutcomes, m.evaluationAssessments, at)
}

func feedArchiveSourceClock(owner string, h FeedArchiveHeader, at time.Time) error {
	if h.Validate() != nil || h.ReceiverID != owner {
		return ErrFeedArchiveConflict
	}
	if !at.After(h.ArchivedAt) {
		return feedcollector.ErrClockBackwards
	}
	return nil
}

// Every caller already holds the collector ownership lock. Decision, outcome
// and assessment issuance take that same lock before retaining their cuts.
func readSourceObservationClock(ctx context.Context, tx pgx.Tx, owner string, at time.Time) error {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT header FROM mantik_feed_archives ORDER BY sequence DESC LIMIT 1").Scan(&raw)
	if err == nil {
		var h FeedArchiveHeader
		if workerplan.ReadBoundedShape(raw, &h, 4096) != nil {
			return ErrFeedArchiveConflict
		}
		if err := feedArchiveSourceClock(owner, h, at); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := readPaperFeedCheckpointClock(ctx, tx, owner, at); err != nil {
		return err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return err
	}
	outcomes, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return err
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return err
	}
	return sourceObservationClock(owner, decisions, outcomes, assessments, at)
}
