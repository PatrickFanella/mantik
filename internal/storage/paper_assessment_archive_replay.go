package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

func paperAssessmentOwnerKeys(all map[string]PaperEvaluationAssessment) []string {
	keys := make([]string, 0, len(all))
	for id := range all {
		keys = append(keys, "assessment:"+id)
	}
	return keys
}

func paperArchiveFeedKeys(a PaperRecordArchive) []string {
	keys := []string{}
	for _, r := range a.Records {
		if r.Kind == "decision" || r.Kind == "outcome" {
			keys = append(keys, r.Kind+":"+r.ID)
		}
	}
	return keys
}

// Old assessments bind their complete issuance-time catalogue, including rows
// outside the declared sample window. Replay from the original owner context,
// not from whatever decision/outcome rows remain active today. This does not
// select evidence for a newly issued assessment.
func paperAssessmentOwnerCatalog(ctx context.Context, owner string, all map[string]PaperEvaluationAssessment, epochs map[string]PaperRecordEpoch, head int64, original func(int64) (PaperRecordArchive, error), current func() (paperEvaluationSources, error), de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	out := PaperEvaluationAssessmentCatalog{ReceiverID: owner, Records: []PaperEvaluationAssessment{}}
	if !ValidReceiverID(owner) || len(all) > MaxPaperEvaluationAssessments || de == nil || oe == nil {
		return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentInvalid
	}
	ids := make([]string, 0, len(all))
	for id, r := range all {
		e, ok := epochs["assessment:"+id]
		if !ok || e.Kind != "assessment" || e.ID != id || e.CreatedEpoch < 0 || e.CreatedEpoch > head {
			return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentConflict
		}
		if e.RecordSHA256 != r.SHA256 || !e.At.Equal(r.AssessedAt) || r.Command.ID != id || r.Command.ReceiverID != owner {
			return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentInvalid
		}
		ids = append(ids, id)
	}
	// Group original reads so the resolver retains only one bounded segment and
	// replays each segment's native decision/outcome sources once per request.
	slices.SortFunc(ids, func(a, b string) int {
		x, y := epochs["assessment:"+a].CreatedEpoch, epochs["assessment:"+b].CreatedEpoch
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return strings.Compare(a, b)
	})
	var sequence int64
	var sources paperEvaluationSources
	var archived map[string]PaperEvaluationAssessment
	bytes := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return PaperEvaluationAssessmentCatalog{}, err
		}
		r := all[id]
		e := epochs["assessment:"+id]
		// The next archive is absent for owners born at the current head.
		next := e.CreatedEpoch + 1
		if sequence != next {
			var err error
			if next > head {
				sources, err = current()
				archived = nil
			} else {
				a, loadErr := original(next)
				if loadErr != nil {
					if errors.Is(loadErr, ErrPaperRecordArchiveNotFound) || errors.Is(loadErr, ErrPaperRecordArchiveConflict) || errors.Is(loadErr, ErrFeedArchiveConflict) {
						loadErr = ErrPaperEvaluationAssessmentConflict
					}
					return PaperEvaluationAssessmentCatalog{}, loadErr
				}
				if a.Sequence != next || a.ReceiverID != owner {
					return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentConflict
				}
				o, decodeErr := decodePaperArchiveOwners(a.Records)
				if decodeErr != nil {
					return PaperEvaluationAssessmentCatalog{}, decodeErr
				}
				sources, err = paperArchiveEvaluationSources(ctx, a.Context, o, de, oe, portfolio)
				archived = o.assessments
			}
			if err != nil {
				return PaperEvaluationAssessmentCatalog{}, err
			}
			sequence = next
		}
		if archived != nil {
			original, ok := archived[id]
			if !ok || !reflect.DeepEqual(original, r) {
				return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentConflict
			}
		}
		verified, err := paperEvaluationAssessmentCatalog(ctx, owner, map[string]PaperEvaluationAssessment{id: r}, sources)
		if err != nil {
			return PaperEvaluationAssessmentCatalog{}, err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return PaperEvaluationAssessmentCatalog{}, err
		}
		bytes += len(raw)
		if bytes > MaxPaperEvaluationAssessmentCatalogBytes {
			return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentCapacity
		}
		out.Records = append(out.Records, verified.Records[0])
	}
	slices.SortFunc(out.Records, func(a, b PaperEvaluationAssessment) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	if err := ctx.Err(); err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	return out, nil
}

func (m *Memory) paperAssessmentOwnersLocked(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	return paperAssessmentOwnerCatalog(ctx, m.receiverID, m.evaluationAssessments, m.paperRecordEpochs, int64(len(m.paperRecordArchives)), func(sequence int64) (PaperRecordArchive, error) {
		a, err := m.paperRecordArchivePayloadLocked(sequence)
		if err != nil {
			return PaperRecordArchive{}, err
		}
		a.Context.Feed.custody, err = m.feedCustodyForOwnersLocked(a.Context.Feed.Retention, paperArchiveFeedKeys(a))
		return a, err
	}, func() (paperEvaluationSources, error) { return m.paperEvaluationSourcesLocked(de, oe) }, de, oe, m.portfolioEvaluator)
}

func readPaperAssessmentOwners(ctx context.Context, tx pgx.Tx, owner string, all map[string]PaperEvaluationAssessment, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	epochs, err := readPaperRecordEpochs(ctx, tx, paperAssessmentOwnerKeys(all))
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	var head int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&head); err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	return paperAssessmentOwnerCatalog(ctx, owner, all, epochs, head, func(sequence int64) (PaperRecordArchive, error) {
		a, err := readPaperRecordArchivePayload(ctx, tx, sequence)
		if err != nil {
			return PaperRecordArchive{}, err
		}
		a.Context.Feed.custody, err = readFeedCustodyForOwners(ctx, tx, a.Context.Feed.Retention, paperArchiveFeedKeys(a))
		return a, err
	}, func() (paperEvaluationSources, error) {
		return readPaperEvaluationSources(ctx, tx, false, de, oe, portfolio)
	}, de, oe, portfolio)
}
