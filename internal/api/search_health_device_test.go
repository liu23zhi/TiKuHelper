package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

// Exercise the actual auth -> read-only device probe -> health handler chain.
// Application singletons are confined to a child and an owned temporary DB.
func TestSearchHealthDeviceAdmissionOnBothProductionRoutes(t *testing.T) {
	if os.Getenv("SEARCH_HEALTH_DEVICE_CHILD") != "1" {
		dir := t.TempDir()
		config, err := json.Marshal(map[string]any{
			"database": map[string]any{"path": filepath.Join(dir, "synthetic.db")},
			"limit":    map[string]any{"enable": true, "duration": 60, "requests": 120},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestSearchHealthDeviceAdmissionOnBothProductionRoutes$", "-test.v")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "SEARCH_HEALTH_DEVICE_CHILD=1", "SQL_DSN=", "JWT_SECRET=HealthDeviceContractSynthetic246!ABC", "TIKU_ADMIN_PASSWORD=HealthDeviceFixture246!", "TIKU_APP_MODE=production", "EXTENSION_UPDATE_ENABLED=false", "EXTENSION_EDGE_ID=", "EXTENSION_CHROME_ID=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated real-router device checks failed: %v\n%s", err, out)
		}
		return
	}
	mgr, err := manager.CreateManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mgr.CloseManager(); err != nil {
			t.Error(err)
		}
	})
	db := mgr.GetDB()
	owner := entity.User{Username: "health-device-owner", Role: "user", Status: "enabled", Balance: 100}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	level := entity.UserLevel{Name: "health-device-level", Rank: 99, Enabled: true, DeviceLimit: 1}
	if err := db.Create(&level).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(entity.TableNameUser).Where("id = ?", owner.ID).Updates(map[string]any{"level_id": level.ID, "device_limit_override": -1}).Error; err != nil {
		t.Fatal(err)
	}
	plain := "tk_health_device_fixture_0123456789"
	sum := sha256.Sum256([]byte(plain))
	token := entity.AccessToken{UserID: owner.ID, Name: "health-device-token", TokenHash: hex.EncodeToString(sum[:]), TokenValue: plain, TokenPrefix: plain[:12], Status: "enabled"}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SystemSetting{Key: "device_limit_enabled", Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.AccountDeviceBinding{UserID: owner.ID, DeviceID: "existing-device"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.UserDeviceBlock{UserID: owner.ID, DeviceID: "blocked-device"}).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	var beforeBills int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ?", owner.ID).Count(&beforeBills).Error; err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/api", "/adapter-service"} {
		for _, scenario := range []struct {
			device string
			status int
			reason string
		}{
			{"blocked-device", http.StatusForbidden, "user-blocked"},
			{"new-device", http.StatusForbidden, "limit-exceeded"},
			{"existing-device", http.StatusOK, ""},
		} {
			req := httptest.NewRequest(http.MethodGet, prefix+"/search/health", nil)
			req.Header.Set("Authorization", "Bearer "+plain)
			req.Header.Set("X-Device-Id", scenario.device)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != scenario.status {
				t.Fatalf("%s device=%s status=%d want=%d body=%s", prefix, scenario.device, out.Code, scenario.status, out.Body.String())
			}
			if scenario.reason != "" {
				var body struct {
					Code   string `json:"code"`
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != "DEVICE_LIMIT_EXCEEDED" || body.Reason != scenario.reason {
					t.Fatalf("specific rejection must reach client: %s", out.Body.String())
				}
			} else {
				var body map[string]any
				if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["deviceAccess"] != nil {
					t.Fatalf("a full pool alone must not warn an existing device: %s", out.Body.String())
				}
			}
		}
	}
	// Lowering a limit must keep existing devices usable while reporting that the
	// pool is over capacity. No device identity or list is exposed in this hint.
	if err := db.Create(&entity.AccountDeviceBinding{UserID: owner.ID, DeviceID: "second-existing-device"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/api", "/adapter-service"} {
		req := httptest.NewRequest(http.MethodGet, prefix+"/search/health", nil)
		req.Header.Set("Authorization", "Bearer "+plain)
		req.Header.Set("X-Device-Id", "existing-device")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		var body struct {
			Success      bool   `json:"success"`
			TaskID       string `json:"taskId"`
			DeviceAccess *struct {
				Allowed bool   `json:"allowed"`
				Limit   int    `json:"limit"`
				Used    int    `json:"used"`
				Reason  string `json:"reason"`
			} `json:"deviceAccess"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if out.Code != http.StatusOK || !body.Success || body.TaskID == "" || body.DeviceAccess == nil || !body.DeviceAccess.Allowed || body.DeviceAccess.Limit != 1 || body.DeviceAccess.Used != 2 || body.DeviceAccess.Reason != "already-registered" {
			t.Fatalf("existing device must get a successful health response plus capacity hint: %d %s", out.Code, out.Body.String())
		}
	}
	var afterBills, bindings int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ?", owner.ID).Count(&afterBills).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("user_id = ?", owner.ID).Count(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	var after entity.User
	if err := db.First(&after, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if beforeBills != afterBills || bindings != 2 || after.Balance != 100 {
		t.Fatalf("health must not charge or register: bills %d -> %d, bindings %d, balance %v", beforeBills, afterBills, bindings, after.Balance)
	}
}
