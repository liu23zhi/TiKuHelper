package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 敏感词检测记录的**只读**管理视图（123 号 T-27）。
//
// ⚠️ 这个文件只读；写入链路在 sensitive_words.go，已完整实现并且**只读不改**：
//   - 键是 textHash（sensitive_words.go:267）= sha256(profile + "\x00" + text)，
//     profile 是哈希域分隔前缀（:248），所以「不同 profile 的同一文本」是两个键；
//   - loadSensitiveWordCacheWithDB(:298) 命中即返回，零外呼；
//   - saveSensitiveWordCacheWithDB(:329) 用 OnConflict upsert；
//   - 失败上限 sensitiveWordCacheMaxAttempts=3(:242) 到顶后不再重试。
// ⇒ 「同一文本二次检测不产生上游调用」这条**本来就成立**，本文件不重复实现。
//
// 这里补的只是「列表 + 三态筛选」——controller/sensitive_words.go 里
// 一个 List 类 handler 都没有，所以之前管理端看不到这些记录。
//
// 原文可见性：缓存表**按设计不存原文**（entity/sensitive_word_cache.go:9-11），
// 列里只有 MaskedText。所以「原文不可看」是**后端事实**而不是前端隐藏
// （判据 ② 要求有后端断言 —— 见 sensitive_word_cache_test.go）。

var (
	// ErrSensitiveWordCacheFilterInvalid 表示筛选参数非法（日期格式 / 时间区间 / 状态枚举）。
	ErrSensitiveWordCacheFilterInvalid = errors.New("敏感词检测记录筛选参数无效")
	// ErrSensitiveWordCacheUnavailable 表示数据库不可用。
	ErrSensitiveWordCacheUnavailable = errors.New("敏感词检测记录不可用")
)

// 敏感词检测记录的三种状态。判据 ① 要求按这三态筛选。
//
// 三态与缓存行的对应关系（与 resolveSensitiveWordPayloadWithDB 的决策表逐条对齐）：
//
//	hit       → Error 为空 且 Sensitive = true    （上游判定命中）
//	clean     → Error 为空 且 Sensitive = false   （上游判定未命中）
//	failed    → Error 非空                        （本次检测失败，含「达上限不再重试」）
const (
	SensitiveWordCacheStateHit    = "hit"
	SensitiveWordCacheStateClean  = "clean"
	SensitiveWordCacheStateFailed = "failed"
)

// SensitiveWordCacheListQuery 是管理端的筛选条件。
//
// Keyword 会同时匹配 text_hash / masked_text / api_status / error：
// 因为表里没有原文，管理员只能按这些字段反查（例如找某个错误文案的全部记录）。
type SensitiveWordCacheListQuery struct {
	Keyword     string `json:"keyword"`
	State       string `json:"state"`
	CheckedFrom string `json:"checkedFrom"`
	CheckedTo   string `json:"checkedTo"`
}

// SensitiveWordCacheListItem 是下发给管理端的一条记录。
//
// **刻意不含任何原文字段**：既没有 question/answer，也没有 text。
// 表的列里本来就没有原文，这个结构保证将来有人往查询里加列时不会
// 顺手把原文带出去（原文根本不存在，加了也是空）。
type SensitiveWordCacheListItem struct {
	TextHash     string    `json:"textHash"`
	Sensitive    bool      `json:"sensitive"`
	Words        []string  `json:"words"`
	MaskedText   string    `json:"maskedText"`
	APIStatus    string    `json:"apiStatus"`
	Error        string    `json:"error"`
	AttemptCount int       `json:"attemptCount"`
	CheckedAt    time.Time `json:"checkedAt"`
	// State 是三态归类的结论，直接下发免去前端重复推导（前端推导易与后端漂移）。
	State string `json:"state"`
}

// SensitiveWordCacheListResult 是分页结果。
type SensitiveWordCacheListResult struct {
	Data     []SensitiveWordCacheListItem `json:"data"`
	Total    int64                        `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"pageSize"`
	// States 是当前筛选条件下的三态计数，方便管理员一眼看到分布。
	States SensitiveWordCacheStateCounts `json:"states"`
}

// SensitiveWordCacheStateCounts 是三态计数。
type SensitiveWordCacheStateCounts struct {
	Hit    int64 `json:"hit"`
	Clean  int64 `json:"clean"`
	Failed int64 `json:"failed"`
}

// classifySensitiveWordCacheState 把一行缓存归类成三态之一。
//
// 判定顺序不能反：**Error 非空优先于 Sensitive**。
// 理由与 service 层一致 —— 失败不等于命中，一行既有 Error 又有
// Sensitive=true 是不可能的组合（resolve 里失败分支固定写 Sensitive=false），
// 但脏数据真出现时必须归到 failed，不能因为 Sensitive 被误读成"命中"。
func classifySensitiveWordCacheState(sensitive bool, errorText string) string {
	if strings.TrimSpace(errorText) != "" {
		return SensitiveWordCacheStateFailed
	}
	if sensitive {
		return SensitiveWordCacheStateHit
	}
	return SensitiveWordCacheStateClean
}

// normalizeSensitiveWordCacheState 校验并归一化状态筛选值。
//
// 空串 = 不筛选（返回 ""）。非法值必须**报错而不是静默忽略**：
// 静默忽略会让管理员以为"筛选生效了、就是这些"，实际看到的是全量 ——
// 这是「看起来在筛、其实是没筛」的典型误导。
func normalizeSensitiveWordCacheState(state string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(state))
	switch normalized {
	case "":
		return "", nil
	case SensitiveWordCacheStateHit, SensitiveWordCacheStateClean, SensitiveWordCacheStateFailed:
		return normalized, nil
	}
	return "", fmt.Errorf("%w: 状态只能是 hit / clean / failed", ErrSensitiveWordCacheFilterInvalid)
}

// parseSensitiveWordCacheDate 解析筛选日期（与 OCR 缓存同一口径）。
func parseSensitiveWordCacheDate(value string, endOfDay bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err == nil && endOfDay {
			parsed = parsed.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: 日期格式无效", ErrSensitiveWordCacheFilterInvalid)
	}
	return &parsed, nil
}

func parseSensitiveWordCacheRange(filters SensitiveWordCacheListQuery) (from, to *time.Time, err error) {
	from, err = parseSensitiveWordCacheDate(filters.CheckedFrom, false)
	if err != nil {
		return nil, nil, err
	}
	to, err = parseSensitiveWordCacheDate(filters.CheckedTo, true)
	if err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, fmt.Errorf("%w: 开始时间必须早于结束时间", ErrSensitiveWordCacheFilterInvalid)
	}
	return from, to, nil
}

// applySensitiveWordCacheFilters 把筛选条件落到查询上。
//
// 三态筛选必须用**与 classifySensitiveWordCacheState 完全一致**的 SQL 条件：
// 两处口径一旦漂移，就会出现「列表显示 hit 但按 hit 筛不出来」的诡异现象。
// 所以这里刻意把条件写成同一套判定的直译，并配 test 里的交叉断言。
func applySensitiveWordCacheFilters(query *gorm.DB, filters SensitiveWordCacheListQuery) (*gorm.DB, error) {
	if condition, args := util.BuildInstrCondition(filters.Keyword,
		"COALESCE(text_hash, '')", "COALESCE(masked_text, '')",
		"COALESCE(api_status, '')", "COALESCE(error, '')", "COALESCE(words, '')"); condition != "" {
		query = query.Where(condition, args...)
	}
	state, err := normalizeSensitiveWordCacheState(filters.State)
	if err != nil {
		return nil, err
	}
	switch state {
	case SensitiveWordCacheStateFailed:
		// 失败：error 非空（含空白字符也算非空 —— 与 classify 的 TrimSpace 判定对齐）。
		query = query.Where("TRIM(COALESCE(error, '')) <> ''")
	case SensitiveWordCacheStateHit:
		query = query.Where("TRIM(COALESCE(error, '')) = ''").Where("sensitive = ?", true)
	case SensitiveWordCacheStateClean:
		query = query.Where("TRIM(COALESCE(error, '')) = ''").Where("sensitive = ?", false)
	}
	from, to, err := parseSensitiveWordCacheRange(filters)
	if err != nil {
		return nil, err
	}
	if from != nil {
		query = query.Where("checked_at >= ?", *from)
	}
	if to != nil {
		query = query.Where("checked_at < ?", *to)
	}
	return query, nil
}

// validateSensitiveWordCacheFilters 只做参数校验，不碰 DB。
//
// 单独一个函数而不是「拿 nil 传进 applyFilters 试一下」：
// applyFilters 会对 *gorm.DB 调 Where，db 为 nil 时那里直接 panic
// （gorm getInstance 解引用空 Statement）—— 校验不该依赖调用方的 nil 处理。
func validateSensitiveWordCacheFilters(filters SensitiveWordCacheListQuery) error {
	if _, err := normalizeSensitiveWordCacheState(filters.State); err != nil {
		return err
	}
	_, _, err := parseSensitiveWordCacheRange(filters)
	return err
}

// ListSensitiveWordCacheWithDB 返回敏感词检测记录的管理端分页视图。
//
// 只读：不改任何缓存行。缓存行的写入/重试完全由 sensitive_words.go 负责。
func ListSensitiveWordCacheWithDB(db *gorm.DB, filters SensitiveWordCacheListQuery, page, pageSize int) (SensitiveWordCacheListResult, error) {
	result := SensitiveWordCacheListResult{Data: make([]SensitiveWordCacheListItem, 0)}
	// 先校验筛选参数：非法状态/日期必须 400，而不是拿全量结果糊过去。
	if err := validateSensitiveWordCacheFilters(filters); err != nil {
		return result, err
	}
	if db == nil {
		return result, ErrSensitiveWordCacheUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	result.Page, result.PageSize = page, pageSize
	// 表还没建好（未迁移）时返回空列表而不是报错：管理页要能正常打开。
	if !db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
		return result, nil
	}
	session := db.Session(&gorm.Session{NewDB: true})
	base, err := applySensitiveWordCacheFilters(session.Model(&entity.SensitiveWordCache{}), filters)
	if err != nil {
		return result, err
	}
	if err := base.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var rows []entity.SensitiveWordCache
	if err := base.Order("checked_at DESC, text_hash ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		result.Data = append(result.Data, SensitiveWordCacheListItem{
			TextHash:     row.TextHash,
			Sensitive:    row.Sensitive,
			Words:        decodeSensitiveWordCacheWords(row.Words),
			MaskedText:   row.MaskedText,
			APIStatus:    row.APIStatus,
			Error:        row.Error,
			AttemptCount: row.AttemptCount,
			CheckedAt:    row.CheckedAt,
			State:        classifySensitiveWordCacheState(row.Sensitive, row.Error),
		})
	}
	// 三态计数在**不受 state 筛选**的范围内统计（其余筛选条件保留），
	// 否则筛了 hit 之后 clean/failed 恒为 0，计数就没有参考价值。
	//
	// ⚠️ 每个计数必须**各自重建** base 查询：gorm 的链式 API 在同一个
	// *gorm.DB 上连续 Where 会**累积条件**，复用同一个 countBase 会让
	// 后面的计数继承前面的 `sensitive = ?`，结果是三个计数里只有
	// 第一个对、其余恒 0（实测 Hit:0 Clean:0 Failed:1）。
	countFilters := SensitiveWordCacheListQuery{
		Keyword: filters.Keyword, CheckedFrom: filters.CheckedFrom, CheckedTo: filters.CheckedTo,
	}
	countFor := func(state string) (int64, error) {
		query, err := applySensitiveWordCacheFilters(
			db.Session(&gorm.Session{NewDB: true}).Model(&entity.SensitiveWordCache{}),
			SensitiveWordCacheListQuery{
				Keyword: countFilters.Keyword, State: state,
				CheckedFrom: countFilters.CheckedFrom, CheckedTo: countFilters.CheckedTo,
			},
		)
		if err != nil {
			return 0, err
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return 0, err
		}
		return count, nil
	}
	var counts SensitiveWordCacheStateCounts
	if counts.Failed, err = countFor(SensitiveWordCacheStateFailed); err != nil {
		return result, err
	}
	if counts.Hit, err = countFor(SensitiveWordCacheStateHit); err != nil {
		return result, err
	}
	if counts.Clean, err = countFor(SensitiveWordCacheStateClean); err != nil {
		return result, err
	}
	result.States = counts
	return result, nil
}

// SensitiveWordCacheStatusWithDB 返回缓存表的总量与最近一次检测时间。
//
// 给管理页显示"这张表有没有在动"，与 OCR 缓存的 Status 接口同形。
func SensitiveWordCacheStatusWithDB(db *gorm.DB) (SensitiveWordCacheStateCounts, *time.Time, error) {
	var counts SensitiveWordCacheStateCounts
	if db == nil {
		return counts, nil, ErrSensitiveWordCacheUnavailable
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
		return counts, nil, nil
	}
	countWhere := func(condition string, args ...any) (int64, error) {
		var count int64
		// 每次都从干净会话重建：链式 Where 会累积，复用会串条件。
		query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.SensitiveWordCache{}).Where(condition, args...)
		if err := query.Count(&count).Error; err != nil {
			return 0, err
		}
		return count, nil
	}
	var err error
	if counts.Failed, err = countWhere("TRIM(COALESCE(error, '')) <> ''"); err != nil {
		return counts, nil, err
	}
	if counts.Hit, err = countWhere("TRIM(COALESCE(error, '')) = '' AND sensitive = ?", true); err != nil {
		return counts, nil, err
	}
	if counts.Clean, err = countWhere("TRIM(COALESCE(error, '')) = '' AND sensitive = ?", false); err != nil {
		return counts, nil, err
	}
	var latest entity.SensitiveWordCache
	if err := db.Session(&gorm.Session{NewDB: true}).Order("checked_at DESC").
		Limit(1).Find(&latest).Error; err != nil {
		return counts, nil, err
	}
	if latest.TextHash == "" {
		return counts, nil, nil
	}
	checkedAt := latest.CheckedAt
	return counts, &checkedAt, nil
}
