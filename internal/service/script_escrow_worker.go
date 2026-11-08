package service

import (
	"context"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// StartScriptEscrowCloseWorker completes durable closures after billing
// outcomes commit, including on restart. It never treats time or worker
// cancellation as proof that a pending charge did not happen.
func StartScriptEscrowCloseWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if db == nil {
			return
		}
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if ctx.Err() != nil {
					return
				}
				batch, cancel := context.WithTimeout(ctx, 4*time.Second)
				err := FinalizeClosingScriptEscrowsWithDB(db.WithContext(batch), 32)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.SysWarn("脚本关停结算暂未完成，将重试")
				}
				timer.Reset(5 * time.Second)
			}
		}
	}()
	return done
}
