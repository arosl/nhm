package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arosl/nhm/internal/store/sqliteutil"
)

// ErrSchemaTooNew means a newer nhm release wrote the database. This
// release must not read or write it (plan-v8 section 4, rollback).
var ErrSchemaTooNew = errors.New("store: the database schema is newer than this nhm")

// DB is an instance's nhm.sqlite3.
type DB struct {
	sql  *sql.DB
	path string
}

// TimeFormat is how timestamps are stored: RFC 3339 in UTC.
const TimeFormat = time.RFC3339Nano

// FormatTime renders t for a TEXT column.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Open opens (and creates or migrates) the database in an instance
// directory. It uses WAL mode, a five-second busy timeout, foreign keys
// and immediate transactions, so a read-check-write cannot interleave with
// another writer.
func Open(instanceDir string) (*DB, error) {
	path := filepath.Join(instanceDir, DatabaseFile)
	dsn := sqliteutil.ImmediateTransactionFileDSN(path,
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
	)
	sdb, err := sql.Open(sqliteutil.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db := &DB{sql: sdb, path: path}
	if err := db.migrate(context.Background()); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.sql.Close() }

// SQL returns the handle for the packages that own the tables. No
// transaction may stay open across a herdr or Agent Mail call.
func (db *DB) SQL() *sql.DB { return db.sql }

// UserVersion returns the schema version stored in the file.
func (db *DB) UserVersion(ctx context.Context) (int, error) {
	var v int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: user_version: %w", err)
	}
	return v, nil
}

func (db *DB) migrate(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migrate %s: %w", db.path, err)
	}
	defer func() { _ = tx.Rollback() }()
	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: migrate %s: %w", db.path, err)
	}
	if v > len(migrations) {
		return fmt.Errorf("%w: %s has user_version %d, this nhm knows %d", ErrSchemaTooNew, db.path, v, len(migrations))
	}
	if v == len(migrations) {
		return nil
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("store: migrate %s to %d: %w", db.path, i+1, err)
		}
	}
	// PRAGMA takes no bound parameters; the value is an int we control.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return fmt.Errorf("store: migrate %s: %w", db.path, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate %s: %w", db.path, err)
	}
	return nil
}

func (db *DB) setMeta(key, value string) error {
	_, err := db.sql.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: meta %s: %w", key, err)
	}
	return nil
}

// Meta returns a meta value, or "" when the key is absent.
func (db *DB) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: meta %s: %w", key, err)
	}
	return v, nil
}
