package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestManagementTransferHTTPPipelineAllResources(t *testing.T) {
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	for _, model := range []any{&entity.Log{}, &entity.BillingRecord{}, &entity.LocationCache{}, &entity.UserBindingRecord{}, &entity.AIOCRCache{}, &entity.AIOCRCacheSequence{}, &entity.BlacklistEntry{}, &entity.WhitelistEntry{}, &entity.UserBan{}, &entity.AccountDeviceBinding{}, &entity.SensitiveImageCacheEntry{}, &entity.RechargeOrder{}, &entity.SensitiveWordCache{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.TicketAttachment{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"ALTER TABLE users ADD COLUMN archived_reason TEXT DEFAULT ''", "ALTER TABLE users ADD COLUMN archived_at DATETIME", "ALTER TABLE users ADD COLUMN archived_by_user_id INTEGER DEFAULT 0"} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	admin := asyncTaskSeedUser(t, db, 9101, "pipeline-admin", "admin")
	archived := asyncTaskSeedUser(t, db, 9102, "pipeline-archived", "user")
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Model(&entity.User{}).Where("id = ?", archived.ID).Updates(map[string]any{"phone": "13812345678", "deleted_at": now, "archived_reason": "user_closure", "archived_at": now, "archived_by_user_id": archived.ID}).Error; err != nil {
		t.Fatal(err)
	}
	records := []any{
		&entity.Log{Module: "ticket", UserID: admin.ID, Username: admin.Username, ClientIP: "203.0.113.18"},
		&entity.BillingRecord{UserID: admin.ID, Type: service.GiftRecordSourceRegistrationBonus, Amount: 0.000001, RequestID: "pipeline-bill"},
		&entity.LocationCache{Kind: "ip", LookupKey: "203.0.113.18", Source: "uapi", Address: "测试", FetchedAt: now},
		&entity.LocationCache{Kind: "phone", LookupKey: "13812345678", Source: "uapi", Address: "测试", FetchedAt: now},
		&entity.UserBindingRecord{UserID: admin.ID, Kind: "wechat", Value: "openid-private-value", ValueHash: "wechat-hash", State: "released"},
		&entity.UserBindingRecord{UserID: admin.ID, Kind: "phone", Value: "13812345678", ValueHash: "phone-hash", State: "released"},
		&entity.AIOCRCache{URLHash: strings.Repeat("a", 64), URL: "https://img.example/a.png?private=token", ImageSHA256: strings.Repeat("b", 64), OCRText: "缓存文本", UpdatedAt: now},
		&entity.BlacklistEntry{Kind: "ip", Pattern: "203.0.113.18", ValueHash: "blacklist-hash", Reason: "测试"},
		&entity.WhitelistEntry{Kind: "ip", Pattern: "203.0.113.18", ValueHash: "whitelist-hash", Reason: "测试"},
		&entity.UserBan{UserID: archived.ID, Reason: "测试", BannedAt: now},
		&entity.AccountDeviceBinding{UserID: admin.ID, DeviceID: "device-private-fingerprint", FirstIP: "203.0.113.18", LastIP: "203.0.113.18", FirstSeenAt: now, LastSeenAt: now},
		&entity.SensitiveImageCacheEntry{ImageSHA256: strings.Repeat("c", 64), Status: "sensitive", UpdatedAt: now, FirstSeenAt: now},
		&entity.RechargeOrder{UserID: admin.ID, OrderNo: "private-order-number-123456", PaymentProvider: "alipay", PaymentMethodNameSnapshot: "支付宝", Amount: 1.234567, Status: "paid", CallbackPayload: "MUST_NOT_EXPORT_CALLBACK"},
		&entity.SensitiveWordCache{TextHash: strings.Repeat("d", 64), Sensitive: true, Words: `["private-word"]`, MaskedText: "private-text", Error: "private-error", CheckedAt: now},
	}
	for _, record := range records {
		if err := db.Create(record).Error; err != nil {
			t.Fatalf("seed %T: %v", record, err)
		}
	}
	manager := installAsyncTaskControllerTestManager(t, db)
	manager.Concurrency = 1
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	manager.Start()
	t.Cleanup(manager.Stop)
	router := asyncTaskRouter(admin)
	resources := []string{"logs", "billing", "ip_locations", "phone_locations", "wechat_bindings", "phone_bindings", "ocr_cache", "gifts", "blacklist", "whitelist", "user_bans", "devices", "sensitive_images", "archived_users", "recharge_orders", "sensitive_words", "users"}
	for _, resource := range resources {
		t.Run(resource, func(t *testing.T) {
			response := asyncTaskRequest(router, "POST", "/api/export/tasks", fmt.Sprintf(`{"resource":%q,"format":"json"}`, resource))
			if response.Code != http.StatusAccepted {
				t.Fatalf("submit: %d %s", response.Code, response.Body.String())
			}
			var task asyncTaskResponse
			if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
				t.Fatal(err)
			}
			if task.Resource != resource || task.Type != "export" || task.CreatedAt.IsZero() {
				t.Fatalf("submission lacks task history fields: %s", response.Body.String())
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				detail := asyncTaskRequest(router, "GET", "/api/export/tasks/"+task.TaskID, "")
				if detail.Code != http.StatusOK {
					t.Fatalf("detail %d %s", detail.Code, detail.Body.String())
				}
				if err := json.Unmarshal(detail.Body.Bytes(), &task); err != nil {
					t.Fatal(err)
				}
				if task.Status == "succeeded" || task.Status == "failed" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if task.Status != "succeeded" || task.Succeeded < 1 || !task.Masked || task.CreatedAt.IsZero() || task.DownloadURL == "" {
				t.Fatalf("executor result: %+v", task)
			}
			list := asyncTaskRequest(router, "GET", "/api/export/tasks?resource="+resource, "")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), task.TaskID) || strings.Contains(list.Body.String(), "sourcePath") {
				t.Fatalf("history %d %s", list.Code, list.Body.String())
			}
			download := asyncTaskRequest(router, "GET", task.DownloadURL, "")
			var rows []map[string]any
			if download.Code != http.StatusOK || json.Unmarshal(download.Body.Bytes(), &rows) != nil || len(rows) < 1 {
				t.Fatalf("download %d %s", download.Code, download.Body.String())
			}
			if download.Header().Get("Content-Disposition") == "" || !strings.Contains(download.Header().Get("Content-Type"), "json") {
				t.Fatalf("missing download headers: %v", download.Header())
			}
			text := download.Body.String()
			for _, secret := range []string{"13812345678", "203.0.113.18", "private=token", "openid-private-value", "device-private-fingerprint", "private-order-number-123456", "MUST_NOT_EXPORT_CALLBACK", "private-word", "private-text", "private-error"} {
				if strings.Contains(text, secret) {
					t.Fatalf("masked %s leaked %q: %s", resource, secret, text)
				}
			}
			if resource == "archived_users" && (rows[0]["archived_reason"] != "user_closure" || rows[0]["archived_by_user_id"] != "9102") {
				t.Fatalf("archive metadata omitted: %v", rows)
			}
			normal := asyncTaskRouter(archived)
			if denied := asyncTaskRequest(normal, "GET", task.DownloadURL, ""); denied.Code != http.StatusForbidden {
				t.Fatalf("ordinary download = %d", denied.Code)
			}
		})
	}
	for _, resource := range []string{"devices", "recharge_orders", "sensitive_words", "archived_users"} {
		for _, format := range []string{"csv", "json", "xlsx"} {
			t.Run(resource+"_"+format+"_unmasked", func(t *testing.T) {
				submitted := asyncTaskRequest(router, "POST", "/api/export/tasks", fmt.Sprintf(`{"resource":%q,"format":%q,"masked":false}`, resource, format))
				var task asyncTaskResponse
				if submitted.Code != http.StatusAccepted || json.Unmarshal(submitted.Body.Bytes(), &task) != nil {
					t.Fatalf("submit=%d %s", submitted.Code, submitted.Body.String())
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					detail := asyncTaskRequest(router, "GET", "/api/export/tasks/"+task.TaskID, "")
					if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &task) != nil {
						t.Fatalf("detail=%s", detail.Body.String())
					}
					if task.Status == "succeeded" || task.Status == "failed" {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if task.Status != "succeeded" || task.Masked || task.DownloadURL == "" {
					t.Fatalf("task=%+v", task)
				}
				file := asyncTaskRequest(router, "GET", task.DownloadURL, "")
				if file.Code != 200 {
					t.Fatalf("download=%d %s", file.Code, file.Body.String())
				}
				var records []map[string]any
				if format == "json" {
					if err := json.Unmarshal(file.Body.Bytes(), &records); err != nil {
						t.Fatal(err)
					}
				} else {
					header, rows, err := service.ParseImportRows(format, file.Body.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						record := map[string]any{}
						for i, key := range header {
							if i < len(row) {
								record[key] = row[i]
							}
						}
						records = append(records, record)
					}
				}
				if len(records) != 1 {
					t.Fatalf("records=%v", records)
				}
				row := records[0]
				switch resource {
				case "devices":
					if row["device_id"] != "device-private-fingerprint" || row["last_ip"] != "203.0.113.18" {
						t.Fatalf("device fields=%v", row)
					}
				case "recharge_orders":
					if row["order_no"] != "private-order-number-123456" || row["amount"] != "1.234567" || row["payment_method"] != "支付宝" {
						t.Fatalf("order fields=%v", row)
					}
					if _, ok := row["callback_payload"]; ok {
						t.Fatal("private provider payload exported")
					}
				case "sensitive_words":
					if row["words"] != `["private-word"]` || row["masked_text"] != "private-text" {
						t.Fatalf("sensitive fields=%v", row)
					}
				case "archived_users":
					if row["phone"] != "13812345678" || row["archived_reason"] != "user_closure" {
						t.Fatalf("archive fields=%v", row)
					}
				}
			})
		}
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	imports := map[string]string{
		"ip_locations":     `[{"lookup_key":"203.0.113.99","address":"导入归属"}]`,
		"phone_locations":  `[{"lookup_key":"13912345678","address":"导入归属"}]`,
		"blacklist":        `[{"kind":"phone","value":"13912345678","reason":"导入规则","enabled":"false"}]`,
		"whitelist":        `[{"kind":"ip","value":"198.51.100.99","reason":"导入规则","enabled":"false"}]`,
		"ocr_cache":        `[{"url":"https://img.example/import.png","image_sha256":"` + strings.Repeat("e", 64) + `","ocr_text":"导入识别"}]`,
		"sensitive_images": `[{"status":"clean","image_base64":"` + base64.StdEncoding.EncodeToString(picture.Bytes()) + `"}]`,
	}
	for resource, content := range imports {
		t.Run("import_"+resource, func(t *testing.T) {
			response := asyncTaskMultipartRequest(t, router, "/api/import/tasks", map[string]string{"resource": resource}, "data.json", []byte(content))
			var task asyncTaskResponse
			if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &task) != nil || task.CreatedAt.IsZero() || task.Resource != resource || task.Type != "import" {
				t.Fatalf("import submit %d %s", response.Code, response.Body.String())
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				detail := asyncTaskRequest(router, "GET", "/api/export/tasks/"+task.TaskID, "")
				if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &task) != nil {
					t.Fatalf("import detail %s", detail.Body.String())
				}
				if task.Status == "succeeded" || task.Status == "failed" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if task.Status != "succeeded" || task.Succeeded != 1 || task.Failed != 0 {
				errors := asyncTaskRequest(router, "GET", "/api/export/tasks/"+task.TaskID+"/errors", "")
				t.Fatalf("import worker did not persist row: %+v report=%s", task, errors.Body.String())
			}
			list := asyncTaskRequest(router, "GET", "/api/export/tasks?resource="+resource, "")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), task.TaskID) {
				t.Fatalf("import not in shared history: %s", list.Body.String())
			}
		})
	}
}
