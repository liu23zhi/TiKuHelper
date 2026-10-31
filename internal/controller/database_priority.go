package controller

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"gorm.io/gorm"
)

// withDatabasePriority annotates a GORM session for the shared admission
// queue. The scheduler only controls when SQL starts; it never preempts work
// that is already running.
func withDatabasePriority(db *gorm.DB, ctx context.Context, priority dbqueue.Priority) *gorm.DB {
	if db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// NewDB is important when the source DB came from a generated DAO: its
	// statement may still carry that DAO's model/schema (for example Log).
	// A fresh statement lets the service choose its own model without leaking
	// the generated DAO metadata into the next query.
	return db.Session(&gorm.Session{
		Context: dbqueue.WithPriority(ctx, priority),
		NewDB:   true,
	})
}

func databaseRequestContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}
