package controller

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

const adminRestoreDevice = "exe:t18-original-fixture"

type adminRestoreOutcome struct {
	UserID    int32  `json:"userId"`
	BindingID int64  `json:"bindingId"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

type adminRestoreReceipt struct {
	DeviceID string               `json:"deviceId"`
	Deleted  int64                `json:"deleted"`
	Restore  *adminRestoreOutcome `json:"restore"`
}

func adminRestoreFixture(t *testing.T) (*gorm.DB, entity.User, *gin.Engine) {
	t.Helper()
	db := ownedUnbanDB(t)
	if err := db.AutoMigrate(&entity.UserLevel{}, &entity.UserBan{}, &entity.AccessToken{}); err != nil {
		t.Fatal(err)
	}
	for _, col := range []struct{ name, ddl string }{
		{"level_id", "integer NOT NULL DEFAULT 0"},
		{"device_limit_override", "integer NOT NULL DEFAULT -1"},
	} {
		if !db.Migrator().HasColumn(entity.TableNameUser, col.name) {
			if err := db.Exec("ALTER TABLE users ADD COLUMN " + col.name + " " + col.ddl).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	prev := deviceUserDBOverride
	deviceUserDBOverride = db
	t.Cleanup(func() { deviceUserDBOverride = prev })
	admin := seedDeviceAdminActor(t, db, 1301, "fixture-t18-admin")
	return db, admin, deviceAdminTestRouter(&admin)
}

func adminRestoreSeedBinding(t *testing.T, db *gorm.DB, userID int32, device string, sessions int32) entity.AccountDeviceBinding {
	t.Helper()
	token := entity.AccessToken{
		UserID: userID, Name: "t18 synthetic credential", Status: "enabled",
		TokenHash: service.BlacklistPatternHashHex(fmt.Sprintf("t18-fixture:%d:%s", userID, device)),
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	binding := entity.AccountDeviceBinding{
		UserID: userID, DeviceID: device, FirstSeenAt: seen.Add(-time.Hour), LastSeenAt: seen,
		FirstIP: "192.0.2.10", LastIP: "192.0.2.11", OwnerKind: "token", TokenID: token.ID,
		DeviceLabel: "fixture original", ClientInstance: "fixture original instance",
		SessionCount: sessions, SessionReportedAt: &seen,
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	return binding
}

func adminRestoreReadBinding(t *testing.T, db *gorm.DB, id int64) entity.AccountDeviceBinding {
	t.Helper()
	var row entity.AccountDeviceBinding
	if err := db.Take(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func adminRestoreBan(t *testing.T, db *gorm.DB, router *gin.Engine, device, reason string) adminRestoreRule {
	t.Helper()
	out := deviceAdminRequest(router, http.MethodPost, "/api/admin/device-bans",
		fmt.Sprintf("{\"deviceId\":%q,\"reason\":%q}", device, reason))
	var receipt service.DeviceBanResult
	if out.Code != http.StatusCreated || json.Unmarshal(out.Body.Bytes(), &receipt) != nil || receipt.EntryID <= 0 {
		t.Fatalf("fixture ban: HTTP %d, %s", out.Code, out.Body.String())
	}
	return adminRestoreReadRule(t, db, router, device, receipt.EntryID)
}

type adminRestoreRule struct {
	entity.BlacklistEntry
	revision string
}

func adminRestoreReadRule(t *testing.T, db *gorm.DB, router *gin.Engine, device string, entryID int64) adminRestoreRule {
	t.Helper()
	var saved entity.BlacklistEntry
	if err := db.Take(&saved, entryID).Error; err != nil || saved.UpdatedAt.IsZero() {
		t.Fatalf("read saved rule revision: %+v, %v", saved, err)
	}
	path := "/api/admin/devices?" + url.Values{"keyword": {device}}.Encode()
	response := deviceAdminRequest(router, http.MethodGet, path, "")
	var list struct {
		Data []struct {
			DeviceID    string `json:"deviceId"`
			BanEntryID  int64  `json:"banEntryId"`
			BanRevision string `json:"banRevision"`
		} `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil {
		t.Fatalf("read actual admin device list: HTTP %d %s", response.Code, response.Body.String())
	}
	for _, item := range list.Data {
		if item.DeviceID != device || item.BanEntryID != entryID {
			continue
		}
		if decoded, err := hex.DecodeString(item.BanRevision); err == nil && len(decoded) == 32 && item.BanRevision == strings.ToLower(item.BanRevision) {
			return adminRestoreRule{saved, item.BanRevision}
		}
	}
	t.Errorf("admin device list omitted opaque revision for active ban %d", entryID)
	// Keep the older-server failure behavioral too: this is a syntactically valid
	// revision that an older server ignores (and performs a global-only deletion).
	return adminRestoreRule{saved, strings.Repeat("0", 64)}
}

func adminRestorePath(device string, userID int32, bindingID int64, rule adminRestoreRule) string {
	return "/api/admin/device-bans/" + url.PathEscape(device) + "?" + url.Values{
		"restoreUserId":    {strconv.FormatInt(int64(userID), 10)},
		"restoreBindingId": {strconv.FormatInt(bindingID, 10)},
		"banEntryId":       {strconv.FormatInt(rule.ID, 10)},
		"banRevision":      {rule.revision},
	}.Encode()
}

func adminRestoreReadReceipt(t *testing.T, response *httptest.ResponseRecorder) adminRestoreReceipt {
	t.Helper()
	var got adminRestoreReceipt
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || got.Restore == nil {
		t.Fatalf("selected unban needs a restore receipt: HTTP %d %s", response.Code, response.Body.String())
	}
	return got
}

func adminRestoreRequireReceipt(t *testing.T, response *httptest.ResponseRecorder, userID int32, bindingID int64, deleted int64, status, reason string) {
	t.Helper()
	got := adminRestoreReadReceipt(t, response)
	if got.DeviceID != adminRestoreDevice || got.Deleted != deleted ||
		got.Restore.UserID != userID || got.Restore.BindingID != bindingID ||
		got.Restore.Status != status || (reason != "" && got.Restore.Reason != reason) {
		t.Fatalf("receipt=%+v, restore=%+v; want user=%d binding=%d deleted=%d status=%q reason=%q",
			got, got.Restore, userID, bindingID, deleted, status, reason)
	}
}

func adminRestoreRequireQuota(t *testing.T, owner *entity.User, limit int32, used, available int) {
	t.Helper()
	code, got, raw := doDeviceQuota(t, deviceQuotaRouter(owner), nil)
	if code != http.StatusOK || got.Limit == nil || got.Used == nil || got.Available == nil || got.SchemaReady == nil ||
		*got.Limit != limit || *got.Used != used || *got.Available != available || !*got.SchemaReady {
		t.Fatalf("quota HTTP %d %s, want limit=%d used=%d available=%d", code, raw, limit, used, available)
	}
}

func TestAdminDeviceRestoreOnlyChosenAccountAndPreservesRevocation(t *testing.T) {
	db, _, router := adminRestoreFixture(t)
	owner := seedDeviceUserUser(t, db, "fixture-t18-a", 0, 3)
	other := seedDeviceUserUser(t, db, "fixture-t18-b", 0, 0)
	a := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 5)
	b := adminRestoreSeedBinding(t, db, other.ID, adminRestoreDevice, 4)
	adminRestoreSeedBinding(t, db, owner.ID, "exe:t18-other-usage-fixture", 2)

	rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture global restriction")
	beforeA := adminRestoreReadBinding(t, db, a.ID)
	beforeB := adminRestoreReadBinding(t, db, b.ID)
	if beforeA.RevokedAt == nil || beforeA.RevokedReason != "device_banned" || beforeB.RevokedAt == nil {
		t.Fatal("precondition: the real global ban revokes both originals")
	}
	adminRestoreRequireQuota(t, owner, 3, 2, 1)
	adminRestoreRequireQuota(t, other, 0, 0, -1)

	path := adminRestorePath(adminRestoreDevice, owner.ID, a.ID, rule)
	adminRestoreRequireReceipt(t, deviceAdminRequest(router, http.MethodDelete, path, ""), owner.ID, a.ID, 1, "restored", "")
	afterA := adminRestoreReadBinding(t, db, a.ID)
	if afterA.RevokedAt != nil || afterA.RevokedReason != "" || afterA.SessionCount != 0 || afterA.SessionReportedAt != nil ||
		afterA.ID != beforeA.ID || afterA.UserID != beforeA.UserID || afterA.DeviceID != beforeA.DeviceID ||
		afterA.FirstSeenAt != beforeA.FirstSeenAt || afterA.LastSeenAt != beforeA.LastSeenAt ||
		afterA.FirstIP != beforeA.FirstIP || afterA.LastIP != beforeA.LastIP ||
		afterA.OwnerKind != beforeA.OwnerKind || afterA.TokenID != beforeA.TokenID ||
		afterA.DeviceLabel != beforeA.DeviceLabel || afterA.ClientInstance != beforeA.ClientInstance {
		t.Fatalf("restore must reactivate the existing row, not recreate its old sessions or activity: before=%+v after=%+v", beforeA, afterA)
	}
	if afterB := adminRestoreReadBinding(t, db, b.ID); !reflect.DeepEqual(afterB, beforeB) {
		t.Fatal("same fingerprint on an unselected account was modified")
	}
	if _, blocked := service.DefaultBlacklistCache().Snapshot().MatchDevice(adminRestoreDevice); blocked {
		t.Fatal("cache must observe the committed global unban")
	}
	adminRestoreRequireQuota(t, owner, 3, 3, 0)
	adminRestoreRequireQuota(t, other, 0, 0, -1)

	var logs []entity.Log
	if err := db.Where("module = ?", "device").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	preserved := 0
	for _, row := range logs {
		var facts struct {
			TargetUserID int32  `json:"targetUserId"`
			Method       string `json:"method"`
			RevokedAt    string `json:"revokedAt"`
		}
		if json.Unmarshal([]byte(row.NewAnswer), &facts) == nil && facts.TargetUserID == owner.ID &&
			facts.Method == "device_banned" && facts.RevokedAt == beforeA.RevokedAt.UTC().Format(time.RFC3339Nano) {
			preserved++
		}
	}
	if preserved != 1 {
		t.Fatalf("want one durable record of the original revocation: got %d", preserved)
	}
	adminRestoreRequireReceipt(t, deviceAdminRequest(router, http.MethodDelete, path, ""), owner.ID, a.ID, 0, "unchanged", "ban_changed")
	if audits := ownedUnbanAuditRows(t, db); len(audits) != 1 {
		t.Fatalf("want one global unban audit after retry, got %d", len(audits))
	}
}

func TestAdminDeviceRestoreUsesEffectiveQuotaAndBans(t *testing.T) {
	for _, tc := range []struct {
		name              string
		limit             int32
		userBan           bool
		deviceBan         bool
		disableCredential bool
		status            string
		reason            string
	}{
		{name: "override zero is unlimited", limit: 0, status: "restored"},
		{name: "different binding occupies all slots", limit: 2, status: "removed", reason: "quota_full"},
		{name: "active user ban independent of users status", limit: 4, userBan: true, status: "removed", reason: "account_banned"},
		{name: "current personal device block", limit: 4, deviceBan: true, status: "removed", reason: "account_blocked"},
		{name: "revoked token cannot regain its binding", limit: 4, disableCredential: true, status: "removed", reason: "credential_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, router := adminRestoreFixture(t)
			level := seedDeviceUserLevel(t, db, 1, 1)
			owner := seedDeviceUserUser(t, db, "fixture-t18-quota", level, tc.limit)
			original := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 5)
			adminRestoreSeedBinding(t, db, owner.ID, "exe:t18-other-quota-fixture", 2)
			rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture quota rule")
			before := adminRestoreReadBinding(t, db, original.ID)

			var userBan entity.UserBan
			if tc.userBan {
				userBan = entity.UserBan{UserID: owner.ID, Reason: "fixture independent account restriction", BannedAt: time.Now().UTC()}
				if err := db.Create(&userBan).Error; err != nil {
					t.Fatal(err)
				}
			}
			var block entity.UserDeviceBlock
			if tc.deviceBan {
				block = entity.UserDeviceBlock{UserID: owner.ID, DeviceID: adminRestoreDevice, Reason: "fixture independent personal rule"}
				if err := db.Create(&block).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.disableCredential {
				if err := db.Model(&entity.AccessToken{}).Where("id = ?", before.TokenID).Update("status", "disabled").Error; err != nil {
					t.Fatal(err)
				}
			}
			out := deviceAdminRequest(router, http.MethodDelete, adminRestorePath(adminRestoreDevice, owner.ID, original.ID, rule), "")
			adminRestoreRequireReceipt(t, out, owner.ID, original.ID, 1, tc.status, tc.reason)
			after := adminRestoreReadBinding(t, db, original.ID)
			if tc.status == "restored" {
				if after.RevokedAt != nil || after.SessionCount != 0 || after.SessionReportedAt != nil {
					t.Fatal("unlimited restore did not reactivate at minimum slot weight")
				}
				adminRestoreRequireQuota(t, owner, 0, 3, -1)
			} else if !reflect.DeepEqual(after, before) {
				t.Fatal("ineligible binding must remain byte-for-byte equivalent as a row")
			} else if !tc.userBan {
				adminRestoreRequireQuota(t, owner, tc.limit, 2, int(tc.limit)-2)
			}
			if tc.userBan && db.Take(&entity.UserBan{}, userBan.ID).Error != nil ||
				tc.deviceBan && db.Take(&entity.UserDeviceBlock{}, block.ID).Error != nil {
				t.Fatal("device unban must not remove any account or personal rule")
			}
			if _, blocked := service.DefaultBlacklistCache().Snapshot().MatchDevice(adminRestoreDevice); blocked {
				t.Fatal("a binding refusal must not keep the valid global unban in cache")
			}
		})
	}
}

func TestAdminDeviceRestoreRejectsWrongOwnerAndWrongDevice(t *testing.T) {
	db, _, router := adminRestoreFixture(t)
	owner := seedDeviceUserUser(t, db, "fixture-t18-owner", 0, 0)
	other := seedDeviceUserUser(t, db, "fixture-t18-not-owner", 0, 0)
	id := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 1).ID
	otherID := adminRestoreSeedBinding(t, db, owner.ID, "exe:t18-wrong-fingerprint", 1).ID
	rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture belongs to owner")
	cases := []struct {
		userID    int32
		bindingID int64
	}{{other.ID, id}, {owner.ID, otherID}}
	for _, tc := range cases {
		out := deviceAdminRequest(router, http.MethodDelete, adminRestorePath(adminRestoreDevice, tc.userID, tc.bindingID, rule), "")
		if out.Code != http.StatusNotFound {
			t.Errorf("unrelated binding %d / account %d: HTTP %d, want 404; %s",
				tc.bindingID, tc.userID, out.Code, out.Body.String())
		}
	}
	for _, query := range []string{"restoreUserId=1", "restoreUserId=bogus&restoreBindingId=1&banEntryId=1&banRevision=invalid"} {
		path := "/api/admin/device-bans/" + url.PathEscape(adminRestoreDevice) + "?" + query
		if out := deviceAdminRequest(router, http.MethodDelete, path, ""); out.Code != http.StatusBadRequest {
			t.Errorf("malformed explicit selection must not fall back to legacy unban: HTTP %d %s", out.Code, out.Body.String())
		}
	}

	if out := deviceAdminRequest(deviceAdminTestRouter(other), http.MethodDelete,
		adminRestorePath(adminRestoreDevice, owner.ID, id, rule), ""); out.Code != http.StatusForbidden {
		t.Errorf("ordinary actor selected restore: HTTP %d, want 403", out.Code)
	}
	if err := db.Take(&entity.BlacklistEntry{}, rule.ID).Error; err != nil || len(ownedUnbanAuditRows(t, db)) != 0 {
		t.Fatal("rejected restore must not remove or audit the global rule")
	}
}

func TestAdminDeviceRestoreDoesNotUndoAnIndependentRemoval(t *testing.T) {
	db, _, router := adminRestoreFixture(t)
	owner := seedDeviceUserUser(t, db, "fixture-t18-manually-removed", 0, 0)
	original := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 3)
	unbind := "/api/admin/devices/" + strconv.FormatInt(original.ID, 10) + "/unbind"
	if out := deviceAdminRequest(router, http.MethodPost, unbind, ""); out.Code != http.StatusOK {
		t.Fatalf("fixture independent unbind: HTTP %d %s", out.Code, out.Body.String())
	}
	before := adminRestoreReadBinding(t, db, original.ID)
	if before.RevokedAt == nil || before.RevokedReason != "admin_unbound" {
		t.Fatalf("fixture did not independently remove the old binding: %+v", before)
	}
	rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture independent global rule")
	path := adminRestorePath(adminRestoreDevice, owner.ID, original.ID, rule)
	adminRestoreRequireReceipt(t, deviceAdminRequest(router, http.MethodDelete, path, ""), owner.ID, original.ID, 1, "removed", "")
	if after := adminRestoreReadBinding(t, db, original.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("global unban must not undo an independent binding removal")
	}
}

func TestAdminDeviceRestoreDoesNotRemoveChangedOrExpiredBan(t *testing.T) {
	for _, tc := range []string{"changed revision", "changed reason at same stored timestamp", "new rule ID", "expired at execution"} {
		t.Run(tc, func(t *testing.T) {
			db, _, router := adminRestoreFixture(t)
			owner := seedDeviceUserUser(t, db, "fixture-t18-rule-owner", 0, 0)
			original := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 3)
			initial := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture first rule")
			path := adminRestorePath(adminRestoreDevice, owner.ID, original.ID, initial)
			current := initial
			wantUnbans := 0
			switch tc {
			case "changed revision":
				current = adminRestoreBan(t, db, router, adminRestoreDevice, "fixture revised rule")
				if initial.ID != current.ID || current.UpdatedAt.Equal(initial.UpdatedAt) {
					t.Fatal("precondition: same rule with a different DB revision")
				}
			case "changed reason at same stored timestamp":
				if err := db.Model(&current.BlacklistEntry).UpdateColumn("reason", "fixture changed policy without timestamp advance").Error; err != nil {
					t.Fatal(err)
				}
				current = adminRestoreReadRule(t, db, router, adminRestoreDevice, current.ID)
				if !current.UpdatedAt.Equal(initial.UpdatedAt) || current.revision == initial.revision {
					t.Fatal("content change must invalidate even a same-timestamp revision")
				}
			case "new rule ID":
				old := deviceAdminRequest(router, http.MethodDelete, "/api/admin/device-bans/"+url.PathEscape(adminRestoreDevice), "")
				var shape map[string]json.RawMessage
				if old.Code != http.StatusOK || json.Unmarshal(old.Body.Bytes(), &shape) != nil ||
					len(shape) != 2 || string(shape["deleted"]) != "1" || shape["deviceId"] == nil {
					t.Fatalf("legacy unban contract changed: HTTP %d %s", old.Code, old.Body.String())
				}
				if row := adminRestoreReadBinding(t, db, original.ID); row.RevokedAt == nil {
					t.Fatal("legacy unban restored a binding without an explicit selection")
				}
				wantUnbans = 1
				current = adminRestoreBan(t, db, router, adminRestoreDevice, "fixture new episode")
				if initial.ID == current.ID {
					t.Fatal("precondition: later global ban has a different rule ID")
				}
			case "expired at execution":
				if err := db.Model(&current.BlacklistEntry).UpdateColumn("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Take(&current.BlacklistEntry, current.ID).Error; err != nil || current.ExpiresAt == nil {
					t.Fatalf("read persisted expired rule: %+v %v", current, err)
				}
				// Expired rules no longer project a revision to the admin list. Build
				// that same valid revision for the DB fixture to isolate the expiry
				// decision from the separate stale-revision check.
				current.revision = service.BlacklistPatternHashHex(fmt.Sprintf("%d|%t|%q|%q|%q|%q|%q|%q",
					current.ID, current.Enabled, current.CreatedAt.UTC().Format(time.RFC3339Nano),
					current.UpdatedAt.UTC().Format(time.RFC3339Nano), current.ExpiresAt.UTC().Format(time.RFC3339Nano),
					current.ValueHash, current.MatchType, current.Reason))
				path = adminRestorePath(adminRestoreDevice, owner.ID, original.ID, current)
			}
			before := adminRestoreReadBinding(t, db, original.ID)
			adminRestoreRequireReceipt(t, deviceAdminRequest(router, http.MethodDelete, path, ""), owner.ID, original.ID, 0, "unchanged", "ban_changed")
			if err := db.Take(&entity.BlacklistEntry{}, current.ID).Error; err != nil {
				t.Fatalf("stale or expired request removed the current rule: %v", err)
			}
			if after := adminRestoreReadBinding(t, db, original.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("stale or expired request changed the selected binding")
			}
			if audits := ownedUnbanAuditRows(t, db); len(audits) != wantUnbans {
				t.Fatalf("stale or expired request wrote a global unban audit: total=%d want=%d", len(audits), wantUnbans)
			}
		})
	}
}

func TestAdminDeviceRestoreRollsBackRuleWhenRestoreWriteFails(t *testing.T) {
	db, _, router := adminRestoreFixture(t)
	owner := seedDeviceUserUser(t, db, "fixture-t18-restore-rollback", 0, 0)
	original := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 5)
	rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture rollback rule")
	before := adminRestoreReadBinding(t, db, original.ID)
	if err := db.Exec("CREATE TRIGGER reject_fixture_restore BEFORE UPDATE OF revoked_at ON account_device_bindings " +
		"WHEN OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NULL " +
		"BEGIN SELECT RAISE(ABORT,'fixture restore unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	out := deviceAdminRequest(router, http.MethodDelete, adminRestorePath(adminRestoreDevice, owner.ID, original.ID, rule), "")
	if out.Code != http.StatusInternalServerError {
		t.Fatalf("fixture restore write failed: HTTP %d, expected 500; %s", out.Code, out.Body.String())
	}
	after := adminRestoreReadBinding(t, db, original.ID)
	_, blocked := service.DefaultBlacklistCache().Snapshot().MatchDevice(adminRestoreDevice)
	if db.Take(&entity.BlacklistEntry{}, rule.ID).Error != nil || !blocked || len(ownedUnbanAuditRows(t, db)) != 0 ||
		!reflect.DeepEqual(before, after) {
		t.Fatal("restore failure must roll back both changes and leave the cache blocked")
	}
}

func TestAdminDeviceRestoreConcurrentSelectionsCommitOne(t *testing.T) {
	db, admin, router := adminRestoreFixture(t)
	admin2 := seedDeviceAdminActor(t, db, 1302, "fixture-t18-admin-second")
	owner := seedDeviceUserUser(t, db, "fixture-t18-race-a", 0, 0)
	other := seedDeviceUserUser(t, db, "fixture-t18-race-b", 0, 0)
	a := adminRestoreSeedBinding(t, db, owner.ID, adminRestoreDevice, 2)
	b := adminRestoreSeedBinding(t, db, other.ID, adminRestoreDevice, 4)
	rule := adminRestoreBan(t, db, router, adminRestoreDevice, "fixture race rule")
	before := map[int64]entity.AccountDeviceBinding{
		a.ID: adminRestoreReadBinding(t, db, a.ID),
		b.ID: adminRestoreReadBinding(t, db, b.ID),
	}
	start := make(chan struct{})
	type result struct {
		binding entity.AccountDeviceBinding
		adminID int32
		out     *httptest.ResponseRecorder
	}
	results := make(chan result, 2)
	for _, item := range []struct {
		binding entity.AccountDeviceBinding
		adminID int32
		router  *gin.Engine
	}{{a, admin.ID, router}, {b, admin2.ID, deviceAdminTestRouter(&admin2)}} {
		go func() {
			<-start
			path := adminRestorePath(adminRestoreDevice, item.binding.UserID, item.binding.ID, rule)
			results <- result{item.binding, item.adminID, deviceAdminRequest(item.router, http.MethodDelete, path, "")}
		}()
	}
	close(start)
	var winner int32
	for _, r := range []result{<-results, <-results} {
		got := adminRestoreReadReceipt(t, r.out)
		if got.Restore.UserID != r.binding.UserID || got.Restore.BindingID != r.binding.ID {
			t.Fatalf("response restored a different selection: %+v", got.Restore)
		}
		after := adminRestoreReadBinding(t, db, r.binding.ID)
		switch got.Deleted {
		case 1:
			if winner != 0 || got.Restore.Status != "restored" || after.RevokedAt != nil || after.SessionCount != 0 {
				t.Fatal("only one selected binding can win the same global unban transition")
			}
			winner = r.adminID
		case 0:
			if got.Restore.Status != "unchanged" || !reflect.DeepEqual(after, before[r.binding.ID]) {
				t.Fatal("loser of an already-consumed unban must not restore its selection")
			}
		default:
			t.Fatalf("unexpected deleted count %d", got.Deleted)
		}
	}
	if audits := ownedUnbanAuditRows(t, db); winner == 0 || len(audits) != 1 || audits[0].UserID != winner {
		t.Fatal("want one committed unban audit for the winning administrator")
	}
}
