package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

const PaperRetentionWorkerInterval = time.Minute
const PaperRetentionWorkerCapability = "paper_retention"

type PaperRetentionWorkerStore interface {
	storage.PaperDecisionStore
	storage.PaperOutcomeStore
	storage.PaperRecordArchiveStore
	storage.PaperRetirementStore
	storage.FeedEvidenceStore
	storage.FeedRetentionStore
}

// PaperRetentionLimits are the active counts at which the worker starts to
// archive and retire. They sit at half of each working limit, so a writer
// keeps room while records wait for a pending outcome or an open allocation.
type PaperRetentionLimits struct {
	Decisions, Outcomes, Publications, Admissions, Commands int
}

var DefaultPaperRetentionLimits = PaperRetentionLimits{
	Decisions:    storage.MaxPaperDecisions / 2,
	Outcomes:     storage.MaxPaperOutcomes / 2,
	Publications: storage.MaxFeedEvidence / 2,
	Admissions:   feedcollector.MaxAdmissions / 2,
	Commands:     feedcollector.MaxCommands / 2,
}

func (l PaperRetentionLimits) valid() bool {
	return l.Decisions > 0 && l.Outcomes > 0 && l.Publications > 0 && l.Admissions > 0 && l.Commands > 0
}

// PaperRetentionState remembers the active counts at the last archive that
// retired nothing. Every new archive holds the whole active set, so the worker
// waits for growth before it writes another one. A restart forgets this and
// costs at most one extra archive of each kind.
type PaperRetentionState struct {
	paperArchived bool
	paperCount    int
	feedRotated   bool
	feedCount     int
}

// PaperRetentionReport lists what one cycle wrote. Nil means the step did not
// run. None of these receipts grants admission, execution or qualification.
type PaperRetentionReport struct {
	Archive    *storage.PaperRecordArchiveReceipt `json:"archive,omitempty"`
	Retirement *storage.PaperRetirementReceipt    `json:"retirement,omitempty"`
	Rotation   *storage.FeedArchiveReceipt        `json:"rotation,omitempty"`
}

// RunPaperRetentionCycle retires sealed paper records and rotates feed sources
// once an active count reaches its limit. The storage owners decide what may
// leave; this cycle only decides when to ask. Paper records go first because
// feed rotation keeps every source an active decision or outcome still reads.
// Each step commits on its own, so a later error keeps the earlier results.
func RunPaperRetentionCycle(ctx context.Context, store PaperRetentionWorkerStore, clock Clock, limits PaperRetentionLimits, state *PaperRetentionState) (PaperRetentionReport, error) {
	var report PaperRetentionReport
	if clock == nil || state == nil || !limits.valid() {
		return report, storage.ErrPaperRetirementInvalid
	}
	cycleCtx, cancel := context.WithTimeout(ctx, raceDeadlineScale*50*time.Second)
	defer cancel()
	decisions, err := store.PaperDecisions(cycleCtx, evaluateOwnedPaperDecision)
	if err != nil {
		return report, err
	}
	outcomes, err := store.PaperOutcomes(cycleCtx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		return report, err
	}
	owner := decisions.ReceiverID
	if outcomes.ReceiverID != owner {
		return report, storage.ErrPaperRetirementConflict
	}
	if len(decisions.Records) >= limits.Decisions || len(outcomes.Records) >= limits.Outcomes {
		if err := retirePaperRecords(cycleCtx, store, clock, owner, len(decisions.Records)+len(outcomes.Records), max(1, (limits.Decisions+limits.Outcomes)/8), state, &report); err != nil {
			return report, err
		}
	}
	feed, err := store.FeedEvidence(cycleCtx)
	if err != nil {
		return report, err
	}
	if feed.ReceiverID != owner {
		return report, storage.ErrPaperRetirementConflict
	}
	count := len(feed.Records) + len(feed.Collectors.Admissions) + len(feed.Collectors.Commands)
	if len(feed.Records) < limits.Publications && len(feed.Collectors.Admissions) < limits.Admissions && len(feed.Collectors.Commands) < limits.Commands {
		return report, nil
	}
	if state.feedRotated && count < state.feedCount+max(1, limits.Publications/4) {
		return report, nil
	}
	rotation, err := store.RotateFeed(cycleCtx, owner, clock.Now, validateFeedArchiveSource, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		return report, err
	}
	report.Rotation = &rotation
	state.feedRotated, state.feedCount = true, count
	if r := rotation.Retention; r != nil {
		state.feedCount -= r.PublicationsRetired + r.AdmissionsRetired + r.CommandsRetired
	}
	return report, nil
}

// retirePaperRecords asks the retirement owner first. It writes a new archive
// only when that would let a record retire: one the head archive does not
// hold, or one a custody checkpoint binds at the head epoch.
func retirePaperRecords(ctx context.Context, store PaperRetentionWorkerStore, clock Clock, owner string, count, step int, state *PaperRetentionState, report *PaperRetentionReport) error {
	retire := func() (storage.PaperRetirementReceipt, error) {
		r, err := store.RetirePaperRecords(ctx, owner, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err == nil {
			report.Retirement = &r
		}
		return r, err
	}
	r, err := retire()
	if err != nil {
		return err
	}
	if len(r.Retired) > 0 {
		state.paperArchived = false
		return nil
	}
	if r.Epoch != 0 && r.Retained.Unsealed+r.Retained.OutcomeUnsealed+r.Retained.CustodyBound == 0 {
		return nil
	}
	if state.paperArchived && count < state.paperCount+step {
		return nil
	}
	archive, err := store.ArchivePaperRecords(ctx, owner, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		return err
	}
	report.Archive = &archive
	state.paperArchived, state.paperCount = true, count
	if archive.Duplicate {
		return nil
	}
	if r, err = retire(); err != nil {
		return err
	}
	if len(r.Retired) > 0 {
		state.paperArchived = false
	}
	return nil
}

// RunPaperRetentionWorker recovers immediately, then checks every minute. Its
// health is separate from protective monitoring and grants no qualification.
func RunPaperRetentionWorker(ctx context.Context, store PaperRetentionWorkerStore, worker string, clock Clock) {
	pending := pendingFailure{}
	state := &PaperRetentionState{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, PaperRetentionWorkerCapability, clock, PaperRetentionWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		report, err := RunPaperRetentionCycle(ctx, store, clock, DefaultPaperRetentionLimits, state)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, PaperRetentionWorkerCapability, clock, PaperRetentionWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("paper retention cycle failed", "error", err)
		}
		if r := report.Retirement; r != nil && len(r.Retired) > 0 {
			slog.Info("paper records retired", "epoch", r.Epoch, "decisions", r.Decisions, "outcomes", r.Outcomes, "reviews", r.Reviews)
		}
		if r := report.Rotation; r != nil && r.Retention != nil && !r.Retention.Duplicate {
			slog.Info("feed sources rotated", "sequence", r.Retention.Sequence, "publications", r.Retention.PublicationsRetired, "admissions", r.Retention.AdmissionsRetired, "commands", r.Retention.CommandsRetired)
		}
		if err := clock.Wait(ctx, PaperRetentionWorkerInterval); err != nil {
			return
		}
	}
}
