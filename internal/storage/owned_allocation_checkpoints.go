package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// OwnedAllocationCheckpointSchema seals a prefix of the allocation journal.
// Routine replay starts from the checkpoint and trusts it; the sealed records
// move to an archive, are listed by readers, and are re-executed only by the
// full-history audit.
const OwnedAllocationCheckpointSchema = "mantik-owned-allocation-checkpoint-v1"

// ownedCheckpointRecords is the active record count at which an append first
// seals the journal. Tests lower it.
var ownedCheckpointRecords = 64

// OwnedProposalSource is one entry of the sealed proposal alias index: an
// economic source key and the proposal IDs that selected it.
type OwnedProposalSource struct {
	Key string   `json:"key"`
	IDs []string `json:"ids"`
}

// OwnedAllocationCheckpoint is the replay state after Sequence records.
// SealedSHA256 chains every sealed record in order, so archived history can
// be checked against it without re-executing it.
type OwnedAllocationCheckpoint struct {
	Schema       string                `json:"schema"`
	Sequence     int                   `json:"sequence"`
	SealedSHA256 string                `json:"sealed_sha256"`
	At           time.Time             `json:"at"`
	Ledgers      []allocation.Ledger   `json:"ledgers"`
	Granted      []string              `json:"granted"`
	Executed     []string              `json:"executed"`
	Sources      []OwnedProposalSource `json:"sources"`
	// BookProposals names every proposal an entry book owned, so the interim
	// expiry rule keeps skipping them after their books are sealed.
	BookProposals []string `json:"book_proposals"`
	// Seals lists the sequence of every checkpoint so far, this one last.
	// Records appended after a checkpoint bind the sealed ledger form, so the
	// audit re-executes history one sealed segment at a time.
	Seals  []int  `json:"seals"`
	SHA256 string `json:"sha256"`
}

func sealedChain(previous string, sequence int, item retainedOwnedAllocation) string {
	return workerplan.Hash(struct {
		Previous string
		Sequence int
		Item     retainedOwnedAllocation
	}{previous, sequence, item})
}

func sortedUnique(values []string) bool {
	for i := range values {
		if values[i] == "" || i > 0 && values[i-1] >= values[i] {
			return false
		}
	}
	return true
}

// openOwnedCheckpoint checks the journal's checkpoint against current owner
// policies and seeds replay with its sealed ledgers and alias index. When the
// sealed history is loaded, it must match the checkpoint chain; its records
// are then listed and their identities stay unique.
func openOwnedCheckpoint(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, out *OwnedAllocationCatalog, seen map[string]bool, aliases *proposalAliases) ([]allocation.Ledger, error) {
	c := journal.Checkpoint
	if c == nil {
		if len(journal.archived) > 0 {
			return nil, ErrOwnedAllocationInvalid
		}
		return nil, nil
	}
	unsigned := *c
	unsigned.SHA256 = ""
	for i, n := range c.Seals {
		if n < 1 || i > 0 && c.Seals[i-1] >= n {
			return nil, ErrOwnedAllocationInvalid
		}
	}
	if !sortedUnique(c.BookProposals) || len(c.Seals) == 0 || c.Seals[len(c.Seals)-1] != c.Sequence || c.Schema != OwnedAllocationCheckpointSchema || c.Sequence < 1 || c.At.IsZero() || !decisionPin.MatchString(c.SealedSHA256) || workerplan.Hash(unsigned) != c.SHA256 || c.Ledgers == nil || c.Sources == nil || !sortedUnique(c.Granted) || !sortedUnique(c.Executed) {
		return nil, ErrOwnedAllocationInvalid
	}
	ids := map[string]bool{}
	for _, l := range c.Ledgers {
		id := l.Policy.Command.Policy.ID
		if l.Base == nil || len(l.Entries) != 0 || ids[id] || !slices.ContainsFunc(policies.Records, func(p allocation.PolicyRecord) bool { return reflect.DeepEqual(p, l.Policy) }) {
			return nil, ErrOwnedAllocationInvalid
		}
		if _, err := allocation.ReplayLedger(ctx, l); err != nil {
			return nil, ErrOwnedAllocationInvalid
		}
		ids[id] = true
	}
	for i, s := range c.Sources {
		if s.Key == "" || i > 0 && c.Sources[i-1].Key >= s.Key || len(s.IDs) == 0 || !sortedUnique(s.IDs) {
			return nil, ErrOwnedAllocationInvalid
		}
		aliases.previous[s.Key] = map[string]bool{}
		for _, id := range s.IDs {
			aliases.previous[s.Key][id] = true
		}
	}
	if journal.archived != nil {
		if len(journal.archived) != c.Sequence {
			return nil, ErrOwnedAllocationInvalid
		}
		chain := ""
		for i, item := range journal.archived {
			chain = sealedChain(chain, i+1, item)
			id := item.Record.Command.ID
			if item.Execution != nil {
				id = item.Execution.Command.ID
			}
			if seen[id] {
				return nil, ErrOwnedAllocationInvalid
			}
			seen[id] = true
			if item.Execution != nil {
				out.Executions = append(out.Executions, clone(*item.Execution))
			} else {
				out.Records = append(out.Records, clone(item.Record))
			}
		}
		if chain != c.SealedSHA256 {
			return nil, ErrOwnedAllocationInvalid
		}
	}
	return clone(c.Ledgers), nil
}

// sealOwnedJournal seals every active record into a new checkpoint once the
// journal holds ownedCheckpointRecords and no entry book can still change.
// Otherwise, including when a legacy proposal source cannot be resolved, it
// reports false and the journal stays as it is. The caller must replay the
// sealed journal before appending to it.
func sealOwnedJournal(ctx context.Context, journal ownedAllocationJournal, catalog OwnedAllocationCatalog, state ownedJournalState) (ownedAllocationJournal, bool, error) {
	if len(journal.Records) < ownedCheckpointRecords || state.aliases == nil {
		return journal, false, nil
	}
	for _, b := range state.books {
		if !b.done {
			return journal, false, nil
		}
	}
	if state.aliases.check(OwnedAllocationCommand{}) != nil {
		return journal, false, nil
	}
	sealing := clone(journal.Records)
	c, err := buildOwnedCheckpoint(ctx, journal.Checkpoint, sealing, catalog, state)
	if err != nil {
		return journal, false, err
	}
	return ownedAllocationJournal{Checkpoint: &c, Records: []retainedOwnedAllocation{}, lookup: journal.lookup, sealing: sealing}, true, nil
}

// buildOwnedCheckpoint seals records on top of previous from the replayed
// catalog and state after them. Sealing in one step or in several gives the
// same checkpoint, which is what the audit relies on.
func buildOwnedCheckpoint(ctx context.Context, previous *OwnedAllocationCheckpoint, records []retainedOwnedAllocation, catalog OwnedAllocationCatalog, state ownedJournalState) (OwnedAllocationCheckpoint, error) {
	c := OwnedAllocationCheckpoint{Schema: OwnedAllocationCheckpointSchema, At: state.last, Ledgers: []allocation.Ledger{}, Granted: slices.Clone(state.granted), Executed: []string{}, Sources: []OwnedProposalSource{}, BookProposals: []string{}}
	if c.Granted == nil {
		c.Granted = []string{}
	}
	for id := range state.bookProposals() {
		c.BookProposals = append(c.BookProposals, id)
	}
	slices.Sort(c.BookProposals)
	for id := range state.executed {
		c.Executed = append(c.Executed, id)
	}
	slices.Sort(c.Executed)
	if state.aliases != nil {
		if err := state.aliases.check(OwnedAllocationCommand{}); err != nil {
			return c, err
		}
		for key, ids := range state.aliases.previous {
			s := OwnedProposalSource{Key: key}
			for id := range ids {
				s.IDs = append(s.IDs, id)
			}
			slices.Sort(s.IDs)
			c.Sources = append(c.Sources, s)
		}
	}
	slices.SortFunc(c.Sources, func(a, b OwnedProposalSource) int { return strings.Compare(a.Key, b.Key) })
	if previous != nil {
		c.Sequence, c.SealedSHA256 = previous.Sequence, previous.SealedSHA256
	}
	for _, item := range records {
		c.Sequence++
		c.SealedSHA256 = sealedChain(c.SealedSHA256, c.Sequence, item)
	}
	if previous != nil {
		c.Seals = slices.Clone(previous.Seals)
	}
	c.Seals = append(c.Seals, c.Sequence)
	for _, l := range catalog.Ledgers {
		sealed, err := allocation.SealSettledLedger(ctx, l)
		if err != nil {
			return c, err
		}
		c.Ledgers = append(c.Ledgers, sealed)
	}
	c.SHA256 = workerplan.Hash(c)
	return c, nil
}

// archivedRetry answers an exact retry of a sealed record. It returns nil
// when no sealed record has the identity.
func archivedRetry(journal ownedAllocationJournal, id string) (*retainedOwnedAllocation, error) {
	if journal.Checkpoint == nil || journal.lookup == nil {
		return nil, nil
	}
	return journal.lookup(id)
}

// ownedAllocationsForAppendLocked is the active journal with an archive
// lookup for exact retries of sealed records.
func (m *Memory) ownedAllocationsForAppendLocked() ownedAllocationJournal {
	journal := m.ownedAllocations
	archive := m.ownedAllocationArchive
	journal.lookup = func(id string) (*retainedOwnedAllocation, error) {
		for i := range archive {
			item := archive[i]
			if item.Record.Command.ID == id || item.Execution != nil && item.Execution.Command.ID == id {
				found := clone(item)
				return &found, nil
			}
		}
		return nil, nil
	}
	return journal
}

// ownedAllocationHistoryLocked is the journal with its sealed history loaded.
func (m *Memory) ownedAllocationHistoryLocked() ownedAllocationJournal {
	journal := m.ownedAllocations
	if journal.Checkpoint != nil {
		journal.archived = slices.Clone(m.ownedAllocationArchive)
	}
	return journal
}

// commitOwnedAllocationsLocked archives what an append sealed and keeps the
// new active journal.
func (m *Memory) commitOwnedAllocationsLocked(next ownedAllocationJournal) {
	m.ownedAllocationArchive = append(m.ownedAllocationArchive, clone(next.sealing)...)
	m.ownedAllocations = clone(next)
}

// readOwnedAllocationArchive loads the sealed history in sequence order.
func readOwnedAllocationArchive(ctx context.Context, tx pgx.Tx) ([]retainedOwnedAllocation, error) {
	rows, err := tx.Query(ctx, "SELECT sequence, payload FROM mantik_owned_allocation_archive ORDER BY sequence")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []retainedOwnedAllocation{}
	for rows.Next() {
		var sequence int
		var raw []byte
		if err := rows.Scan(&sequence, &raw); err != nil {
			return nil, err
		}
		var item retainedOwnedAllocation
		if sequence != len(out)+1 || workerplan.ReadBoundedShape(raw, &item, MaxOwnedAllocationBytes) != nil {
			return nil, ErrOwnedAllocationInvalid
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// readOwnedAllocationHistory is the journal with its sealed history loaded.
func readOwnedAllocationHistory(ctx context.Context, tx pgx.Tx) (ownedAllocationJournal, error) {
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil || journal.Checkpoint == nil {
		return journal, err
	}
	journal.archived, err = readOwnedAllocationArchive(ctx, tx)
	return journal, err
}

// postgresArchiveLookup finds one sealed record by command identity.
func postgresArchiveLookup(ctx context.Context, tx pgx.Tx) func(string) (*retainedOwnedAllocation, error) {
	return func(id string) (*retainedOwnedAllocation, error) {
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocation_archive WHERE command_id=$1", id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var item retainedOwnedAllocation
		if workerplan.ReadBoundedShape(raw, &item, MaxOwnedAllocationBytes) != nil {
			return nil, ErrOwnedAllocationInvalid
		}
		return &item, nil
	}
}

// writeOwnedAllocationArchive appends the records an append sealed.
func writeOwnedAllocationArchive(ctx context.Context, tx pgx.Tx, journal ownedAllocationJournal) error {
	if len(journal.sealing) == 0 {
		return nil
	}
	if journal.Checkpoint == nil {
		return ErrOwnedAllocationInvalid
	}
	first := journal.Checkpoint.Sequence - len(journal.sealing) + 1
	for i, item := range journal.sealing {
		id := item.Record.Command.ID
		if item.Execution != nil {
			id = item.Execution.Command.ID
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_owned_allocation_archive(sequence,command_id,payload) VALUES($1,$2,$3)", first+i, id, raw); err != nil {
			return err
		}
	}
	return nil
}

// OwnedAllocationAudit reports a full-history re-execution of the journal.
type OwnedAllocationAudit struct {
	Status           string `json:"status"`
	Records          int    `json:"records"`
	Sealed           int    `json:"sealed"`
	CheckpointSHA256 string `json:"checkpoint_sha256,omitempty"`
}
type OwnedAllocationAuditStore interface {
	AuditOwnedAllocations(context.Context, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationAudit, error)
}

var ErrOwnedAllocationAudit = errors.New("owned allocation checkpoint does not match re-executed history")

// auditOwnedAllocations re-executes every record from genesis, one sealed
// segment at a time, rebuilding each checkpoint from its own replay. The last
// rebuilt checkpoint must equal the retained one, and the whole history must
// list the same records and steps as the checkpointed replay.
func auditOwnedAllocations(ctx context.Context, owner string, history ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, declarations PaperEvaluationDeclarationCatalog, assessments ...PaperEvaluationAssessmentCatalog) (OwnedAllocationAudit, error) {
	out := OwnedAllocationAudit{Status: "verified", Records: len(history.Records)}
	checkpointed, err := replayOwnedAllocations(ctx, owner, history, policies, decisions, strategies, retained, x, declarations, assessments...)
	if err != nil || history.Checkpoint == nil {
		return out, err
	}
	sealed := clone(history.archived)
	var previous *OwnedAllocationCheckpoint
	records, executions := []OwnedAllocationRecord{}, []OwnedExecutionRecord{}
	start := 0
	for _, end := range history.Checkpoint.Seals {
		if end > len(sealed) || end <= start {
			return out, ErrOwnedAllocationAudit
		}
		segment := ownedAllocationJournal{Checkpoint: previous, Records: sealed[start:end], unbounded: true}
		catalog, state, err := replayOwnedJournal(ctx, owner, segment, policies, decisions, strategies, retained, x, declarations, assessments...)
		if err != nil {
			return out, err
		}
		records, executions = append(records, catalog.Records...), append(executions, catalog.Executions...)
		c, err := buildOwnedCheckpoint(ctx, previous, sealed[start:end], catalog, state)
		if err != nil {
			return out, err
		}
		previous, start = &c, end
	}
	if start != len(sealed) || !reflect.DeepEqual(*previous, *history.Checkpoint) {
		return out, ErrOwnedAllocationAudit
	}
	active, err := replayOwnedAllocations(ctx, owner, ownedAllocationJournal{Checkpoint: previous, Records: history.Records}, policies, decisions, strategies, retained, x, declarations, assessments...)
	if err != nil {
		return out, err
	}
	records, executions = append(records, active.Records...), append(executions, active.Executions...)
	if len(executions) == 0 {
		executions = nil
	}
	if !reflect.DeepEqual(records, checkpointed.Records) || !reflect.DeepEqual(executions, checkpointed.Executions) || !reflect.DeepEqual(active.Ledgers, checkpointed.Ledgers) {
		return out, ErrOwnedAllocationAudit
	}
	out.Records, out.Sealed, out.CheckpointSHA256 = len(sealed)+len(history.Records), history.Checkpoint.Sequence, history.Checkpoint.SHA256
	return out, nil
}

func (m *Memory) AuditOwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationAudit{}, err
	}
	p, d, s, err := m.ownedAllocationSourcesLocked(eval)
	if err != nil {
		return OwnedAllocationAudit{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, s, p)
	if err != nil {
		return OwnedAllocationAudit{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedAllocationAudit{}, err
	}
	assessments, err := resolveAllocationAssessmentOwner(ctx, m.receiverID, m.evaluationAssessments, declarations, d, s, m.paperDecisions, m.paperOutcomes, m.decisionFeedLocked(), m.corporateActionSources, m.corporateReferenceSources, eval, oe, p, m.portfolioEvaluator)
	if err != nil {
		return OwnedAllocationAudit{}, err
	}
	return auditOwnedAllocations(ctx, m.receiverID, m.ownedAllocationHistoryLocked(), p, d, s, m.paperDecisions, m.ownedExecutionLocked(), declarations, assessments)
}

func (p *Postgres) AuditOwnedAllocations(ctx context.Context, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationAudit, error) {
	var out OwnedAllocationAudit
	_, _, err := p.ownedAllocationAccess(ctx, false, func(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, _ time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		var err error
		out, err = auditOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained, x, declarations, assessments)
		return journal, false, err
	}, eval, outcomeEvals...)
	return out, err
}
