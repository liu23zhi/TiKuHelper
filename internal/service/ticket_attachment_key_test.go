package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestTicketImagesWithoutConfiguredKeySurviveDatabaseReopen(t *testing.T) {
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "")
	t.Setenv("TIKU_REFUND_PROOF_KEY", "")
	path := filepath.Join(t.TempDir(), "images.db")
	open := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.User{}, &entity.Ticket{},
		&entity.TicketAttachment{}, &entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	user := &entity.User{ID: 1, Username: "owner", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 20, 20))
	if err != nil {
		t.Fatalf("images must work without an environment key: %v", err)
	}
	if info.Visible {
		t.Fatal("automatic encryption must not bypass content review")
	}
	admin := &entity.User{ID: 2, Role: "admin"}
	_, plain, _, err := ReadTicketAttachmentWithDB(db, admin, info.ID)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	sha := ticketAttachmentBlobSHA256Of(plain)
	if _, _, err := StoreTicketAttachmentBlobWithDB(db, sha, "image/png", plain); err != nil {
		t.Fatalf("store deduplicated image: %v", err)
	}
	var stored entity.SystemSetting
	if err := db.Where(map[string]any{"key": "ticket_attachment_encryption_key"}).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(stored.Value)
	if err != nil || len(key) != 32 || bytes.Equal(key, make([]byte, 32)) {
		t.Fatal("a random AES-256 key must be persisted")
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	// 新连接从磁盘重新打开，不复用进程中的密钥缓存。
	reopened := open()
	reopenedConnection, err := reopened.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedConnection.Close() })
	if err := InitializeTicketAttachmentEncryptionWithDB(reopened); err != nil {
		t.Fatalf("initialize after restart: %v", err)
	}
	_, decoded, _, err := ReadTicketAttachmentWithDB(reopened, admin, info.ID)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatalf("attachment must survive restart: %v", err)
	}
	_, blobDecoded, err := ReadTicketAttachmentBlobWithDB(reopened, sha)
	if err != nil || !bytes.Equal(blobDecoded, plain) {
		t.Fatalf("deduplicated image must survive restart: %v", err)
	}
}

func TestTicketAttachmentLegacyKeyPersistsAfterEnvironmentRemoval(t *testing.T) {
	for _, source := range []string{"TIKU_TICKET_ATTACHMENT_KEY", "TIKU_REFUND_PROOF_KEY"} {
		t.Run(source, func(t *testing.T) {
			db := newTicketAttachmentTestDB(t)
			t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "")
			t.Setenv("TIKU_REFUND_PROOF_KEY", "")
			t.Setenv(source, base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{37}, 32)))
			before, err := ticketAttachmentCipher(db)
			if err != nil {
				t.Fatal(err)
			}
			nonce := bytes.Repeat([]byte{1}, before.NonceSize())
			ciphertext := before.Seal(nil, nonce, []byte("existing encrypted image"), []byte("image aad"))
			t.Setenv(source, "")
			after, err := ticketAttachmentCipher(db)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := after.Open(nil, nonce, ciphertext, []byte("image aad"))
			if err != nil || string(plain) != "existing encrypted image" {
				t.Fatalf("removing legacy environment key must preserve images: %v", err)
			}
		})
	}
}

func TestTicketAttachmentKeyConfigurationCannotReplacePersistedKey(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "")
	t.Setenv("TIKU_REFUND_PROOF_KEY", "")
	key, err := resolveTicketAttachmentKey(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, configured := range []string{"invalid-key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))} {
		t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", configured)
		if _, err := ticketAttachmentCipher(db); !errors.Is(err, ErrTicketAttachmentEncryption) {
			t.Fatalf("bad or conflicting config must fail without changing the key: %v", err)
		}
	}
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "")
	resolved, err := resolveTicketAttachmentKey(db)
	if err != nil || !bytes.Equal(resolved, key) {
		t.Fatalf("persisted key must remain intact: %v", err)
	}
	if err := db.Model(&entity.SystemSetting{}).Where(map[string]any{"key": "ticket_attachment_encryption_key"}).
		Update("value", "damaged").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ticketAttachmentCipher(db); !errors.Is(err, ErrTicketAttachmentEncryption) {
		t.Fatalf("a damaged stored key must not silently regenerate: %v", err)
	}
}

func TestTicketAttachmentConcurrentInitializationUsesOneKey(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "")
	t.Setenv("TIKU_REFUND_PROOF_KEY", "")
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	const instances = 8
	keys := make([][]byte, instances)
	failures := make([]error, instances)
	var ready, complete sync.WaitGroup
	ready.Add(instances)
	complete.Add(instances)
	// 所有首次读取都先看到空配置，然后竞争保存；模拟多实例同时首次启动。
	if err := db.Callback().Query().After("gorm:after_query").Register("test_image_key_race", func(tx *gorm.DB) {
		if tx.Statement.Table == "system_settings" && errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			ready.Done()
			ready.Wait()
		}
	}); err != nil {
		t.Fatal(err)
	}
	for i := range instances {
		go func() {
			defer complete.Done()
			keys[i], failures[i] = resolveTicketAttachmentKey(db.Session(&gorm.Session{NewDB: true}))
		}()
	}
	complete.Wait()
	for i := range instances {
		if failures[i] != nil || len(keys[i]) != 32 || !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("instance %d must use the one persisted key: %v", i, failures[i])
		}
	}
	var count int64
	if err := db.Model(&entity.SystemSetting{}).Where(map[string]any{"key": "ticket_attachment_encryption_key"}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("must persist exactly one key, count=%d err=%v", count, err)
	}
}

func TestTicketAttachmentKeyIsPrivateAndOutsideEditableSettings(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	key, err := resolveTicketAttachmentKey(db)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)
	assertPrivate := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), encodedKey) || strings.Contains(string(data), "ticket_attachment_encryption_key") || strings.Contains(string(data), "ticketAttachmentEncryptionKey") {
			t.Fatal("editable settings must not expose the image encryption key")
		}
	}
	detail, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(RedactSystemSettingDetail(detail))
	config, err := RiskConfigs(db)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(config)
	thresholds, err := RiskThresholds(db)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(thresholds)

	// HTTP 写接口绑定固定结构，任意键字段不能变成 system_settings 写入。
	var input UpdateSystemSettingInput
	if err := json.Unmarshal([]byte(`{"ticket_attachment_encryption_key":"replaced","ticketAttachmentEncryptionKey":"replaced","key":"ticket_attachment_encryption_key","value":"replaced"}`), &input); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateSystemSettingDetailWithDB(db, input)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(RedactSystemSettingDetail(updated))
	if _, err := UpdateRiskConfigWithDB(db, "ticket_attachment_encryption_key", "replaced"); !errors.Is(err, ErrRiskConfigUnknown) {
		t.Fatalf("key-based config update must reject the encryption key: %v", err)
	}
	if _, err := ResetRiskConfigWithDB(db, "ticket_attachment_encryption_key"); !errors.Is(err, ErrRiskConfigUnknown) {
		t.Fatalf("key-based config reset must reject the encryption key: %v", err)
	}
	if _, err := UpdateRiskThresholdWithDB(db, "ticket_attachment_encryption_key", 1); !errors.Is(err, ErrRiskThresholdUnknown) {
		t.Fatalf("key-based threshold update must reject the encryption key: %v", err)
	}
	if _, err := ResetRiskThresholdWithDB(db, "ticket_attachment_encryption_key"); !errors.Is(err, ErrRiskThresholdUnknown) {
		t.Fatalf("key-based threshold reset must reject the encryption key: %v", err)
	}
	after, err := resolveTicketAttachmentKey(db)
	if err != nil || !bytes.Equal(after, key) {
		t.Fatalf("settings updates must preserve the image key: %v", err)
	}
}

func TestTicketAttachmentInitializationErrorsDoNotExposeKeys(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	key, err := resolveTicketAttachmentKey(db)
	if err != nil {
		t.Fatal(err)
	}
	stored := base64.StdEncoding.EncodeToString(key)
	for _, configured := range []string{"SYNTHETIC_INVALID_IMAGE_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{29}, 32))} {
		t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", configured)
		err := InitializeTicketAttachmentEncryptionWithDB(db)
		if !errors.Is(err, ErrTicketAttachmentEncryption) {
			t.Fatalf("must reject invalid or conflicting image keys: %v", err)
		}
		if strings.Contains(err.Error(), configured) || strings.Contains(err.Error(), stored) {
			t.Fatal("startup diagnostics must not include configured or persisted key material")
		}
	}
}
