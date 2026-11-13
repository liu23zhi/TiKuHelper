package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住「导入用户也要写 user_binding_records」。
//
// # 为什么需要它
//
// 导入是**唯一绕过** `CreateUserWithNewUserBonusInTx` 的建号路径
// （`import_data.go` 直接 `db.Create(&user)`，且带着 Phone + PhoneBoundAt）。
// 注册路径补了绑定记录之后，导入这条仍然漏着 —— 表现为「导入的用户在
// user_binding_records 里一行都没有」。
//
// 后果：R14 binding_churn、44 号追缴的 history_phone_no_bonus、19 号四个开关的
// Current* 分支全都依赖这张表，导入用户对它们完全隐形。

// TestImportUserRowsWritesBindingRecord 是主判据。
func TestImportUserRowsWritesBindingRecord(t *testing.T) {
	db := newBindingFixTestDB(t)
	if !db.Migrator().HasTable(entity.TableNameUser) {
		t.Fatalf("前置：users 表应已建（newBindingFixTestDB 负责）")
	}

	index := map[string]int{"username": 0, "password": 1, "phone": 2}
	rows := [][]string{
		{"imported-with-phone", "Str0ng!Passw0rd", "13800138010"},
		{"imported-no-phone", "Str0ng!Passw0rd", ""},
	}
	fixed := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	result, err := importUserRows(db, ImportDataOptions{ConfirmWeakPassword: true}, index, rows,
		func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("importUserRows: %v", err)
	}
	if result.Succeeded != 2 {
		t.Fatalf("应导入 2 个用户，实际 Succeeded=%d（details=%+v）", result.Succeeded, result.Details)
	}

	// 带 phone 的用户必须有 1 条 current 行。
	var withPhone entity.User
	if err := db.Where("username = ?", "imported-with-phone").First(&withPhone).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rowsWithPhone []entity.UserBindingRecord
	if err := db.Where("user_id = ?", withPhone.ID).Find(&rowsWithPhone).Error; err != nil {
		t.Fatalf("load bindings: %v", err)
	}
	if len(rowsWithPhone) != 1 {
		t.Fatalf("带 phone 的导入用户必须有 1 条绑定记录，实际 %d 条。"+
			"为 0 说明导入路径仍未写 user_binding_records", len(rowsWithPhone))
	}
	if rowsWithPhone[0].Kind != entity.BindingKindPhone {
		t.Fatalf("kind 期望 phone，实际 %q", rowsWithPhone[0].Kind)
	}
	if rowsWithPhone[0].Value != "13800138010" {
		t.Fatalf("value 期望 13800138010，实际 %q", rowsWithPhone[0].Value)
	}

	// 不带 phone 的用户必须 0 条（不能凭空建）。
	var noPhone entity.User
	if err := db.Where("username = ?", "imported-no-phone").First(&noPhone).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rowsNoPhone []entity.UserBindingRecord
	if err := db.Where("user_id = ?", noPhone.ID).Find(&rowsNoPhone).Error; err != nil {
		t.Fatalf("load bindings: %v", err)
	}
	if len(rowsNoPhone) != 0 {
		t.Fatalf("不带 phone 的导入用户不该有绑定记录，实际 %d 条", len(rowsNoPhone))
	}
}

// TestImportUserRowsBindingFailureDoesNotAbortImport 钉住「旁路审计失败不回滚主记录」。
//
// 与注册路径的取舍不同：注册在事务内、失败应整体回滚；导入是逐行的，
// 单行审计失败不该让「用户已建成」被撤销。
func TestImportUserRowsBindingFailureDoesNotAbortImport(t *testing.T) {
	db := newBindingFixTestDB(t)

	// 把 user_binding_records 表删掉 → recordActiveBinding 会因 HasTable 为 false
	// 直接 return nil（fail-safe），导入应当照常成功。
	if err := db.Migrator().DropTable(entity.TableNameUserBindingRecord); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	index := map[string]int{"username": 0, "password": 1, "phone": 2}
	rows := [][]string{{"import-no-binding-table", "Str0ng!Passw0rd", "13800138011"}}
	fixed := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	result, err := importUserRows(db, ImportDataOptions{ConfirmWeakPassword: true}, index, rows,
		func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("绑定表缺失不该让导入失败：%v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("用户仍应导入成功，实际 Succeeded=%d details=%+v", result.Succeeded, result.Details)
	}
	var count int64
	if err := db.Model(&entity.User{}).Where("username = ?", "import-no-binding-table").
		Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("用户必须已落库，实际 %d", count)
	}
}

var _ = gorm.ErrRecordNotFound
