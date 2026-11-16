package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

func assessmentPlan(p forward.AssessmentPlan, w forward.Record, at time.Time) (forward.AssessmentPlanRecord, error) {
	if p.Validate(w.Declaration, at) != nil || at.Before(w.DeclaredAt) {
		return forward.AssessmentPlanRecord{}, forward.ErrInvalid
	}
	r := forward.AssessmentPlanRecord{Plan: p, DeclaredAt: at.UTC(), DeclarationSHA256: w.SHA256}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
func validateAssessmentPlan(r forward.AssessmentPlanRecord, w forward.Record) error {
	want, e := assessmentPlan(r.Plan, w, r.DeclaredAt)
	if e != nil || !sameGate(want, r) {
		return forward.ErrInvalid
	}
	return nil
}

// DeclareForwardAssessment freezes the statistical policy before collection.
func (m *Memory) DeclareForwardAssessment(ctx context.Context, p forward.AssessmentPlan, now func() time.Time) (forward.AssessmentPlanRecord, error) {
	if e := ctx.Err(); e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.forwardAssessmentPlans[p.ID]; ok {
		if !sameGate(old.Plan, p) {
			return old, forward.ErrConflict
		}
		return clone(old), nil
	}
	w, ok := m.forwardDeclarations[p.DeclarationID]
	if !ok {
		return forward.AssessmentPlanRecord{}, forward.ErrNotFound
	}
	if e := m.validateForwardLocked(w); e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	at := time.Now().UTC()
	if now != nil {
		at = now()
	}
	r, e := assessmentPlan(p, w, at)
	if e != nil {
		return r, e
	}
	for _, old := range m.forwardAssessmentPlans {
		if old.Plan.DeclarationID == p.DeclarationID {
			return r, forward.ErrConflict
		}
	}
	if m.forwardAssessmentPlans == nil {
		m.forwardAssessmentPlans = map[string]forward.AssessmentPlanRecord{}
	}
	m.forwardAssessmentPlans[p.ID] = r
	return clone(r), nil
}

// ForwardAssessmentPlan reads a detached, hash-validated pre-window plan.
func (m *Memory) ForwardAssessmentPlan(ctx context.Context, id string) (forward.AssessmentPlanRecord, error) {
	if e := ctx.Err(); e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.forwardAssessmentPlans[id]
	if !ok {
		return r, forward.ErrNotFound
	}
	return clone(r), validateAssessmentPlan(r, m.forwardDeclarations[r.Plan.DeclarationID])
}
func assessmentRetry(b account.CappedState, id string) (forward.Assessment, bool) {
	for _, e := range b.Events {
		if e.Qualification != nil && e.Qualification.PlanID == id {
			return *e.Qualification, true
		}
	}
	return forward.Assessment{}, false
}
func sortedStarts(records map[string]strategytrial.Record, family string) []strategytrial.Start {
	out := []strategytrial.Start{}
	for _, r := range records {
		if r.Start.Family == family {
			out = append(out, clone(r.Start))
		}
	}
	slices.SortFunc(out, func(a, b strategytrial.Start) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}

// AssessForward computes the result and promotion in the same book/family fence.
// Unavailable evidence is a report, never an invented zero or a final admission.
func (m *Memory) AssessForward(ctx context.Context, id string, now func() time.Time) (forward.Assessment, error) {
	if e := ctx.Err(); e != nil {
		return forward.Assessment{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.forwardAssessmentPlans[id]
	if !ok {
		return forward.Assessment{}, forward.ErrNotFound
	}
	w := m.forwardDeclarations[p.Plan.DeclarationID]
	if e := m.validateForwardLocked(w); e != nil {
		return forward.Assessment{}, e
	}
	if e := validateAssessmentPlan(p, w); e != nil {
		return forward.Assessment{}, e
	}
	b := m.cappedBooks[w.Declaration.BookID]
	if e := validateAssessmentCollections(ctx, b, id, m.qualificationEvaluator, w, m.strategyGates[w.Declaration.GateID], int64(len(sortedStarts(m.strategyTrials, w.Declaration.Family)))); e != nil {
		return forward.Assessment{}, e
	}
	if old, ok := assessmentRetry(b, id); ok {
		return clone(old), nil
	}
	at := time.Now().UTC()
	if now != nil {
		at = now()
	}
	pending := m.strategyFamilyReadyLocked(w.Declaration.Family) != nil
	a := evaluateForward(p, w, b, sortedStarts(m.strategyTrials, w.Declaration.Family), pending, at)
	if a.Status == "unavailable" {
		return a, nil
	}
	c := account.BookCommand{ID: "qualification-" + id, Kind: "qualification", At: a.At, Qualification: &a}
	next, e := account.ApplyCappedCommand(b, c)
	if e != nil {
		return a, e
	}
	if e = m.retainCappedLocked(b, next, c); e != nil {
		return a, e
	}
	return clone(a), nil
}
func (p *Postgres) loadAssessmentPlan(ctx context.Context, q cappedQuery, id string) (forward.AssessmentPlanRecord, error) {
	var raw []byte
	var r forward.AssessmentPlanRecord
	e := q.QueryRow(ctx, "SELECT payload FROM mantik_forward_assessment_plans WHERE id=$1", id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, forward.ErrNotFound
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(raw, &r); e != nil {
		return r, e
	}
	w, e := p.loadForward(ctx, q, r.Plan.DeclarationID)
	if e != nil {
		return r, e
	}
	return r, validateAssessmentPlan(r, w)
}

// DeclareForwardAssessment retains an immutable plan without changing v1 bytes.
func (p *Postgres) DeclareForwardAssessment(ctx context.Context, plan forward.AssessmentPlan, now func() time.Time) (forward.AssessmentPlanRecord, error) {
	if _, e := p.ForwardDeclaration(ctx, plan.DeclarationID); e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_assessment_plan:'||$1,0))", plan.DeclarationID); e != nil {
		return forward.AssessmentPlanRecord{}, e
	}
	old, e := p.loadAssessmentPlan(ctx, tx, plan.ID)
	if e == nil {
		if !sameGate(old.Plan, plan) {
			return old, forward.ErrConflict
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(e, forward.ErrNotFound) {
		return old, e
	}
	w, e := p.loadForward(ctx, tx, plan.DeclarationID)
	if e != nil {
		return old, e
	}
	var at time.Time
	if now != nil {
		at = now()
	} else if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); e != nil {
		return old, e
	}
	r, e := assessmentPlan(plan, w, at)
	if e != nil {
		return r, e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO mantik_forward_assessment_plans(id,declaration_id,payload) VALUES($1,$2,$3)", plan.ID, plan.DeclarationID, string(raw))
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

// ForwardAssessmentPlan reads the original plan and verifies its retained pins.
func (p *Postgres) ForwardAssessmentPlan(ctx context.Context, id string) (forward.AssessmentPlanRecord, error) {
	return p.loadAssessmentPlan(ctx, p.pool, id)
}

// AssessForward locks the book first and family second, matching allocation.
func (p *Postgres) AssessForward(ctx context.Context, id string, now func() time.Time) (forward.Assessment, error) {
	// Warm committed custody before taking a connection or the final book fence.
	// The transaction reload below must still verify the head under its row lock.
	if _, e := p.ForwardAssessmentPlan(ctx, id); e != nil {
		return forward.Assessment{}, e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return forward.Assessment{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	plan, e := p.loadAssessmentPlan(ctx, tx, id)
	if e != nil {
		return forward.Assessment{}, e
	}
	w, e := p.loadForward(ctx, tx, plan.Plan.DeclarationID)
	if e != nil {
		return forward.Assessment{}, e
	}
	var locked string
	if e = tx.QueryRow(ctx, "SELECT id FROM mantik_capped_books WHERE id=$1 FOR UPDATE", w.Declaration.BookID).Scan(&locked); e != nil {
		return forward.Assessment{}, e
	}
	if e = lockStrategyFamily(ctx, tx, w.Declaration.Family); e != nil {
		return forward.Assessment{}, e
	}
	b, e := p.loadCapped(ctx, tx, locked)
	if e != nil {
		return forward.Assessment{}, e
	}

	starts := []strategytrial.Start{}
	rows, e := tx.Query(ctx, "SELECT payload FROM mantik_strategy_trials WHERE family=$1 ORDER BY id", w.Declaration.Family)
	if e != nil {
		return forward.Assessment{}, e
	}
	for rows.Next() {
		var raw []byte
		var s strategytrial.Start
		if e = rows.Scan(&raw); e == nil {
			e = json.Unmarshal(raw, &s)
		}
		if e != nil {
			rows.Close()
			return forward.Assessment{}, e
		}
		starts = append(starts, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return forward.Assessment{}, e
	}
	familyErr := strategyFamilyReady(ctx, tx, w.Declaration.Family)
	if familyErr != nil && !errors.Is(familyErr, strategytrial.ErrFamilyPending) {
		return forward.Assessment{}, familyErr
	}
	g, e := loadGate(ctx, tx, w.Declaration.GateID)
	if e != nil {
		return forward.Assessment{}, e
	}
	if e = validateAssessmentCollections(ctx, b, id, p.qualificationEvaluator, w, g, int64(len(starts))); e != nil {
		return forward.Assessment{}, e
	}
	if old, ok := assessmentRetry(b, id); ok {
		return old, tx.Commit(ctx)
	}
	pending := familyErr != nil
	var at time.Time
	if now != nil {
		at = now()
	} else if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); e != nil {
		return forward.Assessment{}, e
	}
	a := evaluateForward(plan, w, b, starts, pending, at)
	if a.Status == "unavailable" {
		return a, tx.Commit(ctx)
	}
	c := account.BookCommand{ID: "qualification-" + id, Kind: "qualification", At: a.At, Qualification: &a}
	next, e := account.ApplyCappedCommand(b, c)
	if e != nil {
		return a, e
	}
	if e = retainCappedTx(ctx, tx, b, next, c); e != nil {
		return a, e
	}
	return a, tx.Commit(ctx)
}

func validateAssessmentCollections(ctx context.Context, b account.CappedState, id string, eval QualificationEvaluator, w forward.Record, g strategygate.Record, n int64) error {
	for i, c := range b.Events {
		if c.Incubation == nil || c.Incubation.Qualification == nil || c.Incubation.Qualification.PlanID != id {
			continue
		}
		if c.Kind != "qualification_plan" || eval == nil || c.Incubation.Qualification.SearchTotal == nil || *c.Incubation.Qualification.SearchTotal < w.SearchTotal || *c.Incubation.Qualification.SearchTotal > n {
			return forward.ErrInvalid
		}
		prefix := account.CappedPrefix(b, i)
		if e := eval(ctx, clone(prefix), clone(c), clone(w), clone(g), *c.Incubation.Qualification.SearchTotal); e != nil {
			return e
		}
	}
	return nil
}
