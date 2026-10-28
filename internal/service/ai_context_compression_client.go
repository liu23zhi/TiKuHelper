package service

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// BuildAIContextCompressionConfigWithDB resolves the active model, including
// display-name aliases and zero-priced rows. An unknown window disables size
// estimation in the client; lookup failure must not block an answer request.
func BuildAIContextCompressionConfigWithDB(db *gorm.DB, cfg AISearchRequestConfig) search.AIContextCompressionConfig {
	order := cfg.ContextCompressionOrder
	if !validAIContextCompressionOrder(order) {
		order = defaultAIContextCompressionOrder()
	}
	compression := search.AIContextCompressionConfig{
		Enabled:        cfg.ContextCompressionEnabled,
		RemoteEnabled:  cfg.UseRemoteContextCompression,
		TriggerPercent: cfg.ContextCompressionTriggerPercent,
		TargetPercent:  cfg.ContextCompressionTargetPercent,
		MaxAttempts:    cfg.ContextCompressionMaxAttempts,
		StrategyOrder:  append([]string(nil), order...),
	}
	if !compression.Enabled || db == nil || strings.TrimSpace(cfg.Model) == "" {
		return compression
	}
	price, found, err := ResolveAIModelTokenPriceWithDB(db, cfg.Model)
	if err != nil {
		logger.SysError(logger.RedactSecrets(fmt.Sprintf("AI上下文窗口读取失败 model=%s: %v", strings.TrimSpace(cfg.Model), err), cfg.APIKey))
		return compression
	}
	if found {
		compression.ContextWindowTokens = price.ContextWindowTokens
	}
	return compression
}
