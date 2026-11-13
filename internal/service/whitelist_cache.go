package service

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 白名单进程内缓存（照抄 blacklist_cache.go 的 60s 快照重载模式）。
//
// 为什么必须有缓存：白名单判定排在黑名单**之前**（见 middleware/whitelist.go），
// 也就是每个请求都要过一遍。若每请求查一次 whitelist_entries + 一次
// system_settings，等于给全站加两次库往返 —— 而且这条链路恰好在「要被放行」的
// 请求上，热路径上更该便宜。
//
// 结构（与 BlacklistSnapshot 同构，只有 IP 一种粒度）：
//
//	ipExact   map[string]*entry      // 精确 IP（key 是 ip.String() 归一化文本）
//	ipCIDR    []cidrRule             // CIDR 段（数量少，线性扫可接受）
//	exempt    []*net.IPNet           // blacklist_exempt_ips 文本域解析结果（T-05）
//
// 刷新策略（与黑名单逐条对齐）：
//   - 管理员增删改 → Reload 立即失效重载（同进程）
//   - 后台每 60s 全量重载（多实例场景下其它实例的改动靠它收敛）
//   - **重载失败保留旧快照**：绝不因为「刷新失败」而把已放行的 IP 重新拦掉
//
// 三条红线级性质：
//   - Enabled=false 与 ExpiresAt 已过期的条目**不进缓存**（于是它们不放行）；
//   - **没有总开关**（见 entity.WhitelistEntry 注释）：表为空 = 零命中，
//     自然不会放行任何人，无需 switch 也能保持「未配置时全站行为不变」；
//   - 白名单命中**不写 blacklist_hits**：那是黑名单的命中日志，把「本次没拦」
//     写进去会把「为什么被拦」的解释数据污染成噪音（T-04 判据①）。

// ErrWhitelistCacheNoDatabase 表示没有可用的数据库连接。
var ErrWhitelistCacheNoDatabase = errors.New("白名单缓存：数据库未初始化")

// WhitelistRule 是一条进缓存的白名单规则（只保留判定需要的字段）。
type WhitelistRule struct {
	// EntryID 是 whitelist_entries.id（进审计与诊断用；白名单没有命中日志表）。
	EntryID   int64
	Kind      string
	Pattern   string
	MatchType string
	// ValueMasked 是展示用脱敏值（管理页与审计用）。
	ValueMasked string
	// Reason 是运营填写的放行原因。
	Reason string
	// ExpiresAt 为 nil 表示永久；进缓存前已过滤掉过期条目。
	ExpiresAt *time.Time
}

// WhitelistCIDRRule 是一条 CIDR 规则。
type WhitelistCIDRRule struct {
	Network *net.IPNet
	Rule    *WhitelistRule
}

// WhitelistSnapshot 是一次全量加载的结果。
//
// 不可变对象：加载完成后只读，替换时整体换指针，判定路径完全无锁（atomic.Pointer），
// 高并发下不会因为「刷新」而阻塞请求。
type WhitelistSnapshot struct {
	// LoadedAt 是本次加载时间，用于排障「规则为什么还没生效」。
	LoadedAt time.Time
	// RuleCount 是进缓存的规则条数（已剔除停用/过期）。
	RuleCount int
	// SkippedCount 是加载时被跳过的坏行条数（IP/CIDR 解析失败等）。
	SkippedCount int
	// ExemptCount 是 blacklist_exempt_ips 解析出的网段条数（T-05）。
	ExemptCount int

	ipExact map[string]*WhitelistRule
	ipCIDR  []WhitelistCIDRRule
	exempt  []*net.IPNet
}

// WhitelistMatch 是一次命中结果。
type WhitelistMatch struct {
	EntryID   int64
	Kind      string
	MatchType string
	// Reason 是命中规则的放行原因（仅诊断/日志用，不进响应体）。
	Reason string
	// Source 说明命中的是哪一层：entry（表规则）/ exempt_ips（文本域）。
	//
	// 两层的处置完全相同（都直接放行、都不查黑名单），分开只为排查
	// 「我明明加了这个 IP 为什么没生效」时能一眼看出是哪一层漏了。
	Source string
}

// 白名单命中来源。
const (
	// WhitelistSourceEntry 命中 whitelist_entries 表规则。
	WhitelistSourceEntry = "entry"
	// WhitelistSourceExemptIPs 命中 blacklist_exempt_ips 文本域。
	WhitelistSourceExemptIPs = "exempt_ips"
)

func newWhitelistSnapshot(now time.Time) *WhitelistSnapshot {
	return &WhitelistSnapshot{
		LoadedAt: now,
		ipExact:  make(map[string]*WhitelistRule),
	}
}

// MatchIP 判定一个客户端 IP 是否在白名单内（精确优先，再扫 CIDR，最后查 exempt_ips）。
//
// 规范化只用 net.ParseIP：不做字符串前缀比较。IPv4-mapped IPv6
// （::ffff:203.0.113.7）与 IPv4 文本会被 ip.String() 收敛成同一形式，
// 因此精确规则不会因为写法差异漏判（判据③）。
//
// 顺序：精确 map（O(1)）→ CIDR 线性扫 → exempt_ips 线性扫。
// 精确与 CIDR 都是「运营显式配的规则」，先判它们让最常见的单 IP 场景零分配。
func (s *WhitelistSnapshot) MatchIP(clientIP string) (WhitelistMatch, bool) {
	if s == nil {
		return WhitelistMatch{}, false
	}
	ip := parseWhitelistClientIP(clientIP)
	if ip == nil {
		return WhitelistMatch{}, false
	}
	if rule, ok := s.ipExact[ip.String()]; ok && rule != nil {
		return WhitelistMatch{
			EntryID:   rule.EntryID,
			Kind:      rule.Kind,
			MatchType: rule.MatchType,
			Reason:    rule.Reason,
			Source:    WhitelistSourceEntry,
		}, true
	}
	for _, cidrRule := range s.ipCIDR {
		if cidrRule.Network != nil && cidrRule.Network.Contains(ip) {
			rule := cidrRule.Rule
			if rule == nil {
				continue
			}
			return WhitelistMatch{
				EntryID:   rule.EntryID,
				Kind:      rule.Kind,
				MatchType: rule.MatchType,
				Reason:    rule.Reason,
				Source:    WhitelistSourceEntry,
			}, true
		}
	}
	for _, network := range s.exempt {
		if network != nil && network.Contains(ip) {
			return WhitelistMatch{
				Kind:      WhitelistKindIP,
				MatchType: WhitelistMatchCIDR,
				Source:    WhitelistSourceExemptIPs,
			}, true
		}
	}
	return WhitelistMatch{}, false
}

// Count 返回进缓存的规则条数（诊断用，等于 RuleCount）。
func (s *WhitelistSnapshot) Count() int {
	if s == nil {
		return 0
	}
	return s.RuleCount
}

// LoadWhitelistSnapshot 从库里全量加载一份快照。
//
// 两段数据一次读齐（规则表 + blacklist_exempt_ips）：中间件只查询一次快照，
// 不需要为「两层白名单」分别判定。
//
// 过滤规则：
//   - Enabled=false 的条目不进缓存（立即不生效，刷新后即失效）；
//   - ExpiresAt 非空且 now >= ExpiresAt 的条目不进缓存（自动失效，但**不删记录**）。
//
// 表不存在（迁移没跑 / 回滚到旧版本）时按「零规则」返回成功：白名单是增强功能，
// 缺表不该让启动或刷新反复告警。
func LoadWhitelistSnapshot(db *gorm.DB, now time.Time) (*WhitelistSnapshot, error) {
	if db == nil {
		return nil, ErrWhitelistCacheNoDatabase
	}
	// 连通性探测：区分「DB 不可用」（刷新失败，要保留旧快照）与
	// 「没有规则」（正常替换成空快照）。两者必须分开，否则 DB 抖动时
	// 白名单会静默清空 —— 已放行的 IP 会重新被拦，且没有任何报错。
	if probeErr := db.Exec("SELECT 1").Error; probeErr != nil {
		return nil, fmt.Errorf("白名单缓存：数据库不可用（%w）", probeErr)
	}

	snapshot := newWhitelistSnapshot(now)

	// 第一段：blacklist_exempt_ips 文本域（读不到就是空，不报错）。
	snapshot.exempt = ParseWhitelistExemptIPs(GetWhitelistExemptIPsWithDB(db))
	snapshot.ExemptCount = len(snapshot.exempt)

	// 第二段：whitelist_entries 表。
	if !db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		return snapshot, nil
	}
	var rows []entity.WhitelistEntry
	// 只取 enabled 的行：Disabled 的规则不该占缓存内存，也不该有任何命中机会。
	// 过期判定放在 Go 里做（SQLite 与 MySQL 的时间函数/时区语义不一致，
	// 放在一侧比放在 SQL 里稳）。
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("加载白名单规则失败：%w", err)
	}
	for i := range rows {
		row := rows[i]
		if row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
			continue
		}
		rule := &WhitelistRule{
			EntryID:     row.ID,
			Kind:        strings.ToLower(strings.TrimSpace(row.Kind)),
			Pattern:     row.Pattern,
			MatchType:   strings.ToLower(strings.TrimSpace(row.MatchType)),
			ValueMasked: row.ValueMasked,
			Reason:      row.Reason,
			ExpiresAt:   row.ExpiresAt,
		}
		if rule.ValueMasked == "" {
			rule.ValueMasked = MaskWhitelistValue(rule.Kind, rule.Pattern, rule.MatchType)
		}
		if !snapshot.addRule(rule) {
			snapshot.SkippedCount++
		}
	}
	snapshot.RuleCount = len(snapshot.ipExact) + len(snapshot.ipCIDR)
	return snapshot, nil
}

// addRule 把一条规则放进对应索引；返回 false 表示这条行无法使用（已跳过）。
//
// 单条坏行只跳过它自己：不能因为一条畸形 CIDR 就让整张表的白名单全部失效
// （白名单失效的后果是「本该放行的又被拦」，且原因极难定位）。
func (s *WhitelistSnapshot) addRule(rule *WhitelistRule) bool {
	if rule == nil {
		return false
	}
	if rule.Kind != "" && rule.Kind != WhitelistKindIP {
		// 非 ip 粒度在白名单里没有判定落点（构造时会被拒绝，这里兜历史脏数据）。
		logger.SysWarn(fmt.Sprintf("白名单规则 %d 的类型 %q 不受支持，已跳过", rule.EntryID, rule.Kind))
		return false
	}
	switch rule.MatchType {
	case WhitelistMatchCIDR, WhitelistMatchPrefix:
		_, network, err := net.ParseCIDR(strings.TrimSpace(rule.Pattern))
		if err != nil || network == nil {
			logger.SysWarn(fmt.Sprintf("白名单规则 %d 的 CIDR %q 无法解析，已跳过", rule.EntryID, rule.Pattern))
			return false
		}
		s.ipCIDR = append(s.ipCIDR, WhitelistCIDRRule{Network: network, Rule: rule})
		return true
	case WhitelistMatchExact, "":
		ip := parseWhitelistClientIP(rule.Pattern)
		if ip == nil {
			logger.SysWarn(fmt.Sprintf("白名单规则 %d 的 IP %q 无法解析，已跳过", rule.EntryID, rule.Pattern))
			return false
		}
		s.ipExact[ip.String()] = rule
		return true
	default:
		logger.SysWarn(fmt.Sprintf("白名单规则 %d 的匹配方式 %q 未知，已跳过", rule.EntryID, rule.MatchType))
		return false
	}
}

// WhitelistCache 是进程级缓存（只持有不可变快照的指针）。
type WhitelistCache struct {
	snapshot atomic.Pointer[WhitelistSnapshot]

	mu sync.Mutex
	// loadErr 是最近一次加载的错误（保留旧快照时用它判断当前处于降级状态）。
	loadErr error
	// loads 是成功加载次数、failures 是失败次数，只用于诊断与测试断言。
	loads    int64
	failures int64
}

// NewWhitelistCache 返回一个空缓存。
//
// **初始状态是「不可用」**（没有快照）：在还没有成功加载过的情况下，
// 中间件把「没有快照」按「零白名单」处理（不放行任何人），
// 于是全站行为与未引入白名单时完全一致。
//
// ⚠️ 这里与黑名单的 fail-open 方向**相反**，且必须相反：
// 黑名单查不到就放行（拦的是服务 → 宁可漏拦），白名单查不到就**不放行**
// （放行是给特定 IP 豁免拦截的特权 → 拿不到规则时不能凭空给出豁免）。
// 两者最终都落到「不改变默认行为」这一条上。
func NewWhitelistCache() *WhitelistCache {
	return &WhitelistCache{}
}

// Reload 全量重载；失败时**保留旧快照**并返回错误。
//
// 「保留旧快照」是这条函数的唯一重点：DB 抖动时应该继续按上次成功的快照放行，
// 而不是因为刷新失败就把白名单清空（那会让正常用户突然被拦）。
func (c *WhitelistCache) Reload(db *gorm.DB) error {
	if c == nil {
		return ErrWhitelistCacheNoDatabase
	}
	previous := c.snapshot.Load()
	if db == nil && previous == nil {
		return ErrWhitelistCacheNoDatabase
	}
	snapshot, err := LoadWhitelistSnapshot(db, time.Now())
	if err != nil {
		c.mu.Lock()
		c.loadErr = err
		c.failures++
		c.mu.Unlock()
		if previous != nil {
			logger.SysWarn(fmt.Sprintf("白名单缓存刷新失败，继续沿用上次成功的快照（%d 条规则）：%v",
				previous.RuleCount, err))
		} else {
			logger.SysWarn(fmt.Sprintf("白名单缓存首次加载失败，当前按「零白名单」处理（不放行任何 IP）：%v", err))
		}
		return err
	}
	c.snapshot.Store(snapshot)
	c.mu.Lock()
	c.loadErr = nil
	c.loads++
	c.mu.Unlock()
	return nil
}

// Invalidate 立即失效并重载（管理员增删改规则后调用）。
//
// 语义等同 Reload，保留独立名字是为了让调用点读起来就是它的意图；
// 失败时同样保留旧快照。
func (c *WhitelistCache) Invalidate(db *gorm.DB) error {
	return c.Reload(db)
}

// Snapshot 返回当前快照（可能为 nil = 从未成功加载）。
func (c *WhitelistCache) Snapshot() *WhitelistSnapshot {
	if c == nil {
		return nil
	}
	return c.snapshot.Load()
}

// Available 表示缓存是否已经有过一次成功加载。
func (c *WhitelistCache) Available() bool {
	return c.Snapshot() != nil
}

// Stats 返回诊断用的计数与最近一次加载错误。
func (c *WhitelistCache) Stats() (loads int64, failures int64, lastErr error) {
	if c == nil {
		return 0, 0, ErrWhitelistCacheNoDatabase
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.failures, c.loadErr
}

// WhitelistCacheRefreshInterval 是后台全量重载的周期（与黑名单一致：60s）。
//
// 多实例部署时，A 实例改规则只能靠 B 实例的周期重载收敛；单实例则靠
// Invalidate 立即生效。
const WhitelistCacheRefreshInterval = 60 * time.Second

// StartWhitelistCacheRefresh 启动后台周期重载，返回停止函数（幂等）。
//
// 停止函数会等待 goroutine 退出后再返回，避免测试收尾时与下一次 Reload 竞争。
func StartWhitelistCacheRefresh(cache *WhitelistCache, db *gorm.DB, interval time.Duration) func() {
	if cache == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = WhitelistCacheRefreshInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := cache.Reload(db); err != nil {
					// Reload 内部已经打过 SysWarn，这里不再重复刷屏。
					continue
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

var (
	defaultWhitelistCache     = NewWhitelistCache()
	defaultWhitelistCacheOnce sync.Once
)

// DefaultWhitelistCache 返回进程级单例缓存。
func DefaultWhitelistCache() *WhitelistCache {
	defaultWhitelistCacheOnce.Do(func() {
		if defaultWhitelistCache == nil {
			defaultWhitelistCache = NewWhitelistCache()
		}
	})
	return defaultWhitelistCache
}

// EnsureDefaultWhitelistCacheStarted 让单例缓存绑定到给定数据库：首次同步加载一次，
// 并启动 60 秒后台全量重载。
//
// **幂等且可重绑定**：同一个 *gorm.DB 重复调用是空操作；换成另一个 DB（测试里每个用例
// 都新建 manager，生产上只有一次）时先停掉旧的后台 goroutine 再启动新的，
// 避免「旧 goroutine 抱着已关闭的连接每 60 秒刷一条 SysWarn」。
//
// 首次加载**同步**做一次：让「启动时白名单已有规则」的场景不用等 60 秒。
// 加载失败只记日志（Reload 内部已打 SysWarn），不阻断启动。
func EnsureDefaultWhitelistCacheStarted(db *gorm.DB) *WhitelistCache {
	cache := DefaultWhitelistCache()
	if db == nil {
		return cache
	}

	defaultWhitelistCacheMu.Lock()
	if defaultWhitelistCacheDB == db && stopDefaultWhitelistCacheRefresh != nil {
		defaultWhitelistCacheMu.Unlock()
		return cache
	}
	stop := stopDefaultWhitelistCacheRefresh
	defaultWhitelistCacheMu.Unlock()
	if stop != nil {
		stop() // 等旧 goroutine 真正退出，避免它与新 DB 的加载并发写同一缓存。
	}

	_ = cache.Reload(db)

	defaultWhitelistCacheMu.Lock()
	defaultWhitelistCacheDB = db
	stopDefaultWhitelistCacheRefresh = StartWhitelistCacheRefresh(cache, db, WhitelistCacheRefreshInterval)
	defaultWhitelistCacheMu.Unlock()
	return cache
}

var (
	defaultWhitelistCacheMu          sync.Mutex
	defaultWhitelistCacheDB          *gorm.DB
	stopDefaultWhitelistCacheRefresh func()
)

// ReloadDefaultWhitelistCache 立即重载单例缓存（管理员改规则后调用）。
//
// 返回错误供调用方决定是否提示运营「规则已写入但缓存刷新失败」；
// 失败时旧快照仍然生效，不会出现「已放行的 IP 突然被拦」的窗口。
func ReloadDefaultWhitelistCache(db *gorm.DB) error {
	return DefaultWhitelistCache().Reload(db)
}
