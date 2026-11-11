package service

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IP 段归属信息（task_doc/71）。
//
// # 这张表解决什么问题
//
// 上游 `network/ipinfo` 一次返回 `beginip` / `endip`，说明**一次查询覆盖一整段**。
// 但既有实现把结果只挂在「查询的那个 IP」上（`location_cache_entries.lookup_key`），
// 于是同一段里第 2、3、…、N 个 IP 都要各自调一次上游 —— 每次都计费。
//
// 本文件负责：
//  1. 把上游响应的段信息解析成 `entity.IPSegment` 并 UPSERT；
//  2. 提供「某 IP 落在哪一段」的区间查询，让段内后续 IP 零调用命中。
//
// 明确的边界（task_doc/71 §3.3）：
//   - 只处理 IPv4。IPv6 是 128 位，uint32 存不下，不写半成品逻辑；
//   - 上游没给 beginip/endip、或解析失败、或 begin > end → 不写段表，
//     只记 SysWarn，**不影响本次查询结果返回**；
//   - 段表写入失败不阻断查询（归属地是辅助信息，不该让主流程失败）。

// ErrIPSegmentInvalid 表示解析出的段不合法（begin > end / 非法地址）。
var ErrIPSegmentInvalid = errors.New("IP 段不合法")

// ErrIPSegmentSchemaUnavailable 表示 ip_segments 表还没建好。
//
// 与 ErrReferralRebateSchemaUnavailable / ErrReviewRebateSchemaUnavailable 同构：
// 结构没就绪时**不能假装成功** —— 管理页会对着空列表以为「没有段数据」，
// 而不是「表还没建」。
var ErrIPSegmentSchemaUnavailable = errors.New("IP 段归属数据表未就绪")

// ipv4ToUint32 把点分十进制 IPv4 转成 uint32。
//
// 返回值 ok=false 表示「不是可用作段边界的形式」：IPv6、非法写法、空串都归入此列。
func ipv4ToUint32(raw string) (uint32, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	addr := net.ParseIP(text)
	if addr == nil {
		return 0, false
	}
	v4 := addr.To4()
	if v4 == nil {
		// IPv6（或其它非 IPv4 形式）→ 不进段表。
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}

// ParseIPSegmentFromInfo 把一次上游查询结果解析成段记录。
//
// 返回 (segment, true) 表示可以写段表；返回 (_, false) 表示这条响应不含可用的段信息
// （IPv6 / 缺字段 / 非法 / begin>end），调用方应静默跳过。
//
// sampleIP 是产生这条响应的查询 IP；source 是查询模式（standard / commercial）；
// rawJSON 是上游原始响应（保留，供日后结构列不全时还原）。
func ParseIPSegmentFromInfo(sampleIP string, info uapi.IPInfo, source string, rawJSON string) (entity.IPSegment, bool) {
	begin, beginOK := ipv4ToUint32(info.BeginIP)
	end, endOK := ipv4ToUint32(info.EndIP)
	if !beginOK || !endOK {
		return entity.IPSegment{}, false
	}
	if begin > end {
		// begin > end 的段会让区间查询匹配到不该匹配的地址，必须拒绝。
		logger.SysWarn(fmt.Sprintf("IP 段起点大于终点，已跳过：sample=%s begin=%s end=%s", sampleIP, info.BeginIP, info.EndIP))
		return entity.IPSegment{}, false
	}
	segment := entity.IPSegment{
		BeginIP:   begin,
		EndIP:     end,
		BeginText: strings.TrimSpace(info.BeginIP),
		EndText:   strings.TrimSpace(info.EndIP),
		SampleIP:  strings.TrimSpace(sampleIP),
		Region:    strings.TrimSpace(info.Region),
		ISP:       strings.TrimSpace(info.ISP),
		LLC:       strings.TrimSpace(info.LLC),
		ASN:       strings.TrimSpace(info.ASN),
		Source:    strings.TrimSpace(source),
		RawJSON:   rawJSON,
		FetchedAt: time.Now(),
	}
	// 经纬度只在「上游确实给了」时才落值 —— 见 entity.IPSegment 的类型注释：
	// 0 会被误认为赤道/本初子午线上的真实坐标，必须与「没有数据」区分。
	if info.Latitude != 0 {
		lat := info.Latitude
		segment.Latitude = &lat
	}
	if info.Longitude != 0 {
		lng := info.Longitude
		segment.Longitude = &lng
	}
	return segment, true
}

// UpsertIPSegmentsWithDB 把段记录写入 ip_segments。
//
// 幂等：唯一键 (begin_ip, end_ip, source) 冲突时只更新可变字段与 fetched_at，
// 不新增行 —— 同一段被反复查到时不应堆积重复记录。
//
// 失败策略：返回错误，但**调用方不应因它中断主流程**（见本文件头部边界说明）。
func UpsertIPSegmentsWithDB(db *gorm.DB, segments []entity.IPSegment) error {
	if db == nil || len(segments) == 0 {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "begin_ip"}, {Name: "end_ip"}, {Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"sample_ip", "begin_text", "end_text", "region", "isp", "llc", "asn",
			"latitude", "longitude", "raw_json", "fetched_at", "updated_at",
		}),
	}).Create(&segments).Error
}

// lookupIPLocationFromSegments 对一批 IP 做段命中查询，返回命中的归属地。
//
// 返回的 map key 是**原始查询键**（调用方传进来的那个字符串），与
// LookupIPLocationsWithDB 的结果 key 语义一致。
//
// 只返回命中的；未命中的由调用方继续走上游。段表出错时返回空 map ——
// 归属地查询是辅助信息，不该因为段表异常让日志列表/管理页整页失败。
func lookupIPLocationFromSegments(db *gorm.DB, keys []string) map[string]IPLocation {
	hits := make(map[string]IPLocation)
	if db == nil || len(keys) == 0 {
		return hits
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		// 迁移还没跑过（老库首次启动的顺序差异）。静默跳过，等迁移就绪后自然生效。
		return hits
	}
	for _, key := range keys {
		segment, err := FindIPSegmentWithDB(db, key)
		if err != nil {
			logger.SysWarn("查询 IP 段归属失败，本次回退到上游查询: " + err.Error())
			continue
		}
		if segment == nil {
			continue
		}
		hits[key] = IPLocation{
			IP:      key,
			Region:  segment.Region,
			ISP:     segment.ISP,
			LLC:     segment.LLC,
			Address: FormatIPLocationAddress(segment.Region, segment.LLC, segment.ISP),
		}
	}
	return hits
}

// PersistIPSegmentsFromCacheRows 从「刚刚查到的缓存行」里提取段信息并写段表。
//
// 调用时机：上游查询成功、缓存行已经落库之后。这样段表与单 IP 缓存来自
// 同一份上游响应，不会出现两者不一致。
//
// 失败只记 SysWarn：段表是优化项，写入失败不该让本次查询失败。
func PersistIPSegmentsFromCacheRows(db *gorm.DB, rows []entity.LocationCache) {
	if db == nil || len(rows) == 0 {
		return
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return
	}
	segments := make([]entity.IPSegment, 0, len(rows))
	for _, row := range rows {
		if row.Kind != entity.LocationKindIP {
			continue
		}
		info := parseIPInfoRawJSON(row.RawJSON)
		segment, ok := ParseIPSegmentFromInfo(row.LookupKey, info, row.Source, row.RawJSON)
		if !ok {
			continue
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return
	}
	if err := UpsertIPSegmentsWithDB(db, segments); err != nil {
		logger.SysWarn("写入 IP 段归属失败（不影响归属地查询）: " + err.Error())
	}
}

// 返回 nil 表示未命中（调用方应回落到上游查询）。
//
// 选择规则：命中多条时取**最窄**的那一条（end-begin 最小），即精度最高的段。
// 同一地址可能同时落在 standard 的大段与 commercial 的小段里 —— 小段的信息更精确。
func FindIPSegmentWithDB(db *gorm.DB, ip string) (*entity.IPSegment, error) {
	if db == nil {
		return nil, nil
	}
	value, ok := ipv4ToUint32(ip)
	if !ok {
		// IPv6 / 非法地址 → 段表不覆盖，交给调用方走上游。
		return nil, nil
	}
	var rows []entity.IPSegment
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("begin_ip <= ? AND end_ip >= ?", value, value).
		Limit(32).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	best := rows[0]
	bestWidth := best.EndIP - best.BeginIP
	for _, row := range rows[1:] {
		if width := row.EndIP - row.BeginIP; width < bestWidth {
			best, bestWidth = row, width
		}
	}
	return &best, nil
}
