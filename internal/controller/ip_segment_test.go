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

// IP 段归属管理页控制器测试（task_doc/71 §3.5）。
//
// 全部用真 sqlite 内存库、不 mock service：列表/按 IP 定位/删除/批量删除
// 都打真实区间查询与真实 DELETE，「命中哪一段」「删了几行」这类断言只有
// 在真实 SQL 上才有意义 —— 换成 mock 就退化成「我们调用了一个函数」。
//
// 唯一的例外是 refresh：RefreshIPSegmentWithDB 必然外呼上游，
// 单测替换成桩函数（与 user_test.go 里替换 consumeCaptchaTokenWithDB 同构）。
// 它替换的是 controller 的注入点，不是 service 逻辑。

// newIPSegmentControllerTestDB 建控制器测试库（IP 段表 + 系统设置）。
func newIPSegmentControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:ip-segment-controller-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.IPSegment{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	dao.SetDefault(db)
	return db
}

// ipSegmentRouter 组装与 router.go 完全一致的中间件顺序（Auth 注入身份 + RequireAdmin）。
//
// 这里用「设置 c.Set("user", actor)」模拟 Auth 的效果：本测试关心的是
// RequireAdmin 与 handler 内的 requireAdmin 双层闸门，不是 JWT 解析。
func ipSegmentRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/adapter-service/admin/ip-segments", auth, middleware.RequireAdmin, IPSegmentList)
	router.GET("/adapter-service/admin/ip-segments/lookup", auth, middleware.RequireAdmin, IPSegmentLookup)
	router.POST("/adapter-service/admin/ip-segments/batch-delete", auth, middleware.RequireAdmin, IPSegmentBatchDelete)
	router.DELETE("/adapter-service/admin/ip-segments/:id", auth, middleware.RequireAdmin, IPSegmentDelete)
	router.POST("/adapter-service/admin/ip-segments/:id/refresh", auth, middleware.RequireAdmin, IPSegmentRefresh)
	return router
}

// ipSegmentRequest 发一次请求并返回 recorder。
func ipSegmentRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// ipv4Value 把 a.b.c.d 拼成 uint32，与 entity.IPSegment 的存储形式一致。
func ipv4Value(a, b, c, d uint32) uint32 {
	return a<<24 | b<<16 | c<<8 | d
}

// seedIPSegment 落一条段记录。
func seedIPSegment(t *testing.T, db *gorm.DB, begin uint32, end uint32, beginText string, endText string,
	sampleIP string, region string, isp string, llc string, asn string, source string) entity.IPSegment {
	t.Helper()

	segment := entity.IPSegment{
		BeginIP: begin, EndIP: end,
		BeginText: beginText, EndText: endText,
		SampleIP: sampleIP, Region: region, ISP: isp, LLC: llc, ASN: asn, Source: source,
		FetchedAt: time.Now(),
	}
	if err := db.Create(&segment).Error; err != nil {
		t.Fatalf("seed ip segment: %v", err)
	}
	return segment
}

// 五条接口在「已登录但非管理员」与「未登录」两种身份下都必须是 403，
// 且不得产生任何写 —— 段表是可直接改写归属地判定的主数据，越权写比越权读更严重。
func TestIPSegmentAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	target := seedIPSegment(t, db, ipv4Value(120, 239, 173, 0), ipv4Value(120, 239, 173, 255),
		"120.239.173.0", "120.239.173.255", "120.239.173.110", "中国 广东 中山", "中国移动", "移动", "AS9808", "standard")

	users := []*entity.User{
		{ID: 9202, Username: "SYNTHETIC_IPSEGMENT_PLAIN", Role: "user", Status: "enabled"},
		nil,
	}
	idText := strconv.FormatInt(target.ID, 10)
	for index, actor := range users {
		router := ipSegmentRouter(actor)
		cases := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodGet, "/adapter-service/admin/ip-segments", ""},
			{http.MethodGet, "/adapter-service/admin/ip-segments/lookup?ip=120.239.173.110", ""},
			{http.MethodDelete, "/adapter-service/admin/ip-segments/" + idText, ""},
			{http.MethodPost, "/adapter-service/admin/ip-segments/batch-delete", `{"asn":"AS9808"}`},
			{http.MethodPost, "/adapter-service/admin/ip-segments/" + idText + "/refresh", ""},
		}
		for _, item := range cases {
			recorder := ipSegmentRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("actor %d %s %s: want 403, got %d (%s)",
					index, item.method, item.path, recorder.Code, recorder.Body.String())
			}
		}
	}

	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if count != 1 {
		t.Fatalf("non-admin requests must not delete or add segments, want 1 row got %d", count)
	}
}

// 列表 200：筛选生效、total 是筛选后的总数、summary 跨全量算而不是只算当前页。
func TestIPSegmentListReturnsFilteredPageWithSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}

	// 两条广东移动（不同段）+ 一条北京联通。
	seedIPSegment(t, db, ipv4Value(120, 239, 173, 0), ipv4Value(120, 239, 173, 255),
		"120.239.173.0", "120.239.173.255", "120.239.173.110", "中国 广东 中山", "中国移动", "移动", "AS9808", "standard")
	seedIPSegment(t, db, ipv4Value(120, 239, 174, 0), ipv4Value(120, 239, 174, 255),
		"120.239.174.0", "120.239.174.255", "120.239.174.9", "中国 广东 深圳", "中国移动", "移动", "AS9808", "commercial")
	seedIPSegment(t, db, ipv4Value(182, 48, 98, 0), ipv4Value(182, 48, 98, 255),
		"182.48.98.0", "182.48.98.255", "182.48.98.61", "中国 北京", "中国联通", "联通", "AS4808", "standard")

	router := ipSegmentRouter(&admin)
	recorder := ipSegmentRequest(router, http.MethodGet, "/adapter-service/admin/ip-segments?asn=AS9808&page=1&pageSize=1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}

	var result service.IPSegmentListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode list: %v (%s)", err, recorder.Body.String())
	}
	// 分页页大小 1 → 只回一行，但 total 必须是筛选后的 2 条。
	if len(result.Items) != 1 {
		t.Fatalf("want 1 item on page, got %d", len(result.Items))
	}
	if result.Total != 2 {
		t.Fatalf("want total 2 for asn=AS9808, got %d", result.Total)
	}
	// 汇总跨全量：2 段 × 256 地址 = 512（段不重叠，此处等于实际覆盖数）。
	if result.Summary.SegmentCount != 2 || result.Summary.CoveredAddresses != 512 {
		t.Fatalf("summary must cover the whole filter, got segmentCount=%d covered=%d",
			result.Summary.SegmentCount, result.Summary.CoveredAddresses)
	}
	if result.Summary.ASNCount != 1 {
		t.Fatalf("want asnCount 1, got %d", result.Summary.ASNCount)
	}
	if result.Page != 1 || result.PageSize != 1 {
		t.Fatalf("want page=1 pageSize=1, got page=%d pageSize=%d", result.Page, result.PageSize)
	}
	// 排序按 begin_ip 升序，第一页必须是较小的那个段。
	if result.Items[0].BeginText != "120.239.173.0" {
		t.Fatalf("want first row begin 120.239.173.0, got %s", result.Items[0].BeginText)
	}
}

// 按 IP 查段 200：命中给 matched=true + 段；未命中给 matched=false + reason，且都是 200。
func TestIPSegmentLookupReportsMatchAndMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}
	// 大段 + 小段重叠：service 取最窄的一段（精度更高）。
	seedIPSegment(t, db, ipv4Value(120, 239, 0, 0), ipv4Value(120, 239, 255, 255),
		"120.239.0.0", "120.239.255.255", "120.239.1.1", "中国 广东", "中国移动", "移动", "AS9808", "standard")
	seedIPSegment(t, db, ipv4Value(120, 239, 173, 0), ipv4Value(120, 239, 173, 255),
		"120.239.173.0", "120.239.173.255", "120.239.173.110", "中国 广东 中山", "中国移动", "移动", "AS9808", "commercial")

	router := ipSegmentRouter(&admin)
	recorder := ipSegmentRequest(router, http.MethodGet, "/adapter-service/admin/ip-segments/lookup?ip=120.239.173.110", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200 for hit, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var hit service.IPSegmentLookupResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &hit); err != nil {
		t.Fatalf("decode lookup: %v (%s)", err, recorder.Body.String())
	}
	if !hit.Matched || hit.Segment == nil {
		t.Fatalf("want matched segment, got matched=%v segment=%v", hit.Matched, hit.Segment)
	}
	// 必须是较窄的那条（end-begin 最小），否则段精度判定就错了。
	if hit.Segment.BeginText != "120.239.173.0" || hit.Segment.Region != "中国 广东 中山" {
		t.Fatalf("want the narrowest segment, got %s / %s", hit.Segment.BeginText, hit.Segment.Region)
	}

	// 未命中：200 + matched=false + reason（不是 404，管理页要显示原因）。
	missRecorder := ipSegmentRequest(router, http.MethodGet, "/adapter-service/admin/ip-segments/lookup?ip=203.0.113.9", "")
	if missRecorder.Code != http.StatusOK {
		t.Fatalf("want 200 for miss, got %d (%s)", missRecorder.Code, missRecorder.Body.String())
	}
	var miss service.IPSegmentLookupResult
	if err := json.Unmarshal(missRecorder.Body.Bytes(), &miss); err != nil {
		t.Fatalf("decode miss: %v (%s)", err, missRecorder.Body.String())
	}
	if miss.Matched || miss.Segment != nil || strings.TrimSpace(miss.Reason) == "" {
		t.Fatalf("want unmatched with reason, got matched=%v segment=%v reason=%q", miss.Matched, miss.Segment, miss.Reason)
	}
}

// 删除 200：行真的消失；再删一次必须 404（区分「删掉了」与「本来就没有」）。
func TestIPSegmentDeleteRemovesRowThenReportsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}
	target := seedIPSegment(t, db, ipv4Value(203, 0, 113, 0), ipv4Value(203, 0, 113, 255),
		"203.0.113.0", "203.0.113.255", "203.0.113.7", "中国 云南 玉溪", "中国移动", "移动", "AS9808", "standard")

	router := ipSegmentRouter(&admin)
	path := "/adapter-service/admin/ip-segments/" + strconv.FormatInt(target.ID, 10)
	recorder := ipSegmentRequest(router, http.MethodDelete, path, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200 for delete, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if count != 0 {
		t.Fatalf("row must be deleted, still %d rows", count)
	}

	again := ipSegmentRequest(router, http.MethodDelete, path, "")
	if again.Code != http.StatusNotFound {
		t.Fatalf("deleting a missing segment must be 404, got %d (%s)", again.Code, again.Body.String())
	}
}

// 批量删除：无筛选条件必须 400（不能变成清库），带条件时返回真实删除行数。
func TestIPSegmentBatchDeleteRequiresFilterAndDeletesMatches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}
	seedIPSegment(t, db, ipv4Value(120, 239, 173, 0), ipv4Value(120, 239, 173, 255),
		"120.239.173.0", "120.239.173.255", "120.239.173.110", "中国 广东 中山", "中国移动", "移动", "AS9808", "standard")
	seedIPSegment(t, db, ipv4Value(182, 48, 98, 0), ipv4Value(182, 48, 98, 255),
		"182.48.98.0", "182.48.98.255", "182.48.98.61", "中国 北京", "中国联通", "联通", "AS4808", "standard")

	router := ipSegmentRouter(&admin)
	for _, body := range []string{`{}`, `{"keyword":"   "}`, `{"asn":"","llc":"","source":"","keyword":""}`} {
		recorder := ipSegmentRequest(router, http.MethodPost, "/adapter-service/admin/ip-segments/batch-delete", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("empty filter %s must be 400, got %d (%s)", body, recorder.Code, recorder.Body.String())
		}
	}

	recorder := ipSegmentRequest(router, http.MethodPost, "/adapter-service/admin/ip-segments/batch-delete", `{"asn":"AS9808"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode batch delete: %v (%s)", err, recorder.Body.String())
	}
	if body.Deleted != 1 {
		t.Fatalf("want deleted 1 for asn=AS9808, got %d", body.Deleted)
	}
	// 只删了匹配的那条：另一条 AS4808 必须还在。
	var remain int64
	if err := db.Model(&entity.IPSegment{}).Where("asn = ?", "AS4808").Count(&remain).Error; err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remain != 1 {
		t.Fatalf("unrelated segment must survive batch delete, got %d", remain)
	}
}

// 刷新 200：handler 把 :id 与密钥选项原样交给 service，并把结果包成 {data, configured}。
func TestIPSegmentRefreshPassesIDAndReturnsUpdatedSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}
	target := seedIPSegment(t, db, ipv4Value(120, 239, 173, 0), ipv4Value(120, 239, 173, 255),
		"120.239.173.0", "120.239.173.255", "120.239.173.110", "中国 广东 中山", "中国移动", "移动", "AS9808", "standard")

	originalRefresh := refreshIPSegment
	defer func() { refreshIPSegment = originalRefresh }()
	var gotID int64
	var gotOptions service.LocationQueryOptions
	refreshIPSegment = func(_ *gorm.DB, id int64, options service.LocationQueryOptions) (*entity.IPSegment, error) {
		gotID = id
		gotOptions = options
		updated := target
		updated.Region = "中国 广东 中山（刷新后）"
		return &updated, nil
	}

	router := ipSegmentRouter(&admin)
	recorder := ipSegmentRequest(router, http.MethodPost,
		"/adapter-service/admin/ip-segments/"+strconv.FormatInt(target.ID, 10)+"/refresh", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if gotID != target.ID {
		t.Fatalf("handler must pass :id to service, want %d got %d", target.ID, gotID)
	}
	// 没配密钥时 UAPIKey 必须是空串 —— 有值就说明把别处的密钥带出来了。
	if gotOptions.UAPIKey != "" {
		t.Fatalf("unconfigured api key must be empty, got %q", gotOptions.UAPIKey)
	}
	var body struct {
		Data       *entity.IPSegment `json:"data"`
		Configured bool              `json:"configured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refresh: %v (%s)", err, recorder.Body.String())
	}
	if body.Data == nil || body.Data.Region != "中国 广东 中山（刷新后）" {
		t.Fatalf("want updated segment in body, got %+v", body.Data)
	}
	if body.Configured {
		t.Fatalf("configured must be false without api key")
	}
}

// 直调 handler 的非管理员 403：不走路由中间件，专门验证 handler 自己的 requireAdmin。
//
// 为什么需要这一条：`TestIPSegmentAdminRejectsNonAdminOnEveryRoute` 的 403 由
// RequireAdmin 与 handler 内 requireAdmin 两道闸共同提供，任删一层它都仍然是 403 ——
// 也就是说那条用例**证明不了** handler 有没有自己校验。这里绕过中间件直接调 handler，
// 于是「handler 忘了 requireAdmin」这个具体缺陷只有它能抓到（变异验证用它）。
func TestIPSegmentHandlersRejectNonAdminWithoutRouteMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newIPSegmentControllerTestDB(t)
	player := &entity.User{ID: 9203, Username: "SYNTHETIC_IPSEGMENT_PLAIN", Role: "user", Status: "enabled"}
	target := seedIPSegment(t, db, ipv4Value(203, 0, 113, 0), ipv4Value(203, 0, 113, 255),
		"203.0.113.0", "203.0.113.255", "203.0.113.7", "中国 云南 玉溪", "中国移动", "移动", "AS9808", "standard")
	idText := strconv.FormatInt(target.ID, 10)

	handlerCases := []struct {
		name    string
		method  string
		path    string
		body    string
		params  gin.Params
		handler gin.HandlerFunc
	}{
		{"list", http.MethodGet, "/adapter-service/admin/ip-segments", "", nil, IPSegmentList},
		{"lookup", http.MethodGet, "/adapter-service/admin/ip-segments/lookup?ip=203.0.113.7", "", nil, IPSegmentLookup},
		{"delete", http.MethodDelete, "/adapter-service/admin/ip-segments/" + idText, "", gin.Params{{Key: "id", Value: idText}}, IPSegmentDelete},
		{"batch-delete", http.MethodPost, "/adapter-service/admin/ip-segments/batch-delete", `{"asn":"AS9808"}`, nil, IPSegmentBatchDelete},
		{"refresh", http.MethodPost, "/adapter-service/admin/ip-segments/" + idText + "/refresh", "", gin.Params{{Key: "id", Value: idText}}, IPSegmentRefresh},
	}
	for _, item := range handlerCases {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(item.method, item.path, bytes.NewBufferString(item.body))
		if item.body != "" {
			ctx.Request.Header.Set("Content-Type", "application/json")
		}
		ctx.Params = item.params
		ctx.Set("user", player)

		item.handler(ctx)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: handler must reject a logged-in non-admin on its own, got %d (%s)",
				item.name, recorder.Code, recorder.Body.String())
		}
	}

	var count int64
	if err := db.Model(&entity.IPSegment{}).Count(&count).Error; err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if count != 1 {
		t.Fatalf("rejected calls must not mutate data, want 1 row got %d", count)
	}
}

// 刷新失败路径：段表未就绪必须是 503（不是 500），前端才能提示「数据表还没建」而不是「接口坏了」。
func TestIPSegmentRefreshMapsSchemaUnavailableTo503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newIPSegmentControllerTestDB(t)
	admin := entity.User{ID: 9201, Username: "SYNTHETIC_IPSEGMENT_ADMIN", Role: "admin", Status: "enabled"}

	originalRefresh := refreshIPSegment
	defer func() { refreshIPSegment = originalRefresh }()
	refreshIPSegment = func(_ *gorm.DB, _ int64, _ service.LocationQueryOptions) (*entity.IPSegment, error) {
		return nil, service.ErrIPSegmentSchemaUnavailable
	}

	router := ipSegmentRouter(&admin)
	recorder := ipSegmentRequest(router, http.MethodPost, "/adapter-service/admin/ip-segments/7/refresh", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("schema unavailable must be 503, got %d (%s)", recorder.Code, recorder.Body.String())
	}
}
