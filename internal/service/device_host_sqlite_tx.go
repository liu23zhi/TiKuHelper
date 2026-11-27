package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"gorm.io/gorm"
)

// glebarez SQLite can leave the native transaction open if COMMIT fails
// (e.g. a deferred constraint). The database/sql Tx is already done and
// cannot roll back then; keep its physical connection until cleanup ends.
func withSQLiteHostTransaction(db *gorm.DB, run func(*gorm.DB) error) error {
	ctx := db.Statement.Context
	drop := func(conn *sql.Conn) {
		// database/sql retires this connection instead of lending it again.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	onConnection := func(conn *sql.Conn) error {
		native, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() {
			if err := native.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				drop(conn)
			}
		}()
		tx := db.Session(&gorm.Session{NewDB: true, Context: ctx})
		tx.Statement.ConnPool = native
		if err := run(tx); err != nil {
			return err
		}
		if err := native.Commit(); err != nil {
			// Cleanup must still run after request cancellation. A rejected
			// ROLLBACK leaves the connection's state uncertain; retire it.
			if _, cleanErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK"); cleanErr != nil {
				drop(conn)
			}
			return err
		}
		return nil
	}
	switch pool := db.Statement.ConnPool.(type) {
	case interface {
		WithPinnedConnection(context.Context, func(*sql.Conn) error) error
	}:
		return pool.WithPinnedConnection(ctx, onConnection)
	case *sql.DB:
		conn, err := pool.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		return onConnection(conn)
	default:
		return ErrDeviceHostUnavailable
	}
}
