package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockKey int64 = 917_263_541

type migration struct {
	version int64
	name    string
	sql     string
}

// Migrate applies pending migrations under an advisory lock. It refuses to run
// when the database was migrated by a newer binary, which is the compatibility
// gate required before an application rollback.
func (db *DB) Migrate(ctx context.Context) ([]int64, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrationLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		create table if not exists schema_migrations (
			version bigint primary key,
			name text not null,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	rows, err := conn.Query(ctx, "select version from schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := map[int64]bool{}
	var maxApplied int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
		if v > maxApplied {
			maxApplied = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var maxKnown int64
	for _, m := range migrations {
		if m.version > maxKnown {
			maxKnown = m.version
		}
	}
	if maxApplied > maxKnown {
		return nil, fmt.Errorf("database schema version %d is newer than this binary (max %d); upgrade the application image or restore a compatible backup", maxApplied, maxKnown)
	}
	var ran []int64
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return ran, fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, "insert into schema_migrations (version, name) values ($1, $2)", m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ran, fmt.Errorf("commit migration %d: %w", m.version, err)
		}
		ran = append(ran, m.version)
	}
	return ran, nil
}

// SchemaVersion returns the highest applied migration.
func (db *DB) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	err := db.Pool.QueryRow(ctx, `select coalesce(max(version), 0) from schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// BinarySchemaVersion reports the newest migration embedded in this binary.
func BinarySchemaVersion() (int64, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	var max int64
	for _, m := range migrations {
		if m.version > max {
			max = m.version
		}
	}
	return max, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("migration file %q must start with a numeric version", entry.Name())
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration file %q: %w", entry.Name(), err)
		}
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: strings.TrimSuffix(parts[1], ".sql"), sql: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
