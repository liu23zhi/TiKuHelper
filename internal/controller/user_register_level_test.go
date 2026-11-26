package controller

// 注册成功后触发等级评估（LV1）的接线断言（137 §10.3 / A1-6）。
//
// ## 为什么必须单独一个文件
//
// service 侧的用例（internal/registry + internal/service）只证明了
// 「EvaluateUserLevelWithDB(db, id, "register") 能正确评级」——
// 它**完全不能**证明注册链路真的调了它。这正是本仓反复出现的
// 「实现层对、接线层空」形态（router.go 那 33 个未挂 handler 同源）。
//
// ## 三条判据
//
//	LV1  注册成功后 users.level_id 必须 != 0（被评过级）
//	LV2  流水 trigger 必须是 "register"（不是 manual）
//	LV3  等级评估失败不得影响注册成功（fail-open）
//
// ⚠️ 必须**显式构造 DeviceLimit 非 0 的等级**再用它验证「评估真的让上限生效」——
// 4 个种子等级的 device_limit 全是 0（137 §12.0），依赖种子数据的话
// 「评估是否发生」在设备上限层面恒等，用例会恒绿（红线 6）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newRegisterLevelTestDB 建一个含注册所需全部表 + 等级体系的库。
//
// 与 newUserControllerTestDB 的差别：多挂 user_levels / user_level_histories /
// account_device_bindings / user_device_blocks，并补 level_id / device_limit_override 列，
// 否则「注册后评估」这条路径根本跑不起来（表缺失时评估会静默早退，
// 用例会因此变成假绿）。
func newRegisterLevelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.RechargeOrder{}, &entity.TicketRefund{}, &entity.BillingRecord{}, &entity.UserLevel{}, &entity.UserLevelHistory{},
		&entity.AccountDeviceBinding{}, &entity.UserDeviceBlock{}); err != nil {
		t.Fatalf("migrate level tables: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN device_limit_override integer NOT NULL DEFAULT -1").Error; err != nil {
			t.Fatalf("add device_limit_override: %v", err)
		}
	}
	return db
}

// stubRegisterVerification 把图形/短信验证码校验短路掉（只 stub 这两处，
// 注册主流程与「评估接线」都走真实代码）。
func stubRegisterVerification(t *testing.T) {
	t.Helper()
	originalConsume, originalVerify := consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx
	t.Cleanup(func() {
		consumeRegisterCaptchaTokenInTx, verifyRegisterSMSCodeInTx = originalConsume, originalVerify
	})
	consumeRegisterCaptchaTokenInTx = func(*gorm.DB, string, string, string) error { return nil }
	verifyRegisterSMSCodeInTx = func(*gorm.DB, string, string, string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}
}

// postRegister 以真实 handler 走一次公开注册。
func postRegister(t *testing.T, username, phone string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	payload := []byte(`{"username":"` + username + `","password":"Abc12345!","nickname":"评级用户","phone":"` +
		phone + `","smsCode":"123456","captchaToken":"token-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/user/register", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	UserRegister(ctx)
	return recorder
}

// LV1 + LV2：注册成功后必须被评级，且流水 trigger 是 register。
//
// 变异方向「去掉注册评估调用」→ 本用例必红（level_id 保持 0）。
func TestUserRegisterTriggersLevelEvaluation(t *testing.T) {
	db := newRegisterLevelTestDB(t)
	if err := db.Create(&entity.SystemSetting{
		Key: service.RegisterEnabledSettingKey, Value: "true",
	}).Error; err != nil {
		t.Fatalf("seed register setting: %v", err)
	}
	// 显式构造一个 device_limit 非 0 的最低级（不依赖种子，种子全是 0）。
	lowest := entity.UserLevel{
		Name: "等级-注册触发", Rank: 1, Enabled: true, DeviceLimit: 2,
		Description: "A1-6 用例专用",
	}
	if err := db.Create(&lowest).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	stubRegisterVerification(t)

	recorder := postRegister(t, "register_level_user", "13800001111")
	if recorder.Code != http.StatusOK {
		t.Fatalf("注册必须成功，got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var created entity.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	// LV1：level_id 必须被评上（0 = 没评估）。
	levelID, err := service.CurrentUserLevelIDWithDB(db, created.ID)
	if err != nil {
		t.Fatalf("read level_id: %v", err)
	}
	if levelID == 0 {
		t.Fatalf("注册成功后必须被评级（level_id != 0），实际 %d —— "+
			"评估没接线 ⇒ 设备上限对新用户永久不生效（137 §10.3）", levelID)
	}
	if levelID != lowest.ID {
		t.Fatalf("应评到最低级 %d，实际 %d", lowest.ID, levelID)
	}

	// LV2：流水 trigger 必须是 "register"（字面量，不是 manual）。
	var histories []entity.UserLevelHistory
	if err := db.Where("user_id = ?", created.ID).Find(&histories).Error; err != nil {
		t.Fatalf("read histories: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("首次评级应写 1 行流水，实际 %d 行", len(histories))
	}
	if histories[0].Trigger != "register" {
		t.Fatalf("流水 trigger 必须是 \"register\"（与 manual/recharge 必须可区分），实际 %q", histories[0].Trigger)
	}
	if histories[0].FromLevelID != 0 || histories[0].ToLevelID != lowest.ID {
		t.Fatalf("流水应当是 0 -> %d，实际 %d -> %d", lowest.ID, histories[0].FromLevelID, histories[0].ToLevelID)
	}

	// 语义链：评估让设备上限真的生效（这是 A1-6 存在的全部理由）。
	limit, source, err := service.EffectiveDeviceLimit(db, created.ID)
	if err != nil {
		t.Fatalf("EffectiveDeviceLimit: %v", err)
	}
	if limit != 2 || source != "level" {
		t.Fatalf("注册后应读到等级上限 (2, level)，实际 (%d, %q)", limit, source)
	}
}

// 微信注册流程（registrationFlowToken 分支）同样必须触发评级。
//
// 两条注册路径各有一处「成功后写响应」的位置，只接其中一条会让
// 「用微信注册的用户设备上限不生效」—— 而这种差异在日志上完全不可见。
// 微信流程需要完整的 wechatRegisterFlowStore 会话，单测里构造成本高且
// 易变成「测的是 stub」；这里改用**源码级接线断言**，定位稳定（按审计场景串）。
func TestUserRegisterWechatFlowTriggersLevelEvaluation(t *testing.T) {
	source := readControllerSource(t, "user.go")
	// 两条注册路径（普通 / 微信）各一次调用。
	if got := countSubstring(source, "evaluateUserLevelAfterRegister(user.ID)"); got != 2 {
		t.Fatalf("两条注册路径都必须调用 evaluateUserLevelAfterRegister(user.ID)，实际 %d 处 —— "+
			"漏掉微信路径会让该路径用户的设备上限永久不生效", got)
	}
	// 每处调用都必须出现在对应审计点**之前**（失败路径不得评估）。
	for _, scene := range []string{`"scene": "register"`, `"scene": "wechat_register"`} {
		sceneIndex := indexOf(source, scene)
		if sceneIndex < 0 {
			t.Fatalf("找不到审计场景 %s —— 本用例的路径假设已失效", scene)
		}
		if lastIndexOfBefore(source, "evaluateUserLevelAfterRegister(user.ID)", sceneIndex) < 0 {
			t.Fatalf("场景 %s 的注册成功路径里没有 evaluateUserLevelAfterRegister 调用 —— "+
				"该路径的用户设备上限不会生效", scene)
		}
	}
}

// LV3：等级评估失败不得影响注册成功（fail-open）。
//
// 造「评估失败」的手法：把 user_level_histories 表换成一个同名视图，
// 于是写入流水时必然报错。评估内部会先把错误往上抛，接线层必须吞掉它。
func TestUserRegisterSurvivesLevelEvaluationFailure(t *testing.T) {
	db := newRegisterLevelTestDB(t)
	if err := db.Create(&entity.SystemSetting{
		Key: service.RegisterEnabledSettingKey, Value: "true",
	}).Error; err != nil {
		t.Fatalf("seed register setting: %v", err)
	}
	if err := db.Create(&entity.UserLevel{
		Name: "等级-失败用例", Rank: 1, Enabled: true, DeviceLimit: 0,
	}).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	// 让流水写入必然失败：删表后建一个会让 INSERT 报错的视图。
	if err := db.Migrator().DropTable(entity.TableNameUserLevelHistory); err != nil {
		t.Fatalf("drop histories: %v", err)
	}
	if err := db.Exec("CREATE VIEW " + entity.TableNameUserLevelHistory +
		" AS SELECT 1 AS id").Error; err != nil {
		t.Fatalf("create blocking view: %v", err)
	}
	stubRegisterVerification(t)

	recorder := postRegister(t, "register_level_failopen", "13800002222")
	if recorder.Code != http.StatusOK {
		t.Fatalf("等级评估失败不得让注册失败（fail-open），got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("register_level_failopen")) {
		t.Fatalf("注册成功响应必须包含新建用户，实际 %s", recorder.Body.String())
	}
	// 用户必须真的建出来了（不是「响应 200 但事务回滚」）。
	if _, err := dao.User.Where(dao.User.Username.Eq("register_level_failopen")).First(); err != nil {
		t.Fatalf("用户必须已建出：%v", err)
	}
}

// countSubstring 数子串出现次数。
func countSubstring(source, needle string) int {
	count := 0
	for offset := 0; ; {
		index := indexOf(source[offset:], needle)
		if index < 0 {
			return count
		}
		count++
		offset += index + len(needle)
	}
}

// indexOf 是 strings.Index 的薄封装（本文件刻意不引 strings，与既有 helper 风格一致）。
func indexOf(source, needle string) int {
	for i := 0; i+len(needle) <= len(source); i++ {
		if source[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// lastIndexOfBefore 返回 needle 在 source[:limit] 里最后一次出现的位置。
func lastIndexOfBefore(source, needle string, limit int) int {
	if limit > len(source) {
		limit = len(source)
	}
	last := -1
	for offset := 0; offset < limit; {
		index := indexOf(source[offset:limit], needle)
		if index < 0 {
			break
		}
		last = offset + index
		offset += index + len(needle)
	}
	return last
}
