package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	// defaultResultCacheTTL 是搜题 / AI 兜底两类结果缓存的默认存活时间。
	defaultResultCacheTTL = 10 * time.Minute
	// searchResultCacheProvider 是命中缓存时对外暴露的 hitProvider 文案。
	searchResultCacheProvider = "cache"
)

// resultCacheEntry 是一条缓存记录。
type resultCacheEntry[V any] struct {
	value     V
	expiresAt time.Time
}

// resultCache 是「按 key 复用结果」的通用内存缓存，带 TTL 与代次（epoch）失效。
//
// 本项目有两条链路各用一个**独立实例**（搜题与 AI 兜底），二者不共用 map、
// 不共用代次、键构造器也不同 —— 任何一侧的写入都不可能被另一侧读到。
// 泛型只共享代码，不共享状态。
type resultCache[V any] struct {
	mu      sync.RWMutex
	entries map[string]resultCacheEntry[V]
	ttl     time.Duration
	now     func() time.Time

	// epoch 是缓存代次。invalidate 时自增，旧 key 因为键里带了 epoch 而天然不可命中，
	// 无需遍历删除，避免「清空」与并发读之间出现窗口。
	epoch uint64
}

func newResultCache[V any](ttl time.Duration, now func() time.Time) *resultCache[V] {
	if ttl <= 0 {
		ttl = defaultResultCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &resultCache[V]{
		entries: make(map[string]resultCacheEntry[V]),
		ttl:     ttl,
		now:     now,
	}
}

// get 读取缓存。
//
// 用读锁：这是纯读路径，高并发搜题时大量请求会同时走这里。
// 命中过期条目时**只报 miss、不在此删除** —— 删除需要写锁，
// 会把读路径退化成串行；过期条目的清理由 cleanupExpired 统一负责。
func (c *resultCache[V]) get(key string, epoch uint64) (V, bool) {
	var zero V

	c.mu.RLock()
	defer c.mu.RUnlock()

	if epoch != c.epoch {
		return zero, false
	}
	entry, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	if !c.now().Before(entry.expiresAt) {
		return zero, false
	}
	return entry.value, true
}

// put 写入缓存。
//
// usable 是「这条结果值不值得缓存」的判据，由调用方提供 —— 缓存必须拒绝
// 不可用结果，否则一次上游抖动产生的空答案会被固化整个 TTL，
// 用户在此期间怎么重试都只能拿到空结果。
//
// epoch 不是当前代次时丢弃写入，避免「失效期间在途的旧请求」把陈旧结果灌回缓存。
func (c *resultCache[V]) put(key string, value V, epoch uint64, usable func(V) bool) {
	if key == "" {
		return
	}
	if usable != nil && !usable(value) {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if epoch != c.epoch {
		return
	}
	c.entries[key] = resultCacheEntry[V]{
		value:     value,
		expiresAt: c.now().Add(c.ttl),
	}
}

// cleanupExpired 清理过期条目，返回删除条数。
//
// 独立于 put：原先每次写入都全表扫描（O(n)），写入路径随条目数线性劣化；
// 现在由定时任务按固定节奏调用，写路径只做一次 map 赋值。
func (c *resultCache[V]) cleanupExpired() int {
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			removed++
		}
	}
	return removed
}

// invalidate 自增代次，使所有旧 key 立即失效。
func (c *resultCache[V]) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.epoch++
	c.entries = make(map[string]resultCacheEntry[V], len(c.entries))
}

// currentEpoch 读取当前代次，用于构造缓存 key。
func (c *resultCache[V]) currentEpoch() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.epoch
}

// size 返回当前条目数（仅供测试与观测）。
func (c *resultCache[V]) size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// clear 清空条目并重置代次（仅供测试）。
//
// 刻意提供 clear 而不是让测试直接给缓存变量重新赋值：缓存实例被多处并发引用，
// 中途替换实例会让「读到的实例」与「持锁的实例」不一致，从而留下永不释放的锁。
func (c *resultCache[V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]resultCacheEntry[V])
	c.epoch = 0
}

// hashCacheKey 把缓存键压成固定长度的十六进制摘要。
//
// 为什么需要：键里包含完整题面与选项，线上实测 options 可达 55 条含编辑器脚本，
// 直接当 map key 会产生数 KB 的长字符串，大量堆积对内存不友好。
// 哈希只压缩表示，**不改变「完全一致才复用」的语义**（SHA-256 碰撞可忽略）。
func hashCacheKey(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// marshalCacheKey 序列化缓存键载荷；失败时返回空串让调用方拒绝缓存。
func marshalCacheKey(payload any) (string, bool) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return hashCacheKey(encoded), true
}
