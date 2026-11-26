package registry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

type hostCapacityProbe struct {
	gormlog.Interface
	atCount chan struct{}
	resume  <-chan struct{}
}

func (p *hostCapacityProbe) Trace(_ context.Context, _ time.Time, sql func() (string, int64), err error) {
	if err != nil || p.atCount == nil {
		return
	}
	statement, _ := sql()
	if strings.Contains(statement, "session_count AS session_count") && strings.Contains(statement, "account_device_bindings") {
		close(p.atCount)
		p.atCount = nil
		<-p.resume
	}
}

func TestMariaDBDeviceRegistrationSharesLastAccountSlot(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		firstExplicit, secondExplicit, firstRevoked bool
	}{
		{"two-ordinary", false, false, false},
		{"ordinary-rebind-versus-explicit", false, true, true},
		{"explicit-rebind-versus-ordinary", true, false, true},
		{"two-explicit", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testMariaDBHostLastAccountSlot(t, tc.firstExplicit, tc.secondExplicit, tc.firstRevoked)
		})
	}
}

func testMariaDBHostLastAccountSlot(t *testing.T, firstExplicit, secondExplicit, firstRevoked bool) {
	db := mysqlTestFreshDB(t)
	mustAutoMigrate(t, db, &entity.SystemSetting{}, &entity.User{}, &entity.UserLevel{}, &entity.Log{}, &entity.BlacklistEntry{}, &entity.UserBan{}, &entity.AccessToken{})
	if err := db.Exec("ALTER TABLE users ADD COLUMN level_id INT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE users ADD COLUMN device_limit_override INT NOT NULL DEFAULT -1").Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatal(err)
	}
	account := entity.User{Username: "fixture-last-slot", Status: "enabled"}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	level := entity.UserLevel{Name: "fixture-last-slot", Rank: 1, Enabled: true, DeviceLimit: 1}
	if err := db.Create(&level).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("users").Where("id = ?", account.ID).Update("level_id", level.ID).Error; err != nil {
		t.Fatal(err)
	}
	token := entity.AccessToken{UserID: account.ID, Name: "fixture", Status: "enabled", TokenHash: "fixture-test-only"}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	auth := service.AccessTokenAuthContext{User: account, AccessToken: token}
	if firstRevoked {
		prior := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
		row := entity.AccountDeviceBinding{UserID: account.ID, DeviceID: "exe:fixture-last-a", SessionCount: 5,
			RevokedAt: &prior, RevokedReason: "idle_timeout", SessionReportedAt: &prior}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	register := func(db *gorm.DB, explicit bool, device, instance string) (service.DevicePoolDecision, error) {
		if explicit {
			_, decision, err := service.AdmitDeviceHost(db, auth, device, instance, "")
			return decision, err
		}
		return service.RegisterDeviceWithInstance(db, account.ID, device, instance)
	}
	writer := openMariaDBTestDB(t, mysqlTestDSN(t))
	type answer struct {
		decision service.DevicePoolDecision
		err      error
	}
	first, second := make(chan answer, 1), make(chan answer, 1)
	counted, resume := make(chan struct{}), make(chan struct{})
	probe := &hostCapacityProbe{Interface: db.Logger, atCount: counted, resume: resume}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, err := register(db.WithContext(ctx).Session(&gorm.Session{Logger: probe}), firstExplicit, "exe:fixture-last-a", "fixture-a")
		first <- answer{result, err}
	}()
	select {
	case <-counted:
	case <-ctx.Done():
		close(resume)
		t.Fatal("first registration did not read capacity")
	}
	go func() {
		result, err := register(writer.WithContext(ctx), secondExplicit, "exe:fixture-last-b", "fixture-b")
		second <- answer{result, err}
	}()
	var b answer
	finishedEarly := false
	select {
	case b = <-second:
		finishedEarly = true
	case <-time.After(150 * time.Millisecond):
	}
	close(resume)
	a := <-first
	if !finishedEarly {
		b = <-second
	}
	used, err := service.CountDevices(db, account.ID)
	if a.err != nil || b.err != nil || err != nil || !a.decision.Allowed || b.decision.Reason != "limit-exceeded" || b.decision.Allowed || used != 1 {
		t.Fatalf("competing registrations must commit only one slot: fast_writer=%v first=%+v second=%+v used=%d err=%v", finishedEarly, a, b, used, err)
	}
	var row entity.AccountDeviceBinding
	if err := db.Where("user_id = ? AND device_id = ?", account.ID, "exe:fixture-last-a").Take(&row).Error; err != nil || row.SessionCount != 0 ||
		row.SessionReportedAt != nil || row.RevokedAt != nil {
		t.Fatalf("last-slot write failed to persist a zero-session effective device: %+v %v", row, err)
	}
	if firstRevoked {
		var audit int64
		if err := db.Model(&entity.Log{}).Where("module = ? AND description = ?", "device", "保全历史设备撤销证据").Count(&audit).Error; err != nil || audit != 1 {
			t.Fatalf("required preserved removal audit missing: %d %v", audit, err)
		}
	}
}
