package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is the SQLite-backed ports.Store.
type DB struct{ db *sql.DB }

var _ ports.Store = (*DB)(nil)

// Open opens <home>/ferry.db and runs the migrations.
func Open(path string) (ports.Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := ensureDir(dir); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// SQLite takes a single writer; one connection keeps writes serialised and
	// the per-connection pragmas above identical everywhere.
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := migrate(sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := restrict(path); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &DB{db: sqlDB}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// migrate applies every migration whose number is above the recorded schema
// version. Running it again on the same database is a no-op.
func migrate(db *sql.DB) error {
	current, err := schemaVersion(db)
	if err != nil {
		return err
	}
	files, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.version <= current {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + f.name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", f.name, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", f.name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %s: %w", f.name, err)
		}
		if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %s: %w", f.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, f.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %s: %w", f.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %s: %w", f.name, err)
		}
		current = f.version
	}
	return nil
}

// schemaVersion returns 0 for a database the migrations have never touched.
func schemaVersion(db *sql.DB) (int, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	var version sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return int(version.Int64), nil
}

type migrationFile struct {
	version int
	name    string
}

// migrationFiles lists the embedded migrations in version order. Names look
// like "001_init.sql".
func migrationFiles() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	out := make([]migrationFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q has no version prefix", e.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q has no version prefix: %w", e.Name(), err)
		}
		out = append(out, migrationFile{version: version, name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// ---------- KV ----------

func (d *DB) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: kv %q: %w", key, fsp.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: kv %q: %w", key, err)
	}
	return v, nil
}

func (d *DB) SetKV(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, value)
	if err != nil {
		return fmt.Errorf("store: set kv %q: %w", key, err)
	}
	return nil
}

// ---------- time ----------

// millis stores times as Unix milliseconds; the zero time becomes 0 so that it
// survives the round trip through columns that default to 0.
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
