package storage

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"github.com/jackc/pgx/v5"
)

var ErrReceiverMismatch = errors.New("research receiver identity mismatch")
var receiverPattern = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)

func ValidReceiverID(id string) bool { return receiverPattern.MatchString(id) }

type ResearchSnapshot = domain.ResearchSnapshot
type ResearchReceiverStore interface {
	ResearchSnapshot(context.Context) (ResearchSnapshot, error)
	SubmitResearch(context.Context, string, domain.RoutingInput) (domain.RoutingReceipt, error)
}

func (m *Memory) ResearchSnapshot(ctx context.Context) (ResearchSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ResearchSnapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return ResearchSnapshot{ID: m.receiverID, Inputs: clone(m.state.Routing.Inputs)}, nil
}
func (p *Postgres) ResearchSnapshot(ctx context.Context) (ResearchSnapshot, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ResearchSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var out ResearchSnapshot
	if err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&out.ID); err != nil {
		return out, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return out, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return out, err
	}
	out.Inputs = state.Routing.Inputs
	return out, tx.Commit(ctx)
}
func validateResearch(receiver string, i domain.RoutingInput) error {
	if !ValidReceiverID(receiver) || i.Source == "fixture" {
		return errors.Join(routing.ErrInvalid, errors.New("research submission requires a receiver identity and attributed market input"))
	}
	if err := routing.ValidateInput(i); err != nil {
		return errors.Join(routing.ErrInvalid, err)
	}
	return nil
}
func (m *Memory) SubmitResearch(ctx context.Context, receiver string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	if err := validateResearch(receiver, i); err != nil {
		return domain.RoutingReceipt{}, err
	}
	return m.routeInput(ctx, receiver, i)
}
func (p *Postgres) SubmitResearch(ctx context.Context, receiver string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	if err := validateResearch(receiver, i); err != nil {
		return domain.RoutingReceipt{}, err
	}
	return p.routeInput(ctx, receiver, i)
}
