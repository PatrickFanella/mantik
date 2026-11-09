package storage

import (
	"cmp"
	"context"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const maxPaperSamplePage = 128

// These private pages contain selection metadata, not evaluation evidence. The
// consumer must resolve and replay the original payloads before using a sample.
// The declaration must already be replayed against its actual owner sources.
// All pages share the same memory lock or PostgreSQL owner transaction/snapshot.
type paperSampleRecord struct {
	PaperRecordEpoch
	Scope        paperRecordScope
	RetiredEpoch int64
}

type paperSampleCursor struct {
	QuerySHA256 string
	After       paperSampleRecord
}

type paperSamplePage struct {
	Records []paperSampleRecord
	Next    *paperSampleCursor
}

func paperSampleOrder(a, b paperSampleRecord) int {
	for _, comparison := range []int{a.Scope.DecisionAt.Compare(b.Scope.DecisionAt), cmp.Compare(a.Scope.DecisionID, b.Scope.DecisionID), cmp.Compare(a.Kind, b.Kind), a.At.Compare(b.At), cmp.Compare(a.ID, b.ID)} {
		if comparison != 0 {
			return comparison
		}
	}
	return 0
}

func paperSampleQuery(d PaperEvaluationDeclaration, at time.Time, head int64, after *paperSampleCursor, limit int) (string, error) {
	if d.Command.Validate() != nil || !decisionPin.MatchString(d.SHA256) || !declarationTime(at) || head < 0 || limit < 1 || limit > maxPaperSamplePage {
		return "", ErrPaperRecordArchiveInvalid
	}
	sha := workerplan.Hash(struct {
		DeclarationSHA256 string
		Command           PaperEvaluationDeclarationCommand
		AsOf              time.Time
		ArchiveHead       int64
	}{d.SHA256, d.Command, at.UTC(), head})
	if after != nil && (after.QuerySHA256 != sha || !paperSampleMatches(after.After, d.Command.Plan, at, head)) {
		return "", ErrPaperRecordArchiveInvalid
	}
	return sha, nil
}

func validPaperSampleRecord(r paperSampleRecord, head int64) bool {
	s := r.Scope
	if (r.Kind != "decision" && r.Kind != "outcome") || !paperaccount.ID.MatchString(r.ID) || !decisionPin.MatchString(r.RecordSHA256) ||
		r.CreatedEpoch < 0 || r.CreatedEpoch > head || !declarationTime(r.At) ||
		r.RetiredEpoch != 0 && (r.RetiredEpoch <= r.CreatedEpoch || r.RetiredEpoch > head) ||
		!paperaccount.ID.MatchString(s.StrategyID) || !decisionPin.MatchString(s.StrategySHA256) ||
		!paperaccount.ID.MatchString(s.DecisionID) || !decisionPin.MatchString(s.DecisionSHA256) || !declarationTime(s.DecisionAt) || s.DecisionAt.After(r.At) ||
		r.Kind == "decision" && (s.DecisionID != r.ID || s.DecisionSHA256 != r.RecordSHA256 || !s.DecisionAt.Equal(r.At)) {
		return false
	}
	return true
}

func paperSampleMatches(r paperSampleRecord, plan PaperEvaluationPlan, at time.Time, head int64) bool {
	s := r.Scope
	if !validPaperSampleRecord(r, head) || r.At.After(at) || s.DecisionAt.Before(plan.WindowStart) || !s.DecisionAt.Before(plan.WindowEnd) {
		return false
	}
	for _, v := range plan.Variants {
		if s.StrategyID == v.ID && s.StrategySHA256 == v.SHA256 {
			return true
		}
	}
	return false
}

func finishPaperSamplePage(records []paperSampleRecord, sha string, limit int) paperSamplePage {
	page := paperSamplePage{Records: records}
	if len(records) > limit {
		page.Records = records[:limit:limit]
		page.Next = &paperSampleCursor{sha, records[limit-1]}
	}
	return page
}

func (m *Memory) paperSampleWindowLocked(ctx context.Context, d PaperEvaluationDeclaration, at time.Time, after *paperSampleCursor, limit int) (paperSamplePage, error) {
	if err := ctx.Err(); err != nil {
		return paperSamplePage{}, err
	}
	if d.Command.ReceiverID != m.receiverID {
		return paperSamplePage{}, ErrReceiverMismatch
	}
	head := int64(len(m.paperRecordArchives))
	sha, err := paperSampleQuery(d, at, head, after, limit)
	if err != nil {
		return paperSamplePage{}, err
	}
	records := []paperSampleRecord{}
	for key, e := range m.paperRecordEpochs {
		if err := ctx.Err(); err != nil {
			return paperSamplePage{}, err
		}
		if e.Kind != "decision" && e.Kind != "outcome" {
			continue
		}
		scope, ok := m.paperRecordScopes[key]
		if !ok || key != e.Kind+":"+e.ID {
			return paperSamplePage{}, ErrPaperRecordArchiveConflict
		}
		r := paperSampleRecord{e, scope, m.paperRecordRetiredEpochs[key]}
		if !validPaperSampleRecord(r, head) {
			return paperSamplePage{}, ErrPaperRecordArchiveConflict
		}
		if !paperSampleMatches(r, d.Command.Plan, at, head) || after != nil && paperSampleOrder(r, after.After) <= 0 {
			continue
		}
		// Keep only the next bounded batch, even when history or this window
		// contains more rows than any active catalogue.
		i, _ := slices.BinarySearchFunc(records, r, paperSampleOrder)
		if i <= limit {
			records = slices.Insert(records, i, r)
			if len(records) > limit+1 {
				records = records[:limit+1]
			}
		}
	}
	return finishPaperSamplePage(records, sha, limit), nil
}

// Each declared variant uses the scope/time index and returns at most limit+1
// small metadata rows. The outer merge never joins historical payload bodies.
const paperSampleWindowQuery = `SELECT e.kind,e.record_id,e.created_epoch,e.record_sha256,e.at_seconds,e.at_nanos,
e.strategy_id,e.strategy_sha256,e.decision_id,e.decision_sha256,e.decision_at_seconds,e.decision_at_nanos,coalesce(e.retired_epoch,0)
FROM unnest($1::text[],$2::text[]) AS v(id,sha)
CROSS JOIN LATERAL (
 SELECT * FROM mantik_paper_record_epochs e
 WHERE e.strategy_id=v.id AND e.strategy_sha256=v.sha AND e.kind IN ('decision','outcome') AND e.created_epoch<=$3
 AND (e.decision_at_seconds,e.decision_at_nanos)>=($4::bigint,$5::integer)
 AND (e.decision_at_seconds,e.decision_at_nanos)<($6::bigint,$7::integer)
 AND (e.at_seconds,e.at_nanos)<=($8::bigint,$9::integer)
 AND ($10::boolean OR (e.decision_at_seconds,e.decision_at_nanos,e.decision_id COLLATE "C",e.kind COLLATE "C",e.at_seconds,e.at_nanos,e.record_id COLLATE "C")>
     ($11::bigint,$12::integer,$13::text,$14::text,$15::bigint,$16::integer,$17::text))
 ORDER BY e.decision_at_seconds,e.decision_at_nanos,e.decision_id COLLATE "C",e.kind COLLATE "C",e.at_seconds,e.at_nanos,e.record_id COLLATE "C"
 LIMIT $18
) e
ORDER BY e.decision_at_seconds,e.decision_at_nanos,e.decision_id COLLATE "C",e.kind COLLATE "C",e.at_seconds,e.at_nanos,e.record_id COLLATE "C"
LIMIT $18`

func readPaperSampleWindow(ctx context.Context, tx pgx.Tx, d PaperEvaluationDeclaration, at time.Time, after *paperSampleCursor, limit int) (paperSamplePage, error) {
	var head int64
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id,(SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives) FROM mantik_research_receiver WHERE singleton").Scan(&owner, &head); err != nil {
		return paperSamplePage{}, err
	}
	if d.Command.ReceiverID != owner {
		return paperSamplePage{}, ErrReceiverMismatch
	}
	sha, err := paperSampleQuery(d, at, head, after, limit)
	if err != nil {
		return paperSamplePage{}, err
	}
	ids, hashes := []string{}, []string{}
	for _, v := range d.Command.Plan.Variants {
		ids, hashes = append(ids, v.ID), append(hashes, v.SHA256)
	}
	var last paperSampleRecord
	if after != nil {
		last = after.After
	}
	p := d.Command.Plan
	rows, err := tx.Query(ctx, paperSampleWindowQuery, ids, hashes, head, p.WindowStart.Unix(), p.WindowStart.Nanosecond(), p.WindowEnd.Unix(), p.WindowEnd.Nanosecond(), at.Unix(), at.Nanosecond(), after == nil,
		last.Scope.DecisionAt.Unix(), last.Scope.DecisionAt.Nanosecond(), last.Scope.DecisionID, last.Kind, last.At.Unix(), last.At.Nanosecond(), last.ID, limit+1)
	if err != nil {
		return paperSamplePage{}, err
	}
	defer rows.Close()
	records := []paperSampleRecord{}
	for rows.Next() {
		r, err := scanPaperSampleRecord(rows)
		if err != nil {
			return paperSamplePage{}, err
		}
		if !paperSampleMatches(r, p, at, head) || after != nil && paperSampleOrder(r, after.After) <= 0 || len(records) > 0 && paperSampleOrder(records[len(records)-1], r) >= 0 {
			return paperSamplePage{}, ErrPaperRecordArchiveConflict
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return paperSamplePage{}, err
	}
	return finishPaperSamplePage(records, sha, limit), nil
}

func scanPaperSampleRecord(row pgx.Row, extra ...any) (paperSampleRecord, error) {
	var r paperSampleRecord
	var seconds, decisionSeconds int64
	var nanos, decisionNanos int
	fields := []any{&r.Kind, &r.ID, &r.CreatedEpoch, &r.RecordSHA256, &seconds, &nanos, &r.Scope.StrategyID, &r.Scope.StrategySHA256, &r.Scope.DecisionID, &r.Scope.DecisionSHA256, &decisionSeconds, &decisionNanos, &r.RetiredEpoch}
	if err := row.Scan(append(fields, extra...)...); err != nil {
		return paperSampleRecord{}, err
	}
	r.At, r.Scope.DecisionAt = time.Unix(seconds, int64(nanos)).UTC(), time.Unix(decisionSeconds, int64(decisionNanos)).UTC()
	return r, nil
}
