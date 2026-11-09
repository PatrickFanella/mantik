package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const paperWindowEvidenceSchema = "mantik-owned-paper-evaluation-evidence-v4"
const paperWindowSelectionMethod = "owner-declaration-window-original-epochs-v1"

// This cut is produced by the owner reader, never accepted as a request manifest.
// Physical retirement does not change its original creation pins or native rows.
type PaperEvaluationWindowSelection struct {
	Method        string `json:"method"`
	ThroughEpoch  int64  `json:"through_epoch"`
	Records       int    `json:"records"`
	RecordsSHA256 string `json:"records_sha256"`
}

func validPaperEvidenceSelection(e PaperEvaluationEvidence) bool {
	if e.Schema == "mantik-owned-paper-evaluation-evidence-v3" {
		return e.Selection == nil
	}
	if e.Schema != paperWindowEvidenceSchema || e.Selection == nil || e.Selection.Method != paperWindowSelectionMethod || e.Selection.ThroughEpoch < 0 || !decisionPin.MatchString(e.Selection.RecordsSHA256) {
		return false
	}
	count := 0
	for _, row := range e.Rows {
		if row.Decision.FinalizedAt.Before(e.Declaration.Command.Plan.WindowStart) || !row.Decision.FinalizedAt.Before(e.Declaration.Command.Plan.WindowEnd) {
			return false
		}
		count += 1 + len(row.Outcomes) + len(row.LateOutcomes)
	}
	return count == e.Selection.Records
}

func paperWindowEvidence(ctx context.Context, id string, s paperEvaluationSources, at time.Time, selection PaperEvaluationWindowSelection) (PaperEvaluationEvidence, error) {
	e, err := paperEvaluationEvidence(ctx, id, s.declarations, s.decisions, s.outcomes, s.retained, at)
	if err != nil {
		return PaperEvaluationEvidence{}, err
	}
	if selection.Method != paperWindowSelectionMethod || selection.ThroughEpoch < 0 || selection.Records != len(s.decisions.Records)+len(s.outcomes.Records) || !decisionPin.MatchString(selection.RecordsSHA256) {
		return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
	}
	e.Schema, e.Selection = paperWindowEvidenceSchema, &selection
	e.SourceSHA256 = workerplan.Hash(struct {
		Declaration PaperEvaluationDeclaration
		Selection   PaperEvaluationWindowSelection
		Decisions   PaperDecisionCatalog
		Outcomes    PaperOutcomeCatalog
	}{e.Declaration, selection, s.decisions, s.outcomes})
	e.SHA256 = ""
	raw, err := json.Marshal(e)
	if err != nil || len(raw)+64 > MaxPaperEvaluationEvidenceBytes {
		return PaperEvaluationEvidence{}, ErrPaperEvaluationEvidenceInvalid
	}
	e.SHA256 = workerplan.Hash(e)
	return clone(e), nil
}

func paperWindowSources(ctx context.Context, id string, at time.Time, epoch int64, s paperEvaluationSources, replay bool,
	page func(PaperEvaluationDeclaration, *paperSampleCursor) (paperSamplePage, error),
	native func([]paperSampleRecord) (paperArchiveOwners, error),
) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
	fail := func(err error) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, err
	}
	if !declarationTime(at) || epoch < 0 || !replay && at.Before(s.sourceClock) {
		return fail(ErrPaperEvaluationEvidenceInvalid)
	}
	// Retain the existing issuance clock floor over the bounded active owners.
	// Only the selected sample needs full native replay; old assessment replay
	// continues to use its original complete catalogue and evidence version.
	if !replay {
		for key, d := range s.retained {
			if key != d.Record.Command.ID || d.Record.Command.ReceiverID != s.declarations.ReceiverID || d.Record.Validate() != nil || d.Record.FinalizedAt.After(at) {
				return fail(ErrPaperEvaluationEvidenceInvalid)
			}
		}
		for key, o := range s.retainedOutcomes {
			if key != o.Record.Command.ID || o.Record.Command.ReceiverID != s.declarations.ReceiverID || o.Record.Validate() != nil || o.Record.EvaluatedAt.After(at) {
				return fail(ErrPaperEvaluationEvidenceInvalid)
			}
		}
	}
	var declaration PaperEvaluationDeclaration
	for _, d := range s.declarations.Records {
		if d.DeclaredAt.After(at) {
			if replay {
				continue
			}
			return fail(ErrPaperEvaluationEvidenceInvalid)
		}
		if d.Command.ID == id {
			declaration = d
		}
	}
	if declaration.SHA256 == "" {
		return fail(ErrPaperEvaluationDeclarationNotFound)
	}
	s.declarations.Records = []PaperEvaluationDeclaration{declaration}
	s.retained, s.retainedOutcomes = map[string]retainedPaperDecision{}, map[string]retainedPaperOutcome{}
	selection := PaperEvaluationWindowSelection{Method: paperWindowSelectionMethod, ThroughEpoch: epoch}
	selection.RecordsSHA256 = workerplan.Hash(struct {
		DeclarationSHA256 string
		AsOf              time.Time
		Epoch             int64
	}{declaration.SHA256, at.UTC(), epoch})
	var cursor *paperSampleCursor
	bytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		batch, err := page(declaration, cursor)
		if err != nil {
			return fail(err)
		}
		owners, err := native(batch.Records)
		if err != nil {
			return fail(err)
		}
		for _, r := range batch.Records {
			selection.RecordsSHA256 = workerplan.Hash(struct {
				Previous string
				Original PaperRecordEpoch
				Scope    paperRecordScope
			}{selection.RecordsSHA256, r.PaperRecordEpoch, r.Scope})
			selection.Records++
		}
		for id, d := range owners.decisions {
			if old, ok := s.retained[id]; ok {
				if !reflect.DeepEqual(old, d) {
					return fail(ErrPaperRecordArchiveConflict)
				}
				continue
			}
			raw, err := json.Marshal(d)
			if err != nil {
				return fail(err)
			}
			bytes += len(raw)
			if bytes > MaxPaperRecordArchiveBytes {
				return fail(ErrPaperRecordArchiveCapacity)
			}
			s.retained[id] = d
		}
		for id, o := range owners.outcomes {
			if _, exists := s.retainedOutcomes[id]; exists {
				return fail(ErrPaperRecordArchiveConflict)
			}
			raw, err := json.Marshal(o)
			if err != nil {
				return fail(err)
			}
			bytes += len(raw)
			if bytes > MaxPaperRecordArchiveBytes {
				return fail(ErrPaperRecordArchiveCapacity)
			}
			s.retainedOutcomes[id] = o
		}
		if batch.Next == nil {
			break
		}
		cursor = batch.Next
	}
	if len(s.retained)+len(s.retainedOutcomes) != selection.Records {
		return fail(ErrPaperRecordArchiveConflict)
	}
	s.decisions = PaperDecisionCatalog{ReceiverID: s.declarations.ReceiverID, Records: []PaperDecisionRecord{}}
	s.outcomes = PaperOutcomeCatalog{ReceiverID: s.declarations.ReceiverID, Records: []PaperOutcomeRecord{}}
	for _, d := range s.retained {
		s.decisions.Records = append(s.decisions.Records, d.Record)
	}
	for _, o := range s.retainedOutcomes {
		s.outcomes.Records = append(s.outcomes.Records, o.Record)
	}
	slices.SortFunc(s.decisions.Records, func(a, b PaperDecisionRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	slices.SortFunc(s.outcomes.Records, func(a, b PaperOutcomeRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.selection = &selection
	return s, selection, nil
}

func (m *Memory) paperWindowSourcesLocked(ctx context.Context, id string, at time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
	if de == nil || oe == nil {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, ErrPaperEvaluationEvidenceInvalid
	}
	s, err := m.paperEvaluationContextLocked()
	if err != nil {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, err
	}
	return m.paperWindowSourcesWithContextLocked(ctx, id, at, int64(len(m.paperRecordArchives)), s, false, de, oe)
}

func (m *Memory) paperWindowSourcesWithContextLocked(ctx context.Context, id string, at time.Time, epoch int64, s paperEvaluationSources, replay bool, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
	return paperWindowSources(ctx, id, at, epoch, s, replay, func(d PaperEvaluationDeclaration, after *paperSampleCursor) (paperSamplePage, error) {
		return m.paperSampleWindowAtEpochLocked(ctx, d, at, epoch, after, maxPaperNativePage)
	}, func(records []paperSampleRecord) (paperArchiveOwners, error) {
		return m.paperSampleNativeWithSourcesLocked(ctx, s.declarations.ReceiverID, records, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
			feed := s.feed
			view, err := m.feedCustodyForOwnersLocked(feed.Retention, keys)
			feed.custody = view
			return feed, s.strategies, err
		})
	})
}

func readPaperWindowSources(ctx context.Context, tx pgx.Tx, id string, at time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
	if de == nil || oe == nil {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, ErrPaperEvaluationEvidenceInvalid
	}
	s, err := readPaperEvaluationContext(ctx, tx, false, portfolio)
	if err != nil {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, err
	}
	var epoch int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&epoch); err != nil {
		return paperEvaluationSources{}, PaperEvaluationWindowSelection{}, err
	}
	return readPaperWindowSourcesWithContext(ctx, tx, id, at, epoch, s, false, de, oe)
}

func readPaperWindowSourcesWithContext(ctx context.Context, tx pgx.Tx, id string, at time.Time, epoch int64, s paperEvaluationSources, replay bool, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperEvaluationSources, PaperEvaluationWindowSelection, error) {
	return paperWindowSources(ctx, id, at, epoch, s, replay, func(d PaperEvaluationDeclaration, after *paperSampleCursor) (paperSamplePage, error) {
		return readPaperSampleWindowAtEpoch(ctx, tx, d, at, epoch, after, maxPaperNativePage)
	}, func(records []paperSampleRecord) (paperArchiveOwners, error) {
		return readPaperSampleNativeWithSources(ctx, tx, s.declarations.ReceiverID, records, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
			feed := s.feed
			view, err := readFeedCustodyForOwners(ctx, tx, feed.Retention, keys)
			feed.custody = view
			return feed, s.strategies, err
		})
	})
}

func (m *Memory) paperWindowAssessmentSourcesLocked(ctx context.Context, r PaperEvaluationAssessment, original *PaperArchiveContext, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperEvaluationSources, error) {
	if r.Evidence.Selection == nil {
		return paperEvaluationSources{}, ErrPaperEvaluationAssessmentInvalid
	}
	var s paperEvaluationSources
	var err error
	if original == nil {
		s, err = m.paperEvaluationContextLocked()
	} else {
		s, err = paperArchiveEvaluationContext(ctx, *original, m.portfolioEvaluator)
	}
	if err != nil {
		return paperEvaluationSources{}, err
	}
	s, selection, err := m.paperWindowSourcesWithContextLocked(ctx, r.Command.DeclarationID, r.AssessedAt, r.Evidence.Selection.ThroughEpoch, s, true, de, oe)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	if selection != *r.Evidence.Selection {
		return paperEvaluationSources{}, ErrPaperEvaluationAssessmentConflict
	}
	return s, nil
}

func readPaperWindowAssessmentSources(ctx context.Context, tx pgx.Tx, r PaperEvaluationAssessment, original *PaperArchiveContext, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (paperEvaluationSources, error) {
	if r.Evidence.Selection == nil {
		return paperEvaluationSources{}, ErrPaperEvaluationAssessmentInvalid
	}
	var s paperEvaluationSources
	var err error
	if original == nil {
		s, err = readPaperEvaluationContext(ctx, tx, false, portfolio)
	} else {
		s, err = paperArchiveEvaluationContext(ctx, *original, portfolio)
	}
	if err != nil {
		return paperEvaluationSources{}, err
	}
	s, selection, err := readPaperWindowSourcesWithContext(ctx, tx, r.Command.DeclarationID, r.AssessedAt, r.Evidence.Selection.ThroughEpoch, s, true, de, oe)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	if selection != *r.Evidence.Selection {
		return paperEvaluationSources{}, ErrPaperEvaluationAssessmentConflict
	}
	return s, nil
}
