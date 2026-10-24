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
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperEvaluationDeclarations = 16
const MaxPaperEvaluationDeclarationBytes = 32768
const PaperEvaluationRecipe = "native-threshold-paper-evaluation-plan-v1"

var ErrPaperEvaluationDeclarationInvalid = errors.New("invalid paper evaluation declaration")
var ErrPaperEvaluationDeclarationConflict = errors.New("paper evaluation declaration identity or owner-source conflict")
var ErrPaperEvaluationDeclarationCapacity = errors.New("paper evaluation declaration capacity exhausted")
var ErrPaperEvaluationDeclarationNotFound = errors.New("paper evaluation declaration not found")

type PaperEvaluationVariant struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// This is an operator declaration of intended checks, never supplied results.
// Registration precedes the observation window and pins the whole variant family.
type PaperEvaluationPlan struct {
	FamilyID                string                   `json:"family_id"`
	Variants                []PaperEvaluationVariant `json:"variants"`
	WindowStart             time.Time                `json:"window_start"`
	WindowEnd               time.Time                `json:"window_end"`
	OutcomeDeadline         time.Time                `json:"outcome_deadline"`
	MinIndependentDecisions int                      `json:"min_independent_decisions"`
	MinimumNetExcessCents   int64                    `json:"minimum_net_excess_cents"`
	MaximumDrawdownCents    int64                    `json:"maximum_drawdown_cents"`
	AdmissionTTLSeconds     int64                    `json:"admission_ttl_seconds"`
	Benchmarks              []string                 `json:"benchmarks"`
	StressModel             paperfill.Model          `json:"stress_model"`
}
type PaperEvaluationDeclarationCommand struct {
	ID           string              `json:"id"`
	ReceiverID   string              `json:"receiver_id"`
	Recipe       string              `json:"recipe"`
	PolicyID     string              `json:"policy_id"`
	PolicySHA256 string              `json:"policy_sha256"`
	Plan         PaperEvaluationPlan `json:"plan"`
}

func declarationTime(t time.Time) bool {
	if t.IsZero() || t.UTC().Year() < 1 || t.UTC().Year() > 9998 {
		return false
	}
	_, err := t.MarshalJSON()
	return err == nil
}
func (c PaperEvaluationDeclarationCommand) Validate() error {
	p := c.Plan
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || c.Recipe != PaperEvaluationRecipe || !paperaccount.ID.MatchString(c.PolicyID) || !decisionPin.MatchString(c.PolicySHA256) || !paperaccount.ID.MatchString(p.FamilyID) || p.Variants == nil || len(p.Variants) < 1 || len(p.Variants) > paperstrategy.MaxDefinitions || !declarationTime(p.WindowStart) || !declarationTime(p.WindowEnd) || !declarationTime(p.OutcomeDeadline) || !p.WindowEnd.After(p.WindowStart) || p.WindowEnd.Sub(p.WindowStart) > 90*24*time.Hour || !p.OutcomeDeadline.After(p.WindowEnd) || p.OutcomeDeadline.Sub(p.WindowEnd) > 48*time.Hour || p.MinIndependentDecisions < 2 || p.MinIndependentDecisions > 1000000 || p.MinimumNetExcessCents < -paperaccount.MaxOpeningCashCents || p.MinimumNetExcessCents > paperaccount.MaxOpeningCashCents || p.MaximumDrawdownCents < 0 || p.MaximumDrawdownCents > paperaccount.MaxOpeningCashCents || p.AdmissionTTLSeconds < 1 || p.AdmissionTTLSeconds > 86400 || !reflect.DeepEqual(p.Benchmarks, []string{"cash_no_action", "passive_same_symbol"}) || p.StressModel.Validate() != nil {
		return ErrPaperEvaluationDeclarationInvalid
	}
	for i, v := range p.Variants {
		if !paperaccount.ID.MatchString(v.ID) || !decisionPin.MatchString(v.SHA256) || i > 0 && p.Variants[i-1].ID >= v.ID {
			return ErrPaperEvaluationDeclarationInvalid
		}
	}
	return nil
}

type PaperEvaluationDeclaration struct {
	Schema              string                            `json:"schema"`
	Command             PaperEvaluationDeclarationCommand `json:"command"`
	DeclaredAt          time.Time                         `json:"declared_at"`
	SourceSHA256        string                            `json:"source_sha256"`
	PlannedChecks       []string                          `json:"planned_checks"`
	PlannedVariants     int                               `json:"planned_variants"`
	Status              string                            `json:"status"`
	Authority           string                            `json:"authority"`
	EvaluationAvailable bool                              `json:"evaluation_available"`
	AdmissionAvailable  bool                              `json:"admission_available"`
	ExecutionAvailable  bool                              `json:"execution_available"`
	QualificationCredit bool                              `json:"qualification_credit"`
	SHA256              string                            `json:"sha256"`
}
type PaperEvaluationDeclarationReceipt struct {
	Record    PaperEvaluationDeclaration `json:"record"`
	Duplicate bool                       `json:"duplicate"`
}
type PaperEvaluationDeclarationCatalog struct {
	ReceiverID string                       `json:"receiver_id"`
	Records    []PaperEvaluationDeclaration `json:"records"`
}
type PaperEvaluationDeclarationStore interface {
	DeclarePaperEvaluation(context.Context, PaperEvaluationDeclarationCommand, func() time.Time) (PaperEvaluationDeclarationReceipt, error)
	PaperEvaluationDeclarations(context.Context) (PaperEvaluationDeclarationCatalog, error)
}

func newPaperEvaluationDeclaration(c PaperEvaluationDeclarationCommand, strategies paperstrategy.Catalog, policies allocation.PolicyCatalog, at time.Time) (PaperEvaluationDeclaration, error) {
	if c.Validate() != nil || !declarationTime(at) || strategies.Validate() != nil || policies.Validate() != nil || strategies.ReceiverID != c.ReceiverID || policies.ReceiverID != c.ReceiverID || !c.Plan.WindowStart.After(at) || c.Plan.WindowStart.Sub(at) > 365*24*time.Hour {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationInvalid
	}
	var policy allocation.PolicyRecord
	for _, p := range policies.Records {
		if p.Command.Policy.ID == c.PolicyID {
			policy = p
			break
		}
	}
	if policy.SHA256 != c.PolicySHA256 || policy.Command.ExecutionModel == nil || at.Before(policy.RegisteredAt) {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationConflict
	}
	base, stress := *policy.Command.ExecutionModel, c.Plan.StressModel
	if stress.SlippageBPS < base.SlippageBPS || stress.FeeBPS < base.FeeBPS || stress.CommissionPerFillCents < base.CommissionPerFillCents || stress.LatencyMillis < base.LatencyMillis || stress.LiquidityBPS > base.LiquidityBPS || stress.MaxQuoteAgeSeconds > base.MaxQuoteAgeSeconds || reflect.DeepEqual(base, stress) {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationInvalid
	}
	variants := []paperstrategy.Record{}
	for _, v := range c.Plan.Variants {
		strategy, err := paperstrategy.Select(strategies, v.ID)
		if err != nil || strategy.SHA256 != v.SHA256 || at.Before(strategy.RegisteredAt) {
			return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationConflict
		}
		d := strategy.Command.Definition
		matched := false
		for _, rule := range policy.Command.Policy.Strategies {
			if rule.ID == v.ID && rule.ArtifactSHA256 == strategy.DefinitionSHA256 {
				matched = true
			}
		}
		if !matched || d.Method != paperstrategy.Method || c.Plan.OutcomeDeadline.Before(c.Plan.WindowEnd.Add(time.Duration(d.Safety.MaxHoldingSeconds)*time.Second)) {
			return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationConflict
		}
		variants = append(variants, strategy)
	}
	r := PaperEvaluationDeclaration{Schema: "mantik-owned-paper-evaluation-declaration-v1", Command: clone(c), DeclaredAt: at.UTC(), SourceSHA256: workerplan.Hash(struct {
		Variants []paperstrategy.Record
		Policy   allocation.PolicyRecord
	}{variants, policy}), PlannedChecks: []string{"leakage", "warm_up", "out_of_sample", "parameter_and_cost_sensitivity", "benchmark_comparison", "forecast_calibration", "sample_sufficiency", "search_multiplicity"}, PlannedVariants: len(variants), Status: "declared_not_evaluated", Authority: "operator_predeclared_evaluation_plan_only"}
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationDeclarationBytes {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationCapacity
	}
	return r, nil
}
func paperEvaluationDeclarationCatalog(owner string, all map[string]PaperEvaluationDeclaration, strategies paperstrategy.Catalog, policies allocation.PolicyCatalog) (PaperEvaluationDeclarationCatalog, error) {
	out := PaperEvaluationDeclarationCatalog{ReceiverID: owner, Records: []PaperEvaluationDeclaration{}}
	if !ValidReceiverID(owner) || len(all) > MaxPaperEvaluationDeclarations || strategies.ReceiverID != owner || policies.ReceiverID != owner || strategies.Validate() != nil || policies.Validate() != nil {
		return out, ErrPaperEvaluationDeclarationInvalid
	}
	families := map[string]bool{}
	for id, r := range all {
		want, err := newPaperEvaluationDeclaration(r.Command, strategies, policies, r.DeclaredAt)
		if err != nil || id != r.Command.ID || r.Command.ReceiverID != owner || families[r.Command.Plan.FamilyID] || !reflect.DeepEqual(want, r) {
			return out, ErrPaperEvaluationDeclarationInvalid
		}
		families[r.Command.Plan.FamilyID] = true
		out.Records = append(out.Records, clone(r))
	}
	slices.SortFunc(out.Records, func(a, b PaperEvaluationDeclaration) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	return out, nil
}
func appendPaperEvaluationDeclaration(owner string, all map[string]PaperEvaluationDeclaration, strategies paperstrategy.Catalog, policies allocation.PolicyCatalog, c PaperEvaluationDeclarationCommand, now func() time.Time, allocations ownedAllocationJournal) (PaperEvaluationDeclarationReceipt, error) {
	catalog, err := paperEvaluationDeclarationCatalog(owner, all, strategies, policies)
	if err != nil {
		return PaperEvaluationDeclarationReceipt{}, err
	}
	if c.Validate() != nil {
		return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
	}
	if c.ReceiverID != owner {
		return PaperEvaluationDeclarationReceipt{}, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if workerplan.Hash(c) != workerplan.Hash(old.Command) {
			return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationConflict
		}
		return PaperEvaluationDeclarationReceipt{clone(old), true}, nil
	}
	for _, r := range catalog.Records {
		if r.Command.Plan.FamilyID == c.Plan.FamilyID {
			return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationConflict
		}
	}
	if len(all) >= MaxPaperEvaluationDeclarations {
		return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationCapacity
	}
	if now == nil {
		return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
	}
	at := now()
	// Allocation v7 captures the complete catalogue available at its clock.
	// A later new plan must not enter that historical cut, including equality.
	for _, item := range allocations.Records {
		r := item.Record
		if r.Schema == "mantik-owned-allocation-deferral-v7" || r.Schema == "mantik-owned-allocation-deferral-v8" {
			unsigned := clone(r)
			unsigned.SHA256 = ""
			if r.Command.ReceiverID != owner || !declarationTime(r.At) || workerplan.Hash(unsigned) != r.SHA256 || !at.After(r.At) {
				return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
			}
		}
	}
	for _, r := range catalog.Records {
		if at.Before(r.DeclaredAt) {
			return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
		}
	}
	r, err := newPaperEvaluationDeclaration(c, strategies, policies, at)
	return PaperEvaluationDeclarationReceipt{r, false}, err
}
func (m *Memory) DeclarePaperEvaluation(ctx context.Context, c PaperEvaluationDeclarationCommand, now func() time.Time) (PaperEvaluationDeclarationReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationDeclarationReceipt{}, err
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperEvaluationDeclarationReceipt{}, err
	}
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return PaperEvaluationDeclarationReceipt{}, err
	}
	r, err := appendPaperEvaluationDeclaration(m.receiverID, m.paperEvaluationPlans, strategies, policies, c, now, m.ownedAllocations)
	if err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return PaperEvaluationDeclarationReceipt{}, err
	}
	if !r.Duplicate {
		if err := paperEvaluationSourceClock(m.receiverID, m.evaluationAssessments, r.Record.DeclaredAt); err != nil {
			return PaperEvaluationDeclarationReceipt{}, err
		}
		m.paperEvaluationPlans[c.ID] = clone(r.Record)
	}
	return r, nil
}
func (m *Memory) PaperEvaluationDeclarations(ctx context.Context) (PaperEvaluationDeclarationCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperEvaluationDeclarationCatalog{}, err
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, err
	}
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, err
	}
	return paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
}
func readPaperEvaluationDeclarations(ctx context.Context, tx pgx.Tx) (map[string]PaperEvaluationDeclaration, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_evaluation_declarations ORDER BY id LIMIT $1", MaxPaperEvaluationDeclarations+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]PaperEvaluationDeclaration{}
	for rows.Next() {
		var id string
		var raw []byte
		var r PaperEvaluationDeclaration
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if workerplan.ReadBoundedShape(raw, &r, MaxPaperEvaluationDeclarationBytes) != nil || id != r.Command.ID {
			return nil, ErrPaperEvaluationDeclarationInvalid
		}
		all[id] = r
	}
	if len(all) > MaxPaperEvaluationDeclarations {
		return nil, ErrPaperEvaluationDeclarationCapacity
	}
	return all, rows.Err()
}
func (p *Postgres) paperEvaluationDeclarationAccess(ctx context.Context, write bool, c PaperEvaluationDeclarationCommand, now func() time.Time) (PaperEvaluationDeclarationCatalog, PaperEvaluationDeclarationReceipt, error) {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		opts = pgx.TxOptions{}
	}
	tx, err := p.pool.BeginTx(ctx, opts)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Same state -> receiver order as account policy writers. Never acquire a
	// collector lock after these locks; no feed data is read or evaluated here.
	stateQuery := "SELECT payload FROM mantik_state WHERE id='demo'"
	receiverQuery := "SELECT id FROM mantik_research_receiver WHERE singleton"
	if write {
		stateQuery += " FOR UPDATE"
		receiverQuery += " FOR UPDATE"
	}
	var raw []byte
	var state domain.State
	var owner string
	if err = tx.QueryRow(ctx, stateQuery).Scan(&raw); err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
	}
	if err = tx.QueryRow(ctx, receiverQuery).Scan(&owner); err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	policies, err := readAllocationPolicies(ctx, tx, owner, state)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	all, err := readPaperEvaluationDeclarations(ctx, tx)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	var receipt PaperEvaluationDeclarationReceipt
	if write {
		// State and receiver locks serialize this clock cut with comparison writes.
		journal, e := readOwnedAllocationJournal(ctx, tx)
		if e != nil {
			return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, e
		}
		receipt, err = appendPaperEvaluationDeclaration(owner, all, strategies, policies, c, now, journal)
		if err != nil {
			return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
		}
		if !receipt.Duplicate {
			assessments, e := readPaperEvaluationAssessments(ctx, tx)
			if e != nil {
				return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, e
			}
			if e = paperEvaluationSourceClock(owner, assessments, receipt.Record.DeclaredAt); e != nil {
				return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, e
			}
			raw, err = json.Marshal(receipt.Record)
			if err != nil {
				return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_evaluation_declarations(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
				return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
			}
			all[c.ID] = receipt.Record
		}
	}
	catalog, err := paperEvaluationDeclarationCatalog(owner, all, strategies, policies)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	// A staged plan is not a durable declaration. A transport failure at COMMIT
	// can leave the outcome uncertain; read the owner catalogue before retrying.
	if err = tx.Commit(ctx); err != nil {
		return PaperEvaluationDeclarationCatalog{}, PaperEvaluationDeclarationReceipt{}, err
	}
	return catalog, receipt, nil
}
func (p *Postgres) DeclarePaperEvaluation(ctx context.Context, c PaperEvaluationDeclarationCommand, now func() time.Time) (PaperEvaluationDeclarationReceipt, error) {
	if c.Validate() != nil {
		return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
	}
	_, r, err := p.paperEvaluationDeclarationAccess(ctx, true, c, now)
	return r, err
}
func (p *Postgres) PaperEvaluationDeclarations(ctx context.Context) (PaperEvaluationDeclarationCatalog, error) {
	c, _, err := p.paperEvaluationDeclarationAccess(ctx, false, PaperEvaluationDeclarationCommand{}, nil)
	return c, err
}

var _ PaperEvaluationDeclarationStore = (*Memory)(nil)
var _ PaperEvaluationDeclarationStore = (*Postgres)(nil)
