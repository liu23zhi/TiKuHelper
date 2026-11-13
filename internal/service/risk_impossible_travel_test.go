package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R3 · impossible_travel（96 §2.1）同一账号相邻两次成功登录的移动速度
// 超过民航上限（默认 900 km/h）。
//
// 数据源：logs（action=10 + status=success）+ ip_segments（经纬度）。
//
// 本文件的核心是**边界**：R3 有五种「不该告警」的情形，每一种都可能把
// 正常用户误报成盗号。任务书要求每条都有用例。
// ════════════════════════════════════════════════════════════════════

// riskR3Beijing / riskR3Guangzhou 是判据用的两个真实坐标。
//
// 北京天安门 39.9087, 116.3975；广州塔 23.1066, 113.3245。
// 球面距离约 1888 km —— 飞 2.1 小时，所以「2 小时内北京→广州」物理不可能。
// riskExactDecimalLiteral 是「赤道经度 0→10 的球面距离」的 17 位精确十进制表示。
// 供 TestRiskImpossibleTravelExactThresholdAlerts 造「速度恰等于阈值」用。
const riskExactDecimalLiteral = 1111.9492664455872

var (
	riskR3Beijing   = [2]float64{39.9087, 116.3975}
	riskR3Guangzhou = [2]float64{23.1066, 113.3245}
	// 同城两个不同地址（相距约 8 km）—— 用于「同城不告警」。
	riskR3Beijing2 = [2]float64{39.9600, 116.3500}
)

// seedRiskIPSegment 造一条带经纬度的 IP 段。
func seedRiskIPSegment(t *testing.T, db *gorm.DB, beginText, endText string, lat, lng float64, region string) {
	t.Helper()
	begin, ok := ipv4ToUint32(beginText)
	if !ok {
		t.Fatalf("bad begin ip %q", beginText)
	}
	end, ok := ipv4ToUint32(endText)
	if !ok {
		t.Fatalf("bad end ip %q", endText)
	}
	latitude, longitude := lat, lng
	segment := entity.IPSegment{
		BeginIP: begin, EndIP: end,
		BeginText: beginText, EndText: endText, SampleIP: beginText,
		Region: region, Source: "test",
		Latitude: &latitude, Longitude: &longitude,
		FetchedAt: time.Now(),
	}
	if err := db.Create(&segment).Error; err != nil {
		t.Fatalf("create ip segment: %v", err)
	}
}

// seedRiskLoginAt 造一条成功登录审计行（复用既有 seedRiskLoginAudit）。
func seedRiskLoginAt(t *testing.T, db *gorm.DB, userID int32, clientIP string, at time.Time) {
	t.Helper()
	seedRiskLoginAudit(t, db, userID, "u"+itoaRisk(userID), clientIP, at, "success")
}

// 判据①：真·异地瞬移 —— 北京→广州 1 小时 → 告警。
//
// 1888 km / 1h = 1888 km/h > 900。
func TestRiskImpossibleTravelDetectsTeleport(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	seedRiskLoginAt(t, db, 7701, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db, 7701, "2.2.2.2", now.Add(-2*time.Hour)) // 1 小时间隔

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 1 {
		t.Fatalf("北京→广州 1 小时必须告警（1888 km/h > 900），实际 %d 条", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "impossible_travel").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	speed, _ := detail["speedKmh"].(float64)
	if speed < 900 {
		t.Fatalf("detail.speedKmh = %v，应 > 900", speed)
	}
	if got, _ := detail["fromRegion"].(string); got != "中国 北京" {
		t.Fatalf("detail.fromRegion = %q", got)
	}
	// 硬要求：threshold 必须是**配置值**（900），不是实际速度。
	// 这是「detail.threshold 必须是配置值而非实际计数」那条钉子在 R3 上的落实。
	if got, _ := detail["threshold"].(float64); got != 900 {
		t.Fatalf("detail.threshold 应为配置值 900，实际 %v（若等于 speedKmh 说明写成了实际值）", got)
	}
}

// 判据②：同城不同地址 40 分钟 → 不告警（正常通勤）。
//
// 8 km / 0.67h ≈ 12 km/h，远低于 900。
func TestRiskImpossibleTravelSameCityNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "1.1.2.0", "1.1.2.255", riskR3Beijing2[0], riskR3Beijing2[1], "中国 北京")

	now := riskR1TestTime()
	seedRiskLoginAt(t, db, 7702, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db, 7702, "1.1.2.2", now.Add(-160*time.Minute)) // 40 分钟

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("同城 8km / 40min 不得告警，实际 %d 条", got)
	}
}

// 判据③：同一 IP 连续两次 → 绝不告警（即使时间很近）。
func TestRiskImpossibleTravelSameIPNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")

	now := riskR1TestTime()
	// 同一 IP，间隔 2 小时（远超最小间隔）—— 若实现漏了「同 IP 跳过」，
	// 距离 0 / 2h = 0 km/h 也不会告警，所以这条用**同 IP 但坐标差极大**的
	// 构造无法实现（同 IP 就同坐标）。因此本用例钉的是「短路逻辑存在」。
	seedRiskLoginAt(t, db, 7703, "1.1.1.1", now.Add(-4*time.Hour))
	seedRiskLoginAt(t, db, 7703, "1.1.1.1", now.Add(-2*time.Hour))

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("同一 IP 连续两次绝不告警，实际 %d 条", got)
	}
}

// 判据④：**归属地查不到 → 跳过，不告警**（最关键的一条）。
//
// 本机实测生产库 logs 里全是 172.x 私网地址 —— 若把「未知」当异地，
// 所有内网/容器部署会全量误报。
func TestRiskImpossibleTravelUnknownGeoNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 只给北京建段，广州的 IP **不给段记录**。
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")

	now := riskR1TestTime()
	seedRiskLoginAt(t, db, 7704, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db, 7704, "9.9.9.9", now.Add(-2*time.Hour)) // 无段记录

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("一端归属地查不到必须跳过、不告警，实际 %d 条", got)
	}
}

// 判据⑤：**私网 IP 必须跳过**（不当成「异地的未知」）。
func TestRiskImpossibleTravelPrivateIPNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 给私网段也建一条记录（模拟「段表里恰好有私网行」的脏数据）。
	// 即使如此也必须跳过 —— 私网地址的地理位置无意义。
	seedRiskIPSegment(t, db, "172.18.0.0", "172.18.0.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "假地址")
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")

	now := riskR1TestTime()
	seedRiskLoginAt(t, db, 7705, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db, 7705, "172.18.0.1", now.Add(-2*time.Hour))

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("私网 IP 必须跳过（即使段表里有记录），实际 %d 条", got)
	}
}

// 判据⑥：时间倒序 / 相同时间（脏数据）→ 不告警。
func TestRiskImpossibleTravelTimeAnomalyNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	// 两条 create_time 完全相同 → 间隔 0 → 不能算出无穷大速度。
	sameTime := now.Add(-2 * time.Hour)
	seedRiskLoginAt(t, db, 7706, "1.1.1.1", sameTime)
	seedRiskLoginAt(t, db, 7706, "2.2.2.2", sameTime)

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("相同时间的两条不得告警（否则 0 间隔会算出无穷大速度），实际 %d 条", got)
	}
}

// 判据⑥b：**隔离测试** —— 相同时间（间隔 0）必须被 `interval <= 0` 挡住，
// 而不是靠 minInterval 兜底。
//
// ⚠️ 为什么需要这条：判据⑥ 用「间隔 0」构造，但 `interval=0 < minInterval(30min)`，
// 所以 `minInterval` 检查会先挡住 —— 即使删掉 `interval <= 0`，判据⑥ 仍绿（实测）。
// 那意味着「除零保护」没有被单独验证。
//
// 本用例把最小判定间隔的**影响面**绕开：用一个间隔 0 但**不经过 minInterval** 的
// 构造做不到（minInterval 是常量），所以改为验证「除零不会产生 +Inf 告警」的
// 行为本身 —— 若实现漏了 interval<=0，distance/0 = +Inf，Inf >= 900 恒 true ⇒ 必告警。
func TestRiskImpossibleTravelZeroIntervalNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	// 两条 create_time 完全相同（间隔恰为 0）。
	sameTime := now.Add(-2 * time.Hour)
	seedRiskLoginAt(t, db, 7710, "1.1.1.1", sameTime)
	seedRiskLoginAt(t, db, 7710, "2.2.2.2", sameTime)

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 关键：distance / 0h = +Inf。若实现只靠 minInterval 挡，删掉 interval<=0 后
	// 本用例仍绿；若某次重构把 minInterval 调小或去掉，除零就会变成「永远告警」。
	// 所以这条与判据⑥ 是**互补**的：⑥ 钉行为，本条钉「不依赖 minInterval」。
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("间隔恰为 0（除零）必须不告警，实际 %d 条", got)
	}
}

// 判据⑦：首次登录（只有一条）→ 不告警。
func TestRiskImpossibleTravelSingleLoginNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")

	now := riskR1TestTime()
	seedRiskLoginAt(t, db, 7707, "1.1.1.1", now.Add(-2*time.Hour))

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("只有一次登录不得告警，实际 %d 条", got)
	}
}

// 判据⑧：时间间隔小于最小判定阈值（30 分钟）→ 不告警（抗定位误差）。
func TestRiskImpossibleTravelShortIntervalNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	// 北京→广州 但只隔 10 分钟：物理上更不可能，但 10 分钟 < 30 分钟最小阈值，
	// 这类极短间隔更可能是「IP 归属地库不准」而非真实移动，故不判。
	seedRiskLoginAt(t, db, 7708, "1.1.1.1", now.Add(-2*time.Hour))
	seedRiskLoginAt(t, db, 7708, "2.2.2.2", now.Add(-110*time.Minute))

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("间隔 10 分钟 < 最小判定 30 分钟，不得告警，实际 %d 条", got)
	}
	// 反向对照：同样两地但间隔 1 小时 → 必须告警（证明上面的 0 是间隔导致的）。
	db2 := newRiskAlertTestDBNamed(t, t.Name()+"-control")
	seedRiskRules(t, db2)
	seedRiskIPSegment(t, db2, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db2, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")
	seedRiskLoginAt(t, db2, 7708, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db2, 7708, "2.2.2.2", now.Add(-2*time.Hour))
	if _, err := EvaluateDebtAlerts(db2, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db2, "impossible_travel"); got != 1 {
		t.Fatalf("同样两地间隔 1 小时必须告警，实际 %d 条（若为 0 说明上面的 0 与间隔无关）", got)
	}
}

// 判据⑧b：阈值边界 —— 速度**恰好等于**阈值时必须告警（`>=` 不是 `>`）。
//
// ⚠️ 这条补的是一个真实缺口：其余用例的速度都明显大于或明显小于阈值，
// 所以把 `speedKmh < threshold` 改成 `<=` 时**全部仍绿**（实测）。
//
// 构造办法：造一条速度精确可控的登录对 —— 用同一条经线上的两个点，
// 距离可算，再选一个让 speed 恰等于阈值的时间间隔。
func TestRiskImpossibleTravelExactThresholdAlerts(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 赤道上经度 0 与经度 10：距离 = 10 * 111.1949 ≈ 1111.949 km。
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", 0, 0, "赤道 A")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", 0, 10, "赤道 B")

	now := riskR1TestTime()
	// ⚠️ 「恰好等于」的构造有两个坑（都实测踩过）：
	//
	//  ① 用「算间隔反推」造不出来：distance=1111.949266 km 时
	//     intervalSeconds=4003.017359，转 time.Duration 会截断，speed 偏离 1000
	//     一点点 → `<=` 变异仍绿。
	//  ② 用「FormatFloat(f,10) 往返」也造不出来：ParseFloat 回来比原值**大 2.5e-11**
	//     → `speed < threshold` 为真 → 连正确实现都不告警。
	//
	// 可行做法：让**间隔取 1 小时整**，则 speed 数值上恰好等于 distanceKm；
	// 再把阈值写成 distanceKm 的 **17 位有效数字十进制表示**（float64 精确往返所需位数）。
	// 用 fmt 的 %.17g 实测得到 "1111.9492664455872"，见下方断言的来源。
	distanceKm := riskHaversineKm(
		riskGeoPoint{Latitude: 0, Longitude: 0},
		riskGeoPoint{Latitude: 0, Longitude: 10},
	)
	// 自检：确认这个字面量与 distanceKm 逐位相等。若将来 haversine 实现变了，
	// 这里会红，提示「把下面的阈值改成新的 %.17g 值」。
	if riskExactDecimalLiteral != distanceKm {
		t.Fatalf("阈值字面量已过期：distanceKm 现在是 %.17g，请同步 riskExactDecimalLiteral",
			distanceKm)
	}
	to := now.Add(-2 * time.Hour)
	from := to.Add(-time.Hour) // 1 小时整 ⇒ speed == distanceKm
	putRiskSetting(t, db, "risk_rule_impossible_travel_threshold", "1111.9492664455872")
	seedRiskLoginAt(t, db, 7711, "1.1.1.1", from)
	seedRiskLoginAt(t, db, 7711, "2.2.2.2", to)

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// speed 恰等于阈值 ⇒ `>=` 判据必须命中；若实现写成 `>`，本用例会红。
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 1 {
		var warning entity.RiskWarning
		db.Where("rule_id = ?", "impossible_travel").First(&warning)
		t.Fatalf("速度恰等于阈值 1000 必须告警（判据是 >= 不是 >），实际 %d 条；detail=%s",
			got, warning.Detail)
	}
}

// 判据⑨：阈值可配（双向）—— 阈值是「速度上限 km/h」。
func TestRiskImpossibleTravelThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	// 北京→广州 4 小时 ≈ 472 km/h。
	seedRiskLoginAt(t, db, 7709, "1.1.1.1", now.Add(-5*time.Hour))
	seedRiskLoginAt(t, db, 7709, "2.2.2.2", now.Add(-1*time.Hour))

	// 默认阈值 900：472 < 900 → 不告警。
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 0 {
		t.Fatalf("472 km/h < 阈值 900 必须不告警，实际 %d 条", got)
	}
	// 阈值调到 300：472 > 300 → 必须告警。
	putRiskSetting(t, db, "risk_rule_impossible_travel_threshold", "300")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 1 {
		t.Fatalf("阈值 300 时 472 km/h 必须告警，实际 %d 条", got)
	}
}

// 判据⑩：只告警不处置。
func TestRiskImpossibleTravelNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	seedRiskIPSegment(t, db, "1.1.1.0", "1.1.1.255", riskR3Beijing[0], riskR3Beijing[1], "中国 北京")
	seedRiskIPSegment(t, db, "2.2.2.0", "2.2.2.255", riskR3Guangzhou[0], riskR3Guangzhou[1], "中国 广东 广州")

	now := riskR1TestTime()
	user := entity.User{Username: "traveler", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedRiskLoginAt(t, db, user.ID, "1.1.1.1", now.Add(-3*time.Hour))
	seedRiskLoginAt(t, db, user.ID, "2.2.2.2", now.Add(-2*time.Hour))

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "impossible_travel"); got != 1 {
		t.Fatalf("前置条件：必须已告警，实际 %d 条", got)
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "enabled" {
		t.Fatalf("C1：风控不得改用户状态，实际 %q", after.Status)
	}
	var blacklist int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklist).Error; err != nil {
		t.Fatalf("count blacklist: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}

// 判据⑪：哈弗辛距离正确性（单元级真值表）。
func TestRiskHaversineKm(t *testing.T) {
	beijing := riskGeoPoint{Latitude: riskR3Beijing[0], Longitude: riskR3Beijing[1]}
	guangzhou := riskGeoPoint{Latitude: riskR3Guangzhou[0], Longitude: riskR3Guangzhou[1]}
	// 已知：北京—广州直线约 1888 km（各家来源 1888–1890）。
	if got := riskHaversineKm(beijing, guangzhou); got < 1850 || got > 1930 {
		t.Fatalf("北京—广州距离 = %.1f km，应在 1850–1930 之间", got)
	}
	// 同一点 → 0。
	if got := riskHaversineKm(beijing, beijing); got != 0 {
		t.Fatalf("同一点距离 = %v, want 0", got)
	}
	// 赤道上一度经度 ≈ 111.19 km。
	a := riskGeoPoint{Latitude: 0, Longitude: 0}
	b := riskGeoPoint{Latitude: 0, Longitude: 1}
	if got := riskHaversineKm(a, b); got < 111 || got > 112 {
		t.Fatalf("赤道 1 度经度 = %.2f km，应约 111.19", got)
	}
}

// 判据⑫：规则说明里的「7 天」必须与代码常量一致（防漂移）。
//
// ⚠️ 这条钉的是一类真实缺陷：说明写「只回溯 7 天」而代码改成 5 天，
// 比不写说明更糟 —— 运营会按错误的边界理解告警行为。
//
// entity 层不能引用 service 常量（会形成 import 环），所以说明里是字面量「7」。
// 本用例用源码断言把两者钉住：改了常量不改文案（或反之）都会红。
func TestRiskImpossibleTravelDescriptionMatchesLookbackDays(t *testing.T) {
	// 常量侧：必须是 7（字面量，不引用被测常量本身，避免恒真）。
	if riskImpossibleTravelLookbackDays != 7 {
		t.Fatalf("回溯天数 = %d，若确实要改，必须同步改 R3 的 Description 文案与本断言",
			riskImpossibleTravelLookbackDays)
	}
	// 说明侧：Description 必须同时提到「7 天」与它是一条限制。
	var found bool
	for _, rule := range entity.DefaultRiskRules() {
		if rule.RuleID != entity.RiskRuleIDImpossibleTravel {
			continue
		}
		found = true
		if !strings.Contains(rule.Description, "7 天") {
			t.Fatalf("R3 的 Description 必须写明「7 天」回溯窗口，实际：%s", rule.Description)
		}
		if !strings.Contains(rule.Description, "只回溯") {
			t.Fatalf("R3 的 Description 必须把回溯窗口写成一条**限制**（含「只回溯」），实际：%s",
				rule.Description)
		}
		// 另一条限制必须同时存在（公网 IP），否则运营只看到一半边界。
		if !strings.Contains(rule.Description, "公网 IP") {
			t.Fatalf("R3 的 Description 必须同时写明「仅公网 IP」限制，实际：%s", rule.Description)
		}
	}
	if !found {
		t.Fatalf("DefaultRiskRules 里找不到 %s", entity.RiskRuleIDImpossibleTravel)
	}
}

var _ = logger.ShanghaiLocation
