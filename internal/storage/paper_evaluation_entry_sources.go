package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5"
)

// Internal read input, never an HTTP command. Every owner is replayed in the
// same locked/repeatable-read snapshot; callers select only a declaration ID.
type PaperEvaluationEntrySources struct {
	Evidence PaperEvaluationEvidence
	Policy   allocation.PolicyRecord
	Rows     []PaperEvaluationEntrySource
}
type PaperEvaluationEntrySource struct {
	Decision   PaperDecisionRecord
	Strategy   paperstrategy.Record
	Outcome    *PaperOutcomeSources
	BaseCost   *OwnedExecutionCost
	StressCost *OwnedExecutionCost
	Status     string
}
type PaperEvaluationEntrySourceStore interface {
	PaperEvaluationEntrySources(context.Context, string, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperEvaluationEntrySources, error)
}

func evaluationEntrySources(ctx context.Context, id string, now func() time.Time, s paperEvaluationSources, policies allocation.PolicyCatalog, retainedOutcomes map[string]retainedPaperOutcome) (PaperEvaluationEntrySources, error) {
	if now == nil {
		return PaperEvaluationEntrySources{}, ErrPaperEvaluationEvidenceInvalid
	}
	at := now()
	if at.Before(s.sourceClock) {
		return PaperEvaluationEntrySources{}, ErrPaperEvaluationEvidenceInvalid
	}
	e, err := paperEvaluationEvidence(ctx, id, s.declarations, s.decisions, s.outcomes, s.retained, at)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	return evaluationEntrySourcesForEvidence(ctx, e, s.strategies, policies, retainedOutcomes)
}

func evaluationEntrySourcesForEvidence(ctx context.Context, e PaperEvaluationEvidence, strategies paperstrategy.Catalog, policies allocation.PolicyCatalog, retainedOutcomes map[string]retainedPaperOutcome) (PaperEvaluationEntrySources, error) {
	out := PaperEvaluationEntrySources{Evidence: e, Rows: []PaperEvaluationEntrySource{}}
	for _, p := range policies.Records {
		if p.Command.Policy.ID == e.Declaration.Command.PolicyID && p.SHA256 == e.Declaration.Command.PolicySHA256 {
			out.Policy = clone(p)
		}
	}
	if out.Policy.Command.ExecutionModel == nil {
		return PaperEvaluationEntrySources{}, ErrPaperEvaluationDeclarationConflict
	}
	for _, row := range e.Rows {
		if row.Status != "selected_research_evidence" {
			continue
		}
		strategy, err := paperstrategy.Select(strategies, row.Decision.Command.StrategyID)
		if err != nil || strategy.SHA256 != row.Decision.Command.StrategySHA256 {
			return PaperEvaluationEntrySources{}, ErrPaperEvaluationEvidenceInvalid
		}
		v := PaperEvaluationEntrySource{Decision: clone(row.Decision), Strategy: clone(strategy), Status: "outcome_sources_unavailable"}
		// Latest on-time retained outcome supplies the broadest historical source
		// cut; equal-clock ties use the lexicographically smallest outcome ID.
		var selected *PaperOutcomeRecord
		for i := range row.Outcomes {
			o := row.Outcomes[i]
			if selected == nil || o.EvaluatedAt.After(selected.EvaluatedAt) || o.EvaluatedAt.Equal(selected.EvaluatedAt) && o.Command.ID < selected.Command.ID {
				copy := o
				selected = &copy
			}
		}
		if selected != nil {
			source, err := selectPaperOutcomeSources(selected.Command.ID, retainedOutcomes)
			if err != nil || source.Outcome.SHA256 != selected.SHA256 {
				return PaperEvaluationEntrySources{}, ErrPaperEvaluationEvidenceInvalid
			}
			v.Outcome = &source
			until := row.Decision.FinalizedAt.Add(time.Duration(strategy.Command.Definition.Safety.MaxHoldingSeconds) * time.Second)
			base, err := executionCost(out.Policy, row.Decision, strategy, until)
			stressPolicy := clone(out.Policy)
			stress := e.Declaration.Command.Plan.StressModel
			stressPolicy.Command.ExecutionModel = &stress
			stressed, se := executionCost(stressPolicy, row.Decision, strategy, until)
			v.Status = "entry_cost_bounds_unavailable"
			if err == nil && se == nil {
				v.BaseCost = &base
				v.StressCost = &stressed
				v.Status = "entry_sources_selected"
				if selected.EvaluatedAt.Before(until) {
					v.Status = "outcome_cut_before_entry_expiry"
				}
			}
		}
		out.Rows = append(out.Rows, v)
	}
	return clone(out), nil
}
func (m *Memory) PaperEvaluationEntrySources(ctx context.Context, id string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationEntrySources, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	s, err := m.paperEvaluationSourcesLocked(de, oe)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	return evaluationEntrySources(ctx, id, now, s, policies, m.paperOutcomes)
}
func (p *Postgres) PaperEvaluationEntrySources(ctx context.Context, id string, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationEntrySources, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	s, err := readPaperEvaluationSources(ctx, tx, false, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	var state domain.State
	if json.Unmarshal(raw, &state) != nil {
		return PaperEvaluationEntrySources{}, ErrPaperEvaluationEvidenceInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, s.declarations.ReceiverID, state)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	outcomes, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	out, err := evaluationEntrySources(ctx, id, now, s, policies, outcomes)
	if err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperEvaluationEntrySources{}, err
	}
	return out, nil
}

var _ PaperEvaluationEntrySourceStore = (*Memory)(nil)
var _ PaperEvaluationEntrySourceStore = (*Postgres)(nil)
