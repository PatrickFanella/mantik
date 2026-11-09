package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
	"reflect"
	"slices"
	"strings"
	"time"
)

const MaxPaperOutcomes = 256
const MaxPaperOutcomeBytes = 4 << 20
const PaperOutcomeRecipe = "native-threshold-forward-quote-v1"

var ErrPaperOutcomeInvalid = errors.New("invalid owned paper outcome")
var ErrPaperOutcomeConflict = errors.New("owned paper outcome identity or source conflict")
var ErrPaperOutcomeCapacity = errors.New("owned paper outcome capacity exhausted")
var ErrPaperOutcomeNotFound = errors.New("owned paper outcome not found")

// Public callers select an actual retained decision. All observation selection,
// clocks, horizon and metrics belong to the service's compiled evaluator.
type PaperOutcomeCommand struct {
	ID             string `json:"id"`
	ReceiverID     string `json:"receiver_id"`
	DecisionID     string `json:"decision_id"`
	DecisionSHA256 string `json:"decision_sha256"`
}

func (c PaperOutcomeCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.DecisionID) || !decisionPin.MatchString(c.DecisionSHA256) {
		return ErrPaperOutcomeInvalid
	}
	return nil
}

type PaperOutcomeBasis struct {
	Decision    PaperDecisionRecord `json:"decision"`
	Observation PaperDecisionBasis  `json:"observation"`
}
type PaperOutcomeEvaluation struct {
	Status             string    `json:"status"`
	ReasonCode         string    `json:"reason_code"`
	DueAt              time.Time `json:"due_at"`
	WindowEnd          time.Time `json:"window_end"`
	EntryPriceCents    int64     `json:"entry_price_cents"`
	ObservedPriceCents int64     `json:"observed_price_cents"`
	PriceDeltaCents    int64     `json:"price_delta_cents"`
	InputID            string    `json:"input_id"`
	CaptureSHA256      string    `json:"capture_sha256"`
	OccurredAt         time.Time `json:"occurred_at"`
	PublishedAt        time.Time `json:"published_at"`
	AvailableAt        time.Time `json:"available_at"`
}
type PaperOutcomeRecord struct {
	Schema              string                 `json:"schema"`
	Command             PaperOutcomeCommand    `json:"command"`
	EvaluatedAt         time.Time              `json:"evaluated_at"`
	Recipe              string                 `json:"recipe"`
	Evaluation          PaperOutcomeEvaluation `json:"evaluation"`
	BasisSHA256         string                 `json:"basis_sha256"`
	Authority           string                 `json:"authority"`
	AdmissionAvailable  bool                   `json:"admission_available"`
	ExecutionAvailable  bool                   `json:"execution_available"`
	QualificationCredit bool                   `json:"qualification_credit"`
	SHA256              string                 `json:"sha256"`
}

func (r PaperOutcomeRecord) Validate() error {
	if r.Command.Validate() != nil || (r.Schema != "mantik-owned-paper-outcome-v1" && r.Schema != "mantik-owned-paper-outcome-v2" && r.Schema != paperOutcomeSchemaV3 && r.Schema != paperOutcomeSchemaV4) || r.Recipe != PaperOutcomeRecipe || r.EvaluatedAt.IsZero() || r.Authority != "service_owned_forward_quote_outcome_only" || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || !decisionPin.MatchString(r.BasisSHA256) {
		return ErrPaperOutcomeInvalid
	}
	switch r.Evaluation.Status {
	case "not_applicable", "waiting_horizon", "waiting_price", "price_unavailable", "observed":
	default:
		return ErrPaperOutcomeInvalid
	}
	want := r.SHA256
	r.SHA256 = ""
	if !decisionPin.MatchString(want) || workerplan.Hash(r) != want {
		return ErrPaperOutcomeInvalid
	}
	return nil
}

type PaperOutcomeReceipt struct {
	Record    PaperOutcomeRecord `json:"record"`
	Duplicate bool               `json:"duplicate"`
}
type PaperOutcomeCatalog struct {
	ReceiverID string               `json:"receiver_id"`
	Records    []PaperOutcomeRecord `json:"records"`
}
type retainedPaperOutcome struct {
	Record PaperOutcomeRecord `json:"record"`
	Basis  PaperOutcomeBasis  `json:"basis"`
}

// Trusted pure service callback only; never supplied by an HTTP caller.
type PaperOutcomeEvaluator func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error)
type PaperOutcomeStore interface {
	AssessPaperOutcome(context.Context, PaperOutcomeCommand, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperOutcomeReceipt, error)
	PaperOutcomes(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperOutcomeCatalog, error)
}

func newPaperOutcome(c PaperOutcomeCommand, b PaperOutcomeBasis, at time.Time, eval PaperOutcomeEvaluator) (PaperOutcomeRecord, error) {
	return paperOutcomeForSchema(paperOutcomeSchemaV3, c, b, at, eval)
}

func paperOutcomeForSchema(schema string, c PaperOutcomeCommand, b PaperOutcomeBasis, at time.Time, eval PaperOutcomeEvaluator) (PaperOutcomeRecord, error) {
	if schema != "mantik-owned-paper-outcome-v1" && schema != "mantik-owned-paper-outcome-v2" && schema != paperOutcomeSchemaV3 && schema != paperOutcomeSchemaV4 {
		return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
	}
	if schema == paperOutcomeSchemaV4 && !canonicalPaperBasisValid(b.Observation) {
		return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
	}
	if c.Validate() != nil || eval == nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 || b.Decision.Validate() != nil || b.Decision.Command.ReceiverID != c.ReceiverID || b.Decision.Command.ID != c.DecisionID || b.Decision.SHA256 != c.DecisionSHA256 || at.Before(b.Decision.FinalizedAt) || at.Before(b.Observation.Feed.Collectors.LastClock) {
		return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
	}
	for _, source := range b.Observation.Feed.Records {
		if source.PublishedAt.IsZero() || source.PublishedAt.After(at) {
			return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
		}
	}
	v, err := eval(clone(b), at.UTC())
	if err != nil {
		return PaperOutcomeRecord{}, err
	}
	r := PaperOutcomeRecord{Schema: schema, Command: c, EvaluatedAt: at.UTC(), Recipe: PaperOutcomeRecipe, Evaluation: v, BasisSHA256: workerplan.Hash(b), Authority: "service_owned_forward_quote_outcome_only"}
	r.SHA256 = workerplan.Hash(r)
	if r.Validate() != nil {
		return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
	}
	return r, nil
}

// Reuse only pure evaluations of an exactly equal basis and clock inside one
// catalogue replay. Each record still receives owner-snapshot custody, command,
// envelope and hash validation. Hash buckets are guarded by full equality;
// neither a digest collision nor an omitted JSON field can alias inputs.
func memoPaperOutcomeEvaluator(eval PaperOutcomeEvaluator) PaperOutcomeEvaluator {
	type evaluated struct {
		basis PaperOutcomeBasis
		at    time.Time
		value PaperOutcomeEvaluation
	}
	cache := map[[32]byte][]evaluated{}
	bytes := 0
	return func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		raw, marshalErr := json.Marshal(struct {
			Basis PaperOutcomeBasis
			At    time.Time
		}{b, at})
		key := sha256.Sum256(raw)
		for _, old := range cache[key] {
			if reflect.DeepEqual(old.basis, b) && reflect.DeepEqual(old.at, at) {
				return old.value, nil
			}
		}
		// Own a detached copy before calling the evaluator, which receives another
		// copy so even a faulty callback cannot mutate a future cache comparison.
		original := clone(b)
		value, err := eval(clone(b), at)
		if err != nil {
			return PaperOutcomeEvaluation{}, err
		}
		if marshalErr == nil && len(raw) <= MaxPaperOutcomeBytes-bytes {
			cache[key] = append(cache[key], evaluated{original, at, value})
			bytes += len(raw)
		}
		return value, nil
	}
}

func paperOutcomeCatalog(owner string, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (PaperOutcomeCatalog, error) {
	out := PaperOutcomeCatalog{ReceiverID: owner, Records: []PaperOutcomeRecord{}}
	if eval == nil || len(all) > MaxPaperOutcomes {
		return out, ErrPaperOutcomeInvalid
	}
	if _, err := paperDecisionCatalog(owner, decisions, strategies, feed, decisionEval); err != nil {
		return out, err
	}
	if feed.Retention != nil {
		closed := 0
		for _, v := range all {
			if !v.Record.EvaluatedAt.After(feed.Retention.Checkpoint.CutAt) {
				closed++
			}
		}
		if closed != feed.Retention.Checkpoint.Outcomes {
			return out, ErrPaperOutcomeConflict
		}
	}
	eval = memoPaperOutcomeEvaluator(eval)
	// The actual owner snapshot is fixed for this catalogue call. Reuse only
	// successful custody checks for a fully equal command, basis, clock and mode.
	index := newFeedPrefixIndex(feed)
	verify := memoPaperOutcomeCustody(func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, membership bool) error {
		if err := verifyPaperDecisionBasis(c, b, at, strategies, feed); err != nil {
			return err
		}
		if membership {
			return paperDecisionSourceMembership(c, b, at, feed)
		}
		return nil
	})
	for id, v := range all {
		r, b := v.Record, v.Basis
		d, ok := decisions[r.Command.DecisionID]
		if id != r.Command.ID || r.Command.ReceiverID != owner || !ok || !reflect.DeepEqual(d.Record, b.Decision) {
			return out, ErrPaperOutcomeConflict
		}
		// Outcome observation has its own availability cut. Legacy and current
		// membership modes are distinct cache inputs, never inferred from a hit.
		if feed.Retention != nil {
			if err := verifyFeedSourcePin(feed, outcomeFeedSourcePin(v)); err != nil {
				return out, err
			}
			if err := verifyPaperDecisionHeaderBasis(d.Record.Command, b.Observation, r.EvaluatedAt, strategies, feed); err != nil {
				return out, err
			}
			if err := verifyRetiredPaperBasis(d.Record.Command, b.Observation, r.EvaluatedAt, feed); err != nil {
				return out, err
			}
		} else if err := verify(d.Record.Command, b.Observation, r.EvaluatedAt, r.Schema == "mantik-owned-paper-outcome-v2"); err != nil {
			return out, err
		}
		if (r.Schema == paperOutcomeSchemaV3 || r.Schema == paperOutcomeSchemaV4) && feed.Retention == nil {
			if err := verifyFeedPrefix(d.Record.Command, b.Observation, r.EvaluatedAt, d.Record.FinalizedAt, index); err != nil {
				return out, err
			}
		}
		expected, err := paperOutcomeForSchema(r.Schema, r.Command, b, r.EvaluatedAt, eval)
		if err != nil || !reflect.DeepEqual(expected, r) {
			return out, ErrPaperOutcomeInvalid
		}
		out.Records = append(out.Records, r)
	}
	slices.SortFunc(out.Records, func(a, b PaperOutcomeRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	return clone(out), nil
}
func appendPaperOutcome(c PaperOutcomeCommand, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, now func() time.Time, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (retainedPaperOutcome, bool, error) {
	return appendPaperOutcomeFromFeed(c, all, decisions, strategies, feed, feed, now, decisionEval, eval)
}
func appendPaperOutcomeFromFeed(c PaperOutcomeCommand, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed, observation FeedEvidenceSnapshot, now func() time.Time, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (retainedPaperOutcome, bool, error) {
	if _, err := paperOutcomeCatalog(feed.ReceiverID, all, decisions, strategies, feed, decisionEval, eval); err != nil {
		return retainedPaperOutcome{}, false, err
	}
	if c.Validate() != nil {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeInvalid
	}
	if c.ReceiverID != feed.ReceiverID {
		return retainedPaperOutcome{}, false, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if old.Record.Command != c {
			return retainedPaperOutcome{}, false, ErrPaperOutcomeConflict
		}
		return clone(old), true, nil
	}
	if len(all) >= MaxPaperOutcomes {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeCapacity
	}
	if now == nil {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeInvalid
	}
	d, ok := decisions[c.DecisionID]
	if !ok {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeNotFound
	}
	if d.Record.SHA256 != c.DecisionSHA256 {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeConflict
	}
	full, err := decisionBasis(d.Record.Command, strategies, observation)
	if err != nil {
		return retainedPaperOutcome{}, false, err
	}
	at := now()
	b := PaperOutcomeBasis{d.Record, withFeedPrefix(d.Record.Command, full, observation, at, d.Record.FinalizedAt)}
	var r PaperOutcomeRecord
	if feed.Retention != nil {
		b.Observation, err = canonicalPaperBasis(b.Observation)
		if err != nil {
			return retainedPaperOutcome{}, false, err
		}
		r, err = paperOutcomeForSchema(paperOutcomeSchemaV4, c, b, at, eval)
	} else {
		r, err = newPaperOutcome(c, b, at, eval)
	}
	if err != nil {
		return retainedPaperOutcome{}, false, err
	}
	v := retainedPaperOutcome{r, b}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > MaxPaperOutcomeBytes {
		return retainedPaperOutcome{}, false, ErrPaperOutcomeCapacity
	}
	return v, false, nil
}
func (m *Memory) AssessPaperOutcome(ctx context.Context, c PaperOutcomeCommand, now func() time.Time, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (PaperOutcomeReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperOutcomeReceipt{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	feed := m.decisionFeedLocked()
	observation, ownedNow, err := outcomeObservationFeed(c, m.paperOutcomes, m.paperDecisions, feed, now, func(reg string, since, at time.Time) (FeedEvidenceSnapshot, error) {
		return m.archivedOutcomeFeedLocked(ctx, feed, reg, since, at)
	})
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	v, dup, err := appendPaperOutcomeFromFeed(c, m.paperOutcomes, m.paperDecisions, s, feed, observation, ownedNow, decisionEval, eval)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperOutcomeReceipt{}, err
	}
	if !dup {
		if err := m.paperFeedCheckpointClockLocked(v.Record.EvaluatedAt); err != nil {
			return PaperOutcomeReceipt{}, err
		}
		if err := paperEvaluationSourceClock(m.receiverID, m.evaluationAssessments, v.Record.EvaluatedAt); err != nil {
			return PaperOutcomeReceipt{}, err
		}
		m.retainPaperFeedSourceSeal(outcomeFeedSourcePin(v))
		m.paperOutcomes[c.ID] = clone(v)
	}
	return PaperOutcomeReceipt{clone(v.Record), dup}, nil
}
func (m *Memory) PaperOutcomes(ctx context.Context, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (PaperOutcomeCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperOutcomeCatalog{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	return paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, s, m.decisionFeedLocked(), decisionEval, eval)
}
func readPaperOutcomes(ctx context.Context, tx pgx.Tx) (map[string]retainedPaperOutcome, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_outcomes ORDER BY id LIMIT $1", MaxPaperOutcomes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]retainedPaperOutcome{}
	for rows.Next() {
		var id string
		var raw []byte
		var v retainedPaperOutcome
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if workerplan.ReadBoundedShape(raw, &v, MaxPaperOutcomeBytes) != nil || id != v.Record.Command.ID {
			return nil, ErrPaperOutcomeInvalid
		}
		all[id] = v
	}
	if len(all) > MaxPaperOutcomes {
		return nil, ErrPaperOutcomeCapacity
	}
	return all, rows.Err()
}
func (p *Postgres) AssessPaperOutcome(ctx context.Context, c PaperOutcomeCommand, now func() time.Time, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (PaperOutcomeReceipt, error) {
	if c.Validate() != nil {
		return PaperOutcomeReceipt{}, ErrPaperOutcomeInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Acquire the global row-order subset collector -> state -> receiver.
	// Feed publication/submission locks collector -> state, while capital policy
	// and fixture binding lock state -> receiver. Receiver-first here can cycle
	// with an owner holding state while waiting for receiver.
	feed, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
		return PaperOutcomeReceipt{}, err
	}
	if owner != c.ReceiverID || owner != feed.ReceiverID {
		return PaperOutcomeReceipt{}, ErrReceiverMismatch
	}
	s, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	observation, ownedNow, err := outcomeObservationFeed(c, all, decisions, feed, now, func(reg string, since, at time.Time) (FeedEvidenceSnapshot, error) {
		return readArchivedOutcomeFeed(ctx, tx, feed, reg, since, at)
	})
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	v, dup, err := appendPaperOutcomeFromFeed(c, all, decisions, s, feed, observation, ownedNow, decisionEval, eval)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	if !dup {
		if err := readPaperFeedCheckpointClock(ctx, tx, owner, v.Record.EvaluatedAt); err != nil {
			return PaperOutcomeReceipt{}, err
		}
		assessments, e := readPaperEvaluationAssessments(ctx, tx)
		if e != nil {
			return PaperOutcomeReceipt{}, e
		}
		if e = paperEvaluationSourceClock(owner, assessments, v.Record.EvaluatedAt); e != nil {
			return PaperOutcomeReceipt{}, e
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return PaperOutcomeReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_outcomes(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
			return PaperOutcomeReceipt{}, err
		}
		if err := writePaperFeedSourceSeal(ctx, tx, feed.Retention, outcomeFeedSourcePin(v)); err != nil {
			return PaperOutcomeReceipt{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperOutcomeReceipt{}, err
	}
	return PaperOutcomeReceipt{v.Record, dup}, nil
}
func (p *Postgres) PaperOutcomes(ctx context.Context, decisionEval PaperDecisionEvaluator, eval PaperOutcomeEvaluator) (PaperOutcomeCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	s, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	decisions, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	out, err := paperOutcomeCatalog(feed.ReceiverID, all, decisions, s, feed, decisionEval, eval)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperOutcomeCatalog{}, err
	}
	return out, nil
}
