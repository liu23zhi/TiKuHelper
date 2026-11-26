package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestMariaDBDeviceHistoryUpgradeAndLifecycle(t *testing.T) {
	db := mysqlTestFreshDB(t)
	t.Logf("fixture: %s", mariaDBServerVersion(t, db))
	mustAutoMigrate(t, db, &entity.SystemSetting{}, &entity.User{}, &entity.UserLevel{}, &entity.Log{}, &entity.BlacklistEntry{})
	if err := db.Exec("ALTER TABLE users ADD COLUMN level_id INT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE account_device_bindings (
		id BIGINT PRIMARY KEY AUTO_INCREMENT, user_id INT NOT NULL, device_id VARCHAR(191) NOT NULL,
		first_seen_at DATETIME(3), last_seen_at DATETIME(3), created_at DATETIME(3), updated_at DATETIME(3),
		last_ip VARCHAR(45) NOT NULL DEFAULT ''
	)`).Error; err != nil {
		t.Fatal(err)
	}
	firstAt := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	latestAt := firstAt.Add(time.Hour)
	const fixtureDevice = "extension:Maria-history"
	if err := db.Exec("INSERT INTO account_device_bindings (id,user_id,device_id,first_seen_at,last_seen_at,last_ip) VALUES (101,601,?,?,?,?)",
		fixtureDevice, firstAt, latestAt, "192.168.40.2").Error; err != nil {
		t.Fatal(err)
	}
	if err := setSystemSettingFlag(db, accountDeviceBindingsSchemaV1Key); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ensureAccountDeviceBindingsSchema(db); err != nil {
			t.Fatalf("upgrade %d: %v", i, err)
		}
	}
	actor := service.DeviceHistoryActor{UserID: 601, Username: "fixture-owner"}
	if _, _, err := service.RemoveAccountDevice(db, 101, actor); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := service.ClearDeviceHistory(db, 101, 601, actor); err != nil {
			t.Fatal(err)
		}
	}
	var retained entity.AccountDeviceBinding
	if err := db.First(&retained, 101).Error; err != nil || retained.HistoryHiddenAt == nil || retained.RevokedReason != "user_removed" ||
		retained.FirstIP != "" || retained.LastIP != "192.168.40.2" || !retained.FirstSeenAt.Equal(firstAt) || !retained.LastSeenAt.Equal(latestAt) {
		t.Fatalf("changed historical facts: %+v %v", retained, err)
	}
	hidden, err := service.ListAdminDeviceBindingsWithDB(db, service.AdminDeviceListQuery{UserID: 601}, 1, 20)
	if err != nil || hidden.Total != 0 || len(hidden.Data) != 0 {
		t.Fatalf("hidden history leaked into management list: %+v %v", hidden, err)
	}
	for _, uid := range []int32{601, 602} {
		decision, err := service.RegisterDeviceWithOptions(db, uid, fixtureDevice, service.DeviceBindingRegisterOptions{ClientIP: "192.168.40.3"})
		if err != nil || !decision.Allowed {
			t.Fatalf("register uid=%d: %+v %v", uid, decision, err)
		}
	}
	banActor := service.DeviceHistoryActor{UserID: 610, Username: "fixture-admin"}
	for i := 0; i < 2; i++ {
		receipt, err := service.BanDevice(db, service.DeviceBanRequest{DeviceID: fixtureDevice, Reason: "fixture reason", CreatedBy: banActor.UserID, AuditActor: &banActor})
		if err != nil || receipt.RevokedBindings != int64((1-i)*2) {
			t.Fatalf("global ban iteration %d: %+v %v", i, receipt, err)
		}
	}
	view, err := service.ListAdminDeviceBindingsWithDB(db, service.AdminDeviceListQuery{Status: "banned"}, 1, 1)
	if err != nil || view.Total != 2 || view.Blocked != 2 || view.Removed != 0 || len(view.Data) != 1 ||
		view.Data[0].LastIP != "192.168.40.3" || view.Data[0].Deletable || !view.Data[0].Banned {
		t.Fatalf("filtered global history/summary mismatch: %+v %v", view, err)
	}
	if _, err := service.ClearDeviceHistory(db, 101, 0, banActor); !errors.Is(err, service.ErrDeviceHistoryNotRemoved) {
		t.Fatalf("admin global history deletion accepted: %v", err)
	}
	if decision, err := service.RegisterDevice(db, 601, fixtureDevice); err != nil || decision.Allowed {
		t.Fatalf("blocked global rebind: %+v %v", decision, err)
	}
	var logs []entity.Log
	if err := db.Where("module = ?", "device").Order("id").Find(&logs).Error; err != nil || len(logs) != 3 ||
		logs[0].UserID != 601 || !strings.Contains(logs[0].NewAnswer, "user_removed") ||
		logs[2].UserID != 610 || !strings.Contains(logs[2].NewAnswer, "fixture reason") {
		t.Fatalf("audit lifecycle must survive purge and duplicate actions: %+v %v", logs, err)
	}
	const legacyDevice = "extension:legacy-block"
	legacyRow := entity.AccountDeviceBinding{UserID: 615, DeviceID: legacyDevice}
	rule := entity.UserDeviceBlock{UserID: legacyRow.UserID, DeviceID: legacyDevice, Reason: "fixture legacy", CreatedAt: firstAt}
	if err := db.Create(&legacyRow).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if used, err := service.CountDevices(db, legacyRow.UserID); err != nil || used != 0 {
		t.Fatalf("legacy blocked binding must not consume slots: used=%d err=%v", used, err)
	}
	if _, err := service.UnblockAccountDevice(db, rule.ID, service.DeviceHistoryActor{UserID: legacyRow.UserID}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacyRow, legacyRow.ID).Error; err != nil || legacyRow.RevokedAt == nil || !legacyRow.RevokedAt.Equal(firstAt) {
		t.Fatalf("legacy unblock must not reactivate binding: %+v %v", legacyRow, err)
	}
}

func TestMariaDBDeviceHistorySerializesWithSeparateConnection(t *testing.T) {
	db := mysqlTestFreshDB(t)
	mustAutoMigrate(t, db, &entity.SystemSetting{}, &entity.User{}, &entity.UserLevel{}, &entity.Log{}, &entity.BlacklistEntry{})
	if err := db.Exec("ALTER TABLE users ADD COLUMN level_id INT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatal(err)
	}
	const deviceID = "extension:db-lock-fixture"
	holder := db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	// This manually held row cannot participate in Go's in-process mutex.
	if err := holder.Create(&entity.DeviceLifecycleLock{DeviceKey: service.BlacklistPatternHashHex(deviceID)}).Error; err != nil {
		t.Fatal(err)
	}

	writer := openMariaDBTestDB(t, mysqlTestDSN(t))
	started := make(chan struct{}, 1)
	err := writer.Callback().Create().Before("gorm:create").Register("test:detect-cross-connection-lock", func(tx *gorm.DB) {
		if tx.Statement.Table == (&entity.DeviceLifecycleLock{}).TableName() {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.RegisterDevice(writer.WithContext(ctx), 603, deviceID)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("competing writer never reached the database guard")
	}
	select {
	case err := <-done:
		t.Fatalf("competing writer escaped the held database guard: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("competing writer did not resume after lock release")
	}
}

func TestMariaDBDeviceHistorySnapshotOverReadCommitted(t *testing.T) {
	reader := mysqlTestFreshDB(t)
	mustAutoMigrate(t, reader, &entity.SystemSetting{}, &entity.Log{}, &entity.BlacklistEntry{})
	if err := ensureAccountDeviceBindingsSchema(reader); err != nil {
		t.Fatal(err)
	}
	// Override this test connection only. The service must explicitly request
	// a repeatable read for listing, regardless of the server/session default.
	if err := reader.Exec("SET SESSION TRANSACTION ISOLATION LEVEL READ COMMITTED").Error; err != nil {
		t.Fatal(err)
	}
	writer := openMariaDBTestDB(t, mysqlTestDSN(t))
	row := entity.AccountDeviceBinding{UserID: 701, DeviceID: "extension:read-committed-fixture", LastSeenAt: time.Now()}
	if err := writer.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	var ownRules int64
	var before, afterConcurrent entity.AccountDeviceBinding
	err := service.ReadDeviceHistorySnapshot(reader, func(tx *gorm.DB) error {
		if err := tx.Model(&entity.UserDeviceBlock{}).Where("user_id = ?", row.UserID).Count(&ownRules).Error; err != nil {
			return err
		}
		if err := tx.First(&before, row.ID).Error; err != nil {
			return err
		}
		if _, err := service.BlockAccountDevice(writer, row.ID, service.DeviceHistoryActor{UserID: row.UserID}, "fixture"); err != nil {
			return err
		}
		return tx.First(&afterConcurrent, row.ID).Error
	})
	if err != nil || ownRules != 0 || before.RevokedAt != nil || afterConcurrent.RevokedAt != nil {
		t.Fatalf("MariaDB read-committed leaked mixed history: rules=%d before=%v after=%v err=%v", ownRules, before.RevokedAt, afterConcurrent.RevokedAt, err)
	}
	var fresh entity.AccountDeviceBinding
	if err := reader.First(&fresh, row.ID).Error; err != nil || fresh.RevokedAt == nil {
		t.Fatalf("expected fresh query to see the committed revocation: %+v %v", fresh, err)
	}
}

func TestMariaDBDeviceHistoryAuditsStoredRevocationBeforeRebind(t *testing.T) {
	db := mysqlTestFreshDB(t)
	mustAutoMigrate(t, db, &entity.SystemSetting{}, &entity.User{}, &entity.UserLevel{}, &entity.Log{}, &entity.BlacklistEntry{})
	if err := db.Exec("ALTER TABLE users ADD COLUMN level_id INT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatal(err)
	}
	admin := service.DeviceHistoryActor{UserID: 702, Username: "fixture-admin"}
	target := entity.AccountDeviceBinding{UserID: 703, DeviceID: "extension:fixture-admin-audit", LastSeenAt: time.Now()}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 21, 1, 2, 3, 456789123, time.UTC)
	for range 2 {
		if _, err := service.UnbindDeviceBindingAsAdmin(db, target.ID, when, admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&target, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if target.RevokedAt == nil {
		t.Fatal("missing persisted admin transition")
	}

	legacy := entity.AccountDeviceBinding{UserID: target.UserID, DeviceID: "extension:fixture-legacy-audit",
		LastSeenAt: when.Add(-time.Hour), RevokedAt: &when, RevokedReason: "idle_timeout"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []entity.AccountDeviceBinding{target, legacy} {
		decision, err := service.RegisterDevice(db, row.UserID, row.DeviceID)
		if err != nil || !decision.Allowed {
			t.Fatalf("rebind: %+v %v", decision, err)
		}
	}
	var logs []entity.Log
	if err := db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].UserID != admin.UserID || logs[1].UserID != 0 ||
		!strings.Contains(logs[0].NewAnswer, target.RevokedAt.UTC().Format(time.RFC3339Nano)) ||
		!strings.Contains(logs[1].NewAnswer, legacy.RevokedAt.UTC().Format(time.RFC3339Nano)) ||
		!strings.Contains(logs[1].NewAnswer, "idle_timeout") {
		t.Fatalf("lost or duplicated audit for stored MariaDB transitions: %+v", logs)
	}
}
