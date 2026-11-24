package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/CarlSinclair/csuit-libs/goosedb"
	"github.com/CarlSinclair/csuit-libs/sqlretry"
	"tvt/logger"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const TimeFormat = "2006-01-02 15:04:05"

func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	sqlretry.Logf = logger.Warn
	sqlretry.ErrorLogf = logger.Error
	d, err := sqlretry.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	d.SetMaxOpenConns(8)
	if err := d.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if err := migrate(d); err != nil {
		return nil, err
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	return goosedb.Up(context.Background(), goosedb.Config{
		Name:    "tvt",
		DB:      d,
		Dialect: goosedb.SQLite,
		FS:      migrationFS,
		Dir:     "migrations",
		Logf:    logger.Info,
	})
}
