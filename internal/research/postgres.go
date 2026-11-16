package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresJournal retains immutable private evidence. It has no money tables.
type PostgresJournal struct{ pool *pgxpool.Pool }

// OpenPostgresJournal opens the desk journal in an explicit disposable/bootstrap
// database after storage.OpenPostgres has installed the bootstrap migrations.
func OpenPostgresJournal(ctx context.Context, dsn string) (*PostgresJournal, error) {
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("invalid desk database URL")
	}
	// Match the existing storage owner's database guard, without echoing secrets.
	name := cfg.ConnConfig.Database
	if name != "mantik" && (len(name) < 12 || name[:12] != "mantik_test_") {
		return nil, errors.New("desk requires mantik or mantik_test_* database")
	}
	cfg.MaxConns = 2
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, errors.New("cannot open desk journal")
	}
	var installed bool
	if e = p.QueryRow(ctx, "SELECT to_regclass('public.mantik_research_desk_events') IS NOT NULL").Scan(&installed); e != nil || !installed {
		p.Close()
		return nil, errors.New("research desk requires initialized bootstrap storage")
	}
	return &PostgresJournal{pool: p}, nil
}

// Close releases the journal connections, never removes retained evidence.
func (j *PostgresJournal) Close() { j.pool.Close() }

// Lock acquires a nonblocking process-wide session fence held on one connection.
func (j *PostgresJournal) Lock(ctx context.Context, id string) (func(), error) {
	c, e := j.pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256([]byte("mantik-research-desk/" + id))
	key := int64(binary.BigEndian.Uint64(h[:8]))
	var ok bool
	if e = c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); e != nil || !ok {
		c.Release()
		return nil, errors.New("desk session busy")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := c.Exec(cleanup, "SELECT pg_advisory_unlock($1)", key); e != nil { // An uncertain unlock must not return a locked connection to the pool.
			_ = c.Conn().Close(cleanup)
		}
		c.Release()
	}, nil
}

// Events reads retained events in sequence order and canonicalizes JSON bodies.
func (j *PostgresJournal) Events(ctx context.Context, id string) ([]Event, error) {
	rows, e := j.pool.Query(ctx, `SELECT sequence,kind,at,body FROM mantik_research_desk_events WHERE session_id=$1 ORDER BY sequence`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var seq int
		var v Event
		if e = rows.Scan(&seq, &v.Kind, &v.At, &v.Body); e != nil {
			return nil, e
		}
		if seq != len(out) {
			return nil, ErrConflict
		}
		v.At = v.At.UTC()
		v.Body = canonical(v.Body)
		out = append(out, v)
	}
	return out, rows.Err()
}
func canonical(raw []byte) []byte {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil
	}
	return marshal(v)
}

// Append is idempotent for exact events and refuses changed or out-of-order writes.
func (j *PostgresJournal) Append(ctx context.Context, id string, seq int, v Event) error {
	raw := canonical(v.Body)
	if len(raw) == 0 {
		return ErrInvalid
	}
	_, e := j.pool.Exec(ctx, `INSERT INTO mantik_research_desk_events(session_id,sequence,kind,at,body)
 SELECT $1,$2::integer,$3,$4,$5 WHERE $2::integer=(SELECT count(*) FROM mantik_research_desk_events WHERE session_id=$1)
 ON CONFLICT DO NOTHING`, id, seq, v.Kind, v.At, string(raw))
	if e != nil {
		return e
	}
	var kind string
	var at time.Time
	var body []byte
	e = j.pool.QueryRow(ctx, `SELECT kind,at,body FROM mantik_research_desk_events WHERE session_id=$1 AND sequence=$2`, id, seq).Scan(&kind, &at, &body)
	if e != nil || kind != v.Kind || !at.Equal(v.At.UTC().Truncate(time.Microsecond)) || string(canonical(body)) != string(raw) {
		return ErrConflict
	}
	return nil
}
