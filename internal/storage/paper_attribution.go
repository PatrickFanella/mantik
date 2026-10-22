package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperAttributionBytes = 8 << 20

var ErrPaperAttributionInvalid = errors.New("invalid owned paper attribution")

type PaperAllocationAttribution struct {
	ComparisonID          string              `json:"comparison_id"`
	RecordSHA256          string              `json:"record_sha256"`
	PolicySHA256          string              `json:"policy_sha256"`
	AccountID             string              `json:"account_id"`
	At                    time.Time           `json:"at"`
	Status                string              `json:"status"`
	Reasons               []string            `json:"reasons"`
	ReconsiderOn          []string            `json:"reconsider_on"`
	RequestedFundingCents int64               `json:"requested_funding_cents"`
	CostCeiling           *OwnedExecutionCost `json:"cost_ceiling,omitempty"`
}

// Unknown economic values are null, not manufactured zero costs or PnL.
// This view describes the implemented deferral owner, not future funded effects.
type PaperEconomicAttribution struct {
	Status           string `json:"status"`
	ExecutedQuantity *int64 `json:"executed_quantity"`
	FeesCents        *int64 `json:"fees_cents"`
	RealizedPnLCents *int64 `json:"realized_pnl_cents"`
	BaselineStatus   string `json:"baseline_status"`
}
type PaperAttributionRow struct {
	Decision            PaperDecisionRecord          `json:"decision"`
	Method              string                       `json:"method"`
	Symbol              string                       `json:"symbol"`
	EvidenceClass       string                       `json:"evidence_class"`
	DefinitionSHA256    string                       `json:"definition_sha256"`
	ForecastStatus      string                       `json:"forecast_status"`
	ObservationStatus   string                       `json:"observation_status"`
	Observations        []PaperOutcomeRecord         `json:"observations"`
	Allocations         []PaperAllocationAttribution `json:"allocations"`
	Economics           PaperEconomicAttribution     `json:"economics"`
	QualificationCredit bool                         `json:"qualification_credit"`
	SHA256              string                       `json:"sha256"`
}
type PaperAttributionCatalog struct {
	Schema              string                `json:"schema"`
	ReceiverID          string                `json:"receiver_id"`
	SourceSHA256        string                `json:"source_sha256"`
	Rows                []PaperAttributionRow `json:"rows"`
	Authority           string                `json:"authority"`
	ExecutionAvailable  bool                  `json:"execution_available"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}
type PaperAttributionStore interface {
	PaperAttribution(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperAttributionCatalog, error)
}

// All source catalogues have been replayed against actual retained owner sources
// in one Memory lock or repeatable-read transaction before this join.
func paperAttribution(ctx context.Context, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, outcomes PaperOutcomeCatalog, allocations OwnedAllocationCatalog) (PaperAttributionCatalog, error) {
	if decisions.ReceiverID != strategies.ReceiverID || outcomes.ReceiverID != decisions.ReceiverID || allocations.ReceiverID != decisions.ReceiverID {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	out := PaperAttributionCatalog{Schema: "mantik-owned-paper-attribution-v1", ReceiverID: decisions.ReceiverID, Rows: []PaperAttributionRow{}, Authority: "service_owned_phase_attribution_only"}
	out.SourceSHA256 = workerplan.Hash(struct {
		Decisions   PaperDecisionCatalog
		Outcomes    PaperOutcomeCatalog
		Allocations OwnedAllocationCatalog
	}{decisions, outcomes, allocations})
	seen := map[string]bool{}
	for _, d := range decisions.Records {
		if err := ctx.Err(); err != nil {
			return PaperAttributionCatalog{}, err
		}
		s, err := paperstrategy.Select(strategies, d.Command.StrategyID)
		if err != nil || s.SHA256 != d.Command.StrategySHA256 || d.Command.ReceiverID != out.ReceiverID || seen[d.Command.ID] {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
		seen[d.Command.ID] = true
		row := PaperAttributionRow{Decision: d, Method: s.Command.Definition.Method, Symbol: s.Command.Definition.Symbol, EvidenceClass: s.Command.Definition.InputEvidenceClass, DefinitionSHA256: s.DefinitionSHA256, ForecastStatus: "native_predicate_not_calibrated_forecast", ObservationStatus: "not_assessed", Observations: []PaperOutcomeRecord{}, Allocations: []PaperAllocationAttribution{}, Economics: PaperEconomicAttribution{Status: "owned_execution_unavailable", BaselineStatus: "economic_baselines_unavailable"}}
		for _, o := range outcomes.Records {
			if o.Command.DecisionID != d.Command.ID {
				continue
			}
			if o.Command.DecisionSHA256 != d.SHA256 {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
			row.Observations = append(row.Observations, o)
			row.ObservationStatus = "assessments_retained"
		}
		for _, a := range allocations.Records {
			if a.Entry.Plan == nil {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
			for _, choice := range a.Entry.Plan.Decisions {
				if choice.Proposal.ID != d.Command.ID {
					continue
				}
				if choice.Proposal.DecisionSHA256 != d.SHA256 || choice.Proposal.StrategyID != d.Command.StrategyID {
					return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
				}
				item := PaperAllocationAttribution{ComparisonID: a.Command.ID, RecordSHA256: a.SHA256, PolicySHA256: a.PolicySHA256, AccountID: a.Entry.Plan.AccountID, At: a.At, Status: choice.Status, Reasons: choice.Reasons, ReconsiderOn: choice.ReconsiderOn, RequestedFundingCents: choice.Proposal.NotionalCents}
				for _, cost := range a.ExecutionCosts {
					if cost.DecisionID == d.Command.ID {
						v := cost
						item.CostCeiling = &v
						break
					}
				}
				row.Allocations = append(row.Allocations, item)
			}
		}
		row.SHA256 = workerplan.Hash(row)
		out.Rows = append(out.Rows, row)
	}
	// Reject orphaned source rows instead of silently hiding them in a join.
	for _, o := range outcomes.Records {
		if !seen[o.Command.DecisionID] {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
	}
	for _, a := range allocations.Records {
		for _, pin := range a.Command.Decisions {
			if !seen[pin.ID] {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
		}
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > MaxPaperAttributionBytes {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	return clone(out), nil
}

func (m *Memory) PaperAttribution(ctx context.Context, decisionEval PaperDecisionEvaluator, outcomeEval PaperOutcomeEvaluator) (PaperAttributionCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperAttributionCatalog{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	outcomes, err := paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, m.decisionFeedLocked(), decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allocations, err := replayOwnedAllocations(ctx, m.receiverID, m.ownedAllocations, policies, decisions, strategies, m.paperDecisions)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	return paperAttribution(ctx, decisions, strategies, outcomes, allocations)
}
func (p *Postgres) PaperAttribution(ctx context.Context, decisionEval PaperDecisionEvaluator, outcomeEval PaperOutcomeEvaluator) (PaperAttributionCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	decisions, err := paperDecisionCatalog(feed.ReceiverID, retained, strategies, feed, decisionEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	outcomes, err := paperOutcomeCatalog(feed.ReceiverID, all, retained, strategies, feed, decisionEval, outcomeEval)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return PaperAttributionCatalog{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, feed.ReceiverID, state)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	allocations, err := replayOwnedAllocations(ctx, feed.ReceiverID, journal, policies, decisions, strategies, retained)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out, err := paperAttribution(ctx, decisions, strategies, outcomes, allocations)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperAttributionCatalog{}, err
	}
	return out, nil
}
