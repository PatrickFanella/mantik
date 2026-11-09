package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

func readPaperRecordArchiveHeader(ctx context.Context, tx pgx.Tx, sequence int64) (PaperRecordArchiveHeader, error) {
	var raw []byte
	var sha, records string
	var batchSHA, batchSource, batchRecords string
	var count int
	err := tx.QueryRow(ctx, "SELECT a.sha256,a.records_sha256,a.header,coalesce(b.archive_sha256,''),coalesce(b.source_sha256,''),coalesce(b.records_sha256,''),coalesce(b.record_count,-1) FROM mantik_paper_record_archives a LEFT JOIN mantik_paper_archive_batches b ON b.archive_sequence=a.sequence WHERE a.sequence=$1", sequence).Scan(&sha, &records, &raw, &batchSHA, &batchSource, &batchRecords, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperRecordArchiveHeader{}, ErrPaperRecordArchiveNotFound
	}
	if err != nil {
		return PaperRecordArchiveHeader{}, err
	}
	var h PaperRecordArchiveHeader
	if workerplan.ReadBoundedShape(raw, &h, 4096) != nil || h.Validate() != nil || h.Sequence != sequence || h.SHA256 != sha || h.RecordsSHA256 != records || batchSHA != sha || batchSource != h.SourceSHA256 || batchRecords != records || count != h.Decisions+h.Outcomes+h.Assessments+h.Reviews {
		return PaperRecordArchiveHeader{}, ErrPaperRecordArchiveConflict
	}
	return h, nil
}

func readPaperArchiveMembers(ctx context.Context, tx pgx.Tx, a PaperRecordArchive) error {
	var archive, source, records string
	var count int
	err := tx.QueryRow(ctx, "SELECT archive_sha256,source_sha256,records_sha256,record_count FROM mantik_paper_archive_batches WHERE archive_sequence=$1", a.Sequence).Scan(&archive, &source, &records, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPaperRecordArchiveConflict
	}
	if err != nil {
		return err
	}
	if archive != a.SHA256 || source != a.SourceSHA256 || records != a.RecordsSHA256 || count != len(a.Records) {
		return ErrPaperRecordArchiveConflict
	}
	var members int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM mantik_paper_archive_members WHERE archive_sequence=$1", a.Sequence).Scan(&members); err != nil {
		return err
	}
	if members != len(a.Records) {
		return ErrPaperRecordArchiveConflict
	}
	// Immutable creation epochs independently close the complete owner set.
	// Rehashing or deleting both a manifest and its member rows cannot omit a
	// record which actually existed when this archive committed.
	rows, err := tx.Query(ctx, "SELECT e.kind,e.record_id,coalesce(m.created_epoch,-1),coalesce(m.record_sha256,''),coalesce(m.payload_sha256,''),e.created_epoch,e.record_sha256,e.at_seconds,e.at_nanos,e.strategy_id,e.strategy_sha256,e.decision_id,e.decision_sha256,e.decision_at_seconds,e.decision_at_nanos FROM mantik_paper_record_epochs e LEFT JOIN mantik_paper_archive_members m ON m.archive_sequence=$1 AND m.kind=e.kind AND m.record_id=e.record_id WHERE int8range(e.created_epoch,e.retired_epoch,'[)') @> ($1::bigint-1) ORDER BY e.kind,e.record_id LIMIT $2", a.Sequence, maxPaperArchiveRecords+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := []PaperArchiveRecordPin{}
	scopes := map[string]paperRecordScope{}
	for rows.Next() {
		var p PaperArchiveRecordPin
		var epoch, seconds int64
		var sha string
		var nanos int
		var strategyID, strategySHA, decisionID, decisionSHA *string
		var decisionSeconds *int64
		var decisionNanos *int
		if err := rows.Scan(&p.Kind, &p.ID, &p.CreatedEpoch, &p.RecordSHA256, &p.PayloadSHA256, &epoch, &sha, &seconds, &nanos, &strategyID, &strategySHA, &decisionID, &decisionSHA, &decisionSeconds, &decisionNanos); err != nil {
			return err
		}
		if p.Kind == "decision" || p.Kind == "outcome" {
			if strategyID == nil || strategySHA == nil || decisionID == nil || decisionSHA == nil || decisionSeconds == nil || decisionNanos == nil {
				return ErrPaperRecordArchiveConflict
			}
			scopes[p.Kind+":"+p.ID] = paperRecordScope{*strategyID, *strategySHA, *decisionID, *decisionSHA, time.Unix(*decisionSeconds, int64(*decisionNanos)).UTC()}
		} else if strategyID != nil || strategySHA != nil || decisionID != nil || decisionSHA != nil || decisionSeconds != nil || decisionNanos != nil {
			return ErrPaperRecordArchiveConflict
		}
		if epoch != p.CreatedEpoch || sha != p.RecordSHA256 {
			return ErrPaperRecordArchiveConflict
		}
		p.At = time.Unix(seconds, int64(nanos)).UTC()
		actual = append(actual, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, paperArchivePins(a.Records)) {
		return ErrPaperRecordArchiveConflict
	}
	return checkPaperRecordScopes(a.Records, scopes)
}

func readPaperRecordArchivePayload(ctx context.Context, tx pgx.Tx, sequence int64) (PaperRecordArchive, error) {
	if sequence < 1 {
		return PaperRecordArchive{}, ErrPaperRecordArchiveInvalid
	}
	h, err := readPaperRecordArchiveHeader(ctx, tx, sequence)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return PaperRecordArchive{}, err
	}
	if h.ReceiverID != owner {
		return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
	}
	if sequence > 1 {
		previous, err := readPaperRecordArchiveHeader(ctx, tx, sequence-1)
		if err != nil || previous.SHA256 != h.PreviousSHA256 {
			return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
		}
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_paper_record_archives WHERE sequence=$1", sequence).Scan(&raw); err != nil {
		return PaperRecordArchive{}, err
	}
	var a PaperRecordArchive
	if workerplan.ReadBoundedShape(raw, &a, MaxPaperRecordArchiveBytes) != nil || a.Validate() != nil || a.PaperRecordArchiveHeader != h {
		return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
	}
	if err := readPaperArchiveMembers(ctx, tx, a); err != nil {
		return PaperRecordArchive{}, err
	}
	return a, nil
}

func readPaperRecordArchive(ctx context.Context, tx pgx.Tx, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (PaperRecordArchive, error) {
	if de == nil || oe == nil {
		return PaperRecordArchive{}, ErrPaperRecordArchiveInvalid
	}
	a, err := readPaperRecordArchivePayload(ctx, tx, sequence)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	owner := a.ReceiverID
	owners, err := decodePaperArchiveOwners(a.Records)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	a.Context.Feed.custody, err = readFeedCustodyForOwners(ctx, tx, a.Context.Feed.Retention, paperFeedCustodyKeys(owners.decisions, owners.outcomes))
	if err != nil {
		return PaperRecordArchive{}, err
	}
	if err := verifyPaperArchiveOwners(ctx, a.Context, owners, de, oe, portfolio, postgresFeedWindowReader(ctx, tx, owner), func(all map[string]PaperEvaluationAssessment) (PaperEvaluationAssessmentCatalog, error) {
		return readPaperAssessmentOwners(ctx, tx, owner, all, de, oe, portfolio)
	}); err != nil {
		return PaperRecordArchive{}, err
	}
	return a, nil
}

func (p *Postgres) ArchivePaperRecords(ctx context.Context, owner string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchiveReceipt, error) {
	if !ValidReceiverID(owner) || de == nil || oe == nil {
		return PaperRecordArchiveReceipt{}, ErrPaperRecordArchiveInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Paper and social owners share collector -> state -> receiver order. A
	// waiting writer gets its creation epoch only after this archive commits.
	s, err := readPaperEvaluationSources(ctx, tx, true, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if s.feed.ReceiverID != owner {
		return PaperRecordArchiveReceipt{}, ErrReceiverMismatch
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	reviews, err := readDecisionReviews(ctx, tx)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	owners := paperArchiveOwners{s.retained, s.retainedOutcomes, assessments, reviews}
	var social []socialwatch.Journal
	if decisionReviewNeedsSources(DecisionReviewCommand{}, reviews) {
		j, err := readSocialJournal(ctx, tx, owner)
		if err != nil {
			return PaperRecordArchiveReceipt{}, err
		}
		social = []socialwatch.Journal{j}
	}
	c := paperArchiveContext(s, social)
	if err := verifyPaperArchiveOwners(ctx, c, owners, de, oe, p.portfolioEvaluator, postgresFeedWindowReader(ctx, tx, owner), func(all map[string]PaperEvaluationAssessment) (PaperEvaluationAssessmentCatalog, error) {
		return readPaperAssessmentOwners(ctx, tx, owner, all, de, oe, p.portfolioEvaluator)
	}); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	epochs, err := readPaperRecordEpochs(ctx, tx, owners.keys())
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	records, err := owners.records(epochs)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	var sequence int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&sequence); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	var previous PaperRecordArchiveHeader
	if sequence > 0 {
		old, err := readPaperRecordArchive(ctx, tx, sequence, de, oe, p.portfolioEvaluator)
		if err != nil {
			return PaperRecordArchiveReceipt{}, err
		}
		previous = old.PaperRecordArchiveHeader
		if err := paperArchiveExtends(old, records); err != nil {
			return PaperRecordArchiveReceipt{}, err
		}
		if workerplan.Hash(paperArchivePins(records)) == previous.RecordsSHA256 {
			if err := tx.Commit(ctx); err != nil {
				return PaperRecordArchiveReceipt{}, err
			}
			return PaperRecordArchiveReceipt{previous, true}, nil
		}
	}
	a, err := newPaperRecordArchive(c, records, previous, now)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	header, err := json.Marshal(a.PaperRecordArchiveHeader)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_record_archives(sequence,sha256,records_sha256,header,payload) VALUES($1,$2,$3,$4,$5)", a.Sequence, a.SHA256, a.RecordsSHA256, header, raw); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	pins, err := json.Marshal(paperArchivePins(records))
	if err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_archive_members(archive_sequence,kind,record_id,created_epoch,record_sha256,payload_sha256) SELECT $1,kind,id,created_epoch,record_sha256,payload_sha256 FROM jsonb_to_recordset($2::jsonb) AS r(kind text,id text,created_epoch bigint,record_sha256 text,payload_sha256 text)", a.Sequence, pins); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_archive_batches(archive_sequence,archive_sha256,source_sha256,records_sha256,record_count) VALUES($1,$2,$3,$4,$5)", a.Sequence, a.SHA256, a.SourceSHA256, a.RecordsSHA256, len(records)); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if err := readPaperArchiveMembers(ctx, tx, a); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperRecordArchiveReceipt{}, err
	}
	return PaperRecordArchiveReceipt{a.PaperRecordArchiveHeader, false}, nil
}

func (p *Postgres) PaperRecordArchive(ctx context.Context, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchive, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperRecordArchive{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	a, err := readPaperRecordArchive(ctx, tx, sequence, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperRecordArchive{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperRecordArchive{}, err
	}
	return a, nil
}

func (p *Postgres) PaperArchivedRecord(ctx context.Context, kind, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperArchivedRecord, error) {
	if !paperRecordKind(kind) || !paperaccount.ID.MatchString(id) {
		return PaperArchivedRecord{}, ErrPaperRecordArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperArchivedRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var sequence int64
	// Every archive closes its preceding creation epoch. Resolve the original
	// slot through immutable creation metadata, so a deleted first member cannot
	// make lookup silently select a later copy of the same record.
	if err := tx.QueryRow(ctx, "SELECT created_epoch+1 FROM mantik_paper_record_epochs WHERE kind=$1 AND record_id=$2 AND created_epoch<(SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives)", kind, id).Scan(&sequence); errors.Is(err, pgx.ErrNoRows) {
		return PaperArchivedRecord{}, ErrPaperRecordArchiveNotFound
	} else if err != nil {
		return PaperArchivedRecord{}, err
	}
	a, err := readPaperRecordArchive(ctx, tx, sequence, de, oe, p.portfolioEvaluator)
	if err != nil {
		if errors.Is(err, ErrPaperRecordArchiveNotFound) {
			err = ErrPaperRecordArchiveConflict
		}
		return PaperArchivedRecord{}, err
	}
	for _, r := range a.Records {
		if r.Kind == kind && r.ID == id {
			if err := tx.Commit(ctx); err != nil {
				return PaperArchivedRecord{}, err
			}
			return PaperArchivedRecord{a.PaperRecordArchiveHeader, r}, nil
		}
	}
	return PaperArchivedRecord{}, ErrPaperRecordArchiveConflict
}

func (p *Postgres) PaperRecordArchives(ctx context.Context, after int64, limit int) (PaperRecordArchivePage, error) {
	if after < 0 || limit < 1 || limit > 32 {
		return PaperRecordArchivePage{}, ErrPaperRecordArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperRecordArchivePage{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	out := PaperRecordArchivePage{Records: []PaperRecordArchiveHeader{}}
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ReceiverID); err != nil {
		return PaperRecordArchivePage{}, err
	}
	rows, err := tx.Query(ctx, "SELECT a.sequence,a.sha256,a.records_sha256,a.header,coalesce(b.archive_sha256,''),coalesce(b.source_sha256,''),coalesce(b.records_sha256,''),coalesce(b.record_count,-1) FROM mantik_paper_record_archives a LEFT JOIN mantik_paper_archive_batches b ON b.archive_sequence=a.sequence WHERE a.sequence>$1 ORDER BY a.sequence LIMIT $2", after, limit+1)
	if err != nil {
		return PaperRecordArchivePage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var sha, records string
		var batchSHA, batchSource, batchRecords string
		var count int
		var raw []byte
		if err := rows.Scan(&sequence, &sha, &records, &raw, &batchSHA, &batchSource, &batchRecords, &count); err != nil {
			return PaperRecordArchivePage{}, err
		}
		var h PaperRecordArchiveHeader
		if workerplan.ReadBoundedShape(raw, &h, 4096) != nil || h.Validate() != nil || h.Sequence != sequence || sequence != after+int64(len(out.Records))+1 || h.ReceiverID != out.ReceiverID || h.SHA256 != sha || h.RecordsSHA256 != records || batchSHA != sha || batchSource != h.SourceSHA256 || batchRecords != records || count != h.Decisions+h.Outcomes+h.Assessments+h.Reviews || len(out.Records) > 0 && h.PreviousSHA256 != out.Records[len(out.Records)-1].SHA256 {
			return PaperRecordArchivePage{}, ErrPaperRecordArchiveConflict
		}
		if len(out.Records) == limit {
			out.NextAfter = out.Records[len(out.Records)-1].Sequence
			break
		}
		out.Records = append(out.Records, h)
	}
	if err := rows.Err(); err != nil {
		return PaperRecordArchivePage{}, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return PaperRecordArchivePage{}, err
	}
	return out, nil
}

var _ PaperRecordArchiveStore = (*Memory)(nil)
var _ PaperRecordArchiveStore = (*Postgres)(nil)
