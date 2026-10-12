package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type databaseLogger struct {
	level gormlogger.LogLevel
}

func newDatabaseLogger() gormlogger.Interface {
	return &databaseLogger{level: gormlogger.Info}
}

func (l *databaseLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &databaseLogger{level: level}
}

func (l *databaseLogger) Info(_ context.Context, format string, args ...interface{}) {
	if l.level >= gormlogger.Info {
		logger.SysLog("database " + fmt.Sprintf(format, args...))
	}
}

func (l *databaseLogger) Warn(_ context.Context, format string, args ...interface{}) {
	if l.level >= gormlogger.Warn {
		logger.SysWarn("database " + fmt.Sprintf(format, args...))
	}
}

func (l *databaseLogger) Error(_ context.Context, format string, args ...interface{}) {
	if l.level >= gormlogger.Error {
		logger.SysError("database " + fmt.Sprintf(format, args...))
	}
}

func (l *databaseLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	hasError := err != nil && !errors.Is(err, gorm.ErrRecordNotFound)
	if !hasError && elapsed <= 200*time.Millisecond && l.level < gormlogger.Info {
		return
	}
	sql, rows := fc()
	message := fmt.Sprintf("duration=%s rows=%d sql=%s", elapsed, rows, sql)
	switch {
	case hasError:
		l.Error(ctx, "%s error=%v", message, err)
	case elapsed > 200*time.Millisecond:
		l.Warn(ctx, "SLOW SQL %s", message)
	default:
		l.Info(ctx, "%s", message)
	}
}

// SQL parameters include passwords, access tokens and system API keys.
func (l *databaseLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}
