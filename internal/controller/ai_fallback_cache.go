package controller

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/registry/manager"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// fallbackCachedValue 是 AI 兜底缓存的值。
//
// 用强类型而不是序列化成字符串：service.AIFallbackResult 的
// Usage / Model / UnpricedModel 都带 `json:"-"`，走 JSON 会**静默丢字段**，
// 而 Usage 正是计费依据。
type fallbackCachedValue struct {
	result service.AIFallbackResult
}

// fallbackResultCache 缓存 AI 兜底结果。
//
// 与 searchResultCache 完全独立：不同实例、不同键空间、不同值类型，
// 任一方向的写入都不可能被另一方向读到。
var fallbackResultCache = newResultCache[fallbackCachedValue](defaultResultCacheTTL, nil)

// fallbackResultCacheKey 是 AI 兜底缓存的键载荷。
//
// 兜底入参只有 question / type / options（没有渠道与平台概念），
// 因此键维度相应更少。
type fallbackResultCacheKey struct {
	Epoch        uint64   `json:"epoch"`
	UserID       int32    `json:"userId"`
	Question     string   `json:"question"`
	Type         int      `json:"type"`
	DeepThinking bool     `json:"deepThinking"`
	Options      []string `json:"options"`
	UAPISearch   bool     `json:"uapiSearch"`
}

// buildFallbackResultCacheKey 构造兜底缓存键，并返回当前代次。
func buildFallbackResultCacheKey(userID int32, req model.SearchRequest) (string, uint64, bool) {
	epoch := fallbackResultCache.currentEpoch()

	key, ok := marshalCacheKey(fallbackResultCacheKey{
		Epoch:        epoch,
		UserID:       userID,
		Question:     strings.TrimSpace(req.Question),
		Type:         req.Type,
		DeepThinking: req.DeepThinking,
		Options:      req.Options,
		UAPISearch:   uapiSearchToolEnabledForFallback(),
	})
	if !ok {
		return "", epoch, false
	}
	return key, epoch, true
}

func uapiSearchToolEnabledForFallback() bool {
	currentManager := manager.GetManager()
	if currentManager == nil {
		return false
	}
	key := strings.TrimSpace(service.GetUAPIAPIKeyWithDB(currentManager.GetDB()))
	return strings.HasPrefix(key, "uapi-") && len(key) > len("uapi-")
}

// cloneAIFallbackResult 深拷贝一份兜底结果。
//
// Result 与 Error 是指针、Warnings 是切片，浅拷贝会让缓存条目与调用方共享同一块内存，
// 调用方后续追加告警就会污染缓存内容。
func cloneAIFallbackResult(source service.AIFallbackResult) service.AIFallbackResult {
	cloned := source

	if source.Result != nil {
		answer := *source.Result
		cloned.Result = &answer
	}
	if source.Error != nil {
		failure := *source.Error
		cloned.Error = &failure
	}
	if source.Warnings != nil {
		cloned.Warnings = append([]string(nil), source.Warnings...)
	}
	if source.Usage.SearchCalls != nil {
		cloned.Usage.SearchCalls = append([]internalsearch.AISearchCallUsage(nil), source.Usage.SearchCalls...)
	}
	if source.Usage.WebReadCalls != nil {
		cloned.Usage.WebReadCalls = append([]internalsearch.AIWebReadCallUsage(nil), source.Usage.WebReadCalls...)
	}
	return cloned
}

// loadCachedAIFallbackResult 读取兜底缓存。只返回可用的成功结果。
func loadCachedAIFallbackResult(userID int32, req model.SearchRequest) (service.AIFallbackResult, bool) {
	key, epoch, ok := buildFallbackResultCacheKey(userID, req)
	if !ok {
		return service.AIFallbackResult{}, false
	}

	entry, found := fallbackResultCache.get(key, epoch)
	if !found || !entry.result.Success {
		return service.AIFallbackResult{}, false
	}
	return cloneAIFallbackResult(entry.result), true
}

// fallbackCachedValueUsable 是兜底结果的缓存准入判据：只有成功结果才值得缓存。
func fallbackCachedValueUsable(value fallbackCachedValue) bool {
	return value.result.Success
}

// storeCachedAIFallbackResult 写入兜底缓存。
//
// **只缓存成功结果**：失败（AI_UNAVAILABLE / NO_ANSWER / UPSTREAM_ERROR）不入缓存，
// 否则一次网络抖动会被固化 10 分钟，用户在此期间怎么重试都拿不到答案。
func storeCachedAIFallbackResult(userID int32, req model.SearchRequest, result service.AIFallbackResult) {
	if !result.Success {
		return
	}

	key, epoch, ok := buildFallbackResultCacheKey(userID, req)
	if !ok {
		return
	}
	fallbackResultCache.put(key, fallbackCachedValue{result: cloneAIFallbackResult(result)}, epoch, fallbackCachedValueUsable)
}

// shouldBypassFallbackResultCache 判断调用方是否显式要求跳过兜底缓存。
//
// 与搜题一致：只有显式 `"skipCache": true` 才跳过；`false` 与缺省都正常使用缓存。
func shouldBypassFallbackResultCache(req model.SearchRequest) bool {
	return shouldBypassSearchResultCache(req)
}
