package service

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 黑名单进程内缓存（46-黑名单设计.md §4）。
//
// 为什么必须有：`GlobalAPIRateLimit` 之后的**每个**请求都要过黑名单判定。
// 若每请求查一次库，规则表上万条、地区规则还要先查归属地缓存（几十万行），
// 单机 QPS 会被这条查询拖垮。
//
// 结构（与 §4 一一对应）：
//
//	ipExact   map[string]*entry      // 精确 IP（key 是规范化文本）
//	ipCIDR    []cidrRule             // CIDR 段（数量少，线性扫可接受）
//	phone     map[[32]byte]*entry    // sha256(11 位手机号)
//	wechat    map[[32]byte]*entry    // sha256(TrimSpace 后的 OpenID)
//	region    []regionRule           // 地区前缀/结构化规则（BL-4 才接入判定）
//
// 刷新策略：
//   - 管理员增删改 → Invalidate/Reload 立即失效重载（同进程）
//   - 后台每 60s 全量重载（多实例场景下其它实例的改动靠它收敛）
//   - **重载失败保留旧快照**：绝不因为「刷新失败」而放开拦截
//
// 两个红线级的性质：
//   - Enabled=false 与 ExpiresAt 已过期的条目**不进缓存**（于是它们不会命中）；
//   - 开关关闭时 Enabled() 为 false，中间件**零查询、零写日志**，全站行为与今天一致。

// ErrBlacklistCacheNoDatabase 表示没有可用的数据库连接。
var ErrBlacklistCacheNoDatabase = errors.New("黑名单缓存：数据库未初始化")

// BlacklistRule 是一条进缓存的规则（只保留判定与留痕需要的字段）。
type BlacklistRule struct {
	// EntryID 是 blacklist_entries.id，命中日志靠它回指规则。
	EntryID int64
	Kind    string
	Pattern string
	// MatchType 取 exact / cidr（prefix 在归一化阶段已转成 cidr）。
	MatchType string
	// ValueMasked 是展示用脱敏值，写命中日志时用。
	ValueMasked string
	// ExpiresAt 为 nil 表示永久；进缓存前已过滤掉过期条目。
	ExpiresAt *time.Time
}

// BlacklistCIDRRule 是一条 CIDR 规则。
type BlacklistCIDRRule struct {
	Network *net.IPNet
	Rule    *BlacklistRule
}

// BlacklistRegionRule 是地区规则的结构占位（46-*.md §4 的 `region []regionRule`）。
//
// ⚠️ 本期（BL-2）它**永远为空**：加载时遇到 kind='region' 的行会跳过并告警，
// 不往这里塞（见 addRule 的 region 分支）。保留这个类型与字段是为了让 BL-4
// 落地时只改 addRule 一处，不必再动快照结构。
type BlacklistRegionRule struct {
	Prefix string
	Rule   *BlacklistRule
}

// BlacklistSnapshot 是一次全量加载的结果。
//
// 快照是不可变对象：加载完成后只读，替换时整体换指针，
// 因此判定路径可以完全无锁（atomic.Pointer），
// 高并发下不会因为「刷新」而阻塞请求。
type BlacklistSnapshot struct {
	// LoadedAt 是本次加载时间，用于排障「规则为什么还没生效」。
	LoadedAt time.Time
	// Enabled 是加载时读到的 blacklist_enabled。
	Enabled bool
	// RuleCount 是进缓存的规则条数（已剔除停用/过期）。
	RuleCount int
	// SkippedCount 是加载时被跳过的坏行条数（CIDR 解析失败等）。
	SkippedCount int

	ipExact map[string]*BlacklistRule
	ipCIDR  []BlacklistCIDRRule
	phone   map[[32]byte]*BlacklistRule
	wechat  map[[32]byte]*BlacklistRule
	region  []BlacklistRegionRule
}

// BlacklistMatch 是一次命中结果。
//
// 它只带「脱敏后的展示值」，**不带规则原文**：中间件的响应体不得泄漏规则细节
// （46-*.md §3.2），命中日志也只写脱敏值。
type BlacklistMatch struct {
	EntryID     int64
	Kind        string
	MatchType   string
	ValueMasked string
}

func newBlacklistSnapshot(now time.Time, enabled bool) *BlacklistSnapshot {
	return &BlacklistSnapshot{
		LoadedAt: now,
		Enabled:  enabled,
		ipExact:  make(map[string]*BlacklistRule),
		phone:    make(map[[32]byte]*BlacklistRule),
		wechat:   make(map[[32]byte]*BlacklistRule),
	}
}

// MatchIP 判定一个客户端 IP 是否命中（精确优先，再扫 CIDR）。
//
// 规范化只用 net.ParseIP：不做字符串前缀比较（46-*.md §1.1）。
// IPv4-mapped IPv6（::ffff:203.0.113.7）与 IPv4 文本会被 IP.String() 收敛成同一形式，
// 因此精确规则不会因为写法差异漏判。
func (s *BlacklistSnapshot) MatchIP(clientIP string) (BlacklistMatch, bool) {
	if s == nil {
		return BlacklistMatch{}, false
	}
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		return BlacklistMatch{}, false
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return BlacklistMatch{}, false
	}
	if rule, ok := s.ipExact[ip.String()]; ok && rule != nil {
		return rule.toMatch(), true
	}
	for _, cidrRule := range s.ipCIDR {
		if cidrRule.Network != nil && cidrRule.Network.Contains(ip) {
			return cidrRule.Rule.toMatch(), true
		}
	}
	return BlacklistMatch{}, false
}

// MatchPhone 判定手机号（BL-3 接入注册/登录/换绑时调用；本期不挂任何入口）。
func (s *BlacklistSnapshot) MatchPhone(raw string) (BlacklistMatch, bool) {
	if s == nil {
		return BlacklistMatch{}, false
	}
	normalized, err := NormalizeMainlandPhoneInput(raw)
	if err != nil {
		return BlacklistMatch{}, false
	}
	if rule, ok := s.phone[BlacklistPatternHashBytes(normalized)]; ok && rule != nil {
		return rule.toMatch(), true
	}
	return BlacklistMatch{}, false
}

// MatchWechat 判定微信 OpenID（BL-3 接入；本期不挂入口）。
//
// 归一化只 TrimSpace、不转小写：OpenID 大小写敏感（46-*.md §1.2）。
func (s *BlacklistSnapshot) MatchWechat(raw string) (BlacklistMatch, bool) {
	if s == nil {
		return BlacklistMatch{}, false
	}
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return BlacklistMatch{}, false
	}
	if rule, ok := s.wechat[BlacklistPatternHashBytes(normalized)]; ok && rule != nil {
		return rule.toMatch(), true
	}
	return BlacklistMatch{}, false
}

func (r *BlacklistRule) toMatch() BlacklistMatch {
	if r == nil {
		return BlacklistMatch{}
	}
	return BlacklistMatch{
		EntryID:     r.EntryID,
		Kind:        r.Kind,
		MatchType:   r.MatchType,
		ValueMasked: r.ValueMasked,
	}
}

// LoadBlacklistSnapshot 从库里全量加载一份快照。
//
// 过滤规则（§4 与 §7 验收）：
//   - Enabled=false 的条目不进缓存（立即不生效，缓存刷新后即失效）；
//   - ExpiresAt 非空且 now >= ExpiresAt 的条目不进缓存（自动失效，但**不删记录**）。
//
// 开关关闭时**只读一次开关就返回**，不扫规则表：这样「开关默认 false」的稳态下
// 后台刷新只有一条单行索引读（system_settings 的 key 唯一索引），
// 不产生规则表查询、不产生任何请求级查询。这不是「零查询」而是「零请求级查询 +
// 每 60 秒一次单行读」，PR 正文里如实写明。
//
// now 由调用方注入，方便用固定时钟断言过期边界；生产传 time.Now()。
func LoadBlacklistSnapshot(db *gorm.DB, now time.Time) (*BlacklistSnapshot, error) {
	if db == nil {
		return nil, ErrBlacklistCacheNoDatabase
	}
	// 先做一次连通性探测，再读开关。
	//
	// 顺序不能反：GetBlacklistEnabledWithDB 在「DB 不可用」与「没有这条记录」两种情况下
	// 都返回 false，二者必须分开 —— 前者是刷新失败（要保留旧快照），
	// 后者是运营真的没开（要正常替换成空快照）。这一条探测是两者唯一的分界线。
	if probeErr := db.Exec("SELECT 1").Error; probeErr != nil {
		return nil, fmt.Errorf("黑名单缓存：数据库不可用（%w）", probeErr)
	}

	enabled := GetBlacklistEnabledWithDB(db)
	snapshot := newBlacklistSnapshot(now, enabled)
	if !enabled {
		// 关闭时不需要规则：既省掉一次全表读，也让「关闭即零拦截」有明确代码路径。
		return snapshot, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		// 表还没建（迁移没跑/回滚到旧版本）：按「零规则」返回成功，不报错。
		// 开关打开时也不会因为缺表而反复告警，中间件按「零规则」放行。
		return snapshot, nil
	}

	var rows []entity.BlacklistEntry
	// 只取 enabled 的行：Disabled 的规则不该占缓存内存，也不该有任何命中机会。
	// 过期判定放在 Go 里做（SQLite 与 MySQL 的时间函数/时区语义不一致，放在一侧比放在 SQL 里稳）。
	if err := db.Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("加载黑名单规则失败：%w", err)
	}

	for i := range rows {
		row := rows[i]
		if row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
			continue
		}
		rule := &BlacklistRule{
			EntryID:     row.ID,
			Kind:        strings.ToLower(strings.TrimSpace(row.Kind)),
			Pattern:     row.Pattern,
			MatchType:   strings.ToLower(strings.TrimSpace(row.MatchType)),
			ValueMasked: row.ValueMasked,
			ExpiresAt:   row.ExpiresAt,
		}
		if rule.ValueMasked == "" {
			rule.ValueMasked = MaskBlacklistValue(rule.Kind, rule.Pattern, rule.MatchType)
		}
		if !snapshot.addRule(rule) {
			snapshot.SkippedCount++
		}
	}
	snapshot.RuleCount = len(snapshot.ipExact) + len(snapshot.ipCIDR) +
		len(snapshot.phone) + len(snapshot.wechat) + len(snapshot.region)
	return snapshot, nil
}

// addRule 把一条规则放进对应索引；返回 false 表示这条行无法使用（已跳过）。
//
// 单条坏行只跳过它自己：不能因为运营手工插入了一条畸形 CIDR，
// 就让整张表的规则全部失效（那也是 fail-open 的反面）。
func (s *BlacklistSnapshot) addRule(rule *BlacklistRule) bool {
	switch rule.Kind {
	case BlacklistKindIP:
		switch rule.MatchType {
		case BlacklistMatchCIDR, BlacklistMatchPrefix:
			_, network, err := net.ParseCIDR(strings.TrimSpace(rule.Pattern))
			if err != nil || network == nil {
				logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的 CIDR %q 无法解析，已跳过", rule.EntryID, rule.Pattern))
				return false
			}
			s.ipCIDR = append(s.ipCIDR, BlacklistCIDRRule{Network: network, Rule: rule})
			return true
		default:
			ip := net.ParseIP(strings.TrimSpace(rule.Pattern))
			if ip == nil {
				logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的 IP %q 无法解析，已跳过", rule.EntryID, rule.Pattern))
				return false
			}
			s.ipExact[ip.String()] = rule
			return true
		}
	case BlacklistKindPhone:
		normalized, err := NormalizeMainlandPhoneInput(rule.Pattern)
		if err != nil {
			logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的手机号无法归一化，已跳过", rule.EntryID))
			return false
		}
		s.phone[BlacklistPatternHashBytes(normalized)] = rule
		return true
	case BlacklistKindWechat:
		normalized := strings.TrimSpace(rule.Pattern)
		if normalized == "" {
			logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的微信 OpenID 为空，已跳过", rule.EntryID))
			return false
		}
		s.wechat[BlacklistPatternHashBytes(normalized)] = rule
		return true
	case BlacklistKindRegion:
		// 地区规则**不参与判定**（BL-4 未接入）。
		//
		// 处置：**跳过 + 告警**，不静默接受。
		// 为什么不静默收进 region 切片：那样一条手工插入的 kind='region' 行会
		// 看起来「已加载」，运营以为规则生效了，而实际上永远不命中 ——
		// 这与 NormalizeBlacklistValue 拒绝 region 输入是同一个原则：
		// 静默无效比明确失败更糟。
		//
		// 注意这里仍然只是 return false（跳过这一条），**不会**让整次加载失败：
		// 单条脏数据绝不能让整个黑名单瘫痪（否则一条脏行 = 全站放开）。
		logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的类型是 region，但地区匹配（BL-4）尚未接入判定，已跳过该条", rule.EntryID))
		return false
	default:
		logger.SysWarn(fmt.Sprintf("黑名单规则 %d 的类型 %q 未知，已跳过", rule.EntryID, rule.Kind))
		return false
	}
}

// BlacklistCache 是进程级缓存（只持有不可变快照的指针）。
type BlacklistCache struct {
	snapshot atomic.Pointer[BlacklistSnapshot]

	// forceEnabled 只在 NewBlacklistCacheWithSwitch 里设置：
	// 表示「开关为开但没有规则快照」这一 fail-open 状态（见该函数注释）。
	forceEnabled bool

	mu sync.Mutex
	// loadErr 是最近一次加载的错误（保留旧快照时用它判断当前处于降级状态）。
	loadErr error
	// loads 是成功加载次数、failures 是失败次数，只用于诊断与测试断言。
	loads    int64
	failures int64
}

// NewBlacklistCache 返回一个空缓存。
//
// **初始状态是「不可用」**（没有快照）：在还没有成功加载过的情况下，
// 中间件按 fail-open 放行，绝不会因为「缓存还没准备好」而拦人。
func NewBlacklistCache() *BlacklistCache {
	return &BlacklistCache{}
}

// NewBlacklistCacheWithSwitch 构造一个「开关已知为开、但规则快照不可用」的缓存。
//
// 这正是 46-*.md §3.4 的 fail-open 状态：我们知道运营打开了黑名单，
// 却拿不到规则（数据库抖动 / 迁移未完成 / 快照尚未加载）。
// 该状态下的正确处置是**放行 + 打 SysWarn + 写 action='logged_only' 留痕**，
// 而不是拦住所有人 —— 「查不到就拦」等于「数据库抖动时全站 403」。
//
// 生产路径不会主动构造它（正常加载会同时拿到开关与规则）；它存在是为了让
// 「fail-open 必须留痕」这条能被直接驱动和断言，而不是只能靠事故验证。
func NewBlacklistCacheWithSwitch(enabled bool) *BlacklistCache {
	cache := &BlacklistCache{}
	cache.forceEnabled = enabled
	return cache
}

// Reload 全量重载；失败时**保留旧快照**并返回错误。
//
// 「保留旧快照」是这条函数的唯一重点：DB 抖动时规则应该继续按上次成功的快照生效，
// 而不是因为刷新失败就整站放开（那是 fail-open 被误用的方向）。
func (c *BlacklistCache) Reload(db *gorm.DB) error {
	if c == nil {
		return ErrBlacklistCacheNoDatabase
	}
	previous := c.snapshot.Load()
	if db == nil && previous == nil {
		return ErrBlacklistCacheNoDatabase
	}
	snapshot, err := LoadBlacklistSnapshot(db, time.Now())
	if err != nil {
		c.mu.Lock()
		c.loadErr = err
		c.failures++
		c.mu.Unlock()
		if previous != nil {
			logger.SysWarn(fmt.Sprintf("黑名单缓存刷新失败，继续沿用上次成功的快照（%d 条规则）：%v",
				previous.RuleCount, err))
		} else {
			logger.SysWarn(fmt.Sprintf("黑名单缓存首次加载失败，当前处于 fail-open（放行）状态：%v", err))
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
// 失败时同样保留旧快照 —— 「刚改完规则但 DB 抖了一下」不该让规则整批失效。
func (c *BlacklistCache) Invalidate(db *gorm.DB) error {
	return c.Reload(db)
}

// Snapshot 返回当前快照（可能为 nil = 从未成功加载）。
func (c *BlacklistCache) Snapshot() *BlacklistSnapshot {
	if c == nil {
		return nil
	}
	return c.snapshot.Load()
}

// Available 表示缓存是否已经有过一次成功加载。
//
// false → 中间件 fail-open 放行并留痕。true → 按快照判定（即使最近一次刷新失败，
// 旧的快照仍然算「可用」，因为它是上次真实读到的规则）。
func (c *BlacklistCache) Available() bool {
	snapshot := c.Snapshot()
	return snapshot != nil
}

// Enabled 返回黑名单总开关的当前值（来自最近一次成功加载的快照）。
//
// **没有快照时返回 false**：这是「开关默认 false」在缓存层的落点 ——
// 未初始化/加载失败时不能拦人，中间件因此零查询、零日志。
// 唯一例外是 NewBlacklistCacheWithSwitch 构造的 fail-open 探针（见该函数注释）。
func (c *BlacklistCache) Enabled() bool {
	if c == nil {
		return false
	}
	if snapshot := c.Snapshot(); snapshot != nil {
		return snapshot.Enabled
	}
	return c.forceEnabled
}

// Stats 返回诊断用的计数与最近一次加载错误。
func (c *BlacklistCache) Stats() (loads int64, failures int64, lastErr error) {
	if c == nil {
		return 0, 0, ErrBlacklistCacheNoDatabase
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.failures, c.loadErr
}

// BlacklistCacheRefreshInterval 是后台全量重载的周期（46-*.md §4：60s）。
//
// 多实例部署时，A 实例改规则只能靠 B 实例的周期重载收敛；单实例则靠 Invalidate 立即生效。
const BlacklistCacheRefreshInterval = 60 * time.Second

// StartBlacklistCacheRefresh 启动后台周期重载，返回停止函数（幂等）。
//
// 停止函数会等待 goroutine 退出后再返回，避免测试收尾时与下一次 Reload 竞争。
func StartBlacklistCacheRefresh(cache *BlacklistCache, db *gorm.DB, interval time.Duration) func() {
	if cache == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = BlacklistCacheRefreshInterval
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
	defaultBlacklistCache     = NewBlacklistCache()
	defaultBlacklistCacheOnce sync.Once
)

// DefaultBlacklistCache 返回进程级单例缓存。
func DefaultBlacklistCache() *BlacklistCache {
	defaultBlacklistCacheOnce.Do(func() {
		if defaultBlacklistCache == nil {
			defaultBlacklistCache = NewBlacklistCache()
		}
	})
	return defaultBlacklistCache
}

// EnsureDefaultBlacklistCacheStarted 让单例缓存绑定到给定数据库：首次同步加载一次，
// 并启动 60 秒后台全量重载（多实例场景下其它实例的改动靠它收敛）。
//
// **幂等且可重绑定**：同一个 *gorm.DB 重复调用是空操作；换成另一个 DB（测试里每个用例
// 都新建 manager，生产上只有一次）时会先停掉旧的后台 goroutine 再启动新的，
// 因此不会出现「旧 goroutine 抱着已关闭的连接每 60 秒刷一条 SysWarn」。
//
// 首次加载**同步**做一次：让「启动时就已打开开关」的场景不用等 60 秒。
// 加载失败只记日志（Reload 内部已打 SysWarn），不阻断启动 —— 黑名单是增强功能。
func EnsureDefaultBlacklistCacheStarted(db *gorm.DB) *BlacklistCache {
	cache := DefaultBlacklistCache()
	if db == nil {
		return cache
	}

	defaultBlacklistCacheMu.Lock()
	if defaultBlacklistCacheDB == db && stopDefaultBlacklistCacheRefresh != nil {
		defaultBlacklistCacheMu.Unlock()
		return cache
	}
	stop := stopDefaultBlacklistCacheRefresh
	defaultBlacklistCacheMu.Unlock()
	if stop != nil {
		stop() // 等旧 goroutine 真正退出，避免它与新 DB 的加载并发写同一缓存。
	}

	_ = cache.Reload(db)

	defaultBlacklistCacheMu.Lock()
	defaultBlacklistCacheDB = db
	stopDefaultBlacklistCacheRefresh = StartBlacklistCacheRefresh(cache, db, BlacklistCacheRefreshInterval)
	defaultBlacklistCacheMu.Unlock()
	return cache
}

var (
	defaultBlacklistCacheMu          sync.Mutex
	defaultBlacklistCacheDB          *gorm.DB
	stopDefaultBlacklistCacheRefresh func()
)

// ReloadDefaultBlacklistCache 立即重载单例缓存（管理员改规则/改开关后调用）。
//
// 返回错误供调用方决定是否提示运营「规则已写入但缓存刷新失败」；
// 失败时旧快照仍然生效，不会出现「规则失效」的窗口。
func ReloadDefaultBlacklistCache(db *gorm.DB) error {
	return DefaultBlacklistCache().Reload(db)
}

// 命中日志聚合（46-*.md §2.2）。
//
// 被刷时同一个 entry + 同一个客户端 IP 可能在几秒内命中上百次。若每次命中都插一行，
// 这张表会被撑爆。因此这里做**窗口聚合**：窗口内只插一行，后续命中累加 hit_count。
//
// 窗口是**滑动**的：每次都把窗口末尾往后推，于是持续的攻击始终收敛在同一行
// （而不是每 60 秒新起一行）。代价是 hit_count 会一直涨——这正是运营想看的。

// BlacklistHitRecorder 是命中日志的聚合写入器（进程级，并发安全）。
type BlacklistHitRecorder struct {
	mu      sync.Mutex
	slots   map[blacklistHitKey]*blacklistHitSlot
	window  time.Duration
	now     func() time.Time
	lastGC  time.Time
	maxSlot int
}

type blacklistHitKey struct {
	entryID  int64
	clientIP string
	action   string
}

type blacklistHitSlot struct {
	rowID     int64
	windowEnd time.Time
}

// 单个进程内最多保留多少个聚合槽位。超限时先做一次过期清理，仍然超限就**放弃聚合**
// （直接插一行），宁可多几行也不能让内存无界增长。
const defaultBlacklistHitMaxSlots = 4096

// NewBlacklistHitRecorder 返回默认窗口（60s）的聚合器。
func NewBlacklistHitRecorder() *BlacklistHitRecorder {
	return &BlacklistHitRecorder{
		slots:   make(map[blacklistHitKey]*blacklistHitSlot),
		window:  BlacklistHitAggregateWindow,
		now:     time.Now,
		maxSlot: defaultBlacklistHitMaxSlots,
	}
}

// Record 写一条命中（必要时聚合成 UPDATE hit_count+1）。
//
// 返回 nil 表示写入/累加成功；error 表示写库失败（调用方只记日志，不影响请求处置）。
func (r *BlacklistHitRecorder) Record(db *gorm.DB, hit entity.BlacklistHit) error {
	if db == nil {
		return ErrBlacklistCacheNoDatabase
	}
	if r == nil {
		return errors.New("黑名单命中聚合器未初始化")
	}
	hit.Path = truncateBlacklistRunes(hit.Path, 255)
	hit.ValueMasked = truncateBlacklistRunes(hit.ValueMasked, 191)
	hit.ClientIP = truncateBlacklistRunes(hit.ClientIP, 64)
	if hit.Kind == "" {
		hit.Kind = BlacklistKindIP
	}

	key := blacklistHitKey{entryID: hit.EntryID, clientIP: hit.ClientIP, action: hit.Action}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.collectGarbageLocked(now)

	if slot, ok := r.slots[key]; ok && now.Before(slot.windowEnd) {
		// 窗口内：只累加，不新增行。
		err := db.Model(&entity.BlacklistHit{}).Where("id = ?", slot.rowID).
			UpdateColumn("hit_count", gorm.Expr("hit_count + 1")).Error
		if err != nil {
			// 行可能被清理任务删掉了（BL-6）：丢掉这个槽位，下次命中重新插一行。
			delete(r.slots, key)
			return err
		}
		// 滑动窗口：持续命中始终收敛在同一行。
		slot.windowEnd = now.Add(r.window)
		return nil
	}

	if len(r.slots) >= r.maxSlot {
		r.purgeExpiredLocked(now)
		if len(r.slots) >= r.maxSlot {
			// 仍然超限：放弃聚合，直接插一行（不记槽位），保证内存有界。
			hit.HitCount = 1
			hit.CreatedAt = now
			return db.Create(&hit).Error
		}
	}

	hit.HitCount = 1
	hit.CreatedAt = now
	if err := db.Create(&hit).Error; err != nil {
		return err
	}
	r.slots[key] = &blacklistHitSlot{rowID: hit.ID, windowEnd: now.Add(r.window)}
	return nil
}

// SlotCount 返回当前聚合槽位数（测试断言用）。
func (r *BlacklistHitRecorder) SlotCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.slots)
}

// collectGarbageLocked 按窗口节流清理过期槽位。
//
// 不每次都全表扫：攻击流量下 slots 可能上千，但清理本身是 O(槽位数)，
// 每 60 秒做一次足够（过期槽位本来就只在「下一次同键命中」时才有意义）。
func (r *BlacklistHitRecorder) collectGarbageLocked(now time.Time) {
	if len(r.slots) == 0 {
		return
	}
	if !r.lastGC.IsZero() && now.Sub(r.lastGC) < r.window {
		return
	}
	r.purgeExpiredLocked(now)
	r.lastGC = now
}

func (r *BlacklistHitRecorder) purgeExpiredLocked(now time.Time) {
	for key, slot := range r.slots {
		if slot == nil || !now.Before(slot.windowEnd) {
			delete(r.slots, key)
		}
	}
}

// truncateBlacklistRunes 按**字符**截断（AGENTS.md 第 1 条：判定与切片同单位）。
func truncateBlacklistRunes(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes])
}
