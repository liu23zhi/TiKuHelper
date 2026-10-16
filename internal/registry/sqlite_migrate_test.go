package registry

import (
	"fmt"
	"testing"

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
		&entity.CaptchaToken{}, &entity.LocationCache{},
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
