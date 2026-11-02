package registry

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// alterColumnRecorder 统计 AutoMigrate 触发 AlterColumn 的次数。
//
// SQLite 上每次 AlterColumn 都是一次整表 copy-rename，
// 所以「重复启动不该有任何 AlterColumn」就是启动性能的回归断言。
type alterColumnRecorder struct {
	gorm.Migrator
	hits *[]string
}

func (r *alterColumnRecorder) AlterColumn(value interface{}, field string) error {
	*r.hits = append(*r.hits, fmt.Sprintf("%T.%s", value, field))
	return r.Migrator.AlterColumn(value, field)
}

type recordingDialector struct {
	gorm.Dialector
	hits *[]string
}

func (d recordingDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return &alterColumnRecorder{Migrator: d.Dialector.Migrator(db), hits: d.hits}
}

func allEntities() []interface{} {
	return []interface{}{
		&entity.Tiku{}, &entity.User{}, &entity.Log{},
		&entity.SearchAccountConfig{}, &entity.AISearchConfig{},
		&entity.AccessToken{}, &entity.SystemSetting{}, &entity.PriceRule{},
		&entity.PaymentMethod{}, &entity.RechargeOrder{}, &entity.BillingRecord{},
		&entity.AIModelPrice{}, &entity.SMSVerificationCode{}, &entity.CaptchaChallenge{},
		&entity.CaptchaToken{}, &entity.LocationCache{}, &entity.Ticket{},
		&entity.TicketMessage{}, &entity.TicketNotification{}, &entity.AIOCRCache{},
	}
}

// 回归：重复 AutoMigrate 不能再逐列改列（每次改列都会整表重建）。
func TestSqliteAutoMigrateDoesNotRewriteColumns(t *testing.T) {
	hits := make([]string, 0)
	db, err := gorm.Open(recordingDialector{
		Dialector: sqliteQuietDialector{Dialector: sqlite.Open("file:quiet-migrate-repeat?mode=memory&cache=shared")},
		hits:      &hits,
	}, &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := db.AutoMigrate(allEntities()...); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	hits = hits[:0]
	if err := db.AutoMigrate(allEntities()...); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no column rewrite on repeat migrate, got %d: %v", len(hits), hits)
	}
}

// 兜底：关掉「改列」后，建表 / 补列 / 建索引仍然必须生效。
func TestSqliteAutoMigrateStillAddsMissingColumnAndIndex(t *testing.T) {
	db, err := gorm.Open(sqliteQuietDialector{
		Dialector: sqlite.Open("file:quiet-migrate-add?mode=memory&cache=shared"),
	}, &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// 模拟升级前的老表：没有 client_ip 列。
	if err := db.Exec("CREATE TABLE `logs` (`id` integer PRIMARY KEY AUTOINCREMENT,`description` varchar(255))").Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasColumn(entity.TableNameLog, "client_ip") {
		t.Fatal("expected AutoMigrate to add the missing client_ip column")
	}
	if !db.Migrator().HasIndex(entity.TableNameLog, "idx_logs_module") {
		t.Fatal("expected AutoMigrate to create the missing idx_logs_module index")
	}
}

func TestSqliteAIOCRCacheAddsURLToLegacyTableWithoutRewritingRows(t *testing.T) {
	hits := make([]string, 0)
	db, err := gorm.Open(recordingDialector{
		Dialector: sqliteQuietDialector{Dialector: sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")},
		hits:      &hits,
	}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE ai_ocr_cache_entries (url_hash char(64) PRIMARY KEY, image_sha256 char(64) NOT NULL, content_type varchar(128) NOT NULL DEFAULT '', ocr_text text NOT NULL, updated_at datetime)").Error; err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := db.Exec("INSERT INTO ai_ocr_cache_entries (url_hash, image_sha256, content_type, ocr_text, updated_at) VALUES (?, ?, ?, ?, ?)", "legacy-hash", "image-hash", "image/png", "历史识别文本", updatedAt).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.AutoMigrate(&entity.AIOCRCache{}); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameAIOCRCache, "url") {
		t.Fatal("startup migration must add the URL column to an existing OCR table")
	}
	var rows []entity.AIOCRCache
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].URLHash != "legacy-hash" || rows[0].ImageSHA256 != "image-hash" || rows[0].OCRText != "历史识别文本" || rows[0].URL != "" || !rows[0].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("URL column migration changed legacy OCR content or identity: %+v", rows)
	}
	if len(hits) != 0 {
		t.Fatalf("URL column addition must not rewrite existing columns: %v", hits)
	}
}
