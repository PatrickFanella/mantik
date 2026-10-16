package storage

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"github.com/jackc/pgx/v5"
)

type NewsCatalog struct {
	ReceiverID string          `json:"receiver_id"`
	Schema     string          `json:"schema"`
	Policy     string          `json:"policy"`
	Cases      []newscase.Case `json:"cases"`
}
type NewsStore interface {
	NewsCases(context.Context) (NewsCatalog, error)
	NewsRecord(context.Context, string) (newscase.Record, error)
	AdmitNews(context.Context, string, news.Recording, func() time.Time) (newscase.Receipt, error)
	CloseNewsCase(context.Context, string, string, int, func() time.Time) (newscase.Case, error)
}

func catalog(receiver string, cases map[string]newscase.Case) NewsCatalog {
	out := NewsCatalog{ReceiverID: receiver, Schema: newscase.Schema, Policy: newscase.Policy, Cases: []newscase.Case{}}
	for _, c := range cases {
		out.Cases = append(out.Cases, c)
	}
	slices.SortFunc(out.Cases, func(a, b newscase.Case) int { return strings.Compare(a.ID, b.ID) })
	return out
}
func (m *Memory) NewsCases(ctx context.Context) (NewsCatalog, error) {
	if err := ctx.Err(); err != nil {
		return NewsCatalog{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(catalog(m.receiverID, m.news.Cases)), nil
}
func (m *Memory) NewsRecord(ctx context.Context, key string) (newscase.Record, error) {
	if err := ctx.Err(); err != nil {
		return newscase.Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.news.Records[key]
	if !ok {
		return newscase.Record{}, newscase.ErrNotFound
	}
	return clone(r), nil
}
func (m *Memory) AdmitNews(ctx context.Context, receiver string, r news.Recording, now func() time.Time) (newscase.Receipt, error) {
	if !news.OperatorRecording(r) {
		return newscase.Receipt{}, newscase.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return newscase.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return newscase.Receipt{}, ErrReceiverMismatch
	}
	next, receipt, err := newscase.Admit(m.news, r, now())
	if err == nil {
		m.news = next
	}
	return receipt, err
}
func (m *Memory) CloseNewsCase(ctx context.Context, receiver, id string, version int, now func() time.Time) (newscase.Case, error) {
	if err := ctx.Err(); err != nil {
		return newscase.Case{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(receiver) || receiver != m.receiverID {
		return newscase.Case{}, ErrReceiverMismatch
	}
	next, c, err := newscase.Close(m.news, id, version, now())
	if err == nil {
		m.news = next
	}
	return c, err
}
func (p *Postgres) NewsCases(ctx context.Context) (NewsCatalog, error) {
	var receiver string
	var raw []byte
	// Both values come from one statement snapshot; originals are not selected.
	if err := p.pool.QueryRow(ctx, "SELECT r.id,n.payload->'cases' FROM mantik_research_receiver r CROSS JOIN mantik_news_cases n WHERE r.singleton AND n.singleton").Scan(&receiver, &raw); err != nil {
		return NewsCatalog{}, err
	}
	var cases map[string]newscase.Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		return NewsCatalog{}, err
	}
	return catalog(receiver, cases), nil
}
func (p *Postgres) NewsRecord(ctx context.Context, key string) (newscase.Record, error) {
	var raw []byte
	var r newscase.Record
	err := p.pool.QueryRow(ctx, "SELECT payload->'records'->$1 FROM mantik_news_cases WHERE singleton AND payload->'records' ? $1", key).Scan(&raw)
	if err == pgx.ErrNoRows {
		return r, newscase.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (p *Postgres) newsMutation(ctx context.Context, receiver string, apply func(newscase.State) (newscase.State, any, error)) (any, error) {
	if !ValidReceiverID(receiver) {
		return nil, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&expected); err != nil {
		return nil, err
	}
	if expected != receiver {
		return nil, ErrReceiverMismatch
	}
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return nil, err
	}
	var state newscase.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	next, value, err := apply(state)
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE mantik_news_cases SET payload=$1 WHERE singleton", raw); err != nil {
		return nil, err
	}
	return value, tx.Commit(ctx)
}
func (p *Postgres) AdmitNews(ctx context.Context, receiver string, r news.Recording, now func() time.Time) (newscase.Receipt, error) {
	if !news.OperatorRecording(r) {
		return newscase.Receipt{}, newscase.ErrInvalid
	}
	value, err := p.newsMutation(ctx, receiver, func(s newscase.State) (newscase.State, any, error) { return newscase.Admit(s, r, now()) })
	if err != nil {
		return newscase.Receipt{}, err
	}
	return value.(newscase.Receipt), nil
}
func (p *Postgres) CloseNewsCase(ctx context.Context, receiver, id string, version int, now func() time.Time) (newscase.Case, error) {
	value, err := p.newsMutation(ctx, receiver, func(s newscase.State) (newscase.State, any, error) { return newscase.Close(s, id, version, now()) })
	if err != nil {
		return newscase.Case{}, err
	}
	return value.(newscase.Case), nil
}

var _ NewsStore = (*Memory)(nil)
var _ NewsStore = (*Postgres)(nil)
