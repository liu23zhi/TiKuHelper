package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// searchAnswerPayload 是搜题缓存的值：来自各渠道的**原始未格式化**答案。
//
// 必须存原始答案而不是已格式化结果：FillAnswerResponse 在命中与未命中路径
// 都会执行，若缓存的是格式化结果会被二次格式化，而它并非所有分支都幂等。
type searchAnswerPayload struct {
	answers [][]string
}

// searchAnswerPayloadUsable 是搜题结果的缓存准入判据。
//
// 上游失败 / 无结果时 result 为空，这类结果**绝不能入缓存**：
// 否则一次临时抖动会被固化 10 分钟，用户重试只会拿到空答案。
func searchAnswerPayloadUsable(payload searchAnswerPayload) bool {
	return hasUsableSearchAnswers(payload.answers)
}

// searchResultCache 缓存搜题结果。与 fallbackResultCache 完全独立（不同实例、不同键空间）。
var searchResultCache = newResultCache[searchAnswerPayload](defaultResultCacheTTL, nil)

// searchResultCacheKey 是搜题缓存的键载荷。
//
// 维度取「会影响答案」的全部字段：用户、题目、选项、题型、渠道选择、来源指纹、代次。
// **不含 plat**：该字段在本项目与全部调用方恒为 -1（或已删除），没有区分度。
type searchResultCacheKey struct {
	Epoch        uint64   `json:"epoch"`
	UserID       int32    `json:"userId"`
	Question     string   `json:"question"`
	Options      []string `json:"options"`
	Type         int      `json:"type"`
	Use          string   `json:"use"`
	SourceDigest string   `json:"sourceDigest"`
}

// normalizeSearchUseKey 归一化 use 查询参数。
//
// 只做「无语义差异」的归一：去空白、去空段、去重、排序。
// 因此 `use=icodef,ai` 与 `use=ai,icodef` 视为同一个键；
// 但 `use=icodef` 与不传 `use`（走全部启用渠道）仍是**不同**的键。
func normalizeSearchUseKey(useQuery string) string {
	trimmed := strings.TrimSpace(useQuery)
	if trimmed == "" {
		return ""
	}

	parts := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, raw := range strings.Split(trimmed, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		parts = append(parts, item)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// buildSearchSourceDigest 计算「本次查询实际用了什么来源」的指纹。
//
// 之所以需要：改为可按渠道（含 use=ai）缓存之后，必须防止不同上游配置之间互相命中
// —— 同一道题用不同模型/不同 Prompt 问出来的答案不同，绝不能复用。
// 指纹只进键、不出现在响应里。
func buildSearchSourceDigest(resolved resolvedSearchRequestContext, useQuery string) string {
	parts := []string{
		"use=" + normalizeSearchUseKey(useQuery),
	}

	// 实际会启用的渠道集合（排序保证稳定）。
	providers := make([]string, 0, len(resolved.Tokens)+2)
	for provider := range resolved.Tokens {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	parts = append(parts, "tokens="+strings.Join(providers, ","))

	// AI 生效配置：换模型 / 换 Prompt / 换 BaseURL 都会改变答案。
	if config := resolved.AIConfig; strings.TrimSpace(config.BaseURL) != "" || strings.TrimSpace(config.Model) != "" {
		parts = append(parts,
			"aiBase="+strings.TrimSpace(config.BaseURL),
			"aiModel="+strings.TrimSpace(config.Model),
			"aiPrompt="+digestString(config.Prompt),
		)
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:16])
}

// digestString 对可能很长的文本（如自定义 Prompt）取短摘要。
func digestString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}

// buildSearchResultCacheKey 构造搜题缓存键，并返回当前代次。
//
// 键由哈希摘要表示（见 hashCacheKey）：题面与选项可能很长，直接当 map key 会膨胀。
//
// **题面与选项入键前都会归一化**（见 normalizeCacheKeyText）：这是线上实测的必要修复。
// 客户端每次抓取页面得到的标点并不稳定（`. `↔`。`、`,`↔`，`、行尾多余标点），
// 未归一化时「同一个问题」会因为一个标点差异算出不同的键，缓存**永远不命中**，
// 用户在一分钟内重问同一道题会被反复计费。
//
// 归一化**只作用于键**：发给上游的题目、写回响应的题目、落库的审计日志
// 全部保持原始值不变 —— 归一化只是用来判断「这是不是同一个问题」。
func buildSearchResultCacheKey(resolved resolvedSearchRequestContext, req model.SearchRequest, useQuery string) (string, uint64, bool) {
	epoch := searchResultCache.currentEpoch()

	key, ok := marshalCacheKey(searchResultCacheKey{
		Epoch:        epoch,
		UserID:       resolved.UserID,
		Question:     normalizeCacheKeyText(req.Question),
		Options:      normalizeCacheKeyOptions(req.Options),
		Type:         req.Type,
		Use:          normalizeSearchUseKey(useQuery),
		SourceDigest: buildSearchSourceDigest(resolved, useQuery),
	})
	if !ok {
		return "", epoch, false
	}
	return key, epoch, true
}

// normalizeCacheKeyText 把文本归一化到「无语义差异」的形式，仅用于缓存键。
//
// 实现集中在 pkg/util（见 util.NormalizeCacheKeyText）：题库 hash 的候选构造
// 用的是**同一套**归一化，两处必须一致，否则「缓存能命中但题库匹配不到」
// 这类不一致会非常难排查。
func normalizeCacheKeyText(value string) string {
	return util.NormalizeCacheKeyText(value)
}

// normalizeCacheKeyOptions 对选项列表逐项归一化，**保持原顺序**。
func normalizeCacheKeyOptions(options []string) []string {
	return util.NormalizeCacheKeyOptions(options)
}

// canUseSearchResultCache 判断本次搜题请求能否参与缓存。
//
// 与旧实现的区别：**不再因为 use 含 "ai" 而排除**。
// `use` 只作为键的区分维度（不同渠道各自成键，互不共用）；
// 唯一需要整体排除的是本地题库 —— 它必须即时反映后台对题库的增删改。
//
// 另外排除匿名请求（没有用户身份）：否则所有匿名调用会共用一份结果。
//
// 注意**不要**拿 resolved.Tokens 判断"是否携带请求级凭据"：
// 那里混入了系统默认账号的 token（见 service.BuildSearchTokensWithDB 的
// allowDefaultAccount 分支），几乎所有正常请求都会命中，会把缓存整体关掉。
// 需要判断请求级覆盖时用 hasExplicitSearchProviderOverride(query)（只看 query）。
func canUseSearchResultCache(resolved resolvedSearchRequestContext, useQuery string) bool {
	if resolved.UserID <= 0 {
		return false
	}
	if strings.Contains(useQuery, searchBillingProviderLocal) {
		return false
	}
	return true
}

// shouldBypassSearchResultCache 判断调用方是否**显式**要求跳过读取缓存。
//
// 注意两点语义：
//   - 只有显式 `"skipCache": true` 才跳过；`false` 与缺省都正常使用缓存；
//   - 跳过读不代表不写：跳过读之后仍会把新结果写回缓存，即「刷新」语义。
func shouldBypassSearchResultCache(req model.SearchRequest) bool {
	return req.SkipCache != nil && *req.SkipCache
}

// invalidateSearchResultCaches 让搜题与 AI 兜底两类结果缓存同时失效。
//
// 任何会改变「同一个问题应该得到什么答案」的写操作都必须调用它：
// 题库的人工增删改、批量导入，以及 AI 搜索配置变更。
func invalidateSearchResultCaches(reason string) {
	searchResultCache.invalidate()
	fallbackResultCache.invalidate()

	if strings.TrimSpace(reason) != "" {
		logger.SysLog(fmt.Sprintf("搜题结果缓存已失效 reason=%s", strings.TrimSpace(reason)))
	}
}

// resultCacheCleanupInterval 是后台清理的固定节奏。
//
// 不取 TTL（10 分钟）：清理开销很低（一次 map 遍历），跑勤一点可以让
// 过期条目更快归还内存，同时让 put 路径保持「只做一次赋值」。
const resultCacheCleanupInterval = time.Minute

// StartResultCacheCleanup 启动结果缓存的后台清理循环。
//
// 由进程入口调用一次。清理逻辑从 put 路径剥离到这里：原先每次写入都全表扫描，
// 写入开销随条目数线性增长。
func StartResultCacheCleanup() {
	startResultCacheCleanup(resultCacheCleanupInterval)
	logger.SysLog(fmt.Sprintf("结果缓存清理已启动 interval=%s", resultCacheCleanupInterval))
}

// startResultCacheCleanup 启动清理循环，返回停止函数（便于测试收尾）。
func startResultCacheCleanup(interval time.Duration) func() {
	if interval <= 0 {
		interval = resultCacheCleanupInterval
	}

	stop := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				searchResultCache.cleanupExpired()
				fallbackResultCache.cleanupExpired()
			}
		}
	}()

	return func() {
		once.Do(func() { close(stop) })
	}
}
