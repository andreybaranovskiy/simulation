// Package db owns the MySQL connection pool and the migration runner.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/andreybaranovskiy/simulation/internal/config"
)

// MySQL error numbers we translate into domain errors rather than leaking.
const (
	errDupEntry        = 1062
	errNoReferencedRow = 1452
	errRowIsReferenced = 1451
)

// ErrDuplicate is returned when a unique constraint rejects a write, e.g. a
// second account with the same email.
var ErrDuplicate = errors.New("duplicate key")

// ErrForeignKey is returned when a write references a row that does not exist,
// or deletes a row something else still points at.
var ErrForeignKey = errors.New("foreign key constraint")

// DB wraps *sql.DB with the helpers the stores share.
type DB struct {
	*sql.DB
}

// Open connects, verifies the server is reachable and applies the pool limits.
func Open(ctx context.Context, cfg config.Database) (*DB, error) {
	sqlDB, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("connect to %s: %w", cfg.RedactedDSN(), err)
	}

	return &DB{DB: sqlDB}, nil
}

// InTx runs fn inside a transaction, rolling back on error or panic.
func (d *DB) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Translate converts driver-specific constraint violations into the sentinel
// errors above so callers do not have to know MySQL error numbers.
func Translate(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case errDupEntry:
			return fmt.Errorf("%w: %s", ErrDuplicate, me.Message)
		case errNoReferencedRow, errRowIsReferenced:
			return fmt.Errorf("%w: %s", ErrForeignKey, me.Message)
		}
	}
	return err
}
