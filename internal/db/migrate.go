package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migrations are embedded so a single binary carries its own schema. Files are
// named NNNN_description.sql and applied in numeric order, exactly once each.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	Version int
	Name    string
	SQL     string
	Hash    string
}

// Migrate applies every migration not yet recorded in schema_migrations.
// Already-applied files are checksummed: editing a shipped migration is a
// deployment hazard, so it fails loudly instead of drifting silently.
func (d *DB) Migrate(ctx context.Context, log *slog.Logger) error {
	if err := d.ensureMigrationTable(ctx); err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := d.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	var pending []migration
	for _, m := range all {
		prev, ok := applied[m.Version]
		if !ok {
			pending = append(pending, m)
			continue
		}
		if prev != m.Hash {
			return fmt.Errorf("migration %04d_%s changed after it was applied (recorded %s, found %s); "+
				"add a new migration instead of editing an applied one", m.Version, m.Name, prev[:12], m.Hash[:12])
		}
	}

	if len(pending) == 0 {
		log.Debug("schema is up to date", "applied", len(applied))
		return nil
	}

	for _, m := range pending {
		start := time.Now()
		log.Info("applying migration", "version", m.Version, "name", m.Name)

		// DDL in MySQL is not transactional, so a failed migration can leave
		// the schema half-applied. Each file is kept small and idempotent-ish
		// for that reason, and the version row is only written on success.
		if _, err := d.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("migration %04d_%s failed: %w", m.Version, m.Name, err)
		}

		_, err := d.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at, duration_ms)
			 VALUES (?, ?, ?, ?, ?)`,
			m.Version, m.Name, m.Hash, time.Now().UTC(), time.Since(start).Milliseconds())
		if err != nil {
			return fmt.Errorf("record migration %04d: %w", m.Version, err)
		}

		log.Info("migration applied", "version", m.Version, "took", time.Since(start))
	}
	return nil
}

func (d *DB) ensureMigrationTable(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INT          NOT NULL,
    name        VARCHAR(160) NOT NULL,
    checksum    CHAR(64)     NOT NULL,
    applied_at  DATETIME(3)  NOT NULL,
    duration_ms BIGINT       NOT NULL DEFAULT 0,
    PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

	if _, err := d.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (d *DB) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		out[version] = checksum
	}
	return out, rows.Err()
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var out []migration
	seen := make(map[int]string)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}

		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", version, other, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)

		out = append(out, migration{
			Version: version,
			Name:    name,
			SQL:     string(body),
			Hash:    hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, rest, found := strings.Cut(base, "_")
	if !found {
		return 0, "", fmt.Errorf("migration %q must be named NNNN_description.sql", filename)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("migration %q has a non-numeric version prefix", filename)
	}
	return version, rest, nil
}
