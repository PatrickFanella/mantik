package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// OwnedExecutionSchema is comparison v16: v15 plus owner entry execution.
// Each record first advances every open entry book to its own clock.
const OwnedExecutionSchema = "mantik-owned-allocation-deferral-v16"

// OwnedExecutionStepSchema advances open entry books without a comparison.
const OwnedExecutionStepSchema = "mantik-owned-execution-step-v1"
const OwnedEntryExecutionMethod = "owner-selected-quote-ongoing-entry-v1"
const ownedExecutionAuthority = "service_owned_paper_entry_execution_only"

func isOwnedGrantSchema(s string) bool {
	return s == OwnedAllocationGrantSchema || s == OwnedExecutionSchema
}

// PaperExecutionQuoteQuery selects one registration's retained quotes that
// became available in (After, Through] for one strategy quote scope.
type PaperExecutionQuoteQuery struct {
	RegistrationID     string    `json:"registration_id"`
	Symbol             string    `json:"symbol"`
	Feed               string    `json:"feed"`
	EvidenceClass      string    `json:"evidence_class"`
	MaxQuoteAgeSeconds int64     `json:"max_quote_age_seconds"`
	After              time.Time `json:"after"`
	Through            time.Time `json:"through"`
}

// PaperExecutionQuoteSource is service code. It runs without I/O under the
// transaction locks and returns book commands in availability order.
type PaperExecutionQuoteSource func(context.Context, FeedEvidenceSnapshot, PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error)

// OwnedExecutionAdvance holds the ledger effects one record committed to one
// policy ledger from its open entry books, in clock order.
type OwnedExecutionAdvance struct {
	PolicyID string                   `json:"policy_id"`
	Entries  []allocation.LedgerEntry `json:"entries"`
}

type OwnedExecutionCommand struct {
	ID         string `json:"id"`
	ReceiverID string `json:"receiver_id"`
}

// OwnedExecutionRecord is an execution step: effects only, no comparison.
// A step that would commit no effect is not retained.
type OwnedExecutionRecord struct {
	Schema              string                  `json:"schema"`
	Command             OwnedExecutionCommand   `json:"command"`
	At                  time.Time               `json:"at"`
	Executions          []OwnedExecutionAdvance `json:"executions"`
	Authority           string                  `json:"authority"`
	QualificationCredit bool                    `json:"qualification_credit"`
	SHA256              string                  `json:"sha256"`
}
type OwnedExecutionReceipt struct {
	Record    *OwnedExecutionRecord `json:"record"`
	Duplicate bool                  `json:"duplicate"`
	Status    string                `json:"status"`
}
type OwnedExecutionStore interface {
	ExecuteOwnedAllocations(context.Context, OwnedExecutionCommand, func() time.Time, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedExecutionReceipt, error)
}

var ErrOwnedExecutionUnavailable = errors.New("owned paper execution is not configured")

// EnablePaperExecution makes new comparisons v16 and allows execution steps.
func (m *Memory) EnablePaperExecution(source PaperExecutionQuoteSource) error {
	if source == nil {
		return ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executionQuotes = source
	return nil
}
func (p *Postgres) EnablePaperExecution(source PaperExecutionQuoteSource) error {
	if source == nil {
		return ErrOwnedAllocationInvalid
	}
	p.executionQuotes = source
	return nil
}

// ownedExecutionLocked is nil when execution is not configured.
func (m *Memory) ownedExecutionLocked() *ownedExecution {
	if m.executionQuotes == nil {
		return nil
	}
	return &ownedExecution{feed: m.decisionFeedLocked(), quotes: m.executionQuotes}
}
func (p *Postgres) ownedExecution(feed FeedEvidenceSnapshot) *ownedExecution {
	if p.executionQuotes == nil {
		return nil
	}
	return &ownedExecution{feed: feed, quotes: p.executionQuotes}
}

// ownedExecution carries what replay and append need to derive entry books.
type ownedExecution struct {
	feed   FeedEvidenceSnapshot
	quotes PaperExecutionQuoteSource
}

type ownedEntryBook struct {
	policyID string
	query    PaperExecutionQuoteQuery
	expires  []time.Time
	root     paperfill.Book
}

// ownedEntryBooks derives a v16 comparison's entry books: one buy limit per
// reserved proposal at its bound cost ceiling, placed at the reserve clock and
// expiring at the cost's validity, grouped by registration and quote scope so
// a book consumes one feed. Orders reserved by different comparisons never
// share a book, so liquidity is shared only among orders reserved together.
func ownedEntryBooks(r OwnedAllocationRecord, policy allocation.PolicyRecord, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision) ([]ownedEntryBook, error) {
	if r.Schema != OwnedExecutionSchema || r.Entry.Plan == nil {
		return nil, nil
	}
	model := policy.Command.ExecutionModel
	if model == nil {
		return nil, ErrOwnedAllocationInvalid
	}
	type group struct {
		query  PaperExecutionQuoteQuery
		orders []paperfill.Order
	}
	groups := map[string]*group{}
	keys := []string{}
	for _, choice := range r.Entry.Plan.Decisions {
		if choice.Status != "reserve_plan" {
			continue
		}
		p := choice.Proposal
		i := slices.IndexFunc(r.ExecutionCosts, func(c OwnedExecutionCost) bool { return c.DecisionID == p.ID })
		decision, ok := retained[p.ID]
		if i < 0 || !ok || decision.Record.SHA256 != p.DecisionSHA256 {
			return nil, ErrOwnedAllocationInvalid
		}
		cost := r.ExecutionCosts[i]
		strategy, err := paperstrategy.Select(strategies, decision.Record.Command.StrategyID)
		if err != nil {
			return nil, ErrOwnedAllocationInvalid
		}
		d := strategy.Command.Definition
		q := PaperExecutionQuoteQuery{RegistrationID: decision.Record.Command.RegistrationID, Symbol: d.Symbol, Feed: d.Feed, EvidenceClass: d.InputEvidenceClass, MaxQuoteAgeSeconds: d.Safety.MaxQuoteAgeSeconds}
		key := workerplan.Hash(q)
		if groups[key] == nil {
			groups[key] = &group{query: q}
			keys = append(keys, key)
		}
		groups[key].orders = append(groups[key].orders, paperfill.Order{ID: p.ID, Symbol: p.Symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: cost.Quantity, LimitCents: cost.LimitCents, MaxDebitCents: cost.MaxDebitCents, PlacedAt: r.At, ExpiresAt: cost.ValidUntil, Mode: "ongoing_paper"})
	}
	slices.Sort(keys)
	out := []ownedEntryBook{}
	for _, key := range keys {
		g := groups[key]
		slices.SortFunc(g.orders, func(a, b paperfill.Order) int { return strings.Compare(a.ID, b.ID) })
		root, err := paperfill.NewOccurrenceBook(*model, g.orders)
		if err != nil {
			return nil, ErrOwnedAllocationInvalid
		}
		b := ownedEntryBook{policyID: policy.Command.Policy.ID, query: g.query, root: root}
		for _, o := range root.Orders {
			if !slices.ContainsFunc(b.expires, o.ExpiresAt.Equal) {
				b.expires = append(b.expires, o.ExpiresAt)
			}
		}
		slices.SortFunc(b.expires, time.Time.Compare)
		out = append(out, b)
	}
	return out, nil
}

// settled reports whether every command this book can ever receive is at or
// before cut, so advancing it further commits nothing new.
func (b ownedEntryBook) settled(cut time.Time) bool {
	return !b.expires[len(b.expires)-1].After(cut)
}

// at advances the book to cut: retained quotes available after placement and
// through cut, interleaved with expiry timers, which precede quotes at the
// same clock. Nothing is applied after the last order expires.
func (b ownedEntryBook) at(ctx context.Context, x ownedExecution, cut time.Time) (paperfill.Book, error) {
	placed := b.root.Orders[0].PlacedAt
	through := cut
	if last := b.expires[len(b.expires)-1]; last.Before(through) {
		through = last
	}
	if !through.After(placed) {
		return b.root, nil
	}
	query := b.query
	query.After, query.Through = placed, through
	quotes, err := x.quotes(ctx, x.feed, query)
	if err != nil {
		return paperfill.Book{}, err
	}
	commands := []paperfill.BookCommand{}
	timers := slices.DeleteFunc(slices.Clone(b.expires), func(t time.Time) bool { return t.After(through) })
	for _, q := range quotes {
		if !q.At.After(placed) || q.At.After(through) {
			return paperfill.Book{}, ErrOwnedAllocationConflict
		}
		for len(timers) > 0 && !timers[0].After(q.At) {
			commands = append(commands, paperfill.BookCommand{ID: "entry-expiry-" + workerplan.Hash(timers[0]), At: timers[0]})
			timers = timers[1:]
		}
		commands = append(commands, q)
	}
	for _, t := range timers {
		commands = append(commands, paperfill.BookCommand{ID: "entry-expiry-" + workerplan.Hash(t), At: t})
	}
	appender, err := paperfill.NewBookAppender(ctx, b.root)
	if err != nil {
		return paperfill.Book{}, err
	}
	for _, c := range commands {
		if _, _, err := appender.Apply(ctx, c); err != nil {
			if errors.Is(err, paperfill.ErrCapacity) {
				return paperfill.Book{}, ErrOwnedAllocationCapacity
			}
			return paperfill.Book{}, err
		}
	}
	return appender.Book(), nil
}

// advancePolicy stages every fill and unfilled-release effect the open books
// produce by cut that ledger does not hold yet, merged in clock order, and
// returns the advanced ledger with them appended.
func advancePolicy(ctx context.Context, ledger allocation.Ledger, books []ownedEntryBook, x ownedExecution, cut time.Time) (allocation.Ledger, []allocation.LedgerEntry, error) {
	type staged struct {
		command allocation.LedgerCommand
		at      time.Time
	}
	stages := []staged{}
	for _, b := range books {
		book, err := b.at(ctx, x, cut)
		if err != nil {
			return ledger, nil, err
		}
		filled, _, _, err := allocation.ApplyEntryFillBook(ctx, ledger, book)
		if err != nil {
			return ledger, nil, ownedExecutionConflict(err)
		}
		released, _, _, err := allocation.ApplyUnfilledEntryReleases(ctx, filled, book)
		if err != nil {
			return ledger, nil, ownedExecutionConflict(err)
		}
		for _, e := range released.Entries[len(ledger.Entries):] {
			stages = append(stages, staged{e.Command, e.At})
		}
	}
	slices.SortStableFunc(stages, func(a, b staged) int { return a.at.Compare(b.at) })
	added := []allocation.LedgerEntry{}
	for _, s := range stages {
		next, _, receipt, err := allocation.AppendLedger(ctx, ledger, s.command, func() time.Time { return s.at })
		if err != nil {
			return ledger, nil, ownedExecutionConflict(err)
		}
		ledger = next
		added = append(added, ledger.Entries[receipt.Sequence-1])
	}
	return ledger, added, nil
}

func ownedExecutionConflict(err error) error {
	if errors.Is(err, allocation.ErrLedgerConflict) {
		return ErrOwnedAllocationConflict
	}
	return err
}

// executedProposals lists proposals that entry books own. The interim expiry
// rule must not release them: books release unfilled orders, and a filled
// order keeps its protective hold.
func executedProposals(books []ownedEntryBook) map[string]bool {
	out := map[string]bool{}
	for _, b := range books {
		for _, o := range b.root.Orders {
			out[o.ID] = true
		}
	}
	return out
}

// advanceAll advances every policy with an unsettled book to cut, in policy
// ID order, starting from each policy's current ledger. previous is the last
// record clock; a book settled by then cannot change.
func advanceAll(ctx context.Context, ledgers map[string]allocation.Ledger, books []ownedEntryBook, x ownedExecution, previous, cut time.Time) ([]OwnedExecutionAdvance, error) {
	byPolicy := map[string][]ownedEntryBook{}
	for _, b := range books {
		if !b.settled(previous) {
			byPolicy[b.policyID] = append(byPolicy[b.policyID], b)
		}
	}
	ids := []string{}
	for id := range byPolicy {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := []OwnedExecutionAdvance{}
	for _, id := range ids {
		ledger, ok := ledgers[id]
		if !ok {
			return nil, ErrOwnedAllocationInvalid
		}
		next, added, err := advancePolicy(ctx, ledger, byPolicy[id], x, cut)
		if err != nil {
			return nil, err
		}
		if len(added) > 0 {
			ledgers[id] = next
			out = append(out, OwnedExecutionAdvance{PolicyID: id, Entries: added})
		}
	}
	return out, nil
}

// appendOwnedExecution advances every open entry book to now. A step retains
// a record only when it commits an effect; otherwise the journal is unchanged
// and the command identity stays unused. Exact retries return the record.
func appendOwnedExecution(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, c OwnedExecutionCommand, now func() time.Time, sourceClock time.Time, declarations PaperEvaluationDeclarationCatalog, assessments ...PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, OwnedExecutionReceipt, error) {
	if x == nil {
		return journal, OwnedExecutionReceipt{}, ErrOwnedExecutionUnavailable
	}
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) {
		return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationInvalid
	}
	if c.ReceiverID != owner {
		return journal, OwnedExecutionReceipt{}, ErrReceiverMismatch
	}
	catalog, state, err := replayOwnedJournal(ctx, owner, journal, policies, decisions, strategies, retained, x, declarations, assessments...)
	if err != nil {
		return journal, OwnedExecutionReceipt{}, err
	}
	for _, e := range catalog.Executions {
		if e.Command.ID == c.ID {
			if e.Command != c {
				return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationConflict
			}
			record := clone(e)
			return journal, OwnedExecutionReceipt{Record: &record, Duplicate: true, Status: "effects_committed"}, nil
		}
	}
	for _, r := range catalog.Records {
		if r.Command.ID == c.ID {
			return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationConflict
		}
	}
	if now == nil {
		return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationInvalid
	}
	at := now().UTC()
	if at.Before(sourceClock) || at.Before(state.last) {
		return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationInvalid
	}
	ledgers := map[string]allocation.Ledger{}
	for _, l := range catalog.Ledgers {
		ledgers[l.Policy.Command.Policy.ID] = l
	}
	advances, err := advanceAll(ctx, ledgers, state.books, *x, state.last, at)
	if err != nil {
		return journal, OwnedExecutionReceipt{}, err
	}
	if len(advances) == 0 {
		return journal, OwnedExecutionReceipt{Status: "no_effects"}, nil
	}
	if len(journal.Records) >= maxOwnedAllocations {
		return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationCapacity
	}
	r := OwnedExecutionRecord{Schema: OwnedExecutionStepSchema, Command: c, At: at, Executions: advances, Authority: ownedExecutionAuthority}
	r.SHA256 = workerplan.Hash(r)
	journal = clone(journal)
	journal.Records = append(journal.Records, retainedOwnedAllocation{Execution: &r})
	if _, err = replayOwnedAllocations(ctx, owner, journal, policies, decisions, strategies, retained, x, declarations, assessments...); err != nil {
		return journal, OwnedExecutionReceipt{}, err
	}
	raw, err := json.Marshal(journal)
	if err != nil || len(raw) > MaxOwnedAllocationBytes {
		return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationCapacity
	}
	record := clone(r)
	return journal, OwnedExecutionReceipt{Record: &record, Status: "effects_committed"}, nil
}

func (m *Memory) ExecuteOwnedAllocations(ctx context.Context, c OwnedExecutionCommand, now func() time.Time, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedExecutionReceipt{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(eval)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	assessments, err := resolveAllocationAssessmentOwner(ctx, m.receiverID, m.evaluationAssessments, declarations, decisions, strategies, m.paperDecisions, m.paperOutcomes, m.decisionFeedLocked(), m.corporateActionSources, m.corporateReferenceSources, eval, oe, policies, m.portfolioEvaluator)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	next, r, err := appendOwnedExecution(ctx, m.receiverID, m.ownedAllocations, policies, decisions, strategies, m.paperDecisions, m.ownedExecutionLocked(), c, now, m.collectors.LastClock, declarations, assessments)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return OwnedExecutionReceipt{}, err
	}
	if r.Record != nil && !r.Duplicate {
		m.ownedAllocations = clone(next)
	}
	return r, nil
}

func (p *Postgres) ExecuteOwnedAllocations(ctx context.Context, c OwnedExecutionCommand, now func() time.Time, eval PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedExecutionReceipt, error) {
	if p.executionQuotes == nil {
		return OwnedExecutionReceipt{}, ErrOwnedExecutionUnavailable
	}
	var r OwnedExecutionReceipt
	_, _, err := p.ownedAllocationAccess(ctx, true, func(ctx context.Context, owner string, journal ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, sourceClock time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		next, receipt, err := appendOwnedExecution(ctx, owner, journal, policies, decisions, strategies, retained, x, c, now, sourceClock, declarations, assessments)
		r = receipt
		return next, err == nil && receipt.Record != nil && !receipt.Duplicate, err
	}, eval, outcomeEvals...)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	return r, nil
}
