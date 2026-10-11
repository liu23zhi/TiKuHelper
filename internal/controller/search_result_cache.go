package controller

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"fmt"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
)

const (
	defaultSearchResultCacheTTL = 10 * time.Minute
	searchResultCacheProvider   = "cache"
)

type searchResultCacheEntry struct {
	answers   [][]string
	expiresAt time.Time
}

type searchResultCache struct {
	mu      sync.RWMutex
	entries map[string]searchResultCacheEntry
	ttl     time.Duration
	now     func() time.Time
	// epoch 是缓存代次。自增后旧 key 全部失效，无需遍历删除，
	// 避免「清空」与并发读之间出现窗口。
	epoch uint64
}

type searchResultCacheKey struct {
	Epoch  uint64 `json:"epoch"`
	UserID int32  `json:"userId"`
	// Question / Options 保持原样，作为同一用户内的题目维度。
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func newSearchResultCache(ttl time.Duration, now func() time.Time) *searchResultCache {
	if ttl <= 0 {
		ttl = defaultSearchResultCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &searchResultCache{
		entries: make(map[string]searchResultCacheEntry),
		ttl:     ttl,
		now:     now,
	}
}

var defaultSearchResultCache = newSearchResultCache(defaultSearchResultCacheTTL, time.Now)

// invalidateSearchResultCache 让全部已缓存的搜题结果立即失效。
//
// 任何会改变「同一个问题应该得到什么答案」的写操作都必须调用它：
// 题库的人工增删改、批量导入、答案回填，以及 AI 搜索配置变更。
// 否则用户会继续拿到最长 defaultSearchResultCacheTTL 的陈旧答案。
func invalidateSearchResultCache(reason string) {
	if defaultSearchResultCache == nil {
		return
	}
	defaultSearchResultCache.invalidate()
	if reason != "" {
		logger.SysLog(fmt.Sprintf("搜题结果缓存已失效 reason=%s", reason))
	}
}

// invalidate 自增代次，使所有旧 key 立即失效。
// 顺带清掉过期条目，避免长时间只读导致陈旧数据堆积（见第 3 点）。
func (s *searchResultCache) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.epoch++
	s.entries = make(map[string]searchResultCacheEntry, len(s.entries))
}

// currentEpoch 读取当前代次，用于构造缓存 key。
func (s *searchResultCache) currentEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

// canUseSearchResultCache 判断本次请求能否使用搜题结果缓存。
//
// 缓存按用户隔离，因此匿名请求（没有用户身份，UserID 为 0）不参与缓存：
// 否则所有匿名调用会共用同一份结果，既可能串数据，也无法按用户失效。
func canUseSearchResultCache(resolved resolvedSearchRequestContext, useQuery string) bool {
	if resolved.SearchMode != searchModeNormal {
		return false
	}
	if resolved.UserID <= 0 {
		return false
	}
	return !strings.Contains(useQuery, searchBillingProviderLocal) && !strings.Contains(useQuery, searchModeAI)
}

// buildSearchResultCacheKey 构造缓存 key，并返回当前代次。
//
// 缓存按**用户**隔离（userID），而不是按访问令牌：
// 同一用户名下的多把令牌访问同一道题时应当复用同一份结果，
// 否则一把令牌一次上游调用，既浪费额度也拉长响应。
//
// 返回值第二个是代次，调用方必须在写回时原样传给 put，
// 以便检测「请求进行中缓存被失效」的情况。
func buildSearchResultCacheKey(userID int32, req model.SearchRequest) (string, uint64) {
	return buildSearchResultCacheKeyFor(defaultSearchResultCache, userID, req)
}

// buildSearchResultCacheKeyFor 与 buildSearchResultCacheKey 相同，
// 但作用于指定缓存实例，便于单测使用独立实例、不污染全局缓存。
func buildSearchResultCacheKeyFor(cache *searchResultCache, userID int32, req model.SearchRequest) (string, uint64) {
	epoch := cache.currentEpoch()
	payload, _ := json.Marshal(searchResultCacheKey{
		// 带上代次：失效后旧 key 不再可能被命中，无需逐个删除。
		Epoch:    epoch,
		UserID:   userID,
		Question: req.Question,
		Options:  req.Options,
	})
	return string(payload), epoch
}

func shouldBypassSearchResultCache(req model.SearchRequest) bool {
	return req.SkipCache != nil && *req.SkipCache
}

// get 读取缓存，并校验代次（防御 key 被复用的极端情况）。
func (s *searchResultCache) get(key string, epoch uint64) ([][]string, bool) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if epoch != s.epoch {
		return nil, false
	}
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(s.entries, key)
		return nil, false
	}
	return cloneSearchAnswers(entry.answers), true
}

// put 写入缓存，并校验「结果所属代次」仍是最新。
//
// epoch 是发起这次上游请求时读到的代次。若期间发生了失效，
// 该结果已经过期，必须丢弃，否则会把陈旧答案重新灌回缓存。
func (s *searchResultCache) put(key string, answers [][]string, epoch uint64) {
	if key == "" || !hasUsableSearchAnswers(answers) {
		return
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if epoch != s.epoch {
		return
	}
	s.cleanupExpiredLocked(now)
	s.entries[key] = searchResultCacheEntry{
		answers:   cloneSearchAnswers(answers),
		expiresAt: now.Add(s.ttl),
	}
}

func (s *searchResultCache) cleanupExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

func hasUsableSearchAnswers(answers [][]string) bool {
	for _, group := range answers {
		if len(group) > 0 {
			return true
		}
	}
	return false
}

func cloneSearchAnswers(answers [][]string) [][]string {
	cloned := make([][]string, len(answers))
	for i := range answers {
		cloned[i] = append([]string(nil), answers[i]...)
	}
	return cloned
}

// 在 init 中把缓存失效能力注入 middleware，
// 让题库写入（答案回填）也能作废搜题结果缓存，同时避免循环依赖。
func init() {
	middleware.InvalidateSearchResultCache = invalidateSearchResultCache
}
