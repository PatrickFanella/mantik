package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperFeedCheckpointBytes = 1 << 20

// Pins bind actual paper records and their original bases. The checkpoint cut
// closes membership; subsequent source and paper events must be strictly later.
// A custody seal grants no admission, execution or qualification authority.
type PaperFeedSourcePin struct {
	Kind              string    `json:"kind"`
	ID                string    `json:"id"`
	Schema            string    `json:"schema"`
	RecordSHA256      string    `json:"record_sha256"`
	BasisSHA256       string    `json:"basis_sha256"`
	ObservationSHA256 string    `json:"observation_sha256"`
	At                time.Time `json:"at"`
	DecisionID        string    `json:"decision_id"`
	DecisionSHA256    string    `json:"decision_sha256"`
}

type PaperFeedPrefix struct {
	RegistrationID string                  `json:"registration_id"`
	Prefix         PaperDecisionFeedPrefix `json:"prefix"`
}

type PaperFeedCheckpointHeader struct {
	Schema               string            `json:"schema"`
	ReceiverID           string            `json:"receiver_id"`
	Sequence             int64             `json:"sequence"`
	CutAt                time.Time         `json:"cut_at"`
	PaperArchiveSequence int64             `json:"paper_archive_sequence"`
	PreviousSHA256       string            `json:"previous_sha256"`
	Archive              FeedArchiveHeader `json:"archive"`
	SourceSHA256         string            `json:"source_sha256"`
	RecordsSHA256        string            `json:"records_sha256"`
	PrefixesSHA256       string            `json:"prefixes_sha256"`
	Decisions            int               `json:"decisions"`
	Outcomes             int               `json:"outcomes"`
	Authority            string            `json:"authority"`
	AdmissionAvailable   bool              `json:"admission_available"`
	ExecutionAvailable   bool              `json:"execution_available"`
	QualificationCredit  bool              `json:"qualification_credit"`
	SHA256               string            `json:"sha256"`
}

type PaperFeedCheckpoint struct {
	PaperFeedCheckpointHeader
	Records  []PaperFeedSourcePin `json:"records"`
	Prefixes []PaperFeedPrefix    `json:"prefixes"`
}

type PaperFeedCheckpointReceipt struct {
	Header    PaperFeedCheckpointHeader `json:"header"`
	Duplicate bool                      `json:"duplicate"`
}

type PaperFeedArchiveStore interface {
	ArchivePaperFeed(context.Context, string, func() time.Time, FeedArchiveCheck, PaperDecisionEvaluator, PaperOutcomeEvaluator) (FeedArchiveReceipt, error)
	PaperFeedCheckpoint(context.Context, int64, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperFeedCheckpoint, error)
}

type paperFeedArchiveEvaluators struct {
	decision PaperDecisionEvaluator
	outcome  PaperOutcomeEvaluator
}

func paperFeedSourceIdentity(archiveSHA, recordsSHA string, at time.Time, paperArchive int64) string {
	return workerplan.Hash(struct {
		Archive, Records string
		At               time.Time
		PaperArchive     int64
	}{archiveSHA, recordsSHA, at, paperArchive})
}

func (h PaperFeedCheckpointHeader) Validate() error {
	sha := h.SHA256
	h.SHA256 = ""
	if h.Schema != "mantik-paper-feed-checkpoint-v1" || !ValidReceiverID(h.ReceiverID) || h.Sequence < 1 ||
		h.Sequence == 1 && h.PreviousSHA256 != "" || h.Sequence > 1 && !decisionPin.MatchString(h.PreviousSHA256) || h.Archive.Validate() != nil || h.Archive.ReceiverID != h.ReceiverID ||
		!decisionPin.MatchString(h.RecordsSHA256) || !decisionPin.MatchString(h.PrefixesSHA256) || h.SourceSHA256 != paperFeedSourceIdentity(h.Archive.SHA256, h.RecordsSHA256, h.CutAt, h.PaperArchiveSequence) ||
		!declarationTime(h.CutAt) || h.CutAt.Before(h.Archive.ArchivedAt) || h.PaperArchiveSequence != 0 || h.Decisions < 0 || h.Outcomes < 0 || h.Decisions > MaxPaperDecisions || h.Outcomes > MaxPaperOutcomes ||
		h.Authority != "service_owned_paper_source_custody_only" || h.AdmissionAvailable || h.ExecutionAvailable || h.QualificationCredit || !decisionPin.MatchString(sha) || workerplan.Hash(h) != sha {
		return ErrFeedArchiveConflict
	}
	return nil
}

func (c PaperFeedCheckpoint) Validate() error {
	if c.PaperFeedCheckpointHeader.Validate() != nil || c.Records == nil || c.Prefixes == nil || len(c.Records) > MaxPaperDecisions+MaxPaperOutcomes || len(c.Prefixes) != c.Archive.Registrations || workerplan.Hash(c.Records) != c.RecordsSHA256 || workerplan.Hash(c.Prefixes) != c.PrefixesSHA256 {
		return ErrFeedArchiveConflict
	}
	previous := ""
	decisions, outcomes := 0, 0
	for _, pin := range c.Records {
		key := pin.Kind + ":" + pin.ID
		if key <= previous || !paperaccount.ID.MatchString(pin.ID) || !paperaccount.ID.MatchString(pin.DecisionID) || !decisionPin.MatchString(pin.RecordSHA256) || !decisionPin.MatchString(pin.BasisSHA256) || !decisionPin.MatchString(pin.ObservationSHA256) || !decisionPin.MatchString(pin.DecisionSHA256) || !declarationTime(pin.At) || pin.At.After(c.CutAt) {
			return ErrFeedArchiveConflict
		}
		previous = key
		switch pin.Kind {
		case "decision":
			if pin.ID != pin.DecisionID || pin.RecordSHA256 != pin.DecisionSHA256 || pin.BasisSHA256 != pin.ObservationSHA256 || pin.Schema != "mantik-owned-paper-decision-v1" && pin.Schema != "mantik-owned-paper-decision-v2" && pin.Schema != paperDecisionSchemaV3 {
				return ErrFeedArchiveConflict
			}
			decisions++
		case "outcome":
			if pin.Schema != "mantik-owned-paper-outcome-v1" && pin.Schema != "mantik-owned-paper-outcome-v2" && pin.Schema != paperOutcomeSchemaV3 {
				return ErrFeedArchiveConflict
			}
			outcomes++
		default:
			return ErrFeedArchiveConflict
		}
	}
	if decisions != c.Decisions || outcomes != c.Outcomes {
		return ErrFeedArchiveConflict
	}
	previous = ""
	for _, entry := range c.Prefixes {
		p := entry.Prefix
		if !decisionRegistration.MatchString(entry.RegistrationID) || entry.RegistrationID <= previous || p.Records < 0 || p.Admissions < 0 || p.Records == 0 && p.RecordsSHA256 != "" || p.Records > 0 && !decisionPin.MatchString(p.RecordsSHA256) || p.Admissions == 0 && p.AdmissionsSHA256 != "" || p.Admissions > 0 && !decisionPin.MatchString(p.AdmissionsSHA256) {
			return ErrFeedArchiveConflict
		}
		previous = entry.RegistrationID
	}
	return nil
}

// Replays actual retained paper sources against the immutable archived feed.
// All callbacks are trusted pure service functions, never request-supplied.
func paperFeedCheckpointData(a FeedArchive, decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, strategies paperstrategy.Catalog, e *paperFeedArchiveEvaluators) (PaperFeedCheckpoint, error) {
	if e == nil || e.decision == nil || e.outcome == nil || a.Validate() != nil || len(decisions) > MaxPaperDecisions || len(outcomes) > MaxPaperOutcomes {
		return PaperFeedCheckpoint{}, ErrFeedArchiveInvalid
	}
	c := PaperFeedCheckpoint{PaperFeedCheckpointHeader: PaperFeedCheckpointHeader{Schema: "mantik-paper-feed-checkpoint-v1", ReceiverID: a.ReceiverID, Archive: a.FeedArchiveHeader, CutAt: a.ArchivedAt, Authority: "service_owned_paper_source_custody_only"}, Records: []PaperFeedSourcePin{}, Prefixes: []PaperFeedPrefix{}}
	for id, v := range decisions {
		r := v.Record
		if id != r.Command.ID || r.Command.ReceiverID != a.ReceiverID || r.Validate() != nil || workerplan.Hash(v.Basis) != r.BasisSHA256 {
			return PaperFeedCheckpoint{}, ErrPaperDecisionInvalid
		}
		if r.FinalizedAt.After(c.CutAt) {
			c.CutAt = r.FinalizedAt
		}
		if v.Basis.Feed.Collectors.LastClock.After(a.Source.Collectors.LastClock) {
			return PaperFeedCheckpoint{}, ErrPaperDecisionConflict
		}
		c.Records = append(c.Records, PaperFeedSourcePin{"decision", id, r.Schema, r.SHA256, r.BasisSHA256, r.BasisSHA256, r.FinalizedAt, id, r.SHA256})
		c.Decisions++
	}
	for id, v := range outcomes {
		r := v.Record
		if id != r.Command.ID || r.Command.ReceiverID != a.ReceiverID || r.Validate() != nil || workerplan.Hash(v.Basis) != r.BasisSHA256 {
			return PaperFeedCheckpoint{}, ErrPaperOutcomeInvalid
		}
		d, ok := decisions[r.Command.DecisionID]
		if !ok || !reflect.DeepEqual(d.Record, v.Basis.Decision) || r.Command.DecisionSHA256 != d.Record.SHA256 {
			return PaperFeedCheckpoint{}, ErrPaperOutcomeConflict
		}
		if r.EvaluatedAt.After(c.CutAt) {
			c.CutAt = r.EvaluatedAt
		}
		if v.Basis.Observation.Feed.Collectors.LastClock.After(a.Source.Collectors.LastClock) {
			return PaperFeedCheckpoint{}, ErrPaperOutcomeConflict
		}
		c.Records = append(c.Records, PaperFeedSourcePin{"outcome", id, r.Schema, r.SHA256, r.BasisSHA256, workerplan.Hash(v.Basis.Observation), r.EvaluatedAt, r.Command.DecisionID, r.Command.DecisionSHA256})
		c.Outcomes++
	}
	if _, err := paperOutcomeCatalog(a.ReceiverID, outcomes, decisions, strategies, a.Source, e.decision, e.outcome); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	slices.SortFunc(c.Records, func(a, b PaperFeedSourcePin) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	index := newFeedPrefixIndex(a.Source)
	for id := range a.Source.Collectors.Entries {
		c.Prefixes = append(c.Prefixes, PaperFeedPrefix{id, index.at(id, c.CutAt)})
	}
	slices.SortFunc(c.Prefixes, func(a, b PaperFeedPrefix) int { return strings.Compare(a.RegistrationID, b.RegistrationID) })
	c.RecordsSHA256, c.PrefixesSHA256 = workerplan.Hash(c.Records), workerplan.Hash(c.Prefixes)
	c.SourceSHA256 = paperFeedSourceIdentity(a.SHA256, c.RecordsSHA256, c.CutAt, c.PaperArchiveSequence)
	return c, nil
}

func finishPaperFeedCheckpoint(c PaperFeedCheckpoint, previous PaperFeedCheckpointHeader) (PaperFeedCheckpoint, error) {
	if previous.Sequence > 0 && (previous.Validate() != nil || previous.ReceiverID != c.ReceiverID || previous.CutAt.After(c.CutAt)) {
		return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
	}
	c.Sequence, c.PreviousSHA256 = previous.Sequence+1, previous.SHA256
	c.SHA256 = workerplan.Hash(c.PaperFeedCheckpointHeader)
	raw, err := json.Marshal(c)
	if c.Validate() != nil || err != nil || len(raw) > MaxPaperFeedCheckpointBytes {
		return PaperFeedCheckpoint{}, ErrFeedArchiveCapacity
	}
	return c, nil
}

func (m *Memory) preparePaperFeedCheckpoint(a FeedArchive, e *paperFeedArchiveEvaluators) (*PaperFeedCheckpoint, bool, error) {
	if e == nil {
		return nil, false, nil
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return nil, false, err
	}
	data, err := paperFeedCheckpointData(a, m.paperDecisions, m.paperOutcomes, strategies, e)
	if err != nil {
		return nil, false, err
	}
	if sequence, ok := m.paperFeedCheckpointSources[data.SourceSHA256]; ok {
		if sequence < 1 || sequence != int64(len(m.paperFeedCheckpoints)) {
			return nil, false, ErrFeedArchiveConflict
		}
		old := m.paperFeedCheckpoints[sequence-1]
		if old.Validate() != nil || old.Archive != a.FeedArchiveHeader || old.RecordsSHA256 != data.RecordsSHA256 || old.PrefixesSHA256 != data.PrefixesSHA256 {
			return nil, false, ErrFeedArchiveConflict
		}
		return &old, true, nil
	}
	var previous PaperFeedCheckpointHeader
	if len(m.paperFeedCheckpoints) > 0 {
		previous = m.paperFeedCheckpoints[len(m.paperFeedCheckpoints)-1].PaperFeedCheckpointHeader
	}
	c, err := finishPaperFeedCheckpoint(data, previous)
	if err != nil {
		return nil, false, err
	}
	return &c, false, nil
}

func (m *Memory) retainPaperFeedCheckpoint(c *PaperFeedCheckpoint, duplicate bool) *PaperFeedCheckpointReceipt {
	if c == nil {
		return nil
	}
	if !duplicate {
		m.paperFeedCheckpoints = append(m.paperFeedCheckpoints, clone(*c))
		if m.paperFeedCheckpointSources == nil {
			m.paperFeedCheckpointSources = map[string]int64{}
		}
		m.paperFeedCheckpointSources[c.SourceSHA256] = c.Sequence
	}
	return &PaperFeedCheckpointReceipt{c.PaperFeedCheckpointHeader, duplicate}
}

func readPaperFeedCheckpoint(ctx context.Context, tx pgx.Tx, sequence int64) (PaperFeedCheckpoint, error) {
	var archiveSequence, seconds int64
	var nanos int
	var sourceSHA, sha string
	var header, raw []byte
	err := tx.QueryRow(ctx, "SELECT archive_sequence,cut_seconds,cut_nanos,source_sha256,sha256,header,payload FROM mantik_paper_feed_checkpoints WHERE sequence=$1", sequence).Scan(&archiveSequence, &seconds, &nanos, &sourceSHA, &sha, &header, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperFeedCheckpoint{}, ErrFeedArchiveNotFound
	}
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	var c PaperFeedCheckpoint
	var h PaperFeedCheckpointHeader
	if workerplan.ReadBoundedShape(raw, &c, MaxPaperFeedCheckpointBytes) != nil || workerplan.ReadBoundedShape(header, &h, 4096) != nil || h != c.PaperFeedCheckpointHeader || c.Validate() != nil || c.Sequence != sequence || c.Archive.Sequence != archiveSequence || c.CutAt.Unix() != seconds || c.CutAt.Nanosecond() != nanos || c.SourceSHA256 != sourceSHA || c.SHA256 != sha {
		return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
	}
	return c, nil
}

func writePaperFeedCheckpoint(ctx context.Context, tx pgx.Tx, a FeedArchive, e *paperFeedArchiveEvaluators) (*PaperFeedCheckpointReceipt, error) {
	if e == nil {
		return nil, nil
	}
	strategies, err := readPaperStrategies(ctx, tx, a.ReceiverID)
	if err != nil {
		return nil, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return nil, err
	}
	outcomes, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return nil, err
	}
	data, err := paperFeedCheckpointData(a, decisions, outcomes, strategies, e)
	if err != nil {
		return nil, err
	}
	var sequence int64
	err = tx.QueryRow(ctx, "SELECT sequence FROM mantik_paper_feed_checkpoints WHERE source_sha256=$1", data.SourceSHA256).Scan(&sequence)
	if err == nil {
		var latest int64
		if err := tx.QueryRow(ctx, "SELECT max(sequence) FROM mantik_paper_feed_checkpoints").Scan(&latest); err != nil {
			return nil, err
		}
		old, err := readPaperFeedCheckpoint(ctx, tx, sequence)
		if err != nil {
			return nil, err
		}
		if sequence != latest || old.Archive != a.FeedArchiveHeader || old.RecordsSHA256 != data.RecordsSHA256 || old.PrefixesSHA256 != data.PrefixesSHA256 {
			return nil, ErrFeedArchiveConflict
		}
		return &PaperFeedCheckpointReceipt{old.PaperFeedCheckpointHeader, true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var rawHeader []byte
	var previous PaperFeedCheckpointHeader
	err = tx.QueryRow(ctx, "SELECT header FROM mantik_paper_feed_checkpoints ORDER BY sequence DESC LIMIT 1").Scan(&rawHeader)
	if err == nil {
		if workerplan.ReadBoundedShape(rawHeader, &previous, 4096) != nil || previous.Validate() != nil {
			return nil, ErrFeedArchiveConflict
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	c, err := finishPaperFeedCheckpoint(data, previous)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(c.PaperFeedCheckpointHeader)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_feed_checkpoints(sequence,archive_sequence,cut_seconds,cut_nanos,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", c.Sequence, a.Sequence, c.CutAt.Unix(), c.CutAt.Nanosecond(), c.SourceSHA256, c.SHA256, header, raw); err != nil {
		return nil, err
	}
	return &PaperFeedCheckpointReceipt{c.PaperFeedCheckpointHeader, false}, nil
}

func verifyPaperFeedCheckpoint(c PaperFeedCheckpoint, a FeedArchive, decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, strategies paperstrategy.Catalog, e *paperFeedArchiveEvaluators) error {
	if c.Validate() != nil || a.Validate() != nil || c.Archive != a.FeedArchiveHeader {
		return ErrFeedArchiveConflict
	}
	selectedDecisions := map[string]retainedPaperDecision{}
	selectedOutcomes := map[string]retainedPaperOutcome{}
	for id, v := range decisions {
		if !v.Record.FinalizedAt.After(c.CutAt) {
			selectedDecisions[id] = v
		}
	}
	for id, v := range outcomes {
		if !v.Record.EvaluatedAt.After(c.CutAt) {
			selectedOutcomes[id] = v
		}
	}
	expected, err := paperFeedCheckpointData(a, selectedDecisions, selectedOutcomes, strategies, e)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected.Records, c.Records) || !reflect.DeepEqual(expected.Prefixes, c.Prefixes) || expected.SourceSHA256 != c.SourceSHA256 || !expected.CutAt.Equal(c.CutAt) {
		return ErrFeedArchiveConflict
	}
	return nil
}

func (m *Memory) PaperFeedCheckpoint(ctx context.Context, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperFeedCheckpoint, error) {
	if sequence < 1 || de == nil || oe == nil {
		return PaperFeedCheckpoint{}, ErrFeedArchiveInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if sequence > int64(len(m.paperFeedCheckpoints)) {
		return PaperFeedCheckpoint{}, ErrFeedArchiveNotFound
	}
	c := m.paperFeedCheckpoints[sequence-1]
	if c.Sequence != sequence || c.ReceiverID != m.receiverID || c.Archive.Sequence < 1 || c.Archive.Sequence > int64(len(m.feedArchives)) {
		return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
	}
	if sequence > 1 && c.PreviousSHA256 != m.paperFeedCheckpoints[sequence-2].SHA256 {
		return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if err := verifyPaperFeedCheckpoint(c, m.feedArchives[c.Archive.Sequence-1], m.paperDecisions, m.paperOutcomes, strategies, &paperFeedArchiveEvaluators{de, oe}); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	return clone(c), nil
}

func (p *Postgres) PaperFeedCheckpoint(ctx context.Context, sequence int64, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperFeedCheckpoint, error) {
	if sequence < 1 || de == nil || oe == nil {
		return PaperFeedCheckpoint{}, ErrFeedArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	c, err := readPaperFeedCheckpoint(ctx, tx, sequence)
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&owner); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if c.ReceiverID != owner {
		return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
	}
	if sequence > 1 {
		var previous string
		if err := tx.QueryRow(ctx, "SELECT sha256 FROM mantik_paper_feed_checkpoints WHERE sequence=$1", sequence-1).Scan(&previous); err != nil || previous != c.PreviousSHA256 {
			return PaperFeedCheckpoint{}, ErrFeedArchiveConflict
		}
	}
	a, err := readFeedArchive(ctx, tx, c.Archive.Sequence)
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	outcomes, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if err := verifyPaperFeedCheckpoint(c, a, decisions, outcomes, strategies, &paperFeedArchiveEvaluators{de, oe}); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperFeedCheckpoint{}, err
	}
	return c, nil
}

var _ PaperFeedArchiveStore = (*Memory)(nil)
var _ PaperFeedArchiveStore = (*Postgres)(nil)

// These clocks close source membership after a checkpoint. They validate only
// the active metadata header; ongoing paper operation does not read archive
// bodies. Exact historical paper retries bypass new-event clock checks.
func paperFeedCheckpointClock(owner string, h PaperFeedCheckpointHeader, at time.Time) error {
	if h.Validate() != nil || h.ReceiverID != owner {
		return ErrFeedArchiveConflict
	}
	if !at.After(h.CutAt) {
		return feedcollector.ErrClockBackwards
	}
	return nil
}
func (m *Memory) paperFeedCheckpointClockLocked(at time.Time) error {
	if len(m.paperFeedCheckpoints) == 0 {
		return nil
	}
	return paperFeedCheckpointClock(m.receiverID, m.paperFeedCheckpoints[len(m.paperFeedCheckpoints)-1].PaperFeedCheckpointHeader, at)
}
func readPaperFeedCheckpointClock(ctx context.Context, tx pgx.Tx, owner string, at time.Time) error {
	var raw []byte
	var seconds int64
	var nanos int
	err := tx.QueryRow(ctx, "SELECT header,cut_seconds,cut_nanos FROM mantik_paper_feed_checkpoints ORDER BY sequence DESC LIMIT 1").Scan(&raw, &seconds, &nanos)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var h PaperFeedCheckpointHeader
	if workerplan.ReadBoundedShape(raw, &h, 4096) != nil || h.CutAt.Unix() != seconds || h.CutAt.Nanosecond() != nanos {
		return ErrFeedArchiveConflict
	}
	return paperFeedCheckpointClock(owner, h, at)
}
