package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

// 敏感内容「默认遮挡 + 管理员显式展开」的契约测试。
//
// 判断标准只有一条：**非管理员的响应里到底有没有原文的字符**。
// 不是「函数被调了几次」，也不是「前端有没有按钮」。

const (
	sensitiveMaskOriginalAnswer   = "答案是操你妈的选项"
	sensitiveMaskOriginalQuestion = "题目里有敏感词"
	sensitiveMaskHitJSON          = `[{"scope":"answer","status":"forbidden","forbidden":true,"words":["操你妈的","敏感词"]}]`
)

// TestLogListMasksDerivedProjectForNonAdmin 守住「Project 列」这条独立泄露通道。
//
// 背景：Project 是**读取时派生**的列（c710cdf 方案 B），search 分支的内容是
// "搜索答案，题目：<billing_records.question_text>" —— 题目原文来自**账单表**，
// 不在 description/old_answer/new_answer 这三列里。
//
// 所以只遮那三列而不遮 Project，题目原文会从「项目」列原样读出来。
// 这个用例的判据是：非管理员响应体里**任何位置**都不许出现题目原文；
// 同时 Project 仍要有可读内容（不是空、不是 "-"），否则列表失去意义。
func TestLogListMasksDerivedProjectForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	admin := &entity.User{ID: 801, Username: "admin-project", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 802, Username: "member-project", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	const requestID = "req-project-leak"
	// 账单里存着**题目原文**：Project 的 search 分支就靠它拼。
	const billingQuestion = "这是账单里的题目原文含敏感词"
	if err := db.Create(&entity.BillingRecord{
		UserID:       member.ID,
		RequestID:    requestID,
		Type:         "normal_search",
		QuestionText: billingQuestion,
		Amount:       0.5,
	}).Error; err != nil {
		t.Fatalf("seed billing: %v", err)
	}

	entry := entity.Log{
		UserID:      member.ID,
		Username:    member.Username,
		Module:      "search",
		Action:      3,
		Description: "搜索完成 requestId=" + requestID,
		NewAnswer:   `{"status":"success","answer":{"answerText":"` + sensitiveMaskOriginalAnswer + `"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}
	checkedAt := time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC)
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		true, sensitiveMaskHitJSON, checkedAt, entry.ID).Error; err != nil {
		t.Fatalf("mark hit log: %v", err)
	}

	read := func(user *entity.User) (string, map[string]any) {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
		ctx.Set("user", user)
		LogList(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		raw := recorder.Body.String()
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, row := range payload.Data {
			if id, _ := row["id"].(float64); int32(id) == entry.ID {
				return raw, row
			}
		}
		t.Fatalf("响应里找不到日志行 id=%d：%s", entry.ID, raw)
		return "", nil
	}

	// —— 非管理员：Project 不得含题目原文，且不得为空 ——
	memberRaw, memberRow := read(member)
	if strings.Contains(memberRaw, billingQuestion) {
		t.Fatalf("非管理员响应泄露了账单题目原文（Project 列未遮挡）：%s", memberRaw)
	}
	// 命中词本身也不能从 Project 漏出去。
	if strings.Contains(memberRaw, "敏感词") {
		t.Fatalf("非管理员响应泄露命中词：%s", memberRaw)
	}
	project, _ := memberRow["project"].(string)
	if strings.TrimSpace(project) == "" || project == "-" {
		t.Fatalf("Project 被遮挡后仍必须有可读内容（不能变空/'-'），实际 %q", project)
	}
	if memberRow["sensitiveMasked"] != true {
		t.Fatalf("被遮挡的行必须标记 sensitiveMasked=true：%+v", memberRow)
	}

	// —— 管理员：Project 必须是完整原文（口径不受遮挡影响）——
	adminRaw, adminRow := read(admin)
	if !strings.Contains(adminRaw, billingQuestion) {
		t.Fatalf("管理员的 Project 必须保留完整题目原文：%s", adminRaw)
	}
	adminProject, _ := adminRow["project"].(string)
	if !strings.Contains(adminProject, billingQuestion) {
		t.Fatalf("管理员 Project 口径必须与账单明细一致（含题目原文），实际 %q", adminProject)
	}
}

func TestLogListUntouchedWhenSensitiveCheckIsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	// 红线：开关默认 false 时行为与今天一致。
	// 开关关闭 ⇒ 没有检测 ⇒ 没有任何行的 sensitive_checked_at 有值 ⇒
	// 一个字段都不该被遮挡，响应必须与「加这个功能之前」完全相同。
	member := &entity.User{ID: 741, Username: "member-off", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	entry := entity.Log{
		UserID: member.ID, Username: member.Username, Module: "search", Action: 3,
		Description: "这条说明里有敏感词但不能被遮挡（从未检测）",
		OldAnswer:   `{"question":"原始请求","敏感词":"照常保留"}`,
		NewAnswer:   `{"status":"success","answer":{"answerText":"答案原文照常可读"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 注意：**不写** sensitive_flag / sensitive_checked_at —— 模拟开关关闭。

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx.Set("user", member)
	LogList(ctx)

	raw := recorder.Body.String()
	// 原文必须完整可读：说明、原始请求、原始响应一个字节都不能变。
	for _, expected := range []string{
		"这条说明里有敏感词但不能被遮挡（从未检测）",
		"答案原文照常可读",
		"照常保留",
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("开关关闭时不得遮挡任何内容，缺少 %q：%s", expected, raw)
		}
	}
	// 一个遮挡痕迹都不该有。
	for _, forbidden := range []string{"敏感内容已遮挡", "sensitiveMasked", "＊＊"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("开关关闭时响应不该出现 %q：%s", forbidden, raw)
		}
	}
}

func TestLogListMasksHitRowsForNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	admin := &entity.User{ID: 701, Username: "admin-mask", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 702, Username: "member-mask", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	// 命中行：属于 member（非管理员能读到自己的行）。
	hit := entity.Log{
		UserID:      member.ID,
		Username:    member.Username,
		Module:      "search",
		Action:      3,
		Description: sensitiveMaskOriginalQuestion + " requestId=req-mask",
		OldAnswer:   `{"question":"` + sensitiveMaskOriginalQuestion + `"}`,
		NewAnswer:   `{"status":"success","answer":{"answerText":"` + sensitiveMaskOriginalAnswer + `"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&hit).Error; err != nil {
		t.Fatalf("seed hit log: %v", err)
	}
	checkedAt := time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC)
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		true, sensitiveMaskHitJSON, checkedAt, hit.ID).Error; err != nil {
		t.Fatalf("mark hit log: %v", err)
	}

	// 未命中行：内容里有「敏感」二字，但检测通过 → 不该遮挡。
	clean := entity.Log{
		UserID:      member.ID,
		Username:    member.Username,
		Module:      "search",
		Action:      3,
		Description: "普通说明 requestId=req-clean",
		NewAnswer:   `{"status":"success","answer":{"answerText":"干净答案"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 1, 0, 0, time.UTC),
	}
	if err := db.Create(&clean).Error; err != nil {
		t.Fatalf("seed clean log: %v", err)
	}
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		false, `[{"scope":"answer","status":"clean","forbidden":false,"words":[]}]`, checkedAt, clean.ID).Error; err != nil {
		t.Fatalf("mark clean log: %v", err)
	}

	read := func(user *entity.User) (map[int32]map[string]any, map[int32]string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
		ctx.Set("user", user)
		LogList(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		// 原始响应体留着做「整条 JSON 里没有原文」的断言。
		raw := recorder.Body.String()
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		byID := make(map[int32]map[string]any, len(payload.Data))
		for _, row := range payload.Data {
			id, _ := row["id"].(float64)
			byID[int32(id)] = row
		}
		return byID, map[int32]string{hit.ID: raw, clean.ID: raw}
	}

	memberRows, memberRaw := read(member)
	hitRow := memberRows[hit.ID]
	if hitRow == nil {
		t.Fatalf("非管理员必须能看到自己的日志行")
	}
	// 核心断言：整条响应体里不能出现原文的任何实义片段。
	for _, leaked := range []string{"操你妈的", "敏感词"} {
		if strings.Contains(memberRaw[hit.ID], leaked) {
			t.Fatalf("非管理员响应泄露原文片段 %q：%s", leaked, memberRaw[hit.ID])
		}
	}
	if hitRow["sensitiveMasked"] != true {
		t.Fatalf("被遮挡的行必须显式标记 sensitiveMasked=true：%+v", hitRow)
	}
	// 标记本身必须仍在：遮挡不是「不告诉你有敏感词」。
	if hitRow["sensitiveFlag"] != true {
		t.Fatalf("遮挡不能吞掉 sensitiveFlag：%+v", hitRow)
	}
	// 其余列必须照常可读，否则日志对非管理员彻底失去排障价值。
	if hitRow["module"] != "search" {
		t.Fatalf("模块列必须保留：%+v", hitRow["module"])
	}
	if checked, _ := hitRow["createTime"].(string); !strings.Contains(checked, "2026-11-09") {
		t.Fatalf("时间列必须保留：%+v", hitRow["createTime"])
	}

	// 未命中行：一个字段都不该改。
	cleanRow := memberRows[clean.ID]
	if cleanRow == nil {
		t.Fatalf("非管理员必须能看到自己的未命中行")
	}
	if cleanRow["newAnswer"] != clean.NewAnswer {
		t.Fatalf("未命中行不得被遮挡：%v", cleanRow["newAnswer"])
	}
	if cleanRow["sensitiveMasked"] != nil {
		t.Fatalf("未遮挡的行不该出现 sensitiveMasked 字段：%+v", cleanRow)
	}

	// 管理员：拿到原文，且不带 sensitiveMasked。
	adminRows, _ := read(admin)
	adminHit := adminRows[hit.ID]
	if adminHit == nil {
		t.Fatalf("管理员必须能看到该行")
	}
	if adminHit["newAnswer"] != hit.NewAnswer {
		t.Fatalf("管理员必须拿到原文：%v", adminHit["newAnswer"])
	}
	if adminHit["description"] != hit.Description {
		t.Fatalf("管理员必须拿到原文说明：%v", adminHit["description"])
	}
	if adminHit["sensitiveMasked"] != nil {
		t.Fatalf("管理员行不该标记为已遮挡：%+v", adminHit)
	}
}

func TestLogListMaskKeepsSensitiveFieldsForFrontendBadge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	member := &entity.User{ID: 711, Username: "member-badge", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	entry := entity.Log{
		UserID: member.ID, Username: member.Username, Module: "search", Action: 3,
		Description: "搜索完成 requestId=req-badge",
		NewAnswer:   `{"status":"success","answer":{"answerText":"` + sensitiveMaskOriginalAnswer + `"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	checkedAt := time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC)
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		true, sensitiveMaskHitJSON, checkedAt, entry.ID).Error; err != nil {
		t.Fatalf("mark: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs?page=1&pageSize=10", nil)
	ctx.Set("user", member)
	LogList(ctx)

	var payload struct {
		Data []struct {
			SensitiveFlag      *bool  `json:"sensitiveFlag"`
			SensitiveWords     string `json:"sensitiveWords"`
			SensitiveCheckedAt string `json:"sensitiveCheckedAt"`
			SensitiveMasked    bool   `json:"sensitiveMasked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected 1 row, got %d", len(payload.Data))
	}
	row := payload.Data[0]
	// 徽章需要的三个字段必须在：命中明细里带着命中词，是「为什么被遮」的依据。
	if row.SensitiveFlag == nil || !*row.SensitiveFlag {
		t.Fatalf("徽章需要 sensitiveFlag=true：%+v", row)
	}
	if !strings.Contains(row.SensitiveWords, "forbidden") {
		t.Fatalf("徽章需要命中明细：%q", row.SensitiveWords)
	}
	if row.SensitiveCheckedAt == "" {
		t.Fatalf("徽章需要检测时间")
	}
	if !row.SensitiveMasked {
		t.Fatalf("非管理员的行必须标记为已遮挡")
	}
}

func TestLogSensitiveOriginalRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	admin := &entity.User{ID: 721, Username: "admin-orig", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	member := &entity.User{ID: 722, Username: "member-orig", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	entry := entity.Log{
		UserID: member.ID, Username: member.Username, Module: "search", Action: 3,
		Description: sensitiveMaskOriginalQuestion,
		NewAnswer:   `{"status":"success","answer":{"answerText":"` + sensitiveMaskOriginalAnswer + `"}}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	checkedAt := time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC)
	if err := db.Exec(`UPDATE logs SET sensitive_flag = ?, sensitive_words = ?, sensitive_checked_at = ? WHERE id = ?`,
		true, sensitiveMaskHitJSON, checkedAt, entry.ID).Error; err != nil {
		t.Fatalf("mark: %v", err)
	}

	call := func(acting *entity.User, id string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs/"+id+"/sensitive-original", nil)
		ctx.Params = gin.Params{{Key: "id", Value: id}}
		if acting != nil {
			ctx.Set("user", acting)
		}
		LogSensitiveOriginal(ctx)
		return recorder
	}

	// 普通用户：403，且响应体里绝不能带原文。
	memberRecorder := call(member, fmt.Sprint(entry.ID))
	if memberRecorder.Code != http.StatusForbidden {
		t.Fatalf("普通用户必须 403，实际 %d body=%s", memberRecorder.Code, memberRecorder.Body.String())
	}
	if strings.Contains(memberRecorder.Body.String(), "操你妈的") {
		t.Fatalf("403 响应泄露原文：%s", memberRecorder.Body.String())
	}

	// 未登录：401。
	if anonymous := call(nil, fmt.Sprint(entry.ID)); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("未登录必须 401，实际 %d", anonymous.Code)
	}

	// 编号非法：400。
	if invalid := call(admin, "abc"); invalid.Code != http.StatusBadRequest {
		t.Fatalf("非法编号必须 400，实际 %d", invalid.Code)
	}

	// 不存在：404。
	if missing := call(admin, "999999"); missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的行必须 404，实际 %d", missing.Code)
	}

	// 管理员：200 + 原文 + 遮挡形态。
	adminRecorder := call(admin, fmt.Sprint(entry.ID))
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("管理员必须 200，实际 %d body=%s", adminRecorder.Code, adminRecorder.Body.String())
	}
	var payload struct {
		ID                  int32  `json:"id"`
		SensitiveFlag       bool   `json:"sensitiveFlag"`
		OriginalDescription string `json:"originalDescription"`
		OriginalNewAnswer   string `json:"originalNewAnswer"`
		MaskedDescription   string `json:"maskedDescription"`
		MaskedNewAnswer     string `json:"maskedNewAnswer"`
	}
	if err := json.Unmarshal(adminRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.ID != entry.ID || !payload.SensitiveFlag {
		t.Fatalf("响应必须带 id 与命中标记：%+v", payload)
	}
	if payload.OriginalNewAnswer != entry.NewAnswer {
		t.Fatalf("原文必须逐字返回：%q", payload.OriginalNewAnswer)
	}
	if strings.Contains(payload.MaskedNewAnswer, "操你妈的") {
		t.Fatalf("同一响应里的遮挡形态不能含原文：%q", payload.MaskedNewAnswer)
	}
	if !strings.Contains(payload.MaskedNewAnswer, "status") {
		t.Fatalf("遮挡形态必须保留 JSON 结构（前端要靠它派生列）：%q", payload.MaskedNewAnswer)
	}
}

func TestLogSensitiveOriginalWorksForCleanRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newLogControllerTestDB(t)
	seedSensitiveLogColumns(t, db)

	admin := &entity.User{ID: 731, Username: "admin-clean-orig", Password: "123456", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	entry := entity.Log{
		UserID: admin.ID, Username: admin.Username, Module: "search", Action: 3,
		Description: "搜索完成",
		NewAnswer:   `{"answer":"干净答案"}`,
		CreateTime:  time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/logs/1/sensitive-original", nil)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(entry.ID)}}
	ctx.Set("user", admin)
	LogSensitiveOriginal(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("未检测的行也必须能取原文（管理员排障）：%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		SensitiveFlag     bool   `json:"sensitiveFlag"`
		OriginalNewAnswer string `json:"originalNewAnswer"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.SensitiveFlag {
		t.Fatalf("未检测的行不该报命中")
	}
	if payload.OriginalNewAnswer != entry.NewAnswer {
		t.Fatalf("原文必须原样：%q", payload.OriginalNewAnswer)
	}
}
