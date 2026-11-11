package storage

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type PaperRecordArchiveAudit struct {
	Schema              string `json:"schema"`
	ReceiverID          string `json:"receiver_id"`
	Status              string `json:"status"`
	Archives            int64  `json:"archives"`
	Records             int    `json:"records"`
	HeadSHA256          string `json:"head_sha256"`
	OriginalsSHA256     string `json:"originals_sha256"`
	AdmissionAvailable  bool   `json:"admission_available"`
	ExecutionAvailable  bool   `json:"execution_available"`
	QualificationCredit bool   `json:"qualification_credit"`
}

type PaperRecordArchiveAuditStore interface {
	AuditPaperRecordArchives(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperRecordArchiveAudit, error)
}

type originalPaperArchivePin struct {
	PaperArchiveRecordPin
	Sequence int64 `json:"sequence"`
}

func paperArchiveAuditReceipt(owner string, head PaperRecordArchiveHeader, original map[string]originalPaperArchivePin) PaperRecordArchiveAudit {
	pins := make([]originalPaperArchivePin, 0, len(original))
	for _, p := range original {
		pins = append(pins, p)
	}
	slices.SortFunc(pins, func(a, b originalPaperArchivePin) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	return PaperRecordArchiveAudit{Schema: "mantik-paper-record-archive-audit-v1", ReceiverID: owner, Status: "verified", Archives: head.Sequence, Records: len(original), HeadSHA256: head.SHA256, OriginalsSHA256: workerplan.Hash(pins)}
}

func auditPaperArchiveStep(previous PaperRecordArchiveHeader, a PaperRecordArchive, original map[string]originalPaperArchivePin, retired map[string]int64) error {
	if a.Sequence != previous.Sequence+1 || a.PreviousSHA256 != previous.SHA256 || previous.Sequence > 0 && a.ArchivedAt.Before(previous.ArchivedAt) {
		return ErrPaperRecordArchiveConflict
	}
	present := map[string]bool{}
	for _, r := range a.Records {
		key := r.Kind + ":" + r.ID
		present[key] = true
		if old, ok := original[key]; ok {
			if old.PaperArchiveRecordPin != r.PaperArchiveRecordPin {
				return ErrPaperRecordArchiveConflict
			}
		} else {
			original[key] = originalPaperArchivePin{r.PaperArchiveRecordPin, a.Sequence}
		}
	}
	// A later snapshot omits an earlier owner only after an approved
	// retirement has ended its interval by this archive's epoch.
	for key, pin := range original {
		if !present[key] && (retired[key] <= pin.CreatedEpoch || retired[key] > a.Sequence-1) {
			return ErrPaperRecordArchiveConflict
		}
	}
	return nil
}

// A complete audit grows with archive history. Every segment is independently
// replayed against its retained native context and original feed sources.
func (m *Memory) AuditPaperRecordArchives(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchiveAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if de == nil || oe == nil {
		return PaperRecordArchiveAudit{}, ErrPaperRecordArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	original := map[string]originalPaperArchivePin{}
	var previous PaperRecordArchiveHeader
	for n := int64(1); n <= int64(len(m.paperRecordArchives)); n++ {
		if err := ctx.Err(); err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		a, err := m.paperRecordArchiveLocked(ctx, n, de, oe)
		if err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		if err := auditPaperArchiveStep(previous, a, original, m.paperRecordRetiredEpochs); err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		previous = a.PaperRecordArchiveHeader
	}
	expected := map[string]int64{}
	for key, pin := range original {
		expected[key] = pin.Sequence
	}
	if !reflect.DeepEqual(expected, m.paperArchiveOriginals) && !(len(expected) == 0 && len(m.paperArchiveOriginals) == 0) {
		return PaperRecordArchiveAudit{}, ErrPaperRecordArchiveConflict
	}
	return paperArchiveAuditReceipt(m.receiverID, previous, original), nil
}

func (p *Postgres) AuditPaperRecordArchives(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRecordArchiveAudit, error) {
	if de == nil || oe == nil {
		return PaperRecordArchiveAudit{}, ErrPaperRecordArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var count int64
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&count); err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	retired, err := readAllPaperRetiredEpochs(ctx, tx)
	if err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	original := map[string]originalPaperArchivePin{}
	var previous PaperRecordArchiveHeader
	for n := int64(1); n <= count; n++ {
		a, err := readPaperRecordArchive(ctx, tx, n, de, oe, p.portfolioEvaluator)
		if err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		if err := auditPaperArchiveStep(previous, a, original, retired); err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		previous = a.PaperRecordArchiveHeader
	}
	rows, err := tx.Query(ctx, "SELECT kind,record_id,min(archive_sequence) FROM mantik_paper_archive_members GROUP BY kind,record_id")
	if err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var kind, id string
		var sequence int64
		if err := rows.Scan(&kind, &id, &sequence); err != nil {
			return PaperRecordArchiveAudit{}, err
		}
		pin, ok := original[kind+":"+id]
		if !ok || sequence != pin.Sequence {
			return PaperRecordArchiveAudit{}, ErrPaperRecordArchiveConflict
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	rows.Close()
	if seen != len(original) {
		return PaperRecordArchiveAudit{}, ErrPaperRecordArchiveConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperRecordArchiveAudit{}, err
	}
	return paperArchiveAuditReceipt(owner, previous, original), nil
}

func readAllPaperRetiredEpochs(ctx context.Context, tx pgx.Tx) (map[string]int64, error) {
	rows, err := tx.Query(ctx, "SELECT kind,record_id,retired_epoch FROM mantik_paper_record_epochs WHERE retired_epoch IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var kind, id string
		var epoch int64
		if err := rows.Scan(&kind, &id, &epoch); err != nil {
			return nil, err
		}
		out[kind+":"+id] = epoch
	}
	return out, rows.Err()
}
