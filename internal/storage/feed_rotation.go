package storage

import (
	"context"
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"github.com/jackc/pgx/v5"
)

type FeedRetentionReceipt struct {
	Sequence            int64  `json:"sequence"`
	SHA256              string `json:"sha256"`
	CheckpointSHA256    string `json:"checkpoint_sha256"`
	PublicationsRetired int    `json:"publications_retired"`
	AdmissionsRetired   int    `json:"admissions_retired"`
	CommandsRetired     int    `json:"commands_retired"`
	InputsRetired       int    `json:"inputs_retired"`
	Duplicate           bool   `json:"duplicate"`
}

type FeedRetentionStore interface {
	RotateFeed(context.Context, string, func() time.Time, FeedArchiveCheck, PaperDecisionEvaluator, PaperOutcomeEvaluator) (FeedArchiveReceipt, error)
}

func (m *Memory) RotateFeed(ctx context.Context, owner string, now func() time.Time, check FeedArchiveCheck, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (FeedArchiveReceipt, error) {
	if de == nil || oe == nil {
		return FeedArchiveReceipt{}, ErrFeedArchiveInvalid
	}
	return m.archiveFeed(ctx, owner, now, check, &paperFeedArchiveEvaluators{de, oe}, true)
}
func (p *Postgres) RotateFeed(ctx context.Context, owner string, now func() time.Time, check FeedArchiveCheck, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (FeedArchiveReceipt, error) {
	if de == nil || oe == nil {
		return FeedArchiveReceipt{}, ErrFeedArchiveInvalid
	}
	return p.archiveFeed(ctx, owner, now, check, &paperFeedArchiveEvaluators{de, oe}, true)
}

func feedRotationReceipt(before, after FeedEvidenceSnapshot) *FeedRetentionReceipt {
	r := after.Retention
	out := &FeedRetentionReceipt{Sequence: r.Sequence, SHA256: r.SHA256, CheckpointSHA256: r.Checkpoint.SHA256, PublicationsRetired: len(before.Records) - len(after.Records), AdmissionsRetired: len(before.Collectors.Admissions) - len(after.Collectors.Admissions), CommandsRetired: len(before.Collectors.Commands) - len(after.Collectors.Commands)}
	if before.Routing != nil {
		out.InputsRetired = len(before.Routing.Inputs) - len(after.Routing.Inputs)
	}
	return out
}

func unchangedFeedRotation(feed FeedEvidenceSnapshot, latest int64) (FeedArchiveReceipt, bool, error) {
	r := feed.Retention
	if r == nil || r.Checkpoint.Archive.Sequence != latest {
		return FeedArchiveReceipt{}, false, nil
	}
	if err := validateFeedCustody(feed); err != nil {
		return FeedArchiveReceipt{}, false, err
	}
	sha, err := feedWorkingSourceHash(feed)
	if err != nil {
		return FeedArchiveReceipt{}, false, err
	}
	if sha != r.WorkingSourceSHA256 {
		return FeedArchiveReceipt{}, false, nil
	}
	return FeedArchiveReceipt{Header: r.Checkpoint.Archive, Duplicate: true, PaperCustody: &PaperFeedCheckpointReceipt{Header: r.Checkpoint, Duplicate: true}, Retention: &FeedRetentionReceipt{Sequence: r.Sequence, SHA256: r.SHA256, CheckpointSHA256: r.Checkpoint.SHA256, Duplicate: true}}, true, nil
}

func (m *Memory) prepareFeedRotation(source FeedEvidenceSnapshot, cp *PaperFeedCheckpoint, rotate bool) (*FeedEvidenceSnapshot, error) {
	if !rotate {
		return nil, nil
	}
	if cp == nil {
		return nil, ErrFeedArchiveConflict
	}
	next, _, err := retainedFeedSources(source, *cp, m.feedRetention)
	if err != nil {
		return nil, err
	}
	return &next, nil
}
func (m *Memory) retainFeedRotation(source FeedEvidenceSnapshot, next *FeedEvidenceSnapshot) *FeedRetentionReceipt {
	if next == nil {
		return nil
	}
	r := feedRotationReceipt(source, *next)
	m.evidence, m.collectors, m.state.Routing, m.feedRetention = next.Records, next.Collectors, next.Routing, next.Retention
	return r
}

func writeFeedRotation(ctx context.Context, tx pgx.Tx, source FeedEvidenceSnapshot, cp *PaperFeedCheckpointReceipt, rotate bool) (*FeedRetentionReceipt, error) {
	if !rotate {
		return nil, nil
	}
	if cp == nil {
		return nil, ErrFeedArchiveConflict
	}
	checkpoint, err := readPaperFeedCheckpoint(ctx, tx, cp.Header.Sequence)
	if err != nil {
		return nil, err
	}
	next, retirement, err := retainedFeedSources(source, checkpoint, source.Retention)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(retirement)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_feed_retention(sequence,checkpoint_sequence,sha256,payload) VALUES($1,$2,$3,$4)", retirement.Sequence, retirement.Checkpoint.Sequence, retirement.SHA256, raw); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(next.Collectors)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", raw); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return nil, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	state.Routing = next.Routing
	raw, err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(next.Records))
	for key := range next.Records {
		keys = append(keys, key)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM mantik_feed_evidence WHERE NOT ((registration_id||':'||capture_key)=ANY($1::text[]))", keys); err != nil {
		return nil, err
	}
	return feedRotationReceipt(source, next), nil
}

var _ FeedRetentionStore = (*Memory)(nil)
var _ FeedRetentionStore = (*Postgres)(nil)
