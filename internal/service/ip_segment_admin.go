package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// IP 段归属信息管理（task_doc/71 §3.5）。
//
// 本文件只做「管理面」的读与维护：列表、按 IP 定位所在段、删除、刷新。
// 查询链路的段命中在 ip_segment.go 的 FindIPSegmentWithDB / lookupIPLocationFromSegments。

// IPSegmentListQuery 是列表筛选条件。
type IPSegmentListQuery struct {
	// Keyword 匹配 region / isp / llc / asn / 段文本（begin_text / end_text）。
	//
	// ⚠️ 不做「按 IP 值模糊搜索」—— IP 是本表的结构化主数据，按值查应该用
	// IPSegmentLookup（精确落在哪一段），而不是把点分十进制当文本 LIKE。
	Keyword string
	// ASN / LLC / Source 是精确匹配筛选（前端下拉用）。
	ASN    string
	LLC    string
	Source string
}

// IPSegmentListResult 是分页结果。
type IPSegmentListResult struct {
	Items    []entity.IPSegment `json:"items"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
	Summary  IPSegmentSummary   `json:"summary"`
}

// IPSegmentSummary 是列表页的汇总卡片。
type IPSegmentSummary struct {
	// SegmentCount 是当前筛选范围内的段数。
	SegmentCount int64 `json:"segmentCount"`
	// CoveredAddresses 是这些段覆盖的地址总数（各段 end-begin+1 之和）。
	//
	// 注意：段之间可能重叠，所以这个数是**上界**，不是去重后的实际覆盖数。
	// 字段名与注释都在强调这一点，避免被当成「全球独立 IP 数」误读。
	CoveredAddresses int64 `json:"coveredAddresses"`
	// ASNCount 是不同 ASN 的数量。
	ASNCount int64 `json:"asnCount"`
	// LatestFetchedAt 是最近一次刷新时间；零值表示没有记录。
	LatestFetchedAt *time.Time `json:"latestFetchedAt,omitempty"`
}

// ListIPSegmentsWithDB 分页列出 IP 段。
func ListIPSegmentsWithDB(db *gorm.DB, query IPSegmentListQuery, page int, pageSize int) (IPSegmentListResult, error) {
	result := IPSegmentListResult{Items: []entity.IPSegment{}, Summary: IPSegmentSummary{}}
	if db == nil {
		return result, fmt.Errorf("数据库未初始化")
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return result, ErrIPSegmentSchemaUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	result.Page, result.PageSize = page, pageSize

	base := db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{})
	base = applyIPSegmentFilters(base, query)

	if err := base.Count(&result.Total).Error; err != nil {
		return result, err
	}

	rows := make([]entity.IPSegment, 0, pageSize)
	if err := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{}), query).
		Order("begin_ip ASC").Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return result, err
	}
	result.Items = rows

	// 汇总独立算一次：不能只统计当前页，否则「段总数」会随翻页变化。
	if err := buildIPSegmentSummary(db, query, &result.Summary); err != nil {
		// 汇总失败不影响列表可用性。
		return result, nil
	}
	return result, nil
}

func applyIPSegmentFilters(query *gorm.DB, filter IPSegmentListQuery) *gorm.DB {
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where(
			"region LIKE ? OR isp LIKE ? OR llc LIKE ? OR asn LIKE ? OR begin_text LIKE ? OR end_text LIKE ?",
			like, like, like, like, like, like,
		)
	}
	if asn := strings.TrimSpace(filter.ASN); asn != "" {
		query = query.Where("asn = ?", asn)
	}
	if llc := strings.TrimSpace(filter.LLC); llc != "" {
		query = query.Where("llc = ?", llc)
	}
	if source := strings.TrimSpace(filter.Source); source != "" {
		query = query.Where("source = ?", source)
	}
	return query
}

func buildIPSegmentSummary(db *gorm.DB, filter IPSegmentListQuery, summary *IPSegmentSummary) error {
	if err := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{}), filter).
		Count(&summary.SegmentCount).Error; err != nil {
		return err
	}
	// coveredAddresses：各段 (end-begin+1) 之和。SQLite/MySQL 都支持该算术聚合。
	var covered struct{ Total *int64 }
	if err := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{}), filter).
		Select("SUM(CAST(end_ip AS INTEGER) - CAST(begin_ip AS INTEGER) + 1) AS total").
		Scan(&covered).Error; err != nil {
		return err
	}
	if covered.Total != nil {
		summary.CoveredAddresses = *covered.Total
	}
	var asnCount struct{ Total *int64 }
	if err := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{}), filter).
		Where("asn <> ''").
		Select("COUNT(DISTINCT asn) AS total").
		Scan(&asnCount).Error; err != nil {
		return err
	}
	if asnCount.Total != nil {
		summary.ASNCount = *asnCount.Total
	}
	var latest struct{ Value *time.Time }
	if err := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}).Model(&entity.IPSegment{}), filter).
		Select("MAX(fetched_at) AS value").
		Scan(&latest).Error; err == nil && latest.Value != nil {
		summary.LatestFetchedAt = latest.Value
	}
	return nil
}

// IPSegmentLookupResult 是「某个 IP 落在哪一段」的答案。
type IPSegmentLookupResult struct {
	// IP 是查询的原始输入。
	IP string `json:"ip"`
	// Matched 表示是否命中段表。false 时 Segment 为零值。
	Matched bool `json:"matched"`
	// Segment 是命中的段（多段重叠时取最窄的，见 FindIPSegmentWithDB）。
	Segment *entity.IPSegment `json:"segment,omitempty"`
	// Reason 只在未命中时出现，说明为什么没命中（IPv6 / 非法 / 无覆盖）。
	Reason string `json:"reason,omitempty"`
}

// LookupIPSegmentWithDB 回答「这个 IP 属于哪一段」。
//
// 与 FindIPSegmentWithDB 的区别：本函数把「未命中」的原因也讲清楚，
// 便于管理员在管理页上理解「为什么这个 IP 查不到段」。
func LookupIPSegmentWithDB(db *gorm.DB, ip string) (IPSegmentLookupResult, error) {
	result := IPSegmentLookupResult{IP: strings.TrimSpace(ip)}
	if db == nil {
		return result, fmt.Errorf("数据库未初始化")
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return result, ErrIPSegmentSchemaUnavailable
	}
	if result.IP == "" {
		result.Reason = "请输入 IP"
		return result, nil
	}
	if _, ok := ipv4ToUint32(result.IP); !ok {
		// 与段表边界一致：IPv6 与非 IPv4 形式不覆盖。
		result.Reason = "仅支持 IPv4；IPv6 与非法地址不参与段归属"
		return result, nil
	}
	segment, err := FindIPSegmentWithDB(db, result.IP)
	if err != nil {
		return result, err
	}
	if segment == nil {
		result.Reason = "该 IP 暂无段归属记录（尚未查询过，或其所在段未被上游返回）"
		return result, nil
	}
	result.Matched = true
	result.Segment = segment
	return result, nil
}

// DeleteIPSegmentWithDB 删除一条段记录。
//
// 删除只影响「段级缓存」，不影响单 IP 缓存 —— 后者由 location_cache_entries 管。
// 删掉后该段的 IP 会重新走上游查询，这是管理员刷新段数据的正常手段。
func DeleteIPSegmentWithDB(db *gorm.DB, id int64) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if id <= 0 {
		return fmt.Errorf("段参数错误")
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return ErrIPSegmentSchemaUnavailable
	}
	tx := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Delete(&entity.IPSegment{})
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return fmt.Errorf("段记录不存在")
	}
	return nil
}

// DeleteIPSegmentsBatchWithDB 按筛选条件批量删除。
//
// 返回删除行数。**必须带至少一个筛选条件** —— 无条件的「全删」不该由这个接口做，
// 那是清库操作，应该走独立的、带二次确认的入口。
func DeleteIPSegmentsBatchWithDB(db *gorm.DB, filter IPSegmentListQuery) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("数据库未初始化")
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return 0, ErrIPSegmentSchemaUnavailable
	}
	if strings.TrimSpace(filter.Keyword) == "" && strings.TrimSpace(filter.ASN) == "" &&
		strings.TrimSpace(filter.LLC) == "" && strings.TrimSpace(filter.Source) == "" {
		return 0, fmt.Errorf("批量删除必须至少指定一个筛选条件")
	}
	tx := applyIPSegmentFilters(db.Session(&gorm.Session{NewDB: true}), filter).Delete(&entity.IPSegment{})
	if tx.Error != nil {
		return 0, tx.Error
	}
	return tx.RowsAffected, nil
}

// RefreshIPSegmentWithDB 用该段的 SampleIP 重新查一次上游，更新段信息。
//
// 为什么用 SampleIP 而不是段内任意地址：SampleIP 是「产生这条记录的原始查询」，
// 用它重查能得到与上次同源的响应，避免不同地址返回不同精度导致段边界漂移。
//
// 上游不再返回段信息（例如该段被上游合并/拆分）时：本函数**不删除旧行**，
// 只更新已有字段。删除需要管理员显式操作 —— 自动删除会让「一次上游抖动」
// 变成「段数据凭空消失」。
func RefreshIPSegmentWithDB(db *gorm.DB, id int64, options LocationQueryOptions) (*entity.IPSegment, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	if id <= 0 {
		return nil, fmt.Errorf("段参数错误")
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return nil, ErrIPSegmentSchemaUnavailable
	}
	var current entity.IPSegment
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&current).Error; err != nil {
		return nil, fmt.Errorf("段记录不存在")
	}
	if strings.TrimSpace(current.SampleIP) == "" {
		return nil, fmt.Errorf("该段缺少样本 IP，无法刷新")
	}

	// 复用既有的单 IP 刷新链路：它会调上游、写单 IP 缓存，并顺带写段表。
	if _, err := RefreshLocationCacheWithDB(db, entity.LocationKindIP, current.SampleIP, options); err != nil {
		return nil, err
	}

	// 段表可能被刷新成「同段更新」或「产生了新段」。回读本次 id 的最新状态；
	// 若已被 upsert 到别的行（段边界变了），返回该 SampleIP 当前所属段。
	updated, err := FindIPSegmentWithDB(db, current.SampleIP)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return &current, nil
	}
	return updated, nil
}
