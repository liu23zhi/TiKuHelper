package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// legacyTicket 是「加 origin/category/context 三列之前」的 tickets 表结构快照。
//
// 用它建表而不是直接用 entity.Ticket，是为了**真实模拟老库**：迁移测试只有在
// 「表里根本没有这三列」的前提下才有意义（用新实体建表等于自证）。
type legacyTicket struct {
	ID            int64      `gorm:"primaryKey;autoIncrement"`
	UserID        int32      `gorm:"column:user_id;not null;index"`
	UserName      string     `gorm:"column:user_name;type:varchar(255);not null"`
	Subject       string     `gorm:"column:subject;type:varchar(120);not null"`
	Status        string     `gorm:"column:status;type:varchar(32);not null;index"`
	Priority      string     `gorm:"column:priority;type:varchar(32);not null;default:normal"`
	LastMessageAt time.Time  `gorm:"column:last_message_at;not null;index"`
	ClosedAt      *time.Time `gorm:"column:closed_at"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (*legacyTicket) TableName() string { return entity.TableNameTicket }

func newTicketTypeMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// 一次性标记落在 system_settings；缺这张表时标记写入会静默失败。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	return db
}

// TestTicketTypeMigrationAddsColumnsAndMarker 验证三列被显式补上、标记写入、迁移幂等。
func TestTicketTypeMigrationAddsColumnsAndMarker(t *testing.T) {
	db := newTicketTypeMigrationDB(t)
	if err := db.AutoMigrate(&legacyTicket{}); err != nil {
		t.Fatalf("create legacy tickets: %v", err)
	}
	// 老库里的存量单（三列还不存在）。
	if err := db.Exec("INSERT INTO `tickets` (`user_id`, `user_name`, `subject`, `status`, `priority`, `last_message_at`, `created_at`, `updated_at`) VALUES (1, 'alice', '旧工单', 'open', 'normal', '2026-01-02 03:04:05', '2026-01-02 03:04:05', '2026-01-02 03:04:05')").Error; err != nil {
		t.Fatalf("insert legacy ticket: %v", err)
	}

	if err := ensureTicketTypeSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	for _, column := range []string{"origin", "category", "context"} {
		if !db.Migrator().HasColumn(entity.TableNameTicket, column) {
			t.Fatalf("tickets 缺少必需列 %q（缺 origin 会让按来源过滤静默漏单）", column)
		}
	}
	if !systemSettingFlagIsSet(db, ticketTypeColumnsReadyKey) {
		t.Fatal("结构标记必须写入")
	}
	if !systemSettingFlagIsSet(db, ticketTypeOriginBackfilledKey) {
		t.Fatal("回填标记必须写入")
	}
	// 老数据本身不能被迁移改坏。
	var subject string
	if err := db.Raw("SELECT subject FROM `tickets` WHERE user_id = 1").Scan(&subject).Error; err != nil {
		t.Fatal(err)
	}
	if subject != "旧工单" {
		t.Fatalf("存量工单正文被改动：%q", subject)
	}

	// 第二次运行必须是 no-op（幂等）。
	if err := ensureTicketTypeSchema(db); err != nil {
		t.Fatalf("second run must be a no-op, got %v", err)
	}
	for _, key := range []string{ticketTypeColumnsReadyKey, ticketTypeOriginBackfilledKey} {
		var count int64
		if err := db.Model(&entity.SystemSetting{}).Where("`key` = ?", key).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("标记 %s 应恰好一行，实际 %d 行", key, count)
		}
	}
}

// TestTicketTypeMigrationBackfillsLegacyOriginSoOriginFilterFindsThem 是**存量回填**的钉子
// （删掉回填这一步必红）。
//
// 构造的是真实风险形态：三列已经存在，但存量行的 origin/category 是空串。
// 这正是「GORM 的 default 只作用于插入」在线上留下的样子 —— 不回填的话：
//   - 按 `origin = 'user'` 过滤（「用户主动发起」这一档）**一张旧单都看不到**；
//   - 旧单在分类维度上落进「未知分类」。
//
// 同时钉两条边界：
//   - origin='system' 的行**不得**被回填改写（否则「系统单对用户不可见」的闸门被抹平）；
//   - 回填不得刷新 updated_at（迁移只补列值，不许伪造业务时间）。
func TestTicketTypeMigrationBackfillsLegacyOriginSoOriginFilterFindsThem(t *testing.T) {
	db := newTicketTypeMigrationDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}); err != nil {
		t.Fatalf("create tickets: %v", err)
	}
	const fixedTime = "2026-01-02 03:04:05"
	// ① 两条「老单」：origin/category 为空串（模拟 default 未落到已有行）。
	//    显式写空串是刻意的：GORM 对带 default tag 的零值会省略该列、让 DB 默认值生效，
	//    那样就构造不出「空 origin」这个待回填状态了。
	for _, subject := range []string{"旧工单A", "旧工单B"} {
		if err := db.Exec("INSERT INTO `tickets` (`user_id`, `user_name`, `subject`, `status`, `priority`, `origin`, `category`, `last_message_at`, `created_at`, `updated_at`) VALUES (1, 'alice', ?, 'open', 'normal', '', '', ?, ?, ?)",
			subject, fixedTime, fixedTime, fixedTime).Error; err != nil {
			t.Fatalf("insert legacy ticket %s: %v", subject, err)
		}
	}
	// ② 一条系统单：回填必须原样保留它。
	if err := db.Exec("INSERT INTO `tickets` (`user_id`, `user_name`, `subject`, `status`, `priority`, `origin`, `category`, `last_message_at`, `created_at`, `updated_at`) VALUES (2, 'bob', '系统单', 'open', 'normal', 'system', 'registration_bonus', ?, ?, ?)",
		fixedTime, fixedTime, fixedTime).Error; err != nil {
		t.Fatalf("insert system ticket: %v", err)
	}

	if err := ensureTicketTypeSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// ③ 回填生效：把「按 origin='user' 过滤仍能看到旧单」作为第一判据。
	//    回填一旦失效，这里会变成 0 —— 用户主动发起的旧工单在所有 origin 维度上消失。
	var userOrigin int64
	if err := db.Model(&entity.Ticket{}).Where("origin = ?", entity.TicketOriginUser).Count(&userOrigin).Error; err != nil {
		t.Fatal(err)
	}
	if userOrigin != 2 {
		t.Fatalf("旧单必须能按 origin=%q 过滤到（期望 2 行，实际 %d 行）—— 存量回填没生效会把全部旧单漏掉",
			entity.TicketOriginUser, userOrigin)
	}
	// ④ 旧单在用户可见范围内仍然可见（服务侧过滤条件是 origin <> 'system'）。
	var visible int64
	if err := db.Model(&entity.Ticket{}).Where("origin <> ?", entity.TicketOriginSystem).Count(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if visible != 2 {
		t.Fatalf("回填后旧单必须仍在用户列表可见范围内（期望 2 行，实际 %d 行）", visible)
	}
	// ⑤ category 也必须回填成 general，而不是留空。
	var generalCount int64
	if err := db.Model(&entity.Ticket{}).Where("category = ?", entity.TicketCategoryGeneral).Count(&generalCount).Error; err != nil {
		t.Fatal(err)
	}
	if generalCount != 2 {
		t.Fatalf("存量 category 必须回填 general（期望 2 行，实际 %d 行）", generalCount)
	}
	// ⑥ 系统单不被改写：origin/category 原样。
	var systemTicket entity.Ticket
	if err := db.Where("subject = ?", "系统单").First(&systemTicket).Error; err != nil {
		t.Fatal(err)
	}
	if systemTicket.Origin != entity.TicketOriginSystem || systemTicket.Category != entity.TicketCategoryRegistrationBonus {
		t.Fatalf("回填不得改写系统单，实际 %s/%s", systemTicket.Origin, systemTicket.Category)
	}
	// ⑦ 回填不得刷新 updated_at（Update 会顺带写 autoUpdateTime，UpdateColumn 不会）。
	//    用 SQL 比字面值：驱动会把 datetime 列解析成 time.Time 再格式化，扫进 string 会变形。
	var unchanged struct{ Count int64 }
	if err := db.Raw("SELECT COUNT(*) AS count FROM `tickets` WHERE subject = '旧工单A' AND updated_at = ?", fixedTime).
		Scan(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Count != 1 {
		t.Fatalf("回填不得改动 updated_at（期望仍为 %q）", fixedTime)
	}
}

// TestTicketTypeMigrationSkipsMissingTable 验证空库首次启动不会因缺 tickets 报错。
func TestTicketTypeMigrationSkipsMissingTable(t *testing.T) {
	db := newTicketTypeMigrationDB(t)
	if db.Migrator().HasTable(entity.TableNameTicket) {
		t.Fatal("前置条件错误：tickets 不应存在")
	}
	if err := ensureTicketTypeSchema(db); err != nil {
		t.Fatalf("缺表时必须静默跳过（交给后续 AutoMigrate），得到 %v", err)
	}
	if err := ensureTicketTypeSchema(nil); err != nil {
		t.Fatalf("nil db 必须安全返回，得到 %v", err)
	}
}

// TestTicketTypeMigrationIsIdempotentWithExistingNewSchema 验证「新库（三列已由
// AutoMigrate 建好）」上的迁移只补标记、不动数据。
func TestTicketTypeMigrationIsIdempotentWithExistingNewSchema(t *testing.T) {
	db := newTicketTypeMigrationDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}); err != nil {
		t.Fatal(err)
	}
	ticket := entity.Ticket{
		UserID: 1, UserName: "alice", Subject: "新库工单", Status: entity.TicketStatusOpen,
		Origin: entity.TicketOriginAppeal, Category: entity.TicketCategoryBlacklistAppeal,
		Priority: entity.TicketPriorityHigh, LastMessageAt: time.Now(),
	}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureTicketTypeSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var reloaded entity.Ticket
	if err := db.Where("id = ?", ticket.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	// appeal 单不是空值，回填必须跳过它。
	if reloaded.Origin != entity.TicketOriginAppeal || reloaded.Category != entity.TicketCategoryBlacklistAppeal {
		t.Fatalf("非空 origin/category 不得被回填覆盖，实际 %s/%s", reloaded.Origin, reloaded.Category)
	}
	if !systemSettingFlagIsSet(db, ticketTypeColumnsReadyKey) {
		t.Fatal("新库也要写结构标记")
	}
}
