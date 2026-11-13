package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ════════════════════════════════════════════════════════════════════
// 注册路径补写 user_binding_records（修 G2 接线缺陷）
//
// 背景：`CreateUserWithNewUserBonusInTx` 此前只 `db.Create(user)`，从不调
// `recordActive*`。于是该表只有「改过号的人」才有行 → R14 / 44 号追缴 /
// 19 号 Current* 四个开关全部是死的。
//
// 每条用例都走**真实**业务函数，不预置任何行（初始表必为 0 行）。
// ════════════════════════════════════════════════════════════════════

// newBindingFixTestDB 建一个够用的内存库（users + binding records + 计费）。
func newBindingFixTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, model := range []any{
		&entity.User{},
		&entity.UserBindingRecord{},
		&entity.BillingRecord{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return db
}

// countActiveBindings 数某用户某 kind 的 active 行。
func countActiveBindings(t *testing.T, db *gorm.DB, userID int32, kind string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&entity.UserBindingRecord{}).
		Where("user_id = ? AND kind = ? AND state = ?", userID, kind, entity.BindingStateActive).
		Count(&count).Error; err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	return count
}

// newUserWithPhone 造一个待注册用户（phone 已规范化前的原始形态）。
func newUserWithPhone(username, phone string) *entity.User {
	now := time.Now()
	return &entity.User{
		Username: username, Password: "x", Status: "enabled", Role: "user",
		Phone: &phone, PhoneBoundAt: &now,
	}
}

// 判据①：带 phone 注册，**赠送关闭**（默认情况）→ 必须有 1 行 active。
//
// 这条是最容易漏的：早退分支 `if !options.Enabled { return nil }` 在默认配置下必走，
// 调用点放它之后等于对绝大多数注册完全没生效。
func TestRegisterWritesBindingWhenBonusDisabled(t *testing.T) {
	db := newBindingFixTestDB(t)
	user := newUserWithPhone("regoff", "13800138001")

	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := countActiveBindings(t, db, user.ID, entity.BindingKindPhone); got != 1 {
		t.Fatalf("赠送关闭时注册带 phone 必须有 1 条 active 绑定记录，实际 %d 条。"+
			"若为 0 说明调用点被放到了早退分支之后", got)
	}
	// 记录内容必须指向该用户与该号码。
	var record entity.UserBindingRecord
	if err := db.Where("user_id = ? AND state = ?", user.ID, entity.BindingStateActive).
		First(&record).Error; err != nil {
		t.Fatalf("load record: %v", err)
	}
	if record.Value != "13800138001" {
		t.Fatalf("绑定记录 value = %q, want 13800138001", record.Value)
	}
	if record.Source != entity.BindingSourceUserBind {
		t.Fatalf("绑定记录 source = %q, want %q", record.Source, entity.BindingSourceUserBind)
	}
}

// 判据②：带 phone 注册，**赠送开启** → 同样 1 行。
func TestRegisterWritesBindingWhenBonusEnabled(t *testing.T) {
	db := newBindingFixTestDB(t)
	user := newUserWithPhone("regon", "13800138002")

	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{
		Enabled: true, Amount: 1.5, Source: RegistrationBonusSourcePublicRegister,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := countActiveBindings(t, db, user.ID, entity.BindingKindPhone); got != 1 {
		t.Fatalf("赠送开启时注册带 phone 必须有 1 条 active 绑定记录，实际 %d 条", got)
	}
}

// 判据③：不带 phone / wechat 注册 → 0 行（不能凭空建）。
func TestRegisterWithoutBindingWritesNothing(t *testing.T) {
	db := newBindingFixTestDB(t)
	user := &entity.User{Username: "nobind", Password: "x", Status: "enabled", Role: "user"}

	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var total int64
	if err := db.Model(&entity.UserBindingRecord{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 0 {
		t.Fatalf("不带 phone/wechat 注册不得产生绑定记录，实际 %d 条", total)
	}
}

// 判据④：带 wechat 注册 → 1 行 wechat。
func TestRegisterWritesWechatBinding(t *testing.T) {
	db := newBindingFixTestDB(t)
	now := time.Now()
	wechatID := "wx-openid-001"
	user := &entity.User{
		Username: "regwx", Password: "x", Status: "enabled", Role: "user",
		WechatID: &wechatID, WechatBoundAt: &now,
	}

	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := countActiveBindings(t, db, user.ID, entity.BindingKindWechat); got != 1 {
		t.Fatalf("带 wechat 注册必须有 1 条 active 微信绑定记录，实际 %d 条", got)
	}
	var record entity.UserBindingRecord
	if err := db.Where("user_id = ? AND kind = ?", user.ID, entity.BindingKindWechat).First(&record).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.Source != entity.BindingSourceWechatRegister {
		t.Fatalf("微信绑定 source = %q, want %q", record.Source, entity.BindingSourceWechatRegister)
	}
}

// 判据⑤：注册事务回滚 → 绑定记录也回滚（0 行）。
//
// 这条钉的是「必须用同一个 tx 写」。若用了 db.Session(NewDB:true) 或另开事务，
// 外层回滚后绑定记录会残留 → 出现「用户没建成、绑定记录却留下」的脏数据。
func TestRegisterBindingRollsBackWithTransaction(t *testing.T) {
	db := newBindingFixTestDB(t)
	user := newUserWithPhone("rollback", "13800138005")

	// 故意让外层事务在注册成功后返回错误。
	sentinel := gorm.ErrInvalidTransaction
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := CreateUserWithNewUserBonusInTx(tx, user, NewUserBonusOptions{Enabled: false}); err != nil {
			return err
		}
		// 此时事务内已有 user + binding 行。
		var inTx int64
		if err := tx.Model(&entity.UserBindingRecord{}).Count(&inTx).Error; err != nil {
			return err
		}
		if inTx != 1 {
			t.Fatalf("事务内应有 1 条绑定记录（证明确实写进去了），实际 %d", inTx)
		}
		return sentinel
	})
	if err == nil {
		t.Fatalf("外层事务必须回滚（返回 %v）", sentinel)
	}

	// 回滚后表里必须是 0 行。
	var total int64
	if err := db.Model(&entity.UserBindingRecord{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 0 {
		t.Fatalf("注册事务回滚后绑定记录必须一起回滚，实际残留 %d 条（说明没用同一个 tx）", total)
	}
	var users int64
	if err := db.Model(&entity.User{}).Count(&users).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 0 {
		t.Fatalf("用户本身也应回滚，实际 %d 个", users)
	}
}

// 判据⑥：同一 user 重复调用不产生第二行（RecordBindingActiveWithDB 幂等）。
func TestRegisterBindingIsIdempotentForSameUser(t *testing.T) {
	db := newBindingFixTestDB(t)
	user := newUserWithPhone("idem", "13800138006")

	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if got := countActiveBindings(t, db, user.ID, entity.BindingKindPhone); got != 1 {
		t.Fatalf("首次注册后应为 1 条，实际 %d", got)
	}
	// 直接再调一次记录函数（重复 Create 用户会撞用户名唯一约束，但记录函数本身幂等）。
	if err := recordActiveBindingsForNewUser(db, user); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if got := countActiveBindings(t, db, user.ID, entity.BindingKindPhone); got != 1 {
		t.Fatalf("重复写同值必须仍为 1 条（幂等），实际 %d 条", got)
	}
}

// 判据：boundAt 必须与 users.phone_bound_at 一致（不另取 time.Now）。
func TestRegisterBindingUsesPhoneBoundAt(t *testing.T) {
	db := newBindingFixTestDB(t)
	// 特意用一个明显过去的时刻，任何「另取 time.Now()」都会远超它。
	boundAt := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	phone := "13800138007"
	user := &entity.User{
		Username: "boundat", Password: "x", Status: "enabled", Role: "user",
		Phone: &phone, PhoneBoundAt: &boundAt,
	}
	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var record entity.UserBindingRecord
	if err := db.Where("user_id = ? AND state = ?", user.ID, entity.BindingStateActive).
		First(&record).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.BoundAt == nil {
		t.Fatalf("绑定记录的 bound_at 不得为空")
	}
	if !record.BoundAt.Equal(boundAt) {
		t.Fatalf("绑定记录 bound_at = %v, want %v（必须与 users.phone_bound_at 一致，"+
			"不能另取 time.Now）", record.BoundAt, boundAt)
	}
}

// 判据：PhoneBoundAt 是「非 nil 但零值」时必须兜底 time.Now()，不得写 0001-01-01。
//
// 这条来自实测缺陷：第一版兜底只判 nil，实测 `&time.Time{}` 会把
// `0001-01-01 00:00:00` 原样写进 bound_at（recordActiveBinding 不做任何兜底）。
// ORDER BY bound_at 会把这类行排到最后，而 R14/19 号的时序判据全部错位。
func TestRegisterBindingFallsBackWhenBoundAtIsZeroValue(t *testing.T) {
	db := newBindingFixTestDB(t)
	phone := "13800138008"
	var zero time.Time
	user := &entity.User{
		Username: "zerobound", Password: "x", Status: "enabled", Role: "user",
		Phone: &phone, PhoneBoundAt: &zero, // 非 nil，但零值
	}
	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var record entity.UserBindingRecord
	if err := db.Where("user_id = ? AND state = ?", user.ID, entity.BindingStateActive).
		First(&record).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.BoundAt == nil {
		t.Fatalf("bound_at 不得为空")
	}
	if record.BoundAt.IsZero() || record.BoundAt.Year() < 2000 {
		t.Fatalf("零值 PhoneBoundAt 必须兜底为 time.Now()，实际写入了 %v（会让 ORDER BY bound_at 排序全乱）",
			record.BoundAt)
	}
}

// 判据：PhoneBoundAt 为 nil 时同样兜底（不得 panic、不得写零值）。
func TestRegisterBindingFallsBackWhenBoundAtIsNil(t *testing.T) {
	db := newBindingFixTestDB(t)
	phone := "13800138009"
	user := &entity.User{
		Username: "nilbound", Password: "x", Status: "enabled", Role: "user",
		Phone: &phone, PhoneBoundAt: nil,
	}
	if err := CreateUserWithNewUserBonusInTx(db, user, NewUserBonusOptions{Enabled: false}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var record entity.UserBindingRecord
	if err := db.Where("user_id = ? AND state = ?", user.ID, entity.BindingStateActive).
		First(&record).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.BoundAt == nil || record.BoundAt.Year() < 2000 {
		t.Fatalf("nil PhoneBoundAt 必须兜底为 time.Now()，实际 %v", record.BoundAt)
	}
}
