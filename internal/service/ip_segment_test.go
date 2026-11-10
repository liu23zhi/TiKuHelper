package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// task_doc/71 IP 段归属信息的解析与区间查询。

// nowForTest 返回一个带时间的稳定值（段表的 FetchedAt 是 not null）。
func nowForTest() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }

func openIPSegmentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 与 internal/service/location_test.go 同一套 driver 与 DSN 形态：
	// glebarez/sqlite 是纯 Go 实现，无需 cgo。
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.IPSegment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestParseIPSegmentFromInfo 覆盖上游真实样例与各种不可用输入。
func TestParseIPSegmentFromInfo(t *testing.T) {
	t.Run("commercial_sample", func(t *testing.T) {
		// 用户提供的商用查询真实样例。
		info := uapi.IPInfo{
			IP: "120.239.173.110", BeginIP: "120.239.173.102", EndIP: "120.239.173.112",
			Region: "中国 广东 中山", ISP: "China Mobile Communications Group Co., Ltd.",
			ASN: "AS9808", LLC: "移动", Latitude: 22.52522, Longitude: 113.378835,
		}
		seg, ok := ParseIPSegmentFromInfo("120.239.173.110", info, "commercial", `{"ip":"120.239.173.110"}`)
		if !ok {
			t.Fatal("commercial sample must produce a segment")
		}
		if seg.BeginText != "120.239.173.102" || seg.EndText != "120.239.173.112" {
			t.Fatalf("text mismatch: %+v", seg)
		}
		// 数值必须单调：begin < end，且等于点分十进制的数值还原。
		if seg.BeginIP >= seg.EndIP {
			t.Fatalf("begin must be < end: %d >= %d", seg.BeginIP, seg.EndIP)
		}
		if seg.ASN != "AS9808" || seg.LLC != "移动" {
			t.Fatalf("asn/llc mismatch: %+v", seg)
		}
		if seg.Latitude == nil || *seg.Latitude != 22.52522 {
			t.Fatalf("latitude must be preserved: %+v", seg.Latitude)
		}
	})

	t.Run("standard_sample", func(t *testing.T) {
		info := uapi.IPInfo{
			IP: "8.8.8.8", BeginIP: "8.8.8.0", EndIP: "8.8.8.255",
			Region: "美国", ISP: "GOOGLE", LLC: "GOOGLE", ASN: "AS15169",
			Latitude: 37.751, Longitude: -97.822,
		}
		seg, ok := ParseIPSegmentFromInfo("8.8.8.8", info, "standard", "")
		if !ok {
			t.Fatal("standard sample must produce a segment")
		}
		// 8.8.8.0 = 0x08080800
		if seg.BeginIP != 0x08080800 {
			t.Fatalf("begin numeric mismatch: %#x", seg.BeginIP)
		}
		if seg.EndIP != 0x080808FF {
			t.Fatalf("end numeric mismatch: %#x", seg.EndIP)
		}
		// 负经度必须保住符号。
		if seg.Longitude == nil || *seg.Longitude != -97.822 {
			t.Fatalf("negative longitude lost: %+v", seg.Longitude)
		}
	})

	t.Run("ipv6_not_applicable", func(t *testing.T) {
		info := uapi.IPInfo{BeginIP: "2001:db8::1", EndIP: "2001:db8::ffff"}
		if _, ok := ParseIPSegmentFromInfo("2001:db8::1", info, "standard", ""); ok {
			t.Fatal("IPv6 must not enter the segment table")
		}
	})

	t.Run("missing_fields", func(t *testing.T) {
		info := uapi.IPInfo{Region: "美国"} // 没有 beginip/endip
		if _, ok := ParseIPSegmentFromInfo("1.2.3.4", info, "standard", ""); ok {
			t.Fatal("missing begin/end must be skipped")
		}
	})

	t.Run("begin_after_end", func(t *testing.T) {
		info := uapi.IPInfo{BeginIP: "8.8.8.255", EndIP: "8.8.8.0"}
		if _, ok := ParseIPSegmentFromInfo("8.8.8.8", info, "standard", ""); ok {
			t.Fatal("begin > end must be rejected")
		}
	})

	t.Run("zero_coordinates_are_nil", func(t *testing.T) {
		// 上游缺经纬度时是 0，必须落成 nil 而不是「赤道/本初子午线上的真实坐标」。
		info := uapi.IPInfo{BeginIP: "10.0.0.0", EndIP: "10.0.0.255", Latitude: 0, Longitude: 0}
		seg, ok := ParseIPSegmentFromInfo("10.0.0.1", info, "standard", "")
		if !ok {
			t.Fatal("must parse")
		}
		if seg.Latitude != nil || seg.Longitude != nil {
			t.Fatalf("zero coordinates must stay nil: %+v %+v", seg.Latitude, seg.Longitude)
		}
	})
}

// TestIPv4Uint32ComparisonIsNumeric 守住「不能用字符串比较」这条红线。
//
// 字符串比较下 '9.0.0.0' > '10.0.0.0'（首字符 '9' > '1'），区间查询会全错。
// 数值比较下 9.0.0.0 < 10.0.0.0。
func TestIPv4Uint32ComparisonIsNumeric(t *testing.T) {
	nine, ok1 := ipv4ToUint32("9.0.0.0")
	ten, ok2 := ipv4ToUint32("10.0.0.0")
	if !ok1 || !ok2 {
		t.Fatal("parse failed")
	}
	if !(nine < ten) {
		t.Fatalf("numeric comparison broken: 9.0.0.0=%d 10.0.0.0=%d", nine, ten)
	}
	// 同时确认字符串比较确实是反的 —— 这条注释与实际一致。
	if !("9.0.0.0" > "10.0.0.0") {
		t.Fatal("fixture assumption changed: string compare is no longer inverted")
	}
}

// TestUpsertAndFindIPSegment 验证幂等写入与区间命中。
func TestUpsertAndFindIPSegment(t *testing.T) {
	db := openIPSegmentTestDB(t)

	seg := entity.IPSegment{
		BeginIP: 0x08080800, EndIP: 0x080808FF,
		BeginText: "8.8.8.0", EndText: "8.8.8.255",
		SampleIP: "8.8.8.8", Region: "美国", ASN: "AS15169", Source: "standard",
		FetchedAt: nowForTest(),
	}
	if err := UpsertIPSegmentsWithDB(db, []entity.IPSegment{seg}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 段内地址必须命中。
	for _, ip := range []string{"8.8.8.0", "8.8.8.8", "8.8.8.255"} {
		got, err := FindIPSegmentWithDB(db, ip)
		if err != nil {
			t.Fatalf("find %s: %v", ip, err)
		}
		if got == nil {
			t.Fatalf("%s must hit the segment", ip)
		}
		if got.ASN != "AS15169" {
			t.Fatalf("%s wrong segment: %+v", ip, got)
		}
	}

	// 段外地址必须不命中（边界两侧各一条）。
	for _, ip := range []string{"8.8.7.255", "8.8.9.0"} {
		got, err := FindIPSegmentWithDB(db, ip)
		if err != nil {
			t.Fatalf("find %s: %v", ip, err)
		}
		if got != nil {
			t.Fatalf("%s must NOT hit the segment, got %+v", ip, got)
		}
	}

	// 幂等：同段同 source 再写一次不新增行，只更新字段。
	updated := seg
	updated.Region = "美国 GOOGLE"
	updated.FetchedAt = nowForTest()
	if err := UpsertIPSegmentsWithDB(db, []entity.IPSegment{updated}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("upsert must be idempotent, got %d rows", count)
	}
	got, err := FindIPSegmentWithDB(db, "8.8.8.8")
	if err != nil || got == nil {
		t.Fatalf("find after update: %v %+v", err, got)
	}
	if got.Region != "美国 GOOGLE" {
		t.Fatalf("update not applied: %+v", got)
	}
}

// TestFindIPSegmentPrefersNarrowest 守住「多段重叠时取最窄」的规则。
func TestFindIPSegmentPrefersNarrowest(t *testing.T) {
	db := openIPSegmentTestDB(t)

	wide := entity.IPSegment{
		BeginIP: 0x08080000, EndIP: 0x0808FFFF, // 8.8.0.0 - 8.8.255.255
		BeginText: "8.8.0.0", EndText: "8.8.255.255",
		SampleIP: "8.8.1.1", Region: "美国（粗）", Source: "standard", FetchedAt: nowForTest(),
	}
	narrow := entity.IPSegment{
		BeginIP: 0x08080800, EndIP: 0x080808FF, // 8.8.8.0 - 8.8.8.255
		BeginText: "8.8.8.0", EndText: "8.8.8.255",
		SampleIP: "8.8.8.8", Region: "美国（精）", Source: "commercial", FetchedAt: nowForTest(),
	}
	if err := UpsertIPSegmentsWithDB(db, []entity.IPSegment{wide, narrow}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := FindIPSegmentWithDB(db, "8.8.8.8")
	if err != nil || got == nil {
		t.Fatalf("find: %v %+v", err, got)
	}
	if got.Region != "美国（精）" {
		t.Fatalf("must prefer the narrowest segment, got %+v", got)
	}

	// 只落在宽段里的地址仍应命中宽段。
	got, err = FindIPSegmentWithDB(db, "8.8.1.1")
	if err != nil || got == nil {
		t.Fatalf("find wide: %v %+v", err, got)
	}
	if got.Region != "美国（粗）" {
		t.Fatalf("wide-only address must hit the wide segment, got %+v", got)
	}
}

// TestFindIPSegmentIgnoresIPv6 确认 IPv6 不进段表、也不误命中原生 IPv4 段。
func TestFindIPSegmentIgnoresIPv6(t *testing.T) {
	db := openIPSegmentTestDB(t)
	got, err := FindIPSegmentWithDB(db, "2001:db8::1")
	if err != nil {
		t.Fatalf("find ipv6: %v", err)
	}
	if got != nil {
		t.Fatalf("IPv6 must not hit IPv4 segments, got %+v", got)
	}
}
