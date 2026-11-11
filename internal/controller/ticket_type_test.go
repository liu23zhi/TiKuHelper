package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newTicketTypeControllerTestDB 在共用的 controller 测试库上补建 users 表：
// 系统建单/「按用户查工单」都要读 users（补 UserName），缺表会直接报 SQL 错误。
func newTicketTypeControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTicketControllerTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	return db
}

// TestTicketControllerCreateIgnoresClientCategoryAndOrigin 是 HTTP 层的钉子：
// 用户即使在请求体里塞 category / origin / priority，也必须被丢弃。
//
// 只在 service 层测是不够的 —— 真正被攻击的面是 HTTP 请求体，一条 `json:"category"`
// 的字段加上原样透传就会打开「自选可一键补发类别」的套利口子。
func TestTicketControllerCreateIgnoresClientCategoryAndOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketTypeControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Nickname: "Alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", user)
	body := `{"subject":"请给我补发赠金","body":"补发一下","category":"registration_bonus","origin":"system","priority":"urgent"}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	CreateTicket(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Ticket entity.Ticket `json:"ticket"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Ticket.Origin != entity.TicketOriginUser ||
		payload.Data.Ticket.Category != entity.TicketCategoryGeneral ||
		payload.Data.Ticket.Priority != entity.TicketPriorityNormal {
		t.Fatalf("客户端自选的三维度必须被丢弃，实际 %s/%s/%s",
			payload.Data.Ticket.Origin, payload.Data.Ticket.Category, payload.Data.Ticket.Priority)
	}
	// 落库值也核一遍（响应体与数据库两套口径是最常见的假绿）。
	var stored entity.Ticket
	if err := db.Where("id = ?", payload.Data.Ticket.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Origin != entity.TicketOriginUser || stored.Category != entity.TicketCategoryGeneral ||
		stored.Priority != entity.TicketPriorityNormal {
		t.Fatalf("落库值必须是 user/general/normal，实际 %s/%s/%s", stored.Origin, stored.Category, stored.Priority)
	}
}

// TestTicketControllerListRejectsInvalidOriginAndCategory 验证筛选参数非法时明确 400，
// 不静默忽略（静默忽略会让前端以为筛了、后端给出全量）。
func TestTicketControllerListRejectsInvalidOriginAndCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketTypeControllerTestDB(t)
	withTicketControllerDB(t, db)
	admin := &entity.User{ID: 1, Username: "admin", Role: "admin", Status: "enabled"}
	user := &entity.User{ID: 2, Username: "alice", Role: "user", Status: "enabled"}
	for _, account := range []*entity.User{admin, user} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		query string
		who   *entity.User
	}{
		{name: "admin-invalid-origin", query: "origin=root", who: admin},
		{name: "admin-invalid-category", query: "category=bogus", who: admin},
		// 普通用户也必须是 400：非法参数在任何角色下都不该被当成「不过滤」。
		{name: "user-invalid-category", query: "category=bogus", who: user},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("user", tc.who)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets?"+tc.query, nil)
			TicketList(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("非法筛选值应 400，实际 status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "无效") {
				t.Fatalf("错误文案应说明无效，实际 %s", recorder.Body.String())
			}
		})
	}
}

// TestTicketControllerSystemTicketVisibilityAndIsolation 是可见性的 HTTP 层钉子
// （45-*.md §0.23.2 Q69-2：被涉及用户**可见**，隔离只靠 user_id）：
//   - A：被涉及用户在列表与详情都能看到发给自己的系统单；
//   - B：看不到**别人**的系统单（去掉 origin 过滤后，这是 HTTP 层唯一护栏）；
//   - 管理员的 origin / category 筛选保持不变（管理能力，与用户可见性无关）。
func TestTicketControllerSystemTicketVisibilityAndIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketTypeControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 5, Username: "carol", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 6, Username: "admin", Role: "admin", Status: "enabled"}
	other := &entity.User{ID: 7, Username: "dave", Role: "user", Status: "enabled"}
	for _, account := range []*entity.User{user, admin, other} {
		if err := db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.CreateSystemTicketWithDB(db, service.CreateSystemTicketInput{
		UserID: 5, Subject: "注册赠金判定失败（自动建单）", Body: "请人工补发。",
		Category: entity.TicketCategoryRegistrationBonus,
	})
	if err != nil {
		t.Fatal(err)
	}
	systemTicketID := result.Ticket.Ticket.ID

	listTotal := func(who *entity.User, target string) (int, int64, string) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("user", who)
		ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
		TicketList(ctx)
		var payload struct {
			Data struct {
				Total int64 `json:"total"`
			} `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		return recorder.Code, payload.Data.Total, recorder.Body.String()
	}
	detailStatus := func(who *entity.User) (int, string) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("user", who)
		ctx.Params = gin.Params{{Key: "id", Value: int64ToString(systemTicketID)}}
		ctx.Request = httptest.NewRequest(http.MethodGet, "/tickets/"+int64ToString(systemTicketID), nil)
		TicketDetail(ctx)
		return recorder.Code, recorder.Body.String()
	}

	// ---- A：被涉及用户可见（列表 / 分类筛选 / 详情）----
	if status, total, body := listTotal(user, "/tickets"); status != http.StatusOK || total != 1 {
		t.Fatalf("被涉及用户必须能在列表看到发给自己的系统单，status=%d total=%d body=%s", status, total, body)
	}
	if status, total, body := listTotal(user, "/tickets?category=registration_bonus"); status != http.StatusOK || total != 1 {
		t.Fatalf("按分类筛选应命中自己的系统单，status=%d total=%d body=%s", status, total, body)
	}
	if status, body := detailStatus(user); status != http.StatusOK || !strings.Contains(body, `"origin":"system"`) {
		t.Fatalf("被涉及用户读系统单详情必须 200 且带 origin=system，status=%d body=%s", status, body)
	}

	// ---- B：别人的系统单看不到（隔离只靠 user_id）----
	if status, total, body := listTotal(other, "/tickets"); status != http.StatusOK || total != 0 {
		t.Fatalf("另一个用户不得看到别人的系统单，status=%d total=%d body=%s", status, total, body)
	}
	if status, body := detailStatus(other); status != http.StatusNotFound {
		t.Fatalf("另一个用户读别人的系统单详情必须 404，status=%d body=%s", status, body)
	}

	// ---- 管理员筛选保持不变 ----
	if status, total, body := listTotal(admin, "/tickets"); status != http.StatusOK || total != 1 {
		t.Fatalf("管理员应看到 1 张系统单，status=%d total=%d body=%s", status, total, body)
	}
	if status, total, body := listTotal(admin, "/tickets?origin=system&category=registration_bonus"); status != http.StatusOK || total != 1 {
		t.Fatalf("管理员按 origin+category 筛选应命中，status=%d total=%d body=%s", status, total, body)
	}
	if status, total, body := listTotal(admin, "/tickets?origin=user"); status != http.StatusOK || total != 0 {
		t.Fatalf("按 origin=user 不应命中系统单，status=%d total=%d body=%s", status, total, body)
	}

	// 用户自己的普通单与系统单并存：两张都要看得见（隔离不误伤、可见性不丢）。
	if _, err := service.CreateTicketWithDB(db, user, service.CreateTicketInput{Subject: "我的问题", Body: "登录失败"}); err != nil {
		t.Fatal(err)
	}
	if status, total, body := listTotal(user, "/tickets"); status != http.StatusOK || total != 2 {
		t.Fatalf("用户应同时看到自己的普通单与系统单，status=%d total=%d body=%s", status, total, body)
	}
	if status, total, body := listTotal(other, "/tickets"); status != http.StatusOK || total != 0 {
		t.Fatalf("另一个用户的列表仍必须为空，status=%d total=%d body=%s", status, total, body)
	}
}

// int64ToString 统一走 strconv，避免测试里自造格式化逻辑。
func int64ToString(value int64) string { return strconv.FormatInt(value, 10) }
