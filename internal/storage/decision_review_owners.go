package storage

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Reviews retain their bounded working catalogue, but a referenced finalized
// decision can already belong to its first original archive. These extra
// records are private review dependencies, not an enlarged active catalogue.
func decisionReviewOwnerDecisions(ctx context.Context, active PaperDecisionCatalog, c DecisionReviewCommand, all map[string]DecisionReviewRecord, original func(string) (PaperDecisionRecord, error)) (PaperDecisionCatalog, error) {
	if len(all) > MaxDecisionReviews {
		return PaperDecisionCatalog{}, ErrDecisionReviewCapacity
	}
	present, needed := map[string]bool{}, map[string]bool{}
	for _, d := range active.Records {
		present[d.Command.ID] = true
	}
	if c.DecisionID != "" && !present[c.DecisionID] {
		needed[c.DecisionID] = true
	}
	for _, r := range all {
		if !present[r.Command.DecisionID] {
			needed[r.Command.DecisionID] = true
		}
	}
	ids := make([]string, 0, len(needed))
	for id := range needed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := clone(active)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return PaperDecisionCatalog{}, err
		}
		d, err := original(id)
		if errors.Is(err, ErrPaperDecisionNotFound) {
			// The existing review constructor/catalogue reports its appropriate
			// missing-target or missing-retained-dependency error.
			continue
		}
		if err != nil {
			return PaperDecisionCatalog{}, err
		}
		out.Records = append(out.Records, d)
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionCatalog{}, err
	}
	return out, nil
}

func (m *Memory) reviewOwnerDecisionsLocked(ctx context.Context, active PaperDecisionCatalog, c DecisionReviewCommand, all map[string]DecisionReviewRecord, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	return decisionReviewOwnerDecisions(ctx, active, c, all, func(id string) (PaperDecisionRecord, error) {
		r, retired, err := m.paperTargetMetadataLocked(ctx, "decision", id)
		if err != nil {
			return PaperDecisionRecord{}, err
		}
		if !retired {
			if r.ID != "" {
				return PaperDecisionRecord{}, ErrPaperDecisionConflict
			}
			return PaperDecisionRecord{}, ErrPaperDecisionNotFound
		}
		owners, err := m.archivedPaperTargetLocked(ctx, r, eval, nil)
		if err != nil {
			return PaperDecisionRecord{}, err
		}
		d, ok := owners.decisions[id]
		if !ok {
			return PaperDecisionRecord{}, ErrPaperDecisionConflict
		}
		return clone(d.Record), nil
	})
}

func readReviewOwnerDecisions(ctx context.Context, tx pgx.Tx, active PaperDecisionCatalog, c DecisionReviewCommand, all map[string]DecisionReviewRecord, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	return decisionReviewOwnerDecisions(ctx, active, c, all, func(id string) (PaperDecisionRecord, error) {
		r, retired, err := readPaperTargetMetadata(ctx, tx, "decision", id)
		if err != nil {
			return PaperDecisionRecord{}, err
		}
		if !retired {
			if r.ID != "" {
				return PaperDecisionRecord{}, ErrPaperDecisionConflict
			}
			return PaperDecisionRecord{}, ErrPaperDecisionNotFound
		}
		owners, err := readArchivedPaperTarget(ctx, tx, r, eval, nil)
		if err != nil {
			return PaperDecisionRecord{}, err
		}
		d, ok := owners.decisions[id]
		if !ok {
			return PaperDecisionRecord{}, ErrPaperDecisionConflict
		}
		return clone(d.Record), nil
	})
}

// A retired review answers an exact retry from its original archive, so its
// ID is never issued a second time. The creation pin and the record hash bind
// the archived payload; the archive chain was checked by the payload reader.
func retiredDecisionReviewRetry(owner string, c DecisionReviewCommand, sha string, a PaperRecordArchive) (DecisionReviewReceipt, error) {
	if c.Validate() != nil {
		return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
	}
	if c.ReceiverID != owner {
		return DecisionReviewReceipt{}, ErrReceiverMismatch
	}
	for _, record := range a.Records {
		if record.Kind != "review" || record.ID != c.ID {
			continue
		}
		var r DecisionReviewRecord
		if record.RecordSHA256 != sha || workerplan.ReadBoundedShape(record.Payload, &r, MaxDecisionReviewBytes) != nil {
			return DecisionReviewReceipt{}, ErrPaperRecordArchiveConflict
		}
		unsigned := clone(r)
		unsigned.SHA256 = ""
		if r.SHA256 != sha || workerplan.Hash(unsigned) != sha || r.Command.ID != c.ID {
			return DecisionReviewReceipt{}, ErrPaperRecordArchiveConflict
		}
		if !reflect.DeepEqual(c, r.Command) {
			return DecisionReviewReceipt{}, ErrDecisionReviewConflict
		}
		return DecisionReviewReceipt{r, true}, nil
	}
	return DecisionReviewReceipt{}, ErrPaperRecordArchiveConflict
}
