package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

// QualificationEvaluator is an immutable constructor dependency of the compiled
// service, never a command field. It recomputes decisions from pinned inputs
// without I/O while the book/family fence is held. It must not call the store.
type QualificationEvaluator func(context.Context, account.CappedState, account.BookCommand, forward.Record, strategygate.Record, int64) error

// QualificationCollector is distinct from the ordinary journal command API.
type QualificationCollector interface {
	CollectQualification(context.Context, string, int64, account.BookCommand) (account.CappedState, error)
}

// NewMemoryWithQualificationEvaluator installs compiled collection validation.
func NewMemoryWithQualificationEvaluator(eval QualificationEvaluator) (*Memory, error) {
	if eval == nil {
		return nil, forward.ErrInvalid
	}
	m := NewMemory()
	m.qualificationEvaluator = eval
	return m, nil
}

// OpenPostgresWithQualificationEvaluator installs compiled validation at startup.
func OpenPostgresWithQualificationEvaluator(ctx context.Context, url string, eval QualificationEvaluator) (*Postgres, error) {
	if eval == nil {
		return nil, forward.ErrInvalid
	}
	p, e := OpenPostgres(ctx, url)
	if e == nil {
		p.qualificationEvaluator = eval
	}
	return p, e
}

func (m *Memory) CollectQualification(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	return m.applyCappedBook(ctx, id, version, c, true)
}
func (p *Postgres) CollectQualification(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	return p.applyCappedBook(ctx, id, version, c, true)
}
func collectionPlanID(c account.BookCommand) (string, error) {
	if c.Kind != "qualification_plan" || c.Qualification != nil || c.Incubation == nil || c.Incubation.Qualification == nil {
		return "", forward.ErrInvalid
	}
	return c.Incubation.Qualification.PlanID, nil
}
func (m *Memory) validateCollectionLocked(ctx context.Context, s account.CappedState, command *account.BookCommand) error {
	c := *command
	id, e := collectionPlanID(c)
	if e != nil || m.qualificationEvaluator == nil {
		return forward.ErrInvalid
	}
	if e = m.checkPortfolioAdmissionsLocked(s, c); e != nil {
		return e
	}
	p := m.forwardAssessmentPlans[id]
	w := m.forwardDeclarations[p.Plan.DeclarationID]
	g := m.strategyGates[w.Declaration.GateID]
	n, e := m.strategySearchTotalLocked(w.Declaration.Family)
	if errors.Is(e, strategytrial.ErrFamilyPending) {
		n = forward.PendingSearchTotal
	} else if e != nil {
		return e
	}
	c, e = bindCollectionSearchTotal(s, c, n)
	if e != nil {
		return e
	}
	*command = c
	return m.qualificationEvaluator(ctx, collectionPrefix(s, c.ID), clone(c), clone(w), clone(g), *c.Incubation.Qualification.SearchTotal)
}
func (p *Postgres) validateCollectionTx(ctx context.Context, tx pgx.Tx, s account.CappedState, command *account.BookCommand) error {
	c := *command
	id, e := collectionPlanID(c)
	if e != nil || p.qualificationEvaluator == nil {
		return forward.ErrInvalid
	}
	plan, e := p.loadAssessmentPlan(ctx, tx, id)
	if e != nil {
		return e
	}
	w, e := p.loadForward(ctx, tx, plan.Plan.DeclarationID)
	if e != nil {
		return e
	}
	if e = lockStrategyFamily(ctx, tx, w.Declaration.Family); e != nil {
		return e
	}
	if e = p.checkPortfolioAdmissionsTx(ctx, tx, s, c); e != nil {
		return e
	}
	g, e := loadGate(ctx, tx, w.Declaration.GateID)
	if e != nil {
		return e
	}
	n, e := strategySearchTotal(ctx, tx, w.Declaration.Family)
	if errors.Is(e, strategytrial.ErrFamilyPending) {
		n = forward.PendingSearchTotal
	} else if e != nil {
		return e
	}
	c, e = bindCollectionSearchTotal(s, c, n)
	if e != nil {
		return e
	}
	*command = c
	return p.qualificationEvaluator(ctx, collectionPrefix(s, c.ID), clone(c), clone(w), clone(g), *c.Incubation.Qualification.SearchTotal)
}

func collectionPrefix(s account.CappedState, id string) account.CappedState {
	for i, e := range s.Events {
		if e.ID == id {
			s = account.CappedPrefix(s, i)
			break
		}
	}
	return clone(s)
}

func bindCollectionSearchTotal(s account.CappedState, c account.BookCommand, n int64) (account.BookCommand, error) {
	c = clone(c)
	for _, old := range s.Events {
		if old.ID == c.ID {
			if old.Incubation == nil || old.Incubation.Qualification == nil || old.Incubation.Qualification.SearchTotal == nil {
				return c, forward.ErrInvalid
			}
			pinned := *old.Incubation.Qualification.SearchTotal
			if c.Incubation.Qualification.SearchTotal != nil && *c.Incubation.Qualification.SearchTotal != pinned {
				return c, forward.ErrConflict
			}
			c.Incubation.Qualification.SearchTotal = &pinned
			return c, nil
		}
	}
	if c.Incubation.Qualification.SearchTotal != nil {
		return c, forward.ErrInvalid
	}
	c.Incubation.Qualification.SearchTotal = &n
	return c, nil
}

// A retained grant is not authority by shape or hash alone. Recheck its original
// collection and statistical result before qualified allocation or submission.
func validateQualifiedAdmissions(ctx context.Context, s account.CappedState, c account.BookCommand, eval QualificationEvaluator, read func(string) (forward.AssessmentPlanRecord, forward.Record, strategygate.Record, []strategytrial.Start, error)) error {
	needed, e := neededAdmissions(s, c)
	if e != nil {
		return e
	}
	for _, v := range needed {
		if v.Stage != "qualified" {
			continue
		}
		q := account.Qualification(s, v.ID)
		if q == nil || eval == nil {
			return forward.ErrInvalid
		}
		p, w, g, starts, e := read(q.PlanID)
		if e != nil {
			return e
		}
		if validateAssessmentPlan(p, w) != nil || int64(len(starts)) != q.SearchTotal || !sameGate(starts, q.SearchStarts) || q.JournalVersion < 0 || q.JournalVersion > s.Version {
			return forward.ErrInvalid
		}
		prefix, e := account.CappedVersionPrefix(s, q.JournalVersion)
		if e != nil {
			return forward.ErrInvalid
		}
		if e = validateAssessmentCollections(ctx, prefix, q.PlanID, eval, w, g, q.SearchTotal); e != nil {
			return e
		}
		actual := evaluateForward(p, w, prefix, starts, false, q.At)
		if !sameGate(actual, *q) {
			return forward.ErrInvalid
		}
	}
	return nil
}
func loadQualificationStarts(ctx context.Context, q cappedQuery, family string) ([]strategytrial.Start, error) {
	rows, e := q.Query(ctx, "SELECT payload FROM mantik_strategy_trials WHERE family=$1 ORDER BY id", family)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	starts := []strategytrial.Start{}
	for rows.Next() {
		var raw []byte
		var s strategytrial.Start
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s); e != nil {
			return nil, e
		}
		starts = append(starts, s)
	}
	return starts, rows.Err()
}
