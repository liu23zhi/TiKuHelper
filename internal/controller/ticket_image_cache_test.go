package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 工单图片缓存管理页控制器测试（T-12）。
//
// 全部用真 sqlite 内存库、不 mock service 的查询/删除：「总占用」「待清理数」
// 「ref_count 保护」这类断言只有在真实 SQL 上才有意义 —— 换成 mock 就退化成
// 「我们调用了一个函数」。
//
// 唯一替换的是「立即清理」的两个注入点：T-11 的清理函数在本分支尚不存在
// （见 ticket_image_cache.go 的 TODO(waiting: T-11)），因此用桩函数验证
// 「两个数字分开上报」与「先时间后大小」的调用顺序。

// newTicketImageCacheTestDB 建控制器测试库。
//
// 与 ticket_attachment_blob_test.go 的迁移形态保持一致：blobs 表 + 带
// blob_sha256 列的附件表。⚠️ **不能**把 entity.TicketAttachmentBlobLink 传给
// AutoMigrate（GORM 会把两个模型合并成一条建表语句，表只剩两列）。
func newTicketImageCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:ticket-image-cache-%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.TicketAttachment{},
		&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, "blob_sha256") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment +
			" ADD COLUMN `blob_sha256` char(64) NOT NULL DEFAULT ''").Error; err != nil {
			t.Fatalf("add blob_sha256: %v", err)
		}
	}
	dao.SetDefault(db)
	return db
}

// ticketImageCacheRouter 组装与 router.go 一致的中间件顺序（Auth 注入身份 + RequireAdmin）。
func ticketImageCacheRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/api/admin/ticket-image-cache", auth, middleware.RequireAdmin, TicketImageCacheList)
	router.DELETE("/api/admin/ticket-image-cache/:sha256", auth, middleware.RequireAdmin, TicketImageCacheDelete)
	router.POST("/api/admin/ticket-image-cache/batch-delete", auth, middleware.RequireAdmin, TicketImageCacheBatchDelete)
	router.POST("/api/admin/ticket-image-cache/clean", auth, middleware.RequireAdmin, TicketImageCacheCleanNow)
	return router
}

// ticketImageCacheRequest 发一次请求并返回 recorder。
func ticketImageCacheRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// seedBlob 落一份 blob（不写密文：列表/删除路径不需要能解密）。
func seedBlob(t *testing.T, db *gorm.DB, sha string, byteSize int64, mediaType string,
	refCount int32, scanState string) entity.TicketAttachmentBlob {
	t.Helper()

	blob := entity.TicketAttachmentBlob{
		SHA256: sha, ByteSize: byteSize, MediaType: mediaType,
		RefCount: refCount, ScanState: scanState,
	}
	if err := db.Create(&blob).Error; err != nil {
		t.Fatalf("seed blob %s: %v", sha, err)
	}
	return blob
}

// seedAttachmentRef 让一个附件行指向该 blob（ref_count 的事实来源）。
func seedAttachmentRef(t *testing.T, db *gorm.DB, ticketID int64, sha string) {
	t.Helper()

	row := entity.TicketAttachment{
		TicketID: ticketID, UserID: 1, MediaType: "image/png", ByteSize: 16,
		SHA256: sha, ScanState: entity.TicketAttachmentScanApproved,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	if err := db.Exec("UPDATE `ticket_attachments` SET `blob_sha256` = ? WHERE `id` = ?", sha, row.ID).Error; err != nil {
		t.Fatalf("set blob_sha256: %v", err)
	}
}

// repeatedSHA 造一个合法的 64 位十六进制 sha256（便于在用例里读出含义）。
func repeatedSHA(prefix string) string {
	return (prefix + strings.Repeat("0", 64))[:64]
}

// adminActor 是四条接口唯一放行的身份。
func adminActor() *entity.User {
	return &entity.User{ID: 9301, Username: "SYNTHETIC_TICKET_IMAGE_CACHE_ADMIN", Role: "admin", Status: "enabled"}
}

// 四条接口在「已登录但非管理员」与「未登录」下都必须是 403，且不得产生任何写。
// 工单图片是用户上传的申诉证据，越权删比越权读严重得多。
func TestTicketImageCacheAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	sha := repeatedSHA("a1")
	seedBlob(t, db, sha, 1024, "image/png", 0, entity.TicketAttachmentScanApproved)

	for index, actor := range []*entity.User{
		{ID: 9302, Username: "SYNTHETIC_TICKET_IMAGE_CACHE_PLAIN", Role: "user", Status: "enabled"},
		nil,
	} {
		router := ticketImageCacheRouter(actor)
		cases := []struct{ method, path, body string }{
			{http.MethodGet, "/api/admin/ticket-image-cache", ""},
			{http.MethodDelete, "/api/admin/ticket-image-cache/" + sha, ""},
			{http.MethodPost, "/api/admin/ticket-image-cache/batch-delete",
				`{"sha256List":["` + sha + `"]}`},
			{http.MethodPost, "/api/admin/ticket-image-cache/clean", ""},
		}
		for _, item := range cases {
			recorder := ticketImageCacheRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("actor %d %s %s: want 403, got %d (%s)",
					index, item.method, item.path, recorder.Code, recorder.Body.String())
			}
		}
	}

	var count int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&count).Error; err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("non-admin must not delete blobs, want 1 row got %d", count)
	}
}

// 判据 ④：空库时各项显示 0 而非报错。
//
// 这条同时挡两种实现错误：① 空表时 SUM 返回 NULL 直接 Scan 到 int64 会报错/留 -1；
// ② 分组查询在无行时返回 nil 切片被当成「查询失败」。
func TestTicketImageCacheListOnEmptyDatabaseReportsZeroWithoutError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTicketImageCacheTestDB(t)

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodGet, "/api/admin/ticket-image-cache", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty database must be 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var result service.TicketImageCacheListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("empty database must return an empty (non-nil) item list, got %#v", result.Items)
	}
	// 四个数字全部是 0，且保留策略是默认值（不是 0 —— 0 在保留策略里是非法值）。
	summary := result.Summary
	if summary.TotalCount != 0 || summary.TotalBytes != 0 || summary.ZeroRefCount != 0 {
		t.Fatalf("want all-zero totals, got count=%d bytes=%d zeroRef=%d",
			summary.TotalCount, summary.TotalBytes, summary.ZeroRefCount)
	}
	if summary.ScanStates != (service.TicketImageCacheScanCounts{}) {
		t.Fatalf("want zero scan-state buckets, got %+v", summary.ScanStates)
	}
	if summary.Retention.MaxAgeDays != 180 || summary.Retention.MaxTotalGiB != 20 ||
		summary.Retention.CleanupIntervalMinutes != 60 {
		t.Fatalf("want default retention 180/20/60, got %+v", summary.Retention)
	}
	if summary.Retention.MaxTotalBytes != 20<<30 {
		t.Fatalf("want 20 GiB = %d bytes, got %d", int64(20<<30), summary.Retention.MaxTotalBytes)
	}
}

// 汇总必须是**跨整个筛选范围**的，并且四个数字各自可独立验证。
//
// 这是页面四个必须显示的项的取值来源，所以逐项钉死：
//
//	① totalBytes    总占用字节
//	② zeroRefCount  ref_count = 0 的待清理数
//	③ scanStates    按 scan_state 分组
//	④ retention     三个保留策略值（含从 system_settings 读到的**非默认**值）
func TestTicketImageCacheListSummaryCoversWholeFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)

	// 三张 approved（其中两张 ref_count=0）+ 一张 sensitive + 一张 failed。
	seedBlob(t, db, repeatedSHA("a1"), 1000, "image/png", 0, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, repeatedSHA("a2"), 2000, "image/png", 0, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, repeatedSHA("a3"), 4000, "image/png", 2, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, repeatedSHA("a4"), 8000, "image/jpeg", 1, entity.TicketAttachmentScanSensitive)
	seedBlob(t, db, repeatedSHA("a5"), 16000, "image/jpeg", 0, entity.TicketAttachmentScanFailed)

	// 保留策略改成**非默认**值：默认值断言会被「硬编码成默认值」的坏实现蒙过。
	for key, value := range map[string]string{
		"ticket_attachment_retention_max_age_days":             "90",
		"ticket_attachment_retention_max_total_gib":            "5",
		"ticket_attachment_retention_cleanup_interval_minutes": "15",
	} {
		if err := db.Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}

	// 页大小 1：列表只回一行，但汇总必须跨全量。
	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()), http.MethodGet,
		"/api/admin/ticket-image-cache?page=1&pageSize=1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var result service.TicketImageCacheListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if len(result.Items) != 1 || result.Total != 5 {
		t.Fatalf("want 1 item on page and total=5, got items=%d total=%d", len(result.Items), result.Total)
	}
	summary := result.Summary
	// ① 总占用：1000+2000+4000+8000+16000 = 31000（不只算当前页那一条）。
	if summary.TotalBytes != 31000 {
		t.Fatalf("totalBytes must cover the whole filter: want 31000, got %d", summary.TotalBytes)
	}
	if summary.TotalCount != 5 {
		t.Fatalf("want totalCount 5, got %d", summary.TotalCount)
	}
	// ② 待清理数：a1/a2/a5 三张 ref_count=0。
	if summary.ZeroRefCount != 3 {
		t.Fatalf("want zeroRefCount 3, got %d", summary.ZeroRefCount)
	}
	// ③ 按 scan_state 分组。
	want := service.TicketImageCacheScanCounts{Pending: 0, Approved: 3, Sensitive: 1, Failed: 1}
	if summary.ScanStates != want {
		t.Fatalf("scan-state buckets: want %+v, got %+v", want, summary.ScanStates)
	}
	if summary.ScanStates.Total() != summary.TotalCount {
		t.Fatalf("bucket sum %d must equal totalCount %d",
			summary.ScanStates.Total(), summary.TotalCount)
	}
	// ④ 保留策略读的是库里改过的值，不是硬编码默认值。
	if summary.Retention.MaxAgeDays != 90 || summary.Retention.MaxTotalGiB != 5 ||
		summary.Retention.CleanupIntervalMinutes != 15 {
		t.Fatalf("want retention 90/5/15 from settings, got %+v", summary.Retention)
	}
	if summary.Retention.MaxTotalBytes != 5<<30 {
		t.Fatalf("want 5 GiB bytes, got %d", summary.Retention.MaxTotalBytes)
	}
}

// 筛选生效：media_type / scan_state / ref_count / sha256 前缀四者各挡一次。
//
// 「分项计数不随 scan_state 筛选塌成一项」单独断言：页面在选了「只看 approved」
// 时仍要显示全部四桶，否则分项就变成一句废话。
func TestTicketImageCacheListFiltersAndKeepsScanBuckets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)

	seedBlob(t, db, repeatedSHA("b1"), 100, "image/png", 0, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, repeatedSHA("b2"), 200, "image/png", 3, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, repeatedSHA("b3"), 400, "image/jpeg", 0, entity.TicketAttachmentScanSensitive)
	seedBlob(t, db, "c"+repeatedSHA("x")[1:], 800, "image/jpeg", 0, entity.TicketAttachmentScanPending)

	router := ticketImageCacheRouter(adminActor())

	cases := []struct {
		name  string
		query string
		want  int64
		bytes int64
		zero  int64
	}{
		{"mediaType=image/png", "mediaType=image/png", 2, 300, 1},
		{"scanState=approved", "scanState=approved", 2, 300, 1},
		{"refFilter=zero", "refFilter=zero", 3, 1300, 3},
		{"refFilter=referenced", "refFilter=referenced", 1, 200, 0},
		{"keyword 前缀 c", "keyword=c", 1, 800, 1},
	}
	for _, item := range cases {
		recorder := ticketImageCacheRequest(router, http.MethodGet,
			"/api/admin/ticket-image-cache?"+item.query, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", item.name, recorder.Code, recorder.Body.String())
		}
		var result service.TicketImageCacheListResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatalf("%s: decode: %v", item.name, err)
		}
		if result.Total != item.want || result.Summary.TotalBytes != item.bytes ||
			result.Summary.ZeroRefCount != item.zero {
			t.Fatalf("%s: want total=%d bytes=%d zero=%d, got total=%d bytes=%d zero=%d",
				item.name, item.want, item.bytes, item.zero,
				result.Total, result.Summary.TotalBytes, result.Summary.ZeroRefCount)
		}
	}

	// 选 scanState=approved 时，分项桶仍是全量的（3 个 approved + 1 sensitive + 1 pending）。
	recorder := ticketImageCacheRequest(router, http.MethodGet,
		"/api/admin/ticket-image-cache?scanState=approved", "")
	var filtered service.TicketImageCacheListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	buckets := filtered.Summary.ScanStates
	if buckets.Approved != 2 || buckets.Sensitive != 1 || buckets.Pending != 1 {
		t.Fatalf("scan buckets must stay全量 when scanState is filtered, got %+v", buckets)
	}

	// 非法 scanState 必须 400：放下去只会返回空列表，管理员会以为是「真没数据」。
	bad := ticketImageCacheRequest(router, http.MethodGet,
		"/api/admin/ticket-image-cache?scanState=bogus", "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown scanState must be 400, got %d (%s)", bad.Code, bad.Body.String())
	}
}

// 判据 ①：删除单条时 ref_count > 0 **必须拒绝**，并说明被几张工单引用。
//
// 变异方向（任务书要求）：把 service 的 `if effective > 0 { ... continue }`
// 改成允许强删，本用例必须变红（详见交付报告的变异三态输出）。
func TestTicketImageCacheDeleteRefusesReferencedBlob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	// ref_count=2 且**真的有两个附件行指向它**（事实值与缓存值一致）。
	sha := repeatedSHA("d1")
	seedBlob(t, db, sha, 4096, "image/png", 2, entity.TicketAttachmentScanApproved)
	seedAttachmentRef(t, db, 101, sha)
	seedAttachmentRef(t, db, 102, sha)

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodDelete, "/api/admin/ticket-image-cache/"+sha, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("deleting a referenced blob must be 409, got %d (%s)",
			recorder.Code, recorder.Body.String())
	}
	var response struct {
		Message string                               `json:"message"`
		Deleted int64                                `json:"deleted"`
		Blocked []service.TicketImageCacheBlockedRef `json:"blocked"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if response.Deleted != 0 {
		t.Fatalf("must not delete anything, got deleted=%d", response.Deleted)
	}
	if len(response.Blocked) != 1 || response.Blocked[0].RefCount != 2 {
		t.Fatalf("want one blocked entry with refCount=2, got %+v", response.Blocked)
	}
	// 「说明被几张工单引用」必须出现在**给管理员看的文案**里，不能只在结构化字段里。
	if !strings.Contains(response.Message, "2") || !strings.Contains(response.Message, "引用") {
		t.Fatalf("message must explain the reference count, got %q", response.Message)
	}
	if response.Blocked[0].SHA256 != sha {
		t.Fatalf("blocked entry must carry the sha256, got %q", response.Blocked[0].SHA256)
	}

	// 字节本体一个都不能丢。
	var remaining int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", sha).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("referenced blob must survive, got %d rows", remaining)
	}
}

// ref_count 列被写错（缓存少算）时也必须拒绝：计数的事实来源是 COUNT。
//
// 这是「取较大者」判据的钉子：若实现只看 ref_count 列，本用例会误删。
func TestTicketImageCacheDeleteRefusesWhenColumnUnderstatesRefs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	sha := repeatedSHA("d2")
	// 列值故意写 0，但事实上有 1 个附件行指向它。
	seedBlob(t, db, sha, 2048, "image/png", 0, entity.TicketAttachmentScanApproved)
	seedAttachmentRef(t, db, 103, sha)

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodDelete, "/api/admin/ticket-image-cache/"+sha, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale ref_count=0 must still be refused, got %d (%s)",
			recorder.Code, recorder.Body.String())
	}
	var remaining int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", sha).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("blob with real references must survive, got %d rows", remaining)
	}
}

// ref_count = 0 的单条可以删，且重复删除是幂等成功（200 + deleted=0）。
func TestTicketImageCacheDeleteUnreferencedBlobThenIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	sha := repeatedSHA("d3")
	seedBlob(t, db, sha, 512, "image/png", 0, entity.TicketAttachmentScanApproved)

	router := ticketImageCacheRouter(adminActor())
	first := ticketImageCacheRequest(router, http.MethodDelete,
		"/api/admin/ticket-image-cache/"+sha, "")
	if first.Code != http.StatusOK {
		t.Fatalf("unreferenced blob must be deletable, got %d (%s)", first.Code, first.Body.String())
	}
	var response struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Deleted != 1 {
		t.Fatalf("want deleted=1, got %d", response.Deleted)
	}

	second := ticketImageCacheRequest(router, http.MethodDelete,
		"/api/admin/ticket-image-cache/"+sha, "")
	if second.Code != http.StatusOK {
		t.Fatalf("repeat delete must be idempotent 200, got %d (%s)", second.Code, second.Body.String())
	}
	var repeat struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &repeat); err != nil {
		t.Fatalf("decode repeat: %v", err)
	}
	if repeat.Deleted != 0 {
		t.Fatalf("repeat delete must report deleted=0, got %d", repeat.Deleted)
	}

	// 非法 sha256 是 400（不是 404）：「参数不对」与「记录不存在」必须可区分。
	bad := ticketImageCacheRequest(router, http.MethodDelete,
		"/api/admin/ticket-image-cache/not-a-sha256", "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid sha256 must be 400, got %d (%s)", bad.Code, bad.Body.String())
	}
}

// 判据 ②：批量删除同样遵守 ref_count 保护。
//
// 混合批量（1 条无引用 + 2 条有引用）：无引用的被删、有引用的被列出，
// 返回 200 + 部分成功说明。**不允许**「单条删不掉但批量能删掉」。
func TestTicketImageCacheBatchDeleteHonoursRefProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)

	free := repeatedSHA("e1")
	held := repeatedSHA("e2")
	heldToo := repeatedSHA("e3")
	seedBlob(t, db, free, 100, "image/png", 0, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, held, 200, "image/png", 1, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, heldToo, 400, "image/png", 3, entity.TicketAttachmentScanApproved)
	seedAttachmentRef(t, db, 201, held)
	seedAttachmentRef(t, db, 202, heldToo)

	body := `{"sha256List":["` + free + `","` + held + `","` + heldToo + `"]}`
	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/batch-delete", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("partial success must be 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Message string                               `json:"message"`
		Deleted int64                                `json:"deleted"`
		Blocked []service.TicketImageCacheBlockedRef `json:"blocked"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if response.Deleted != 1 {
		t.Fatalf("only the unreferenced blob may be deleted, got deleted=%d", response.Deleted)
	}
	if len(response.Blocked) != 2 {
		t.Fatalf("want 2 blocked entries, got %+v", response.Blocked)
	}
	blockedBySHA := map[string]int32{}
	for _, entry := range response.Blocked {
		blockedBySHA[entry.SHA256] = entry.RefCount
	}
	if blockedBySHA[held] != 1 || blockedBySHA[heldToo] != 3 {
		t.Fatalf("blocked entries must carry real ref counts, got %+v", blockedBySHA)
	}

	// 两条被引用的必须都还在，被删的那条必须真的没了。
	var survivors []string
	if err := db.Model(&entity.TicketAttachmentBlob{}).Pluck("sha256", &survivors).Error; err != nil {
		t.Fatalf("pluck: %v", err)
	}
	if len(survivors) != 2 {
		t.Fatalf("want 2 surviving blobs, got %d (%v)", len(survivors), survivors)
	}
	for _, sha := range survivors {
		if sha == free {
			t.Fatalf("the unreferenced blob %s must be deleted", free)
		}
	}
}

// 批量全部被挡住时返回 409，与单条删除的状态码一致。
func TestTicketImageCacheBatchDeleteAllBlockedReturnsConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	first := repeatedSHA("f1")
	second := repeatedSHA("f2")
	seedBlob(t, db, first, 100, "image/png", 1, entity.TicketAttachmentScanApproved)
	seedBlob(t, db, second, 100, "image/png", 2, entity.TicketAttachmentScanApproved)
	seedAttachmentRef(t, db, 301, first)
	seedAttachmentRef(t, db, 302, second)

	body := `{"sha256List":["` + first + `","` + second + `"]}`
	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/batch-delete", body)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("all blocked must be 409, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var count int64
	if err := db.Model(&entity.TicketAttachmentBlob{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("nothing may be deleted, want 2 rows got %d", count)
	}

	// 空列表与非法标识都是 400（不能退化成「全删」）。
	for _, bad := range []string{`{"sha256List":[]}`, `{"sha256List":["nope"]}`} {
		response := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
			http.MethodPost, "/api/admin/ticket-image-cache/batch-delete", bad)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s must be 400, got %d (%s)", bad, response.Code, response.Body.String())
		}
	}
}

// 判据 ③：「立即清理」必须返回**两个数字**，且顺序是「先按时间、再按大小」。
//
// 合成一个 total 就无法判断哪条策略生效 —— 所以本用例分别断言 agePurged
// 与 sizePurged 的**不同取值**（1 与 2），任何把它们相加/取同一个数的实现都会红。
func TestTicketImageCacheCleanReportsBothNumbersInOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketImageCacheTestDB(t)
	// 保留策略设成非默认值，验证清理拿到的是**库里的值**而不是硬编码。
	for key, value := range map[string]string{
		"ticket_attachment_retention_max_age_days":             "77",
		"ticket_attachment_retention_max_total_gib":            "3",
		"ticket_attachment_retention_cleanup_interval_minutes": "5",
	} {
		if err := db.Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}

	var calls []string
	var gotAgeDays int
	var gotTotalBytes int64
	originalAge, originalSize := pruneTicketImageCacheByAge, pruneTicketImageCacheBySize
	t.Cleanup(func() {
		pruneTicketImageCacheByAge, pruneTicketImageCacheBySize = originalAge, originalSize
	})
	pruneTicketImageCacheByAge = func(_ *gorm.DB, _ time.Time, maxAgeDays int) (int64, error) {
		calls = append(calls, "age")
		gotAgeDays = maxAgeDays
		return 1, nil
	}
	pruneTicketImageCacheBySize = func(_ *gorm.DB, _ time.Time, maxTotalBytes int64) (int64, error) {
		calls = append(calls, "size")
		gotTotalBytes = maxTotalBytes
		return 2, nil
	}

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/clean", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		AgePurged  int64                             `json:"agePurged"`
		SizePurged int64                             `json:"sizePurged"`
		Available  bool                              `json:"available"`
		Retention  service.TicketImageCacheRetention `json:"retention"`
		Message    string                            `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if response.AgePurged != 1 || response.SizePurged != 2 {
		t.Fatalf("both numbers must be reported separately: want 1 and 2, got age=%d size=%d",
			response.AgePurged, response.SizePurged)
	}
	if !response.Available {
		t.Fatal("available must be true when both prune functions are wired")
	}
	if response.Retention.MaxAgeDays != 77 || response.Retention.MaxTotalGiB != 3 {
		t.Fatalf("clean must use the stored retention values, got %+v", response.Retention)
	}
	// 传入清理函数的阈值必须是库里那套值（不是默认值）。
	if gotAgeDays != 77 {
		t.Fatalf("age prune must receive 77 days, got %d", gotAgeDays)
	}
	if gotTotalBytes != 3<<30 {
		t.Fatalf("size prune must receive 3 GiB = %d bytes, got %d", int64(3<<30), gotTotalBytes)
	}
	// 顺序：先时间后大小（反过来会让 AgePurged=0 / SizePurged>0，两条策略因果颠倒）。
	if len(calls) != 2 || calls[0] != "age" || calls[1] != "size" {
		t.Fatalf("prune order must be age then size, got %v", calls)
	}
	// 人话说明里两个数字都要出现。
	if !strings.Contains(response.Message, "1") || !strings.Contains(response.Message, "2") {
		t.Fatalf("message must mention both counts, got %q", response.Message)
	}
}

// 「按时间」失败时必须仍下发已拿到的数字，且**不阻断**「按大小」不该被静默吞掉。
//
// 这里钉的是「部分成功要如实上报」：用 5xx 会把已经删掉的条数藏起来。
func TestTicketImageCacheCleanSurfacesAgeFailureWithoutHidingCounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTicketImageCacheTestDB(t)

	originalAge, originalSize := pruneTicketImageCacheByAge, pruneTicketImageCacheBySize
	t.Cleanup(func() {
		pruneTicketImageCacheByAge, pruneTicketImageCacheBySize = originalAge, originalSize
	})
	pruneTicketImageCacheByAge = func(_ *gorm.DB, _ time.Time, _ int) (int64, error) {
		return 4, fmt.Errorf("synthetic age failure")
	}
	sizeCalled := false
	pruneTicketImageCacheBySize = func(_ *gorm.DB, _ time.Time, _ int64) (int64, error) {
		sizeCalled = true
		return 0, nil
	}

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/clean", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("a failed prune must not become 5xx, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		AgePurged int64  `json:"agePurged"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.AgePurged != 4 {
		t.Fatalf("partially purged count must be reported, got %d", response.AgePurged)
	}
	if !strings.Contains(response.Message, "失败") {
		t.Fatalf("message must say it failed, got %q", response.Message)
	}
	if sizeCalled {
		t.Fatal("size prune must not run after an age failure (its threshold depends on age's result)")
	}
}

// T-11 尚未接线时：200 + available=false + 两个 0，且**不报 5xx**。
//
// 这条同时是「不能自己写清理逻辑」的钉子：若有人为了「让按钮能用」在本任务里
// 私自实现一份清理，available 会变成 true，本用例会红。
func TestTicketImageCacheCleanReportsUnavailableWhileWaitingForT11(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTicketImageCacheTestDB(t)

	originalAge, originalSize := pruneTicketImageCacheByAge, pruneTicketImageCacheBySize
	t.Cleanup(func() {
		pruneTicketImageCacheByAge, pruneTicketImageCacheBySize = originalAge, originalSize
	})
	pruneTicketImageCacheByAge, pruneTicketImageCacheBySize = nil, nil

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/clean", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("unwired clean must be 200 (not a fault), got %d (%s)",
			recorder.Code, recorder.Body.String())
	}
	var response struct {
		AgePurged  int64  `json:"agePurged"`
		SizePurged int64  `json:"sizePurged"`
		Available  bool   `json:"available"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Available {
		t.Fatal("available must be false while T-11 is not wired")
	}
	if response.AgePurged != 0 || response.SizePurged != 0 {
		t.Fatalf("want 0/0, got age=%d size=%d", response.AgePurged, response.SizePurged)
	}
	if !strings.Contains(response.Message, "T-11") {
		t.Fatalf("message must name the blocking task, got %q", response.Message)
	}
}

// 空库时「立即清理」也不能报错（与判据 ④ 同口径）。
func TestTicketImageCacheCleanOnEmptyDatabaseStaysZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTicketImageCacheTestDB(t)

	originalAge, originalSize := pruneTicketImageCacheByAge, pruneTicketImageCacheBySize
	t.Cleanup(func() {
		pruneTicketImageCacheByAge, pruneTicketImageCacheBySize = originalAge, originalSize
	})
	pruneTicketImageCacheByAge = func(_ *gorm.DB, _ time.Time, _ int) (int64, error) { return 0, nil }
	pruneTicketImageCacheBySize = func(_ *gorm.DB, _ time.Time, _ int64) (int64, error) { return 0, nil }

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodPost, "/api/admin/ticket-image-cache/clean", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty database clean must be 200, got %d", recorder.Code)
	}
	var response struct {
		AgePurged  int64 `json:"agePurged"`
		SizePurged int64 `json:"sizePurged"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.AgePurged != 0 || response.SizePurged != 0 {
		t.Fatalf("want 0/0, got age=%d size=%d", response.AgePurged, response.SizePurged)
	}
}

// blob 表还没建（迁移未跑）时：列表 200 + 全 0，而不是 5xx。
func TestTicketImageCacheListToleratesMissingTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:ticket-image-cache-no-table?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dao.SetDefault(db)

	recorder := ticketImageCacheRequest(ticketImageCacheRouter(adminActor()),
		http.MethodGet, "/api/admin/ticket-image-cache", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("missing table must be 200 with zeros, got %d (%s)",
			recorder.Code, recorder.Body.String())
	}
	var result service.TicketImageCacheListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Total != 0 || result.Summary.TotalBytes != 0 || result.Summary.ZeroRefCount != 0 {
		t.Fatalf("missing table must report zeros, got %+v", result.Summary)
	}
	// 保留策略即使表不在也要显示（配置在 system_settings 里，与 blobs 表无关）。
	if result.Summary.Retention.MaxAgeDays != 180 {
		t.Fatalf("retention must still be reported, got %+v", result.Summary.Retention)
	}
	_ = strconv.Itoa(0)
}
