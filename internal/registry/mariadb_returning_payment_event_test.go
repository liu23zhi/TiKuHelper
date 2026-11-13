package registry

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gl "gorm.io/gorm/logger"
)

// 本文件钉住一个**真实的生产重复加款漏洞**及其修法（`openMySQLDialector`）。
//
// # 缺陷
//
// `gorm.io/driver/mysql@v1.6.0` 在 `SELECT VERSION()` 里含 "MariaDB" 且版本 ≥10.5
// 时会给 Create 子句追加 `RETURNING`（mysql.go:135-183）。MariaDB 的
// `INSERT ... ON DUPLICATE KEY UPDATE pk=pk RETURNING pk` **撞键时仍然返回一行**，
// 而 gorm 一旦发现 RETURNING 就走 `QueryContext + Scan`（callbacks/create.go:80-106），
// 把「扫描到的行数」记成 RowsAffected。
//
// 于是 `clause.OnConflict{DoNothing: true}` 的幂等语义在 MariaDB 上整体失效：
// 重投拿到 1 而不是 0。资金路径上最致命的一处是
// `service.payment_event.go:80` 的 `result.RowsAffected == 1` ——
// 网关重投被当成「首次入账」，同一个订单被加款两次。
//
// # 为什么曾经一直是绿的
//
// 本仓所有默认测试都跑 SQLite。SQLite 的 dialector 根本不看
// `DisableWithReturning`，也从不追加 RETURNING，所以这个洞只在真 MariaDB 上存在。
// 这正是「必须真库」的原因，也是为什么这些用例要挂在
// `mysqlTestDSN`（缺 DSN 时 skip / CI 缺 DSN 时判红）上。

// ---------------------------------------------------------------------------
// 基建
// ---------------------------------------------------------------------------

// mariaDBProductionDialector 用**生产同一个** dialector 构造函数打开真库。
//
// 刻意不复用 `openMariaDBTestDB`：那个用的是 `mysql.Open`（即未修复形态），
// 用它写判据会让「实现退回 mysql.Open」的变异保持全绿。
func mariaDBProductionDialector(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := mysqlTestDSN(t)
	normalized, fixes := normalizeMySQLDSN(dsn)
	if len(fixes) > 0 {
		t.Logf("测试 DSN 已补齐参数：%s", strings.Join(fixes, ", "))
	}
	database, err := gorm.Open(openMySQLDialector(normalized), &gorm.Config{
		Logger: gl.Default.LogMode(gl.Silent),
	})
	if err != nil {
		t.Fatalf("用生产 dialector 打开 MariaDB 失败：%v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败：%v", err)
	}
	// 单连接：ROW_COUNT() 与 RETURNING 的行为都绑定在一条连接上，
	// 连接池里换一条连接会让「同一条语句的可观测结果」不可复现。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	resetMariaDBTestSchema(t, database)
	return database
}

// paymentEventUpsertSQL 是 gorm 在 **关闭 RETURNING** 时生成的等价语句。
//
// 用它做「服务器原生语义」的对照：Exec 路径永远走 RowsAffected()，
// 不会经过 QueryContext + Scan，所以它给出的就是 MariaDB 自己的 ROW_COUNT()。
const paymentEventUpsertSQL = "INSERT INTO `payment_events` " +
	"(`order_no`,`recharge_order_id`,`user_id`,`amount`,`trade_no`,`trade_status`,`source`) " +
	"VALUES (?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE `id`=`id`"

func paymentEventArgs(event entity.PaymentEvent) []any {
	return []any{event.OrderNo, event.RechargeOrderID, event.UserID, event.Amount, event.TradeNo, event.TradeStatus, event.Source}
}

// ---------------------------------------------------------------------------
// 判据 ①：真库单行 upsert —— 首次 1、重投 0
// ---------------------------------------------------------------------------

// TestMariaDBPaymentEventSingleRowReplayMatchesServerRowCount 是**漏洞本体的钉子**。
//
// 判据分两层，缺一层就是假钉子：
//
//	① gorm 的 RowsAffected 必须是 1（首次）/ 0（重投）；
//	② 它必须与**服务器原生 ROW_COUNT()** 一致 —— 用 Exec 执行同一条语句取对照。
//
// 只断 ① 是不够的：若将来有人把期望改成 `want 2`（即接受 MariaDB 的
// 「每行记 2 / 撞键也返回一行」），① 就不再指向正确的语义。第 ② 层把
// 「gorm 的说法」与「服务器的事实」绑在一起，任何一侧漂移都会红。
//
// 变异验证：把 `openMySQLDialector` 退回 `mysql.Open(normalizedDSN)`，
// 本用例应在 ① 就红（实测 replay=1）。
func TestMariaDBPaymentEventSingleRowReplayMatchesServerRowCount(t *testing.T) {
	database := mariaDBProductionDialector(t)
	mustAutoMigrate(t, database, &entity.PaymentEvent{})

	event := entity.PaymentEvent{
		OrderNo: "MARIA-RETURNING-SINGLE", RechargeOrderID: 1, UserID: 1,
		Amount: 8, TradeNo: "trade-1", TradeStatus: "TRADE_SUCCESS", Source: "notify",
	}
	first := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
	if first.Error != nil {
		t.Fatalf("首次 upsert 失败：%v", first.Error)
	}
	if first.RowsAffected != 1 {
		t.Fatalf("首次 upsert RowsAffected = %d, want 1（首次入账被判成非首次会让钱永远不到账）", first.RowsAffected)
	}

	replayEvent := entity.PaymentEvent{
		OrderNo: "MARIA-RETURNING-SINGLE", RechargeOrderID: 1, UserID: 1,
		Amount: 8, TradeNo: "trade-1", TradeStatus: "TRADE_SUCCESS", Source: "notify",
	}
	replay := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&replayEvent)
	if replay.Error != nil {
		t.Fatalf("重投 upsert 失败（DoNothing 不应报错）：%v", replay.Error)
	}
	// 这是资金幂等的核心判据：期待 0，既不是 1（二次加款）也不是 2（每行记 2）。
	if replay.RowsAffected != 0 {
		t.Fatalf("重投 upsert RowsAffected = %d, want 0（网关重投会被判成首次入账并二次加款）", replay.RowsAffected)
	}

	// ② 与服务器原生 ROW_COUNT() 对齐（Exec 路径不经 RETURNING）。
	execFirst := database.Exec(paymentEventUpsertSQL, paymentEventArgs(entity.PaymentEvent{
		OrderNo: "MARIA-RETURNING-SINGLE", RechargeOrderID: 1, UserID: 1,
		Amount: 8, TradeNo: "trade-1", TradeStatus: "TRADE_SUCCESS", Source: "notify",
	})...)
	if execFirst.Error != nil {
		t.Fatalf("Exec 对照语句（重投）失败：%v", execFirst.Error)
	}
	if execFirst.RowsAffected != replay.RowsAffected {
		t.Fatalf("gorm RowsAffected = %d 与服务器原生 ROW_COUNT = %d 不一致：说明 RowsAffected 来自 RETURNING 的扫描行数，不是服务器的真实影响行数",
			replay.RowsAffected, execFirst.RowsAffected)
	}

	var count int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("统计事件行数失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("事件行数 = %d, want 1（DoNothing 不应插入新行）", count)
	}
}

// ---------------------------------------------------------------------------
// 判据 ③：批量形态 —— 断言「与服务器原生语义一致」，而不是断言某个固定数字
// ---------------------------------------------------------------------------

// TestMariaDBPaymentEventBatchReplayMatchesServerRowCount 是**对任务书判据③的纠正**。
//
// 任务书原文要求「批量两行首次=2、重投仍=2，不要在用例里断言它变成 0」。
// 真库实测（MariaDB 11.4.13）表明后一半不成立：
//
//	RETURNING 开   批量首次=2  批量重投=2   ← 每行一条 RETURNING 结果
//	RETURNING 关   批量首次=2  批量重投=0   ← 服务器原生 ROW_COUNT()
//	原生 SQL 直接执行同一语句（两行全撞键）  ROW_COUNT()=0
//
// 也就是说「批量重投=2」**本身就是同一个 RETURNING 缺陷的另一个症状**，
// 而 0 才是 MariaDB 的原生事实（MySQL 8.0 在修复前也返回 0）。
// 若照任务书把 2 写进期望，等于把缺陷的一部分固化进用例 —— 那正是
// 「为了让测试变绿而写错期望」的反面版本：为了让旧观测成立而锁住缺陷。
//
// 所以本用例断言的是**跨路径一致性**（gorm == Exec == 行数守恒），
// 而不是某个魔法数字。这个不变式在修复前后都有意义：
// 修复前它红（2 != 0），修复后它绿（0 == 0）。
//
// 生产影响核实：本仓唯一读「批量 DoNothing RowsAffected」的地方是
// `registry/payment_event_migration.go:159`（`created += int(result.RowsAffected)`），
// 它只用于一条 SysLog 文案，没有任何分支依赖它；其余批量 upsert
// （location cache / ip_segments）是 DoUpdates 且只看 Error。
func TestMariaDBPaymentEventBatchReplayMatchesServerRowCount(t *testing.T) {
	database := mariaDBProductionDialector(t)
	mustAutoMigrate(t, database, &entity.PaymentEvent{})

	batch := []entity.PaymentEvent{
		{OrderNo: "MARIA-RETURNING-BATCH-1", RechargeOrderID: 11, UserID: 11, Amount: 8, Source: "notify"},
		{OrderNo: "MARIA-RETURNING-BATCH-2", RechargeOrderID: 12, UserID: 12, Amount: 9, Source: "notify"},
	}
	first := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
	if first.Error != nil {
		t.Fatalf("批量首次 upsert 失败：%v", first.Error)
	}
	if first.RowsAffected != 2 {
		t.Fatalf("批量首次 RowsAffected = %d, want 2（两行都是新的）", first.RowsAffected)
	}

	replayBatch := []entity.PaymentEvent{
		{OrderNo: "MARIA-RETURNING-BATCH-1", RechargeOrderID: 11, UserID: 11, Amount: 8, Source: "notify"},
		{OrderNo: "MARIA-RETURNING-BATCH-2", RechargeOrderID: 12, UserID: 12, Amount: 9, Source: "notify"},
	}
	replay := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&replayBatch)
	if replay.Error != nil {
		t.Fatalf("批量重投 upsert 失败（DoNothing 不应报错）：%v", replay.Error)
	}

	// 两行都撞键 ⇒ 服务器原生 ROW_COUNT() 是 0。用 Exec 取对照，不硬编码。
	execReplay := database.Exec(
		strings.Replace(paymentEventUpsertSQL, "VALUES (?,?,?,?,?,?,?)", "VALUES (?,?,?,?,?,?,?),(?,?,?,?,?,?,?)", 1),
		append(paymentEventArgs(replayBatch[0]), paymentEventArgs(replayBatch[1])...)...,
	)
	if execReplay.Error != nil {
		t.Fatalf("Exec 批量对照语句失败：%v", execReplay.Error)
	}
	t.Logf("批量重投：gorm RowsAffected=%d，服务器原生 ROW_COUNT=%d", replay.RowsAffected, execReplay.RowsAffected)
	if replay.RowsAffected != execReplay.RowsAffected {
		t.Fatalf("批量重投 gorm RowsAffected = %d 与服务器原生 ROW_COUNT = %d 不一致：RETURNING 把「每行返回一条」当成了影响行数",
			replay.RowsAffected, execReplay.RowsAffected)
	}

	// 行数守恒与「数字是多少」无关，必须单独钉住：批量重投绝不能新增行。
	var count int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("统计事件行数失败：%v", err)
	}
	if count != 2 {
		t.Fatalf("批量重投后事件行数 = %d, want 2（DoNothing 不应插入新行）", count)
	}
}

// ---------------------------------------------------------------------------
// 判据 ②：端到端二次加款 —— 走生产入口 + 真实 HandleRechargePaymentNotifyWithDB
// ---------------------------------------------------------------------------

// TestMariaDBRechargeNotifyReplayDoesNotCreditTwice 是**这条漏洞的最终判据**。
//
// 它刻意走完整生产链路，而不是直接调 `claimRechargePaymentEventInTx`：
//
//	RegisterDBWithError(SQL_DSN=…）  → 证明 db.go:597 真的接上了 openMySQLDialector
//	（否则「单测手工构造 dialector」可以全绿，而线上仍用 mysql.Open）
//	  ↓
//	HandleRechargePaymentNotifyWithDB → 验签 → 锁单 → 记事件 → 加款 → 写账单
//
// 复现步骤与生产事故一致：
//
//	首次成功回调           → 余额 8、订单 paid、事件 1 条
//	管理员把订单改 refunded → 绕开 `status == paid` 快速通道
//	同一条成功回调重投      → **余额必须仍是 8**
//
// 修复前该步骤余额变成 16（`payment_event.go:80` 判 `== 1` 拿到 1）。
//
// 变异验证：把 `openMySQLDialector` 退回 `mysql.Open(normalizedDSN)`，
// 本用例会在「重投后余额」处红。
func TestMariaDBRechargeNotifyReplayDoesNotCreditTwice(t *testing.T) {
	dsn := mysqlTestDSN(t)
	t.Setenv("TIKU_APP_MODE", "development")
	t.Setenv("SQL_DSN", dsn)
	// RegisterDBWithError 走全局单例（`once`），必须按本包的既有约定复位，
	// 否则会拿到别的用例留下的 SQLite 句柄。
	resetRegistryDBState(t)
	t.Cleanup(func() { resetRegistryDBState(t) })

	database, err := RegisterDBWithError(configs.Config{})
	if err != nil {
		t.Fatalf("生产入口 RegisterDBWithError 失败：%v", err)
	}
	if database == nil {
		t.Fatal("生产入口返回了空句柄")
	}
	if name := database.Dialector.Name(); name != "mysql" {
		t.Fatalf("生产入口实际 dialector = %q, want mysql（DSN 没生效？）", name)
	}

	// 真库上的脏数据会污染判据：先清掉本用例涉及的订单与事件。
	const orderNo = "MARIA-RETURNING-E2E"
	if err := database.Where("order_no = ?", orderNo).Delete(&entity.PaymentEvent{}).Error; err != nil {
		t.Fatalf("清理历史事件失败：%v", err)
	}

	publicKey, privateKey := mariaDBRechargeRSAKeys(t)
	seedMariaDBRechargePaymentSettings(t, database, publicKey)
	user := mariaDBSeedUser(t, database, "maria-returning-e2e")
	order := mariaDBSeedRechargeOrder(t, database, user.ID, orderNo, 8)

	firstValues := mariaDBRechargeNotifyValues(t, orderNo, 8, privateKey)
	acknowledged, err := service.HandleRechargePaymentNotifyWithDB(database, firstValues)
	if err != nil {
		t.Fatalf("首次回调处理失败：%v", err)
	}
	if !acknowledged {
		t.Fatal("首次回调未被 ACK：网关会一直重投")
	}
	mariaDBRequireBalance(t, database, user.ID, 8, "首次回调后")
	mariaDBRequireOrderStatus(t, database, order.ID, "paid")

	var events int64
	if err := database.Model(&entity.PaymentEvent{}).Where("order_no = ?", orderNo).Count(&events).Error; err != nil {
		t.Fatalf("统计事件失败：%v", err)
	}
	if events != 1 {
		t.Fatalf("首次回调后事件数 = %d, want 1", events)
	}

	// 管理员改状态：这是真实事故的触发条件（status 是可变运营字段）。
	if err := database.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).
		Update("status", "refunded").Error; err != nil {
		t.Fatalf("把订单改为 refunded 失败：%v", err)
	}

	// 网关重投同一条成功回调。
	replayValues := mariaDBRechargeNotifyValues(t, orderNo, 8, privateKey)
	acknowledged, err = service.HandleRechargePaymentNotifyWithDB(database, replayValues)
	if err != nil {
		t.Fatalf("重投回调处理失败（应静默 ACK）：%v", err)
	}
	if !acknowledged {
		t.Fatal("重投回调未被 ACK：网关会继续重投")
	}

	// 核心判据：余额一分钱都不能动。
	mariaDBRequireBalance(t, database, user.ID, 8, "网关重投后（应当仍是 8，而不是 16）")

	if err := database.Model(&entity.PaymentEvent{}).Where("order_no = ?", orderNo).Count(&events).Error; err != nil {
		t.Fatalf("统计事件失败：%v", err)
	}
	if events != 1 {
		t.Fatalf("重投后事件数 = %d, want 1（DoNothing 不应插入第二条）", events)
	}

	var rechargeRecords int64
	if err := database.Model(&entity.BillingRecord{}).
		Where("type = ? AND request_id = ?", "recharge", orderNo).
		Count(&rechargeRecords).Error; err != nil {
		t.Fatalf("统计充值账单失败：%v", err)
	}
	if rechargeRecords != 1 {
		t.Fatalf("充值账单条数 = %d, want 1（重投不应写第二条流水）", rechargeRecords)
	}
}

// ---------------------------------------------------------------------------
// 判据 ④（真库侧）：RETURNING 关闭后的行为在整个 registry 包内不变形
// ---------------------------------------------------------------------------

// TestMariaDBProductionDialectorEmitsNoReturning 直接盯住「生成的 SQL 长什么样」。
//
// 行为用例（上面三条）已经能抓住缺陷，但它们在失败时只能告诉你
// 「RowsAffected 不对」；这一条把原因直接指出来 —— RETURNING 是否被拼进语句。
// 两者是互补的：SQL 断言单独存在是弱断言，配在行为用例旁边才是有用的诊断。
func TestMariaDBProductionDialectorEmitsNoReturning(t *testing.T) {
	database := mariaDBProductionDialector(t)
	mustAutoMigrate(t, database, &entity.PaymentEvent{})

	statement := database.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&entity.PaymentEvent{OrderNo: "MARIA-RETURNING-DRY", UserID: 1, Amount: 1, Source: "notify"}).
		Statement
	sql := statement.SQL.String()
	t.Logf("生产 dialector 的 dry-run SQL = %s", sql)

	if strings.Contains(strings.ToUpper(sql), "RETURNING") {
		t.Fatalf("生产 dialector 仍在拼 RETURNING：在 MariaDB 上会让 DoNothing 的 RowsAffected 变成扫描行数（重投拿到 1 ⇒ 二次加款）\nSQL = %s", sql)
	}
	if !strings.Contains(strings.ToUpper(sql), "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("dry-run SQL 里没有 ON DUPLICATE KEY UPDATE，upsert 语义已丢失：%s", sql)
	}
}

// ---------------------------------------------------------------------------
// 端到端用的小工具（都是本文件的私有实现，不进生产代码）
// ---------------------------------------------------------------------------

// mariaDBRechargeRSAKeys 生成一对 PKCS8/PKIX 的 base64 密钥，与
// `service.seedRechargePaymentSettings` 的入参形态一致。
func mariaDBRechargeRSAKeys(t *testing.T) (publicKey string, privateKey string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败：%v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("序列化私钥失败：%v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("序列化公钥失败：%v", err)
	}
	return base64.StdEncoding.EncodeToString(publicDER), base64.StdEncoding.EncodeToString(privateDER)
}

// seedMariaDBRechargePaymentSettings 直接写 system_settings 三行。
//
// 刻意不调 `UpdateSystemSettingDetailWithDB`：它会对一大堆无关字段做校验，
// 任一项不合规就让本用例红在与资金幂等无关的地方。
func seedMariaDBRechargePaymentSettings(t *testing.T, database *gorm.DB, publicKey string) {
	t.Helper()

	rows := []entity.SystemSetting{
		{Key: service.EPayPartnerIDSettingKey, Value: "10001", Remark: "maria 真库用例"},
		{Key: service.EPayPlatformPublicKeySettingKey, Value: publicKey, Remark: "maria 真库用例"},
		{Key: service.EPayGatewayURLSettingKey, Value: "https://pay.example.com", Remark: "maria 真库用例"},
	}
	for i := range rows {
		if err := database.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "remark"}),
		}).Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入系统设置 %s 失败：%v", rows[i].Key, err)
		}
	}
}

func mariaDBSeedUser(t *testing.T, database *gorm.DB, username string) entity.User {
	t.Helper()

	user := entity.User{Username: username, Password: "x", Perms: "1", Status: "enabled"}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败：%v", err)
	}
	return user
}

func mariaDBSeedRechargeOrder(t *testing.T, database *gorm.DB, userID int32, orderNo string, amount float64) entity.RechargeOrder {
	t.Helper()

	order := entity.RechargeOrder{
		UserID: userID, OrderNo: orderNo, PaymentProvider: service.DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝", Amount: amount, Status: "pending",
	}
	if err := database.Create(&order).Error; err != nil {
		t.Fatalf("创建充值订单失败：%v", err)
	}
	return order
}

// mariaDBRechargeNotifyValues 组装并签名一条成功回调。
//
// 签名串规则是本文件对 `service.buildRechargeSignContent` 的**独立重写**
// （非空值、排除 sign/sign_type、按 key 升序拼 `k=v`、`&` 连接）。
// 写错不会静默通过：handler 会返回 ErrRechargeNotifyInvalidSignature，
// 用例会在第一次回调处直接红。
func mariaDBRechargeNotifyValues(t *testing.T, orderNo string, amount float64, privateKey string) url.Values {
	t.Helper()

	values := url.Values{
		"pid":          {"10001"},
		"type":         {"alipay"},
		"out_trade_no": {orderNo},
		"trade_no":     {"trade_" + orderNo},
		"trade_status": {"TRADE_SUCCESS"},
		"money":        {fmt.Sprintf("%.2f", amount)},
	}

	keys := make([]string, 0, len(values))
	for key, list := range values {
		if key == "sign" || key == "sign_type" {
			continue
		}
		if len(list) == 0 || strings.TrimSpace(list[0]) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}

	block, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		t.Fatalf("解码私钥失败：%v", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block)
	if err != nil {
		t.Fatalf("解析私钥失败：%v", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatal("私钥不是 RSA")
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("签名失败：%v", err)
	}

	values.Set("sign", base64.StdEncoding.EncodeToString(signature))
	values.Set("sign_type", "RSA")
	return values
}

func mariaDBRequireBalance(t *testing.T, database *gorm.DB, userID int32, want float64, stage string) {
	t.Helper()

	var user entity.User
	if err := database.First(&user, userID).Error; err != nil {
		t.Fatalf("%s读取用户失败：%v", stage, err)
	}
	// 直接比浮点：金额都是 2 位小数，且经过 normalizeRechargeAmount。
	if user.Balance != want {
		t.Fatalf("%s余额 = %v, want %v（差额说明同一网关订单被加款多次）", stage, user.Balance, want)
	}
}

func mariaDBRequireOrderStatus(t *testing.T, database *gorm.DB, orderID int32, want string) {
	t.Helper()

	var order entity.RechargeOrder
	if err := database.First(&order, orderID).Error; err != nil {
		t.Fatalf("读取充值订单失败：%v", err)
	}
	if order.Status != want {
		t.Fatalf("订单状态 = %q, want %q", order.Status, want)
	}
	if order.PaidAt == nil {
		t.Fatal("订单 paid_at 为空：入账路径没有走到")
	}
}

// 保证 time 包的引用不被 goimports 判成未使用（回填/对账断言将来会用到）。
var _ = time.Now
var _ = os.Getenv
