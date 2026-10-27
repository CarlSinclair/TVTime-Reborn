// Package sqlretry opens SQLite databases through a thin driver wrapper that
// retries SQLITE_BUSY / SQLITE_LOCKED with backoff, so contention is absorbed
// instead of surfacing as a failed request, and never gives up on its own. busy_timeout (set in the DSN)
// still does the first round of waiting inside SQLite; this layer covers what
// it cannot: a locked database at open/prepare/begin time, and write-lock
// upgrades that SQLite refuses without waiting. Statements inside an open
// transaction are never retried (the transaction may be unusable after a busy
// error), they surface to the caller as before. This file is identical in
// every product.
package sqlretry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"

	sqlitedrv "modernc.org/sqlite"
)

// Logf receives one line per retry. ErrorLogf receives the alert-level line
// once a call has been busy for alertAfter attempts and then every alertAfter
// attempts while it keeps failing; each product points it at its error
// logger, which is what reaches the operator. Both default to discarding.
var (
	Logf      = func(format string, args ...any) {}
	ErrorLogf = func(format string, args ...any) {}
)

const (
	alertAfter   = 10
	firstBackoff = 20 * time.Millisecond
	maxBackoff   = 500 * time.Millisecond
)

// IsBusy reports whether err is transient SQLite contention.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	var se *sqlitedrv.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xFF {
		case 5, 6:
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "sqlite_busy")
}

// do retries fn for as long as it fails with a busy error: contention is
// never a permanent failure. Only a cancelled context or a non-busy error
// ends the loop.
func do(ctx context.Context, what string, fn func() error) error {
	backoff := firstBackoff
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			if attempt > 1 {
				Logf("sqlite %s succeeded after %d attempts", what, attempt)
			}
			return nil
		}
		if !IsBusy(err) {
			return err
		}
		if attempt%alertAfter == 0 {
			ErrorLogf("sqlite %s still busy after %d attempts, retrying until it succeeds err=%v", what, attempt, err)
		} else {
			Logf("sqlite %s busy (attempt %d), retrying in %s err=%v", what, attempt, backoff, err)
		}
		select {
		case <-ctx.Done():
			ErrorLogf("sqlite %s abandoned after %d busy attempts: context ended err=%v ctx=%v", what, attempt, err, ctx.Err())
			return err
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Open is sql.Open("sqlite", dsn) with busy retry. Wrap the result with
// sqlx.NewDb(db, "sqlite") where sqlx is used.
func Open(dsn string) (*sql.DB, error) {
	probe, err := sql.Open("sqlite", "")
	if err != nil {
		return nil, err
	}
	drv := probe.Driver()
	if err := probe.Close(); err != nil {
		return nil, err
	}
	return sql.OpenDB(connector{drv: drv, dsn: dsn}), nil
}

type connector struct {
	drv driver.Driver
	dsn string
}

func (c connector) Driver() driver.Driver { return c.drv }

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	var inner driver.Conn
	err := do(ctx, "open", func() (e error) {
		inner, e = c.drv.Open(c.dsn)
		return e
	})
	if err != nil {
		return nil, err
	}
	return &conn{inner: inner}, nil
}

type conn struct {
	inner driver.Conn
	inTx  bool
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var st driver.Stmt
	run := func() (err error) {
		if p, ok := c.inner.(driver.ConnPrepareContext); ok {
			st, err = p.PrepareContext(ctx, query)
		} else {
			st, err = c.inner.Prepare(query)
		}
		return err
	}
	if c.inTx {
		return st, run()
	}
	err := do(ctx, "prepare", run)
	return st, err
}

func (c *conn) Close() error { return c.inner.Close() }

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var t driver.Tx
	err := do(ctx, "begin", func() (e error) {
		if b, ok := c.inner.(driver.ConnBeginTx); ok {
			t, e = b.BeginTx(ctx, opts)
		} else {
			t, e = c.inner.Begin()
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return &tx{inner: t, c: c}, nil
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	var res driver.Result
	run := func() (err error) {
		res, err = e.ExecContext(ctx, query, args)
		return err
	}
	if c.inTx {
		return res, run()
	}
	err := do(ctx, "exec", run)
	return res, err
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	var rows driver.Rows
	run := func() (err error) {
		rows, err = q.QueryContext(ctx, query, args)
		return err
	}
	if c.inTx {
		return rows, run()
	}
	err := do(ctx, "query", run)
	return rows, err
}

func (c *conn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return do(ctx, "ping", func() error { return p.Ping(ctx) })
	}
	return nil
}

func (c *conn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *conn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type tx struct {
	inner driver.Tx
	c     *conn
}

func (t *tx) Commit() error {
	err := do(context.Background(), "commit", t.inner.Commit)
	t.c.inTx = false
	if err != nil {
		if rbErr := t.inner.Rollback(); rbErr != nil {
			Logf("sqlite rollback after failed commit err=%v commitErr=%v", rbErr, err)
		}
	}
	return err
}

func (t *tx) Rollback() error {
	t.c.inTx = false
	return t.inner.Rollback()
}
