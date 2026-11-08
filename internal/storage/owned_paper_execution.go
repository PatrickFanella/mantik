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

// ownedProtection is one order's bound safety. A filled position exits by a
// market sell once the bid reaches the stop or target, or at the holding
// deadline counted from its first fill.
type ownedProtection struct {
	StopCents      int64
	TargetCents    int64
	HoldingSeconds int64
}

type ownedEntryBook struct {
	policyID   string
	query      PaperExecutionQuoteQuery
	expires    []time.Time
	root       paperfill.Book
	protection map[string]ownedProtection
	// done is set once neither the book nor any of its exits can take another
	// command; doneAt is the clock of the last command they took.
	done   bool
	doneAt time.Time
}

// ownedEntryBooks derives a v16 comparison's entry books: one buy limit per
// reserved proposal at its bound cost ceiling, placed at the reserve clock and
// expiring at the cost's validity, grouped by registration and quote scope so
// a book consumes one feed. Orders reserved by different comparisons get
// separate books; advancedBooks shares displayed liquidity between them.
func ownedEntryBooks(r OwnedAllocationRecord, policy allocation.PolicyRecord, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision) ([]ownedEntryBook, error) {
	if r.Schema != OwnedExecutionSchema || r.Entry.Plan == nil {
		return nil, nil
	}
	model := policy.Command.ExecutionModel
	if model == nil {
		return nil, ErrOwnedAllocationInvalid
	}
	type group struct {
		query      PaperExecutionQuoteQuery
		orders     []paperfill.Order
		protection map[string]ownedProtection
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
		if err != nil || strategy.SHA256 != decision.Record.Command.StrategySHA256 {
			return nil, ErrOwnedAllocationInvalid
		}
		d := strategy.Command.Definition
		q := PaperExecutionQuoteQuery{RegistrationID: decision.Record.Command.RegistrationID, Symbol: d.Symbol, Feed: d.Feed, EvidenceClass: d.InputEvidenceClass, MaxQuoteAgeSeconds: d.Safety.MaxQuoteAgeSeconds}
		key := workerplan.Hash(q)
		if groups[key] == nil {
			groups[key] = &group{query: q, protection: map[string]ownedProtection{}}
			keys = append(keys, key)
		}
		groups[key].orders = append(groups[key].orders, paperfill.Order{ID: p.ID, Symbol: p.Symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: cost.Quantity, LimitCents: cost.LimitCents, MaxDebitCents: cost.MaxDebitCents, PlacedAt: r.At, ExpiresAt: cost.ValidUntil, Mode: "ongoing_paper"})
		groups[key].protection[p.ID] = ownedProtection{StopCents: d.Safety.StopCents, TargetCents: d.Safety.TargetCents, HoldingSeconds: d.Safety.MaxHoldingSeconds}
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
		b := ownedEntryBook{policyID: policy.Command.Policy.ID, query: g.query, root: root, protection: g.protection}
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

// bookUse is the shares earlier books filled at one occurrence.
type bookUse struct{ ask, bid int64 }

// occurrenceKey names displayed liquidity: one registration's quote for one
// symbol at one occurrence clock, as the occurrence book keys it.
func occurrenceKey(registration string, q paperfill.Quote) string {
	return workerplan.Hash(struct {
		Registration, Symbol string
		OccurredAt           time.Time
	}{registration, q.Symbol, q.OccurredAt.UTC()})
}

// sharedDisplay is the displayed size whose modeled capacity is what earlier
// books left of displayed. The occurrence book's capacity is
// displayed*bps/10000, rounded down, so ceil(left*10000/bps) restores exactly
// left and never exceeds the original display.
func sharedDisplay(displayed, used, bps int64) int64 {
	if used <= 0 {
		return displayed
	}
	left := displayed*bps/10000 - used
	if left <= 0 {
		return 0
	}
	return (left*10000 + bps - 1) / bps
}

// ownedBookState is a book advanced to a cut: its entry book, which stops
// changing once every order in it is terminal, and one exit book per
// triggered position, in placement order.
type ownedBookState struct {
	entry  paperfill.Book
	exits  []paperfill.Book
	done   bool
	doneAt time.Time
}

type ownedPosition struct {
	firstFill time.Time
	deadline  time.Time
	quantity  int64
	trigger   time.Time
	exit      *paperfill.BookAppender
	exitBook  int
	exitDone  bool
	placedAt  time.Time
	expiresAt time.Time
}

// advance replays the book to cut from the owner's quotes. Per quote, timers
// due by its clock run first (entry expiries, then holding deadlines, then
// exit expiries). A fresh quote after a position's first fill triggers its
// exit when the bid reaches the stop or target, and a trigger cancels the
// position's entry order if it is still working. The quote then reaches the
// entry book (until it stops changing) and every exit book placed before it.
// A triggered position's exit book, one market sell of its filled quantity
// expiring a day later, is placed at the first quote after both the trigger
// and the entry book's last command, so a closed market cannot expire it.
// Displayed sizes shrink by what earlier books, and this book's earlier exits,
// filled at the same occurrence.
func (b ownedEntryBook) advance(ctx context.Context, x ownedExecution, cut time.Time, used map[string]bookUse) (ownedBookState, error) {
	placed := b.root.Orders[0].PlacedAt
	out := ownedBookState{entry: b.root}
	if !cut.After(placed) {
		return out, nil
	}
	query := b.query
	query.After, query.Through = placed, cut
	quotes, err := x.quotes(ctx, x.feed, query)
	if err != nil {
		return out, err
	}
	model := b.root.Model
	bps := model.LiquidityBPS
	entry, err := paperfill.NewBookAppender(ctx, b.root)
	if err != nil {
		return out, err
	}
	projection, err := paperfill.ReplayBook(ctx, b.root)
	if err != nil {
		return out, err
	}
	frozen, frozenAt, last := false, time.Time{}, placed
	positions := map[string]*ownedPosition{}
	ids := []string{}
	exits := []*paperfill.BookAppender{}
	expiries := slices.Clone(b.expires)
	local := map[string]bookUse{}
	capacity := func(err error) error {
		if errors.Is(err, paperfill.ErrCapacity) {
			return ErrOwnedAllocationCapacity
		}
		return err
	}
	applyEntry := func(c paperfill.BookCommand) error {
		p, receipt, err := entry.Apply(ctx, c)
		if err != nil {
			return capacity(err)
		}
		projection, last = p, c.At
		for _, o := range receipt.Outcomes {
			f := o.Outcome.Fill
			if f == nil {
				continue
			}
			pos := positions[f.OrderID]
			if pos == nil {
				pos = &ownedPosition{firstFill: f.At, deadline: f.At.Add(time.Duration(b.protection[f.OrderID].HoldingSeconds) * time.Second)}
				positions[f.OrderID] = pos
				ids = append(ids, f.OrderID)
				slices.Sort(ids)
			}
			pos.quantity += f.Quantity
		}
		frozen = true
		for _, o := range projection.Orders {
			if o.Projection.Status == "working" {
				frozen = false
			}
		}
		if frozen {
			frozenAt = c.At
		}
		return nil
	}
	trigger := func(id string, at time.Time) error {
		pos := positions[id]
		if pos == nil || !pos.trigger.IsZero() {
			return nil
		}
		pos.trigger = at
		if frozen {
			return nil
		}
		for _, o := range projection.Orders {
			if o.OrderID == id && o.Projection.Status == "working" {
				return applyEntry(paperfill.BookCommand{ID: "seal-" + workerplan.Hash(id), At: at, CancelOrderID: id})
			}
		}
		return nil
	}
	applyExit := func(pos *ownedPosition, c paperfill.BookCommand) error {
		p, _, err := pos.exit.Apply(ctx, c)
		if err != nil {
			return capacity(err)
		}
		last = c.At
		pos.exitDone = p.Orders[0].Projection.Status != "working"
		return nil
	}
	advance := func(until time.Time) error {
		for {
			next := time.Time{}
			consider := func(t time.Time) {
				if !t.After(until) && (next.IsZero() || t.Before(next)) {
					next = t
				}
			}
			if !frozen && len(expiries) > 0 {
				consider(expiries[0])
			}
			for _, id := range ids {
				pos := positions[id]
				if pos.trigger.IsZero() {
					consider(pos.deadline)
				}
				if pos.exit != nil && !pos.exitDone {
					consider(pos.expiresAt)
				}
			}
			if next.IsZero() {
				return nil
			}
			for !frozen && len(expiries) > 0 && expiries[0].Equal(next) {
				if err := applyEntry(paperfill.BookCommand{ID: "entry-expiry-" + workerplan.Hash(next), At: next}); err != nil {
					return err
				}
				expiries = expiries[1:]
			}
			if frozen {
				expiries = nil
			}
			for _, id := range ids {
				if pos := positions[id]; pos.trigger.IsZero() && pos.deadline.Equal(next) {
					if err := trigger(id, next); err != nil {
						return err
					}
				}
			}
			for _, id := range ids {
				if pos := positions[id]; pos.exit != nil && !pos.exitDone && pos.expiresAt.Equal(next) {
					if err := applyExit(pos, paperfill.BookCommand{ID: "exit-expiry-" + workerplan.Hash(next), At: next}); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, q := range quotes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if q.Quote == nil || !q.At.After(placed) || q.At.After(cut) {
			return out, ErrOwnedAllocationConflict
		}
		if err := advance(q.At); err != nil {
			return out, err
		}
		for _, id := range ids {
			pos, protection := positions[id], b.protection[id]
			if pos.trigger.IsZero() && q.Quote.OccurredAt.After(pos.firstFill) && q.At.Sub(q.Quote.OccurredAt) <= time.Duration(model.MaxQuoteAgeSeconds)*time.Second && (q.Quote.BidCents <= protection.StopCents || q.Quote.BidCents >= protection.TargetCents) {
				if err := trigger(id, q.At); err != nil {
					return out, err
				}
			}
		}
		key := occurrenceKey(b.query.RegistrationID, *q.Quote)
		if !frozen {
			c := q
			if u, ok := used[key]; ok {
				quote := *q.Quote
				quote.AskQuantity = sharedDisplay(quote.AskQuantity, u.ask, bps)
				quote.BidQuantity = sharedDisplay(quote.BidQuantity, u.bid, bps)
				c.Quote = &quote
			}
			if err := applyEntry(c); err != nil {
				return out, err
			}
		}
		for _, id := range exitOrder(positions, ids) {
			pos := positions[id]
			if pos.exitDone || !pos.placedAt.Before(q.At) {
				continue
			}
			quote := *q.Quote
			quote.BidQuantity = sharedDisplay(quote.BidQuantity, used[key].bid+local[key].bid, bps)
			c := q
			c.ID = "exit-" + q.ID
			c.Quote = &quote
			before := pos.exit.Book()
			if err := applyExit(pos, c); err != nil {
				return out, err
			}
			after := pos.exit.Book()
			for _, e := range after.Entries[len(before.Entries):] {
				for _, o := range e.Outcomes {
					if f := o.Outcome.Fill; f != nil {
						u := local[key]
						u.bid += f.Quantity
						local[key] = u
					}
				}
			}
		}
		for _, id := range ids {
			pos := positions[id]
			if pos.trigger.IsZero() || pos.exit != nil || !frozen || !q.At.After(pos.trigger) || !q.At.After(frozenAt) {
				continue
			}
			order := paperfill.Order{ID: id, Symbol: b.root.Orders[0].Symbol, Currency: "USD", Side: "sell", Kind: "market", Quantity: pos.quantity, PlacedAt: q.At, ExpiresAt: q.At.Add(24 * time.Hour), Mode: "ongoing_paper"}
			root, err := paperfill.NewOccurrenceBook(model, []paperfill.Order{order})
			if err != nil {
				return out, ErrOwnedAllocationInvalid
			}
			if pos.exit, err = paperfill.NewBookAppender(ctx, root); err != nil {
				return out, err
			}
			pos.placedAt, pos.expiresAt, pos.exitBook = order.PlacedAt, order.ExpiresAt, len(exits)
			exits = append(exits, pos.exit)
		}
	}
	if err := advance(cut); err != nil {
		return out, err
	}
	out.entry = entry.Book()
	for _, e := range exits {
		out.exits = append(out.exits, e.Book())
	}
	out.done = frozen
	for _, id := range ids {
		if pos := positions[id]; pos.exit == nil || !pos.exitDone {
			out.done = false
		}
	}
	out.doneAt = last
	return out, nil
}

// exitOrder lists positions with a placed exit in placement order.
func exitOrder(positions map[string]*ownedPosition, ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if positions[id].exit != nil {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return positions[a].exitBook - positions[b].exitBook })
	return out
}

// consume adds an advanced book's fills to the shares used per occurrence:
// entry buys take the ask and exit sells the bid.
func (b ownedEntryBook) consume(state ownedBookState, used map[string]bookUse) {
	for _, book := range append([]paperfill.Book{state.entry}, state.exits...) {
		for _, e := range book.Entries {
			if e.Command.Quote == nil {
				continue
			}
			key := occurrenceKey(b.query.RegistrationID, *e.Command.Quote)
			for _, o := range e.Outcomes {
				if f := o.Outcome.Fill; f != nil {
					u := used[key]
					if f.Side == "sell" {
						u.bid += f.Quantity
					} else {
						u.ask += f.Quantity
					}
					used[key] = u
				}
			}
		}
	}
}

// advancedBooks advances, in reservation order, every book not done, and
// every done book whose commands overlap an open one, so each open book sees
// the liquidity earlier books took. It marks books that become done and
// returns the open books' states.
func advancedBooks(ctx context.Context, books []ownedEntryBook, x ownedExecution, cut time.Time) (map[int]ownedBookState, error) {
	earliest := time.Time{}
	for _, b := range books {
		if p := b.root.Orders[0].PlacedAt; !b.done && (earliest.IsZero() || p.Before(earliest)) {
			earliest = p
		}
	}
	out := map[int]ownedBookState{}
	if earliest.IsZero() {
		return out, nil
	}
	used := map[string]bookUse{}
	for i := range books {
		b := books[i]
		if b.done && !b.doneAt.After(earliest) {
			continue
		}
		state, err := b.advance(ctx, x, cut, used)
		if err != nil {
			return nil, err
		}
		b.consume(state, used)
		if !b.done {
			out[i] = state
			if state.done {
				books[i].done, books[i].doneAt = true, state.doneAt
			}
		}
	}
	return out, nil
}

// advancePolicy stages every effect these advanced books produce that ledger
// does not hold yet: entry fills, unfilled releases, exit closes and releases
// of fully closed positions, merged in clock order, and returns the advanced
// ledger with them appended.
func advancePolicy(ctx context.Context, ledger allocation.Ledger, books []ownedBookState) (allocation.Ledger, []allocation.LedgerEntry, error) {
	type staged struct {
		command allocation.LedgerCommand
		at      time.Time
	}
	stages := []staged{}
	for _, book := range books {
		current, _, _, err := allocation.ApplyEntryFillBook(ctx, ledger, book.entry)
		if err != nil {
			return ledger, nil, ownedExecutionConflict(err)
		}
		if current, _, _, err = allocation.ApplyUnfilledEntryReleases(ctx, current, book.entry); err != nil {
			return ledger, nil, ownedExecutionConflict(err)
		}
		for _, exit := range book.exits {
			if current, _, _, err = allocation.ApplyExitFillBook(ctx, current, book.entry, exit); err != nil {
				return ledger, nil, ownedExecutionConflict(err)
			}
			if current, _, _, err = allocation.ApplyClosedPositionReleases(ctx, current, book.entry, exit); err != nil {
				return ledger, nil, ownedExecutionConflict(err)
			}
		}
		for _, e := range current.Entries[len(ledger.Entries):] {
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

// advanceAll advances every policy with an open book to cut, in policy ID
// order, starting from each policy's current ledger. A done book commits
// nothing new, though it still counts toward the liquidity later books share.
func advanceAll(ctx context.Context, ledgers map[string]allocation.Ledger, books []ownedEntryBook, x ownedExecution, cut time.Time) ([]OwnedExecutionAdvance, error) {
	advanced, err := advancedBooks(ctx, books, x, cut)
	if err != nil {
		return nil, err
	}
	byPolicy := map[string][]ownedBookState{}
	for i, b := range books {
		if state, ok := advanced[i]; ok {
			byPolicy[b.policyID] = append(byPolicy[b.policyID], state)
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
		next, added, err := advancePolicy(ctx, ledger, byPolicy[id])
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
	if item, err := archivedRetry(journal, c.ID); err != nil {
		return journal, OwnedExecutionReceipt{}, err
	} else if item != nil {
		if item.Execution == nil || item.Execution.Command != c {
			return journal, OwnedExecutionReceipt{}, ErrOwnedAllocationConflict
		}
		record := clone(*item.Execution)
		return journal, OwnedExecutionReceipt{Record: &record, Duplicate: true, Status: "effects_committed"}, nil
	}
	if next, ok, err := sealOwnedJournal(ctx, journal, catalog, state); err != nil {
		return journal, OwnedExecutionReceipt{}, err
	} else if ok {
		journal = next
		if catalog, state, err = replayOwnedJournal(ctx, owner, journal, policies, decisions, strategies, retained, x, declarations, assessments...); err != nil {
			return journal, OwnedExecutionReceipt{}, err
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
	advances, err := advanceAll(ctx, ledgers, state.books, *x, at)
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
	sealing := journal.sealing
	journal = clone(journal)
	journal.sealing = sealing
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
	next, r, err := appendOwnedExecution(ctx, m.receiverID, m.ownedAllocationsForAppendLocked(), policies, decisions, strategies, m.paperDecisions, m.ownedExecutionLocked(), c, now, m.collectors.LastClock, declarations, assessments)
	if err != nil {
		return OwnedExecutionReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return OwnedExecutionReceipt{}, err
	}
	if r.Record != nil && !r.Duplicate {
		m.commitOwnedAllocationsLocked(next)
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
