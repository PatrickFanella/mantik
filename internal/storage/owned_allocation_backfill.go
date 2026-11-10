package storage

import (
	"context"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxOwnedAllocationIndexReceiptBytes = 8192

type OwnedAllocationIndexCommand struct {
	ReceiverID string `json:"receiver_id"`
}

func (c OwnedAllocationIndexCommand) Validate() error {
	if !ValidReceiverID(c.ReceiverID) {
		return ErrOwnedAllocationInvalid
	}
	return nil
}

// Checkpoint envelopes added by this operation are native reconstructions
// from retained originals and their recorded seal boundaries. Existing stored
// envelopes must match exactly; no original record or working journal changes.
type OwnedAllocationIndexReceipt struct {
	Schema              string `json:"schema"`
	Method              string `json:"method"`
	ReceiverID          string `json:"receiver_id"`
	ThroughSequence     int    `json:"through_sequence"`
	CheckpointSHA256    string `json:"checkpoint_sha256,omitempty"`
	AuditedRecords      int    `json:"audited_records"`
	SegmentsAdded       int    `json:"segments_added"`
	MembersAdded        int    `json:"members_added"`
	CheckpointsAdded    int    `json:"checkpoints_added"`
	AuditStatus         string `json:"audit_status"`
	Authority           string `json:"authority"`
	QualificationCredit bool   `json:"qualification_credit"`
	SHA256              string `json:"sha256"`
}

func (r OwnedAllocationIndexReceipt) Validate(c OwnedAllocationIndexCommand) error {
	sha := r.SHA256
	r.SHA256 = ""
	if c.Validate() != nil || r.Schema != "mantik-allocation-index-receipt-v1" || r.Method != "native-original-replay-index-backfill-v1" || r.ReceiverID != c.ReceiverID || r.Authority != "service_owned_archive_index_only" || r.QualificationCredit || r.AuditStatus != "verified" || r.ThroughSequence < 0 || r.AuditedRecords < r.ThroughSequence || r.SegmentsAdded < 0 || (r.SegmentsAdded == 0) != (r.MembersAdded == 0) || r.MembersAdded < r.SegmentsAdded || r.MembersAdded > r.ThroughSequence || r.CheckpointsAdded < 0 || r.CheckpointsAdded > r.SegmentsAdded || workerplan.Hash(r) != sha {
		return ErrOwnedAllocationInvalid
	}
	if r.ThroughSequence == 0 {
		if r.CheckpointSHA256 != "" || r.MembersAdded != 0 {
			return ErrOwnedAllocationInvalid
		}
	} else if !decisionPin.MatchString(r.CheckpointSHA256) {
		return ErrOwnedAllocationInvalid
	}
	return nil
}

type OwnedAllocationIndexStore interface {
	IndexOwnedAllocationHistory(context.Context, OwnedAllocationIndexCommand, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationIndexReceipt, error)
}

func finishAllocationIndex(c OwnedAllocationIndexCommand, audit OwnedAllocationAudit, receipt OwnedAllocationIndexReceipt) (OwnedAllocationIndexReceipt, error) {
	receipt.Schema, receipt.Method = "mantik-allocation-index-receipt-v1", "native-original-replay-index-backfill-v1"
	receipt.ReceiverID, receipt.AuditStatus = c.ReceiverID, audit.Status
	receipt.ThroughSequence, receipt.CheckpointSHA256, receipt.AuditedRecords = audit.Sealed, audit.CheckpointSHA256, audit.Records
	receipt.Authority = "service_owned_archive_index_only"
	receipt.SHA256 = workerplan.Hash(receipt)
	if err := receipt.Validate(c); err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	return receipt, nil
}

func allocationOriginalIndexMissing(ctx context.Context, expected ownedAllocationSegment, read func(int) (ownedAllocationSegment, error)) (bool, error) {
	original, err := read(expected.After.Sequence - len(expected.Records) + 1)
	if errors.Is(err, errOwnedAllocationIndexUnavailable) {
		return true, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err != nil || !reflect.DeepEqual(original, expected) {
		return false, ErrOwnedAllocationAudit
	}
	return false, nil
}

func (m *Memory) IndexOwnedAllocationHistory(ctx context.Context, c OwnedAllocationIndexCommand, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationIndexReceipt, error) {
	if c.Validate() != nil || de == nil {
		return OwnedAllocationIndexReceipt{}, ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID {
		return OwnedAllocationIndexReceipt{}, ErrReceiverMismatch
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	if m.ownedAllocations.Checkpoint == nil && (len(m.ownedAllocationArchive) != 0 || len(m.ownedAllocationIndex.Checkpoints) != 0 || len(m.ownedAllocationIndex.Batches) != 0 || len(m.ownedAllocationIndex.Members) != 0 || len(m.ownedAllocationIndex.Decisions) != 0 || len(m.ownedAllocationIndex.Financial) != 0) {
		return OwnedAllocationIndexReceipt{}, ErrOwnedAllocationAudit
	}
	candidate := clone(m.ownedAllocationIndex)
	receipt := OwnedAllocationIndexReceipt{}
	audit, err := m.auditOwnedAllocationsLocked(ctx, func(original ownedAllocationSegment) error {
		missing, err := allocationOriginalIndexMissing(ctx, original, func(n int) (ownedAllocationSegment, error) { return m.ownedAllocationSegmentLocked(ctx, n) })
		if err != nil || !missing {
			return err
		}
		if _, exists := candidate.Checkpoints[original.After.Sequence]; !exists {
			receipt.CheckpointsAdded++
		}
		if err := retainOwnedAllocationIndex(&candidate, original.Before, ownedAllocationJournal{Checkpoint: &original.After, sealing: original.Records}); err != nil {
			return err
		}
		receipt.SegmentsAdded++
		receipt.MembersAdded += len(original.Records)
		return nil
	}, de, oe...)
	if err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	receipt, err = finishAllocationIndex(c, audit, receipt)
	if err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	m.ownedAllocationIndex = candidate
	return receipt, nil
}

func (p *Postgres) IndexOwnedAllocationHistory(ctx context.Context, c OwnedAllocationIndexCommand, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationIndexReceipt, error) {
	if c.Validate() != nil || de == nil {
		return OwnedAllocationIndexReceipt{}, ErrOwnedAllocationInvalid
	}
	var receipt OwnedAllocationIndexReceipt
	_, _, err := p.ownedAllocationAccess(ctx, true, nil, func(ctx context.Context, tx pgx.Tx, owner string, working ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, _ time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		if owner != c.ReceiverID {
			return ownedAllocationJournal{}, false, ErrReceiverMismatch
		}
		if working.Checkpoint == nil {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_archive) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_checkpoints) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_batches) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_members)").Scan(&exists); err != nil {
				return ownedAllocationJournal{}, false, err
			}
			if exists {
				return ownedAllocationJournal{}, false, ErrOwnedAllocationAudit
			}
		}
		history := working
		var err error
		history.archived, err = readOwnedAllocationArchive(ctx, tx)
		if err != nil {
			return ownedAllocationJournal{}, false, err
		}
		decisions, retained, err = readAllocationOwnerDecisions(ctx, tx, decisions, retained, history, nil, de)
		if err != nil {
			return ownedAllocationJournal{}, false, err
		}
		audit, err := auditOwnedAllocationsWithIndex(ctx, owner, history, func(original ownedAllocationSegment) error {
			missing, err := allocationOriginalIndexMissing(ctx, original, func(n int) (ownedAllocationSegment, error) { return readOwnedAllocationSegment(ctx, tx, n) })
			if err != nil || !missing {
				return err
			}
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_checkpoints WHERE sequence=$1)", original.After.Sequence).Scan(&exists); err != nil {
				return err
			}
			if err := writeOwnedAllocationIndex(ctx, tx, ownedAllocationJournal{Checkpoint: &original.After, sealing: original.Records}, original.Before); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					err = ErrOwnedAllocationConflict
				}
				return err
			}
			if !exists {
				receipt.CheckpointsAdded++
			}
			receipt.SegmentsAdded++
			receipt.MembersAdded += len(original.Records)
			return nil
		}, policies, decisions, strategies, retained, x, declarations, assessments)
		if err != nil {
			return ownedAllocationJournal{}, false, err
		}
		receipt, err = finishAllocationIndex(c, audit, receipt)
		return working, false, err
	}, de, oe...)
	if err != nil {
		return OwnedAllocationIndexReceipt{}, err
	}
	return receipt, nil
}
