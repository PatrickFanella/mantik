package storage

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type PaperFeedAudit struct {
	Schema              string `json:"schema"`
	ReceiverID          string `json:"receiver_id"`
	Status              string `json:"status"`
	Decisions           int    `json:"decisions"`
	Outcomes            int    `json:"outcomes"`
	Checkpoints         int64  `json:"checkpoints"`
	SourceArchiveSHA256 string `json:"source_archive_sha256"`
	SourceIndexSHA256   string `json:"source_index_sha256"`
	RecordsSHA256       string `json:"records_sha256"`
	AdmissionAvailable  bool   `json:"admission_available"`
	ExecutionAvailable  bool   `json:"execution_available"`
	QualificationCredit bool   `json:"qualification_credit"`
}
type PaperFeedAuditStore interface {
	AuditPaperFeed(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperFeedAudit, error)
}

// Archive issuance independently checks new seals against original sources.
// A full audit passes a zero lower bound and checks every retained owner. The
// window queries stay bounded; the complete audit's total work grows with its
// archive/checkpoint history and never returns a partial success.
func auditOriginalPaperFeed(ctx context.Context, feed FeedEvidenceSnapshot, decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, strategies paperstrategy.Catalog, e *paperFeedArchiveEvaluators, after time.Time, reader archivedFeedWindowReader) error {
	if e == nil || e.decision == nil || e.outcome == nil {
		return ErrFeedArchiveInvalid
	}
	resolve := func(c PaperDecisionCommand, at, since time.Time) (FeedEvidenceSnapshot, error) {
		actual := feed
		if feed.Retention != nil && !at.After(feed.Retention.Checkpoint.CutAt) {
			prefix, err := reader.prefix(c.RegistrationID, at)
			if err != nil {
				return FeedEvidenceSnapshot{}, err
			}
			archive, err := reader.archive(prefix.Archive.Sequence)
			if err != nil {
				return FeedEvidenceSnapshot{}, err
			}
			actual = archive.Source
		}
		return archivedOutcomeFeed(ctx, actual, c.RegistrationID, since, at, reader)
	}
	verify := func(c PaperDecisionCommand, b PaperDecisionBasis, at, since time.Time, schema string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if schema == "mantik-owned-paper-decision-v1" || schema == "mantik-owned-paper-decision-v2" || schema == "mantik-owned-paper-outcome-v1" || schema == "mantik-owned-paper-outcome-v2" {
			since = b.Feed.Collectors.Entries[c.RegistrationID].RegisteredAt
		}
		actual, err := resolve(c, at, since)
		if err != nil {
			return err
		}
		if err := verifyPaperDecisionBasis(c, b, at, strategies, actual); err != nil {
			return err
		}
		switch schema {
		case "mantik-owned-paper-decision-v2", "mantik-owned-paper-outcome-v2":
			return paperDecisionSourceMembership(c, b, at, actual)
		case paperDecisionSchemaV3, paperDecisionSchemaV4, paperOutcomeSchemaV3, paperOutcomeSchemaV4:
			return verifyFeedPrefix(c, b, at, since, newFeedPrefixIndex(actual))
		}
		return nil
	}
	for id, v := range decisions {
		r := v.Record
		if !r.FinalizedAt.After(after) {
			continue
		}
		if id != r.Command.ID || r.Command.ReceiverID != feed.ReceiverID {
			return ErrPaperDecisionConflict
		}
		if err := verify(r.Command, v.Basis, r.FinalizedAt, time.Time{}, r.Schema); err != nil {
			return err
		}
		expected, err := paperDecisionForSchema(r.Schema, r.Command, v.Basis, r.FinalizedAt, e.decision)
		if err != nil || !reflect.DeepEqual(expected, r) {
			return ErrPaperDecisionInvalid
		}
	}
	for id, v := range outcomes {
		r := v.Record
		if !r.EvaluatedAt.After(after) {
			continue
		}
		d, ok := decisions[r.Command.DecisionID]
		if id != r.Command.ID || r.Command.ReceiverID != feed.ReceiverID || !ok || !reflect.DeepEqual(d.Record, v.Basis.Decision) {
			return ErrPaperOutcomeConflict
		}
		if err := verify(d.Record.Command, v.Basis.Observation, r.EvaluatedAt, d.Record.FinalizedAt, r.Schema); err != nil {
			return err
		}
		expected, err := paperOutcomeForSchema(r.Schema, r.Command, v.Basis, r.EvaluatedAt, e.outcome)
		if err != nil || !reflect.DeepEqual(expected, r) {
			return ErrPaperOutcomeInvalid
		}
	}
	return nil
}

func paperFeedAuditReceipt(owner string, decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, source FeedArchiveSourceAudit, checkpoints int64) PaperFeedAudit {
	pins := []PaperFeedSourcePin{}
	for _, v := range decisions {
		pins = append(pins, decisionFeedSourcePin(v))
	}
	for _, v := range outcomes {
		pins = append(pins, outcomeFeedSourcePin(v))
	}
	slices.SortFunc(pins, func(a, b PaperFeedSourcePin) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	return PaperFeedAudit{Schema: "mantik-paper-feed-audit-v1", ReceiverID: owner, Status: "verified", Decisions: len(decisions), Outcomes: len(outcomes), Checkpoints: checkpoints, SourceArchiveSHA256: source.HeadSHA256, SourceIndexSHA256: source.SourceSHA256, RecordsSHA256: workerplan.Hash(pins)}
}

func (m *Memory) AuditPaperFeed(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperFeedAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperFeedAudit{}, err
	}
	source, err := m.auditFeedArchiveSourcesLocked(ctx)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperFeedAudit{}, err
	}
	feed := m.decisionFeedLocked()
	e := &paperFeedArchiveEvaluators{de, oe}
	if _, err := paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, feed, de, oe); err != nil {
		return PaperFeedAudit{}, err
	}
	if err := auditOriginalPaperFeed(ctx, feed, m.paperDecisions, m.paperOutcomes, strategies, e, time.Time{}, m.feedWindowReaderLocked(ctx, m.receiverID)); err != nil {
		return PaperFeedAudit{}, err
	}
	previous := ""
	for i, c := range m.paperFeedCheckpoints {
		if err := ctx.Err(); err != nil {
			return PaperFeedAudit{}, err
		}
		if c.Sequence != int64(i+1) || c.PreviousSHA256 != previous || c.Archive.Sequence < 1 || c.Archive.Sequence > int64(len(m.feedArchives)) {
			return PaperFeedAudit{}, ErrFeedArchiveConflict
		}
		a := m.feedArchives[c.Archive.Sequence-1]
		a.Source.custody, err = m.feedCustodyLocked(a.Source.Retention)
		if err != nil {
			return PaperFeedAudit{}, err
		}
		if err := verifyPaperFeedCheckpoint(c, a, m.paperDecisions, m.paperOutcomes, strategies, e); err != nil {
			return PaperFeedAudit{}, err
		}
		previous = c.SHA256
	}
	return paperFeedAuditReceipt(m.receiverID, m.paperDecisions, m.paperOutcomes, source, int64(len(m.paperFeedCheckpoints))), nil
}

func (p *Postgres) AuditPaperFeed(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperFeedAudit, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperFeedAudit{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	source, err := auditPostgresFeedArchiveSources(ctx, tx)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	outcomes, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperFeedAudit{}, err
	}
	e := &paperFeedArchiveEvaluators{de, oe}
	if _, err := paperOutcomeCatalog(feed.ReceiverID, outcomes, decisions, strategies, feed, de, oe); err != nil {
		return PaperFeedAudit{}, err
	}
	if err := auditOriginalPaperFeed(ctx, feed, decisions, outcomes, strategies, e, time.Time{}, postgresFeedWindowReader(ctx, tx, feed.ReceiverID)); err != nil {
		return PaperFeedAudit{}, err
	}
	var checkpoints int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_feed_checkpoints").Scan(&checkpoints); err != nil {
		return PaperFeedAudit{}, err
	}
	previous := ""
	for sequence := int64(1); sequence <= checkpoints; sequence++ {
		c, err := readPaperFeedCheckpoint(ctx, tx, sequence)
		if err != nil {
			return PaperFeedAudit{}, err
		}
		if c.PreviousSHA256 != previous {
			return PaperFeedAudit{}, ErrFeedArchiveConflict
		}
		a, err := readFeedArchive(ctx, tx, c.Archive.Sequence)
		if err != nil {
			return PaperFeedAudit{}, err
		}
		a.Source.custody, err = readFeedCustody(ctx, tx, a.Source.Retention)
		if err != nil {
			return PaperFeedAudit{}, err
		}
		if err := verifyPaperFeedCheckpoint(c, a, decisions, outcomes, strategies, e); err != nil {
			return PaperFeedAudit{}, err
		}
		previous = c.SHA256
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperFeedAudit{}, err
	}
	return paperFeedAuditReceipt(feed.ReceiverID, decisions, outcomes, source, checkpoints), nil
}
