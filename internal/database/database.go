// Package database owns the SQLite connection, schema migrations, and
// persistence. All storage access flows through this package.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

var memory_seq uint64

// Database wraps an open SQLite handle and records applied migrations.
type Database struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at dsn_path and
// applies any pending migrations. Use dsn_path == ":memory:" for tests.
func Open(dsn_path string) (*Database, error) {
	dsn := build_dsn(dsn_path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	d := &Database{db: db}
	if err := d.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the underlying database handle.
func (d *Database) Close() error {
	return d.db.Close()
}

// Sql exposes the underlying handle for repository code in this package.
func (d *Database) Sql() *sql.DB {
	return d.db
}

// Ping verifies the connection is alive.
func (d *Database) Ping(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

// Migrations_applied returns the versions currently recorded as applied.
func (d *Database) Migrations_applied() ([]int, error) {
	rows, err := d.db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// migrate applies embedded migration SQL files in numeric order, each within
// its own transaction, recording the version in schema_migrations.
func (d *Database) migrate() error {
	if _, err := d.db.Exec(schema_migrations_table()); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := d.Migrations_applied()
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	have := make(map[int]bool, len(applied))
	for _, v := range applied {
		have[v] = true
	}
	for _, migration := range migrations_available() {
		if have[migration.version] {
			continue
		}
		if err := d.apply_migration(migration); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) apply_migration(m migration) error {
	sql, err := read_migration(m)
	if err != nil {
		return err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(sql); err != nil {
		return fmt.Errorf("apply migration %d: %w", m.version, err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.version, err)
	}
	return nil
}

type migration struct {
	version  int
	filename string
}

//go:embed migrations/*.sql
var migrations_fs embed.FS

func migrations_available() []migration {
	entries, err := fs.ReadDir(migrations_fs, "migrations")
	if err != nil {
		return nil
	}
	var migrations_list []migration
	for _, e := range entries {
		name := e.Name()
		version, ok := migration_version(name)
		if !ok {
			continue
		}
		migrations_list = append(migrations_list, migration{version: version, filename: name})
	}
	sort.Slice(migrations_list, func(i, j int) bool {
		return migrations_list[i].version < migrations_list[j].version
	})
	return migrations_list
}

// migration_version extracts the leading numeric version from a migration
// filename such as "0001_initial.sql" or "0002.sql".
func migration_version(name string) (int, bool) {
	if path.Ext(name) != ".sql" {
		return 0, false
	}
	base := strings.TrimSuffix(name, path.Ext(name))
	if i := strings.IndexByte(base, '_'); i >= 0 {
		base = base[:i]
	}
	version, err := strconv.Atoi(base)
	if err != nil {
		return 0, false
	}
	return version, true
}

func read_migration(m migration) (string, error) {
	data, err := fs.ReadFile(migrations_fs, path.Join("migrations", m.filename))
	if err != nil {
		return "", fmt.Errorf("read migration %d: %w", m.version, err)
	}
	return string(data), nil
}

func schema_migrations_table() string {
	return `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
}

// build_dsn turns a path into a modernc.org/sqlite DSN carrying the pragmas we
// rely on (foreign key enforcement, WAL journal, busy timeout). Each pragma is
// its own _pragma query parameter. In-memory databases get a unique shared
// name so concurrent opens do not collide.
func build_dsn(dsn_path string) string {
	if dsn_path == ":memory:" {
		name := fmt.Sprintf("file:tomovee_mem_%d_%d", os.Getpid(), atomic.AddUint64(&memory_seq, 1))
		return name + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"
	}
	plain_path := strings.TrimPrefix(dsn_path, "file:")
	return "file:" + url.PathEscape(plain_path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
}
