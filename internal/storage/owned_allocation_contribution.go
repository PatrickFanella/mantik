package storage

import (
	"context"
	"encoding/json"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxOwnedAllocationContributionBytes = 2 << 20

// MaxOwnedAllocationContributionPages bounds one assembled decision total.
const MaxOwnedAllocationContributionPages = 4096

// OwnedAllocationContributionQuery selects one decision's financial
// contributions. After is the decision-history cursor of the previous page.
type OwnedAllocationContributionQuery struct {
	ReceiverID string
	DecisionID string
	After      string
	Limit      int
}

func (q OwnedAllocationContributionQuery) history() OwnedAllocationHistoryQuery {
	return OwnedAllocationHistoryQuery{ReceiverID: q.ReceiverID, After: q.After, Limit: q.Limit, DecisionID: q.DecisionID}
}

// Validate requires a decision: an unfiltered contribution has no meaning.
func (q OwnedAllocationContributionQuery) Validate() error {
	if !paperaccount.ID.MatchString(q.DecisionID) {
		return ErrOwnedAllocationInvalid
	}
	return q.history().Validate()
}

// OwnedAllocationContributionChoice is the plan's answer for the decision in
// one comparison. Only a reserve_plan choice commits requested funding.
type OwnedAllocationContributionChoice struct {
	Status                string `json:"status"`
	RequestedFundingCents int64  `json:"requested_funding_cents"`
}

// OwnedAllocationContributionRecord projects one original comparison or
// execution step onto a single decision. A record that only names the
// decision keeps an empty effect list, so no reference is silently dropped.
type OwnedAllocationContributionRecord struct {
	Sequence     int                                `json:"sequence"`
	Kind         string                             `json:"kind"`
	RecordID     string                             `json:"record_id"`
	RecordSHA256 string                             `json:"record_sha256"`
	Choice       *OwnedAllocationContributionChoice `json:"choice,omitempty"`
	Effects      []PaperExecutionEffect             `json:"effects"`
}

// OwnedAllocationContributionSums are plain sums of the listed records.
type OwnedAllocationContributionSums struct {
	ReservedCents    int64 `json:"reserved_cents"`
	FilledCostCents  int64 `json:"filled_cost_cents"`
	ClosedCostCents  int64 `json:"closed_cost_cents"`
	NetProceedsCents int64 `json:"net_proceeds_cents"`
	ReleasedCents    int64 `json:"released_cents"`
}

func (s *OwnedAllocationContributionSums) add(records []OwnedAllocationContributionRecord) {
	for _, r := range records {
		if r.Choice != nil && r.Choice.Status == "reserve_plan" {
			s.ReservedCents += r.Choice.RequestedFundingCents
		}
		for _, e := range r.Effects {
			switch e.Kind {
			case "fill":
				s.FilledCostCents += e.AmountCents
			case "close":
				s.ClosedCostCents += e.AmountCents
				s.NetProceedsCents += e.ProceedsCents
			case "release":
				s.ReleasedCents += e.AmountCents
			}
		}
	}
}

// OwnedAllocationContributionActive is the working journal after the sealed
// cut, read under the same lock or snapshot as the first sealed page.
type OwnedAllocationContributionActive struct {
	ThroughSequence int                                 `json:"through_sequence"`
	Records         []OwnedAllocationContributionRecord `json:"records"`
}

// OwnedAllocationContributionPage is one bounded step of a decision's
// financial contributions. Records come from natively replayed sealed
// segments; Active is present on the first page only. A single page is not a
// decision total: sum every page of one cut with
// AssembleOwnedAllocationContribution.
type OwnedAllocationContributionPage struct {
	Schema              string                              `json:"schema"`
	ReceiverID          string                              `json:"receiver_id"`
	DecisionID          string                              `json:"decision_id"`
	ThroughSequence     int                                 `json:"through_sequence"`
	CheckpointSHA256    string                              `json:"checkpoint_sha256,omitempty"`
	Active              *OwnedAllocationContributionActive  `json:"active,omitempty"`
	Records             []OwnedAllocationContributionRecord `json:"records"`
	Subtotal            OwnedAllocationContributionSums     `json:"subtotal"`
	NextAfter           string                              `json:"next_after,omitempty"`
	Scope               string                              `json:"scope"`
	Authority           string                              `json:"authority"`
	QualificationCredit bool                                `json:"qualification_credit"`
	SHA256              string                              `json:"sha256"`
}

type OwnedAllocationContributionStore interface {
	OwnedAllocationContribution(context.Context, OwnedAllocationContributionQuery, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationContributionPage, error)
}

func validContributionRecords(records []OwnedAllocationContributionRecord, after, through int) (int, error) {
	for _, r := range records {
		if r.Sequence <= after || r.Sequence > through || !paperaccount.ID.MatchString(r.RecordID) || !decisionPin.MatchString(r.RecordSHA256) || r.Effects == nil || r.Kind != "comparison" && (r.Kind != "execution" || r.Choice != nil) {
			return 0, ErrOwnedAllocationInvalid
		}
		if r.Choice != nil && (r.Choice.Status == "" || r.Choice.RequestedFundingCents < 0) {
			return 0, ErrOwnedAllocationInvalid
		}
		for _, e := range r.Effects {
			if e.RecordID != r.RecordID || e.EffectID == "" || e.AmountCents < 0 || e.ProceedsCents < 0 || e.Kind != "fill" && e.Kind != "close" && e.Kind != "release" || e.Kind != "close" && e.ProceedsCents != 0 {
				return 0, ErrOwnedAllocationInvalid
			}
		}
		after = r.Sequence
	}
	return after, nil
}

// Validate checks the presentation envelope: cut binding, ordering, effect
// shapes and the subtotal. The projection itself is established by native
// replay inside the service's lock or repeatable-read transaction.
func (p OwnedAllocationContributionPage) Validate(q OwnedAllocationContributionQuery) error {
	if q.Validate() != nil {
		return ErrOwnedAllocationInvalid
	}
	after, err := q.history().cursor()
	sha := p.SHA256
	p.SHA256 = ""
	if err != nil || p.Schema != "mantik-allocation-contribution-page-v1" || p.ReceiverID != q.ReceiverID || p.DecisionID != q.DecisionID || p.Scope != "native_decision_financial_contributions" || p.Authority != "service_owned_allocation_history_only" || p.QualificationCredit || p.Records == nil || len(p.Records) > q.Limit || workerplan.Hash(p) != sha || p.ThroughSequence < 0 || (after == nil) != (p.Active != nil) {
		return ErrOwnedAllocationInvalid
	}
	start := 0
	if after != nil {
		start = after.AfterSequence
		if after.ThroughSequence != p.ThroughSequence || after.CheckpointSHA256 != p.CheckpointSHA256 {
			return ErrOwnedAllocationInvalid
		}
	}
	if p.ThroughSequence == 0 && (len(p.Records) != 0 || p.NextAfter != "" || p.CheckpointSHA256 != "") || p.ThroughSequence > 0 && !decisionPin.MatchString(p.CheckpointSHA256) {
		return ErrOwnedAllocationInvalid
	}
	last, err := validContributionRecords(p.Records, start, p.ThroughSequence)
	if err != nil {
		return err
	}
	var sums OwnedAllocationContributionSums
	sums.add(p.Records)
	if p.Active != nil {
		a := p.Active
		if a.Records == nil || a.ThroughSequence < p.ThroughSequence || a.ThroughSequence-p.ThroughSequence > MaxOwnedAllocations || len(a.Records) > a.ThroughSequence-p.ThroughSequence {
			return ErrOwnedAllocationInvalid
		}
		if _, err := validContributionRecords(a.Records, p.ThroughSequence, a.ThroughSequence); err != nil {
			return err
		}
		sums.add(a.Records)
	}
	if sums != p.Subtotal {
		return ErrOwnedAllocationInvalid
	}
	if p.NextAfter != "" {
		nextQuery := q.history()
		nextQuery.After = p.NextAfter
		next, err := nextQuery.cursor()
		if err != nil || next == nil || len(p.Records) != q.Limit || next.ThroughSequence != p.ThroughSequence || next.CheckpointSHA256 != p.CheckpointSHA256 || next.AfterSequence != last {
			return ErrOwnedAllocationInvalid
		}
	}
	return nil
}

// allocationContributionRecord projects a natively replayed original onto one
// decision, in the effect order paperExecutionAttribution uses.
func allocationContributionRecord(decision string, r OwnedAllocationHistoryRecord) (OwnedAllocationContributionRecord, error) {
	out := OwnedAllocationContributionRecord{Sequence: r.Sequence, Kind: r.Kind, RecordSHA256: r.RecordSHA256, Effects: []PaperExecutionEffect{}}
	collect := func(entries []allocation.LedgerEntry) {
		for _, e := range entries {
			if f := e.Command.Effect; f != nil && f.DecisionID == decision {
				out.Effects = append(out.Effects, PaperExecutionEffect{RecordID: out.RecordID, EffectID: f.ID, Kind: f.Kind, At: e.At, AmountCents: f.AmountCents, ProceedsCents: f.ProceedsCents})
			}
		}
	}
	switch {
	case r.Kind == "comparison" && r.Comparison != nil && r.Execution == nil:
		c := r.Comparison
		out.RecordID = c.Command.ID
		if c.Entry.Plan != nil {
			for _, choice := range c.Entry.Plan.Decisions {
				if choice.Proposal.ID != decision {
					continue
				}
				if out.Choice != nil {
					return OwnedAllocationContributionRecord{}, ErrOwnedAllocationInvalid
				}
				out.Choice = &OwnedAllocationContributionChoice{Status: choice.Status, RequestedFundingCents: choice.Proposal.NotionalCents}
			}
		}
		for _, advance := range c.Executions {
			collect(advance.Entries)
		}
		collect(c.Expirations)
	case r.Kind == "execution" && r.Execution != nil && r.Comparison == nil:
		out.RecordID = r.Execution.Command.ID
		for _, advance := range r.Execution.Executions {
			collect(advance.Entries)
		}
	default:
		return OwnedAllocationContributionRecord{}, ErrOwnedAllocationInvalid
	}
	return out, nil
}

// activeAllocationContribution projects the working journal's records that
// name the decision. Each original is read back from its native replay.
func activeAllocationContribution(decision string, through int, j ownedAllocationJournal, catalog OwnedAllocationCatalog) (*OwnedAllocationContributionActive, error) {
	out := &OwnedAllocationContributionActive{ThroughSequence: through + len(j.Records), Records: []OwnedAllocationContributionRecord{}}
	for i, item := range j.Records {
		ids, err := allocationFinancialDecisions(item)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, decision) {
			continue
		}
		r := OwnedAllocationHistoryRecord{Sequence: through + i + 1}
		if item.Execution != nil {
			r.Kind = "execution"
			for _, e := range catalog.Executions {
				if e.Command.ID == item.Execution.Command.ID {
					e := clone(e)
					r.Execution, r.RecordSHA256 = &e, e.SHA256
					break
				}
			}
		} else {
			c, err := allocationComparisonFromCatalog(item.Record.Command.ID, catalog)
			if err != nil {
				return nil, ErrOwnedAllocationConflict
			}
			r.Kind, r.Comparison, r.RecordSHA256 = "comparison", &c, c.SHA256
		}
		projected, err := allocationContributionRecord(decision, r)
		if err != nil {
			return nil, ErrOwnedAllocationConflict
		}
		out.Records = append(out.Records, projected)
	}
	return out, nil
}

func finishAllocationContribution(ctx context.Context, q OwnedAllocationContributionQuery, sealed OwnedAllocationHistoryPage, active *OwnedAllocationContributionActive) (OwnedAllocationContributionPage, error) {
	p := OwnedAllocationContributionPage{Schema: "mantik-allocation-contribution-page-v1", ReceiverID: q.ReceiverID, DecisionID: q.DecisionID, ThroughSequence: sealed.ThroughSequence, CheckpointSHA256: sealed.CheckpointSHA256, Active: active, Records: []OwnedAllocationContributionRecord{}, NextAfter: sealed.NextAfter, Scope: "native_decision_financial_contributions", Authority: "service_owned_allocation_history_only"}
	for _, r := range sealed.Records {
		projected, err := allocationContributionRecord(q.DecisionID, r)
		if err != nil {
			return OwnedAllocationContributionPage{}, err
		}
		p.Records = append(p.Records, projected)
	}
	p.Subtotal.add(p.Records)
	if active != nil {
		p.Subtotal.add(active.Records)
	}
	p.SHA256 = workerplan.Hash(p)
	if err := p.Validate(q); err != nil {
		return OwnedAllocationContributionPage{}, err
	}
	if raw, err := json.Marshal(p); err != nil || len(raw) > MaxOwnedAllocationContributionBytes {
		return OwnedAllocationContributionPage{}, ErrOwnedAllocationCapacity
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationContributionPage{}, err
	}
	return clone(p), nil
}

func (m *Memory) OwnedAllocationContribution(ctx context.Context, q OwnedAllocationContributionQuery, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationContributionPage, error) {
	if q.Validate() != nil || de == nil {
		return OwnedAllocationContributionPage{}, ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sealed, err := m.allocationHistoryPageLocked(ctx, q.history(), de, oe...)
	if err != nil {
		return OwnedAllocationContributionPage{}, err
	}
	var active *OwnedAllocationContributionActive
	if q.After == "" {
		j := m.ownedAllocations
		if err := allocationTargetDependencies(j); err != nil {
			return OwnedAllocationContributionPage{}, err
		}
		catalog, err := m.replayAllocationReadLocked(ctx, j, nil, de, oe...)
		if err != nil {
			return OwnedAllocationContributionPage{}, err
		}
		if active, err = activeAllocationContribution(q.DecisionID, sealed.ThroughSequence, j, catalog); err != nil {
			return OwnedAllocationContributionPage{}, err
		}
	}
	return finishAllocationContribution(ctx, q, sealed, active)
}

func (p *Postgres) OwnedAllocationContribution(ctx context.Context, q OwnedAllocationContributionQuery, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationContributionPage, error) {
	if q.Validate() != nil || de == nil {
		return OwnedAllocationContributionPage{}, ErrOwnedAllocationInvalid
	}
	sealed, journal, catalog, err := p.readAllocationHistory(ctx, q.history(), q.After == "", de, oe...)
	if err != nil {
		return OwnedAllocationContributionPage{}, err
	}
	var active *OwnedAllocationContributionActive
	if q.After == "" {
		if active, err = activeAllocationContribution(q.DecisionID, sealed.ThroughSequence, journal, catalog); err != nil {
			return OwnedAllocationContributionPage{}, err
		}
	}
	return finishAllocationContribution(ctx, q, sealed, active)
}

// OwnedAllocationContributionTotal is the caller's sum of every native page
// of one cut for one decision. Its accounting uses the same rule as paper
// attribution, so both agree when read at the same journal position.
type OwnedAllocationContributionTotal struct {
	Schema              string                 `json:"schema"`
	ReceiverID          string                 `json:"receiver_id"`
	DecisionID          string                 `json:"decision_id"`
	ThroughSequence     int                    `json:"through_sequence"`
	CheckpointSHA256    string                 `json:"checkpoint_sha256,omitempty"`
	JournalSequence     int                    `json:"journal_sequence"`
	Pages               int                    `json:"pages"`
	SealedRecords       int                    `json:"sealed_records"`
	ActiveRecords       int                    `json:"active_records"`
	Status              string                 `json:"status"`
	ReservedCents       *int64                 `json:"reserved_cents"`
	FilledCostCents     *int64                 `json:"filled_cost_cents"`
	ClosedCostCents     *int64                 `json:"closed_cost_cents"`
	NetProceedsCents    *int64                 `json:"net_proceeds_cents"`
	ReleasedCents       *int64                 `json:"released_cents"`
	RealizedPnLCents    *int64                 `json:"realized_pnl_cents"`
	Effects             []PaperExecutionEffect `json:"effects"`
	Assembly            string                 `json:"assembly"`
	Authority           string                 `json:"authority"`
	QualificationCredit bool                   `json:"qualification_credit"`
	SHA256              string                 `json:"sha256"`
}

// AssembleOwnedAllocationContribution sums the complete page chain of one
// cut. It rejects a missing first page, a broken or unfinished cursor chain,
// and pages from another receiver, decision or checkpoint.
func AssembleOwnedAllocationContribution(receiverID, decisionID string, limit int, pages []OwnedAllocationContributionPage) (OwnedAllocationContributionTotal, error) {
	if len(pages) == 0 || len(pages) > MaxOwnedAllocationContributionPages {
		return OwnedAllocationContributionTotal{}, ErrOwnedAllocationInvalid
	}
	q := OwnedAllocationContributionQuery{ReceiverID: receiverID, DecisionID: decisionID, Limit: limit}
	first := pages[0]
	out := OwnedAllocationContributionTotal{Schema: "mantik-allocation-contribution-total-v1", ReceiverID: receiverID, DecisionID: decisionID, ThroughSequence: first.ThroughSequence, CheckpointSHA256: first.CheckpointSHA256, Pages: len(pages), Assembly: "caller_sum_of_native_contribution_pages", Authority: "service_owned_allocation_history_only"}
	var sums OwnedAllocationContributionSums
	effects := []PaperExecutionEffect{}
	take := func(records []OwnedAllocationContributionRecord) {
		sums.add(records)
		for _, r := range records {
			effects = append(effects, r.Effects...)
		}
	}
	for i, p := range pages {
		if p.Validate(q) != nil || p.ThroughSequence != first.ThroughSequence || p.CheckpointSHA256 != first.CheckpointSHA256 || (p.NextAfter == "") != (i == len(pages)-1) {
			return OwnedAllocationContributionTotal{}, ErrOwnedAllocationInvalid
		}
		take(p.Records)
		out.SealedRecords += len(p.Records)
		q.After = p.NextAfter
	}
	take(first.Active.Records)
	out.JournalSequence, out.ActiveRecords = first.Active.ThroughSequence, len(first.Active.Records)
	e := paperEconomicsFromEffects(sums.ReservedCents, effects)
	out.Status, out.ReservedCents, out.FilledCostCents, out.ClosedCostCents, out.NetProceedsCents, out.ReleasedCents, out.RealizedPnLCents, out.Effects = e.Status, e.ReservedCents, e.FilledCostCents, e.ClosedCostCents, e.NetProceedsCents, e.ReleasedCents, e.RealizedPnLCents, e.Effects
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
