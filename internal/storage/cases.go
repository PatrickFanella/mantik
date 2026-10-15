package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

var ErrCaseNotFound = errors.New("case not found")
var errCaseCapacity = errors.New("case catalogue exceeds retained routing capacity")

type CaseReader interface {
	Cases(context.Context) (domain.CaseCatalog, error)
	InspectCase(context.Context, string) (domain.CaseInspection, error)
}

func caseCatalog(f domain.DecisionCase, cases []domain.RoutingCase) (domain.CaseCatalog, error) {
	// Routing retains up to 128 research, 128 decide and 4 act cases.
	if len(cases) > 260 {
		return domain.CaseCatalog{}, errCaseCapacity
	}
	if cases == nil {
		cases = []domain.RoutingCase{}
	}
	slices.SortFunc(cases, func(a, b domain.RoutingCase) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return domain.CaseCatalog{Mode: "paper_fixture", Fixture: f, Routing: cases}, nil
}

func (m *Memory) Cases(ctx context.Context) (domain.CaseCatalog, error) {
	if err := ctx.Err(); err != nil {
		return domain.CaseCatalog{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return caseCatalog(clone(m.state.Case), clone(m.state.Routing.Cases))
}

func (p *Postgres) Cases(ctx context.Context) (domain.CaseCatalog, error) {
	var fraw, craw []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload->'case',payload#>'{routing,cases}' FROM mantik_state WHERE id='demo'").Scan(&fraw, &craw); err != nil {
		return domain.CaseCatalog{}, err
	}
	var f domain.DecisionCase
	var cases []domain.RoutingCase
	if err := json.Unmarshal(fraw, &f); err != nil {
		return domain.CaseCatalog{}, err
	}
	if err := json.Unmarshal(craw, &cases); err != nil {
		return domain.CaseCatalog{}, err
	}
	return caseCatalog(f, cases)
}

func (m *Memory) InspectCase(ctx context.Context, id string) (domain.CaseInspection, error) {
	if err := ctx.Err(); err != nil {
		return domain.CaseInspection{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == m.state.Case.ID {
		return domain.CaseInspection{Mode: "paper_fixture", Kind: "fixture_execution", Fixture: ptrClone(m.state.Case), Strategy: ptrClone(m.state.Strategy), ReferencesAvailable: true}, nil
	}
	for _, c := range m.state.Routing.Cases {
		if c.ID != id {
			continue
		}
		out := domain.CaseInspection{Mode: "paper_fixture", Kind: "routing", Routing: ptrClone(c)}
		if id, ok := strings.CutPrefix(c.LastInputRef, "routing:"); ok {
			if i, ok := m.state.Routing.Inputs[id]; ok {
				out.LastInput = ptrClone(i)
			}
		}
		if id, ok := strings.CutPrefix(c.LastInputRef, "quote:"); ok {
			if raw, ok := m.observations[id]; ok {
				if err := json.Unmarshal(raw, &out.LastQuote); err != nil {
					return out, err
				}
			}
		}
		if id, ok := strings.CutPrefix(c.LastInputRef, "timer:"); ok {
			if job, ok := m.state.Routing.Timers.Jobs[id]; ok {
				out.LastTimer = ptrClone(job)
			}
		}
		for _, d := range m.state.Routing.Definitions {
			if d.ID == c.LastDefinitionID {
				out.LastDefinition = ptrClone(d)
				break
			}
		}
		out.ReferencesAvailable = (out.LastInput != nil || out.LastQuote != nil || out.LastTimer != nil) && out.LastDefinition != nil
		return out, nil
	}
	return domain.CaseInspection{}, ErrCaseNotFound
}

func ptrClone[T any](v T) *T { v = clone(v); return &v }

func (p *Postgres) InspectCase(ctx context.Context, id string) (domain.CaseInspection, error) {
	// A single statement projects only the requested case and its two latest
	// references from one MVCC snapshot. Audit history and other inputs stay out.
	var fraw, sraw, craw, iraw, draw, qraw, traw []byte
	err := p.pool.QueryRow(ctx, `SELECT
 CASE WHEN mantik_state.payload->'case'->>'id'=$1 THEN mantik_state.payload->'case' END,
 CASE WHEN mantik_state.payload->'case'->>'id'=$1 THEN mantik_state.payload->'strategy' END,
 c.value,
 mantik_state.payload#>ARRAY['routing','inputs',CASE WHEN starts_with(c.value->>'last_input_ref','routing:') THEN substr(c.value->>'last_input_ref',9) END],
 (SELECT d.value FROM jsonb_array_elements(mantik_state.payload#>'{routing,definitions}') d WHERE d.value->>'id'=c.value->>'last_definition_id'),
 o.payload,
 mantik_state.payload#>ARRAY['routing','timers','jobs',CASE WHEN starts_with(c.value->>'last_input_ref','timer:') THEN substr(c.value->>'last_input_ref',7) END]
 FROM mantik_state LEFT JOIN LATERAL jsonb_array_elements(mantik_state.payload#>'{routing,cases}') c ON c.value->>'id'=$1
 LEFT JOIN mantik_observations o ON starts_with(c.value->>'last_input_ref','quote:') AND o.id=substr(c.value->>'last_input_ref',7)
 WHERE mantik_state.id='demo'`, id).Scan(&fraw, &sraw, &craw, &iraw, &draw, &qraw, &traw)
	if err != nil {
		return domain.CaseInspection{}, err
	}
	out := domain.CaseInspection{Mode: "paper_fixture"}
	if len(fraw) > 0 {
		out.Kind = "fixture_execution"
		if err := json.Unmarshal(fraw, &out.Fixture); err != nil {
			return out, err
		}
		if err := json.Unmarshal(sraw, &out.Strategy); err != nil {
			return out, err
		}
		out.ReferencesAvailable = true
		return out, nil
	}
	if len(craw) == 0 {
		return out, ErrCaseNotFound
	}
	out.Kind = "routing"
	if err := json.Unmarshal(craw, &out.Routing); err != nil {
		return out, err
	}
	if len(iraw) > 0 {
		if err := json.Unmarshal(iraw, &out.LastInput); err != nil {
			return out, err
		}
	}
	if len(draw) > 0 {
		if err := json.Unmarshal(draw, &out.LastDefinition); err != nil {
			return out, err
		}
	}
	if len(qraw) > 0 {
		if err := json.Unmarshal(qraw, &out.LastQuote); err != nil {
			return out, err
		}
	}
	if len(traw) > 0 {
		if err := json.Unmarshal(traw, &out.LastTimer); err != nil {
			return out, err
		}
	}
	out.ReferencesAvailable = (out.LastInput != nil || out.LastQuote != nil || out.LastTimer != nil) && out.LastDefinition != nil
	return out, nil
}
