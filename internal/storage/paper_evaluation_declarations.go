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
const PaperBenchmarkEvaluationRecipe = "native-threshold-observed-benchmark-plan-v3"
const PaperCausalEvaluationRecipe = "native-threshold-causal-feature-plan-v4"
const PaperFeatureCausalityMethod = "native-threshold-owner-feature-availability-v1"
const PaperHoldoutEvaluationRecipe = "native-threshold-purged-holdout-plan-v2"

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
	TotalReturnMethod       string                               `json:"total_return_method,omitempty"`
	CorporateCoverage       *PaperEvaluationCoveragePlan         `json:"corporate_coverage,omitempty"`
	SearchMultiplicity      *PaperEvaluationSearchPlan           `json:"search_multiplicity,omitempty"`
	SampleDesign            *PaperEvaluationSampleDesignPlan     `json:"sample_design,omitempty"`
	FeatureCausalityMethod  string                               `json:"feature_causality_method,omitempty"`
	BenchmarkMethod         string                               `json:"benchmark_method,omitempty"`
	Holdout                 *PaperEvaluationHoldoutPlan          `json:"holdout,omitempty"`
	CorporateSources        []PaperEvaluationCorporateSourcePlan `json:"corporate_sources,omitempty"`
	FamilyID                string                               `json:"family_id"`
	Variants                []PaperEvaluationVariant             `json:"variants"`
	WindowStart             time.Time                            `json:"window_start"`
	WindowEnd               time.Time                            `json:"window_end"`
	OutcomeDeadline         time.Time                            `json:"outcome_deadline"`
	MinIndependentDecisions int                                  `json:"min_independent_decisions"`
	MinimumNetExcessCents   int64                                `json:"minimum_net_excess_cents"`
	MaximumDrawdownCents    int64                                `json:"maximum_drawdown_cents"`
	AdmissionTTLSeconds     int64                                `json:"admission_ttl_seconds"`
	Benchmarks              []string                             `json:"benchmarks"`
	StressModel             paperfill.Model                      `json:"stress_model"`
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
	if validateCorporateSourcePlans(p.CorporateSources) != nil {
		return ErrPaperEvaluationDeclarationInvalid
	}
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !validHoldoutRecipe(c) || !paperaccount.ID.MatchString(c.PolicyID) || !decisionPin.MatchString(c.PolicySHA256) || !paperaccount.ID.MatchString(p.FamilyID) || p.Variants == nil || len(p.Variants) < 1 || len(p.Variants) > paperstrategy.MaxDefinitions || !declarationTime(p.WindowStart) || !declarationTime(p.WindowEnd) || !declarationTime(p.OutcomeDeadline) || !p.WindowEnd.After(p.WindowStart) || p.WindowEnd.Sub(p.WindowStart) > 90*24*time.Hour || !p.OutcomeDeadline.After(p.WindowEnd) || p.OutcomeDeadline.Sub(p.WindowEnd) > 48*time.Hour || p.MinIndependentDecisions < 2 || p.MinIndependentDecisions > 1000000 || p.MinimumNetExcessCents < -paperaccount.MaxOpeningCashCents || p.MinimumNetExcessCents > paperaccount.MaxOpeningCashCents || p.MaximumDrawdownCents < 0 || p.MaximumDrawdownCents > paperaccount.MaxOpeningCashCents || p.AdmissionTTLSeconds < 1 || p.AdmissionTTLSeconds > 86400 || !reflect.DeepEqual(p.Benchmarks, []string{"cash_no_action", "passive_same_symbol"}) || p.StressModel.Validate() != nil {
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
	VariantConfigurations []PaperEvaluationVariantConfiguration `json:"variant_configurations,omitempty"`
	Schema                string                                `json:"schema"`
	Command               PaperEvaluationDeclarationCommand     `json:"command"`
	DeclaredAt            time.Time                             `json:"declared_at"`
	SourceSHA256          string                                `json:"source_sha256"`
	PlannedChecks         []string                              `json:"planned_checks"`
	PlannedVariants       int                                   `json:"planned_variants"`
	Status                string                                `json:"status"`
	Authority             string                                `json:"authority"`
	EvaluationAvailable   bool                                  `json:"evaluation_available"`
	AdmissionAvailable    bool                                  `json:"admission_available"`
	ExecutionAvailable    bool                                  `json:"execution_available"`
	QualificationCredit   bool                                  `json:"qualification_credit"`
	SHA256                string                                `json:"sha256"`
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
	if c.Plan.TotalReturnMethod != "" {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v10")
	}
	if c.Plan.CorporateCoverage != nil {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v9")
	}
	if c.Plan.SearchMultiplicity != nil {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v8")
	}
	if c.Plan.SampleDesign != nil {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v7")
	}
	if c.Plan.FeatureCausalityMethod != "" {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v6")
	}
	if c.Plan.BenchmarkMethod != "" {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v5")
	}
	if c.Plan.Holdout != nil {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v4")
	}
	if c.Plan.CorporateSources != nil {
		return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v3")
	}
	return paperEvaluationDeclarationVersion(c, strategies, policies, at, "mantik-owned-paper-evaluation-declaration-v2")
}

// Legacy declarations keep their ID-count semantics and original bytes. New
// declarations cannot label identical native configurations as distinct variants.
func paperEvaluationDeclarationVersion(c PaperEvaluationDeclarationCommand, strategies paperstrategy.Catalog, policies allocation.PolicyCatalog, at time.Time, schema string) (PaperEvaluationDeclaration, error) {
	if !slices.Contains([]string{"mantik-owned-paper-evaluation-declaration-v1", "mantik-owned-paper-evaluation-declaration-v2", "mantik-owned-paper-evaluation-declaration-v3", "mantik-owned-paper-evaluation-declaration-v4", "mantik-owned-paper-evaluation-declaration-v5", "mantik-owned-paper-evaluation-declaration-v6", "mantik-owned-paper-evaluation-declaration-v7", "mantik-owned-paper-evaluation-declaration-v8", "mantik-owned-paper-evaluation-declaration-v9", "mantik-owned-paper-evaluation-declaration-v10"}, schema) {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationInvalid
	}
	totalReturn := schema == "mantik-owned-paper-evaluation-declaration-v10"
	coverage := schema == "mantik-owned-paper-evaluation-declaration-v9" || totalReturn
	search := schema == "mantik-owned-paper-evaluation-declaration-v8" || coverage
	sampleDesign := schema == "mantik-owned-paper-evaluation-declaration-v7" || search
	causal := schema == "mantik-owned-paper-evaluation-declaration-v6" || sampleDesign
	benchmarks := schema == "mantik-owned-paper-evaluation-declaration-v5" || causal
	holdout := schema == "mantik-owned-paper-evaluation-declaration-v4" || benchmarks
	if totalReturn != (c.Plan.TotalReturnMethod != "") || coverage != (c.Plan.CorporateCoverage != nil) || search != (c.Plan.SearchMultiplicity != nil) || sampleDesign != (c.Plan.SampleDesign != nil) || causal != (c.Plan.FeatureCausalityMethod != "") || benchmarks != (c.Plan.BenchmarkMethod != "") || holdout != (c.Plan.Holdout != nil) || (!holdout && (schema == "mantik-owned-paper-evaluation-declaration-v3") != (c.Plan.CorporateSources != nil)) {
		return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationInvalid
	}
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
	var configurations []PaperEvaluationVariantConfiguration
	seenConfigurations := map[string]bool{}
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
		if sampleDesign && c.Plan.OutcomeDeadline.Before(c.Plan.WindowEnd.Add(time.Duration(d.Safety.MaxHoldingSeconds+min(int64(60), d.Safety.MaxQuoteAgeSeconds))*time.Second)) {
			return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationConflict
		}
		if schema != "mantik-owned-paper-evaluation-declaration-v1" {
			configuration := nativeVariantConfiguration(strategy)
			if seenConfigurations[configuration.ConfigurationSHA256] {
				return PaperEvaluationDeclaration{}, ErrPaperEvaluationDeclarationConflict
			}
			seenConfigurations[configuration.ConfigurationSHA256] = true
			configurations = append(configurations, configuration)
		}
		variants = append(variants, strategy)
	}
	if err := corporateSourcePlanSymbols(c.Plan.CorporateSources, variants); err != nil {
		return PaperEvaluationDeclaration{}, err
	}
	if err := coveragePlanSymbols(c.Plan.CorporateCoverage, variants); err != nil {
		return PaperEvaluationDeclaration{}, err
	}
	planned := []string{"leakage", "warm_up", "out_of_sample", "parameter_and_cost_sensitivity", "benchmark_comparison", "forecast_calibration", "sample_sufficiency", "search_multiplicity"}
	if coverage {
		planned = append(planned, "corporate_coverage")
	}
	r := PaperEvaluationDeclaration{VariantConfigurations: configurations, Schema: schema, Command: clone(c), DeclaredAt: at.UTC(), SourceSHA256: workerplan.Hash(struct {
		Variants []paperstrategy.Record
		Policy   allocation.PolicyRecord
	}{variants, policy}), PlannedChecks: planned, PlannedVariants: len(variants), Status: "declared_not_evaluated", Authority: "operator_predeclared_evaluation_plan_only"}
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
		want, err := paperEvaluationDeclarationVersion(r.Command, strategies, policies, r.DeclaredAt, r.Schema)
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
	// New intents must name a journal identity that the configured acquisition
	// owner can produce. Keep this append-only restriction after exact retry:
	// historical v3 intents with an unresolvable name remain replayable.
	for _, source := range c.Plan.CorporateSources {
		if !strings.HasPrefix(source.JournalID, "journal-") {
			return PaperEvaluationDeclarationReceipt{}, ErrPaperEvaluationDeclarationInvalid
		}
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
		if r.Schema == "mantik-owned-allocation-deferral-v7" || (r.Schema == "mantik-owned-allocation-deferral-v8" || (r.Schema == "mantik-owned-allocation-deferral-v9" || (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || r.Schema == "mantik-owned-allocation-deferral-v14")))))) {
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
