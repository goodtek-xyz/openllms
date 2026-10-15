package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/goodtekxyz/openllms/internal/backoff"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dialect selects SQL adaptations (placeholders, casts, dialect-only queries).
type Dialect int

const (
	DialectPostgres Dialect = iota
	DialectSQLite
)

// Result is a minimal Exec outcome (rows affected).
type Result interface {
	RowsAffected() int64
}

// Rows iterates query results.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// Row is a single-row query result.
type Row interface {
	Scan(dest ...any) error
}

// Tx is a transaction handle.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (Result, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// DB is the store's database handle (Postgres via pgx, or SQLite via database/sql).
type DB interface {
	Dialect() Dialect
	Exec(ctx context.Context, sql string, args ...any) (Result, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Begin(ctx context.Context) (Tx, error)
	Ping(ctx context.Context) error
	Close()
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows)
}

type pgxResult struct{ tag pgconn.CommandTag }

func (r pgxResult) RowsAffected() int64 { return r.tag.RowsAffected() }

type pgxRows struct{ rows pgx.Rows }

func (r *pgxRows) Next() bool             { return r.rows.Next() }
func (r *pgxRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r *pgxRows) Close()                 { r.rows.Close() }
func (r *pgxRows) Err() error             { return r.rows.Err() }

type pgxDB struct{ pool *pgxpool.Pool }

func newPGX(pool *pgxpool.Pool) DB { return &pgxDB{pool: pool} }

func (d *pgxDB) Dialect() Dialect { return DialectPostgres }

// Retrying single statements across a Postgres primary switchover or failover, which leaves a few seconds without a
// writable primary (HA setups, managed databases, operator-driven updates). Only errors that prove the statement had
// no effect are retried:
//   - pgconn.SafeToRetry: the error happened before the statement was sent (pgx's documented retry signal);
//   - SQLSTATE class 08 (connection exception), 57P01/57P02/57P03 (server shutting down / not accepting yet): the
//     server ended the session, so an uncommitted single statement was rolled back;
//   - 25006 (read-only transaction): a write reached the old primary after it became a replica and was refused.
//
// Transactions (Begin) are not retried here: their callers own the unit of work. Backoff 100 ms doubling to 2 s with
// full jitter, for at most retryBudget (longer than a typical switchover) or until ctx ends.
const retryBudget = 15 * time.Second

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if pgconn.SafeToRetry(err) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case len(pgErr.Code) == 5 && pgErr.Code[:2] == "08",
			pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03", pgErr.Code == "25006":
			return true
		}
		return false
	}
	var connErr *pgconn.ConnectError
	return errors.As(err, &connErr)
}

func withRetry[T any](ctx context.Context, op func() (T, error)) (T, error) {
	deadline := time.Now().Add(retryBudget)
	b := backoff.Backoff{Base: 100 * time.Millisecond, Cap: 2 * time.Second}
	for {
		v, err := op()
		if !retryable(err) {
			return v, err
		}
		pause := b.Next()
		if time.Now().Add(pause).After(deadline) || backoff.Sleep(ctx, pause) != nil {
			return v, err
		}
	}
}

func (d *pgxDB) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	tag, err := withRetry(ctx, func() (pgconn.CommandTag, error) { return d.pool.Exec(ctx, sql, args...) })
	if err != nil {
		return nil, err
	}
	return pgxResult{tag: tag}, nil
}

func (d *pgxDB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := withRetry(ctx, func() (pgx.Rows, error) {
		r, err := d.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		// pgx reports some send/connect errors only once the result is read; surface them here so they are retried
		if !r.Next() {
			if rerr := r.Err(); rerr != nil {
				r.Close()
				return nil, rerr
			}
			r.Close()
			return emptyRows{}, nil
		}
		return &peekedRows{rows: r}, nil
	})
	if err != nil {
		return nil, err
	}
	return &pgxRows{rows: rows}, nil
}

func (d *pgxDB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return retryRow{ctx: ctx, run: func(dest ...any) error { return d.pool.QueryRow(ctx, sql, args...).Scan(dest...) }}
}

func (d *pgxDB) Begin(ctx context.Context) (Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgxTx{tx: tx}, nil
}

func (d *pgxDB) Ping(ctx context.Context) error {
	_, err := withRetry(ctx, func() (struct{}, error) { return struct{}{}, d.pool.Ping(ctx) })
	return err
}

// retryRow runs the query when scanned, so a connection error surfaced by Scan can be retried as a whole.
type retryRow struct {
	ctx context.Context
	run func(dest ...any) error
}

func (r retryRow) Scan(dest ...any) error {
	_, err := withRetry(r.ctx, func() (struct{}, error) { return struct{}{}, r.run(dest...) })
	return err
}

// peekedRows hands back the first row that Query read while checking for an error, then continues.
type peekedRows struct {
	rows   pgx.Rows
	peeked bool
}

func (p *peekedRows) Next() bool {
	if !p.peeked {
		p.peeked = true
		return true
	}
	return p.rows.Next()
}
func (p *peekedRows) Scan(dest ...any) error                       { return p.rows.Scan(dest...) }
func (p *peekedRows) Close()                                       { p.rows.Close() }
func (p *peekedRows) Err() error                                   { return p.rows.Err() }
func (p *peekedRows) CommandTag() pgconn.CommandTag                { return p.rows.CommandTag() }
func (p *peekedRows) FieldDescriptions() []pgconn.FieldDescription { return p.rows.FieldDescriptions() }
func (p *peekedRows) Values() ([]any, error)                       { return p.rows.Values() }
func (p *peekedRows) RawValues() [][]byte                          { return p.rows.RawValues() }
func (p *peekedRows) Conn() *pgx.Conn                              { return p.rows.Conn() }

// emptyRows is a finished result with no rows.
type emptyRows struct{}

func (emptyRows) Next() bool                                   { return false }
func (emptyRows) Scan(...any) error                            { return pgx.ErrNoRows }
func (emptyRows) Close()                                       {}
func (emptyRows) Err() error                                   { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRows) RawValues() [][]byte                          { return nil }
func (emptyRows) Conn() *pgx.Conn                              { return nil }
func (d *pgxDB) Close()                                        { d.pool.Close() }

type pgxTx struct{ tx pgx.Tx }

func (t *pgxTx) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxResult{tag: tag}, nil
}

func (t *pgxTx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRows{rows: rows}, nil
}

func (t *pgxTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t *pgxTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgxTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

var (
	reDollar   = regexp.MustCompile(`\$(\d+)`)
	reCast     = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_]*`)
	reNow      = regexp.MustCompile(`(?i)\bnow\s*\(\s*\)`)
	reGreatest = regexp.MustCompile(`(?i)\bgreatest\s*\(`)
	reLeast    = regexp.MustCompile(`(?i)\bleast\s*\(`)
)

func adaptSQLite(query string) string {
	q := reCast.ReplaceAllString(query, "")
	q = reNow.ReplaceAllString(q, "CURRENT_TIMESTAMP")
	q = reGreatest.ReplaceAllString(q, "max(")
	q = reLeast.ReplaceAllString(q, "min(")
	// $1 → ?1 (SQLite numbered params preserve argument mapping).
	return reDollar.ReplaceAllString(q, "?$1")
}

type sqlResult struct{ n int64 }

func (r sqlResult) RowsAffected() int64 { return r.n }

type sqlRows struct{ rows *sql.Rows }

func (r *sqlRows) Next() bool { return r.rows.Next() }
func (r *sqlRows) Scan(dest ...any) error {
	return r.rows.Scan(wrapTimeScanners(dest)...)
}
func (r *sqlRows) Close()     { _ = r.rows.Close() }
func (r *sqlRows) Err() error { return r.rows.Err() }

type sqlRow struct{ row *sql.Row }

func (r sqlRow) Scan(dest ...any) error {
	return r.row.Scan(wrapTimeScanners(dest)...)
}

// wrapTimeScanners lets SQLite TEXT timestamps scan into time.Time / *time.Time.
func wrapTimeScanners(dest []any) []any {
	out := make([]any, len(dest))
	for i, d := range dest {
		switch d.(type) {
		case *time.Time, **time.Time:
			out[i] = &flexibleTime{dest: d}
		default:
			out[i] = d
		}
	}
	return out
}

type flexibleTime struct{ dest any }

func (f *flexibleTime) Scan(src any) error {
	var t time.Time
	var ok bool
	switch v := src.(type) {
	case nil:
		return f.assign(time.Time{}, true)
	case time.Time:
		t, ok = v, true
	case string:
		t, ok = parseSQLiteTime(v)
	case []byte:
		t, ok = parseSQLiteTime(string(v))
	default:
		return fmt.Errorf("unsupported time type %T", src)
	}
	if !ok {
		return fmt.Errorf("cannot parse time %v", src)
	}
	return f.assign(t, false)
}

func (f *flexibleTime) assign(t time.Time, null bool) error {
	switch d := f.dest.(type) {
	case *time.Time:
		if null {
			*d = time.Time{}
			return nil
		}
		*d = t
		return nil
	case **time.Time:
		if null {
			*d = nil
			return nil
		}
		tt := t
		*d = &tt
		return nil
	default:
		return fmt.Errorf("unexpected time dest %T", f.dest)
	}
}

func parseSQLiteTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
		// CURRENT_TIMESTAMP is UTC-naive; treat as UTC.
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type sqliteDB struct{ db *sql.DB }

// NewSQLiteDB wraps a database/sql handle opened for SQLite.
func NewSQLiteDB(db *sql.DB) DB {
	return &sqliteDB{db: db}
}

func (d *sqliteDB) Dialect() Dialect { return DialectSQLite }

func (d *sqliteDB) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	res, err := d.db.ExecContext(ctx, adaptSQLite(query), args...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return sqlResult{n: n}, nil
}

func (d *sqliteDB) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := d.db.QueryContext(ctx, adaptSQLite(query), args...)
	if err != nil {
		return nil, err
	}
	return &sqlRows{rows: rows}, nil
}

func (d *sqliteDB) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlRow{row: d.db.QueryRowContext(ctx, adaptSQLite(query), args...)}
}

func (d *sqliteDB) Begin(ctx context.Context) (Tx, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: tx}, nil
}

func (d *sqliteDB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }
func (d *sqliteDB) Close()                         { _ = d.db.Close() }

type sqliteTx struct{ tx *sql.Tx }

func (t *sqliteTx) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	res, err := t.tx.ExecContext(ctx, adaptSQLite(query), args...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return sqlResult{n: n}, nil
}

func (t *sqliteTx) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := t.tx.QueryContext(ctx, adaptSQLite(query), args...)
	if err != nil {
		return nil, err
	}
	return &sqlRows{rows: rows}, nil
}

func (t *sqliteTx) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlRow{row: t.tx.QueryRowContext(ctx, adaptSQLite(query), args...)}
}

func (t *sqliteTx) Commit(context.Context) error   { return t.tx.Commit() }
func (t *sqliteTx) Rollback(context.Context) error { return t.tx.Rollback() }
