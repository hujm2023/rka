// Package store owns the PostgreSQL connection, transactions and migrations.
//
// Conventions used across the codebase:
//   - uuid columns are read as `id::text` and written as `$1::uuid`, so Go code
//     never depends on a uuid driver type;
//   - timestamps are UTC and always come from the database (`select now()`),
//     except client-supplied event times such as an answer timestamp;
//   - every mutation that spans tables runs inside Tx.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

func Open(ctx context.Context, dsn string, log *slog.Logger) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	db := &DB{Pool: pool, Log: log}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func (db *DB) Close() {
	if db != nil && db.Pool != nil {
		db.Pool.Close()
	}
}

func (db *DB) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.Pool.Ping(ctx)
}

// Now returns the authoritative server time from PostgreSQL.
func (db *DB) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := db.Pool.QueryRow(ctx, "select now()").Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("server time: %w", err)
	}
	return now, nil
}

// Tx runs fn inside a transaction, committing on nil error and rolling back
// otherwise. The callback must not retain the transaction.
func (db *DB) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ErrNotFound is returned by repositories when a row is missing.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a compare-and-set style write loses a race.
var ErrConflict = errors.New("conflict")

// IsUniqueViolation reports whether err is a PostgreSQL unique constraint error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsForeignKeyViolation reports whether err is a PostgreSQL FK error.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// MapError converts a driver error into a domain error callers can match.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
