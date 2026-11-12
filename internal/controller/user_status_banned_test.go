package controller

// `banned` 状态能否真的落库（TIKU P0）。
//
// # 缺陷形态
//
// normalizeUserStatus 原本只认 enabled / disabled / closure_pending，
// **其它值一律落回 enabled**。于是管理员在用户管理页把状态改成「已封禁」，
// 写入时被静默改回 enabled —— 界面提示保存成功、实际没封、且全程不报错。
//
// 这是同一个坑的**第二次发生**（第一次是 closure_pending，见 closure.go:32 与
// closure_test.go:18-24 的注释）。两次症状完全一致，所以本文件刻意照抄
// closure_test.go 的分组方式，让两处形成可对照的一对。
//
// # 四组测试与「如果实现坏了会不会红」
//
//	A 组（三个准入谓词）：**防回归，不是修 bug**。
//	   三个谓词当前逻辑已经正确 —— banned 既不是空串也不是 enabled/closure_pending，
//	   所以自然落到 false。这组钉的是「将来有人为了放行 banned 而放宽谓词」。
//	   它**不该**随着 normalizeUserStatus 的变异变红：谓词根本不调用归一化函数，
//	   两者是独立的两层（见下面的变异验证）。
//
//	B 组（归一化函数本身）：直接钉住 normalizeUserStatus("banned") == "banned"。
//	   把 banned 分支删掉 → 必红。
//
//	C 组（端到端写入）：走真实 UpdateUser handler，落库后读回仍是 banned。
//	   这是**最关键的一组**：它证明缺陷在真实调用路径上被修好，
//	   而不是只修了一个被单测直接调用的私有函数。删掉 banned 分支 → 必红。
//
//	D 组（反向对照）：空串仍回落 enabled。
//	   保证这次改动没有顺手把「空状态默认 enabled」的既有行为改掉。

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// A 组：三个准入谓词对 banned 全部拒绝（防回归）
// ---------------------------------------------------------------------------

// TestBannedUserIsRejectedByAllAdmissionPredicates 钉住「封禁账号什么都做不了」。
//
// 三个谓词的语义区分（见 user.go 各自的注释）：
//
//	isUserEnabled       账号是否正常 —— 空串也算正常（历史兼容）
//	isUserSessionAllowed 能否持有会话 —— 多认 closure_pending（注销等待期要能撤销）
//	isUserSpendable     能否消费 —— 严格 enabled，钱的路上一律从严
//
// banned 在三个里**都必须 false**：
//   - 不能消费（显然）
//   - 不能持有会话（否则封禁后已登录的会话还能继续用，等于没封）
//   - 不能被视为「正常」
//
// ⚠️ 这组测的是谓词本身，与 normalizeUserStatus 无关。
// 变异验证时把 banned 分支删掉，本组**不应该变红** —— 变红说明分组错了
// （把归一化的断言混进了谓词组）。
func TestBannedUserIsRejectedByAllAdmissionPredicates(t *testing.T) {
	cases := []struct {
		label string
		user  *entity.User
	}{
		{"lowercase", &entity.User{Status: userStatusBanned}},
		{"uppercase", &entity.User{Status: "BANNED"}},
		{"space-padded", &entity.User{Status: "  banned  "}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if isUserEnabled(tc.user) {
				t.Fatalf("isUserEnabled must be false for status %q", tc.user.Status)
			}
			if isUserSessionAllowed(tc.user) {
				t.Fatalf("isUserSessionAllowed must be false for status %q —— "+
					"否则封禁后已登录的会话还能继续用，等于没封", tc.user.Status)
			}
			if isUserSpendable(tc.user) {
				t.Fatalf("isUserSpendable must be false for status %q", tc.user.Status)
			}
		})
	}

	// nil 账号同样全拒（三个谓词都要能容忍 nil，别在鉴权路径上 panic）。
	if isUserEnabled(nil) || isUserSessionAllowed(nil) || isUserSpendable(nil) {
		t.Fatal("nil user must be rejected by all three predicates")
	}
}

// ---------------------------------------------------------------------------
// B 组：归一化函数本身必须保住 banned
// ---------------------------------------------------------------------------

// TestNormalizeUserStatusKeepsBanned 是这组缺陷的**最小尺子**。
//
// 删掉 normalizeUserStatus 里的 banned 分支，本用例立刻以
// 「normalizeUserStatus must keep banned, got "enabled"」变红。
func TestNormalizeUserStatusKeepsBanned(t *testing.T) {
	cases := map[string]string{
		// 输入 → 期望输出。大小写与首尾空格都不应影响结果
		// （与 closure_pending / disabled 的既有处理保持一致）。
		"banned":    userStatusBanned,
		"BANNED":    userStatusBanned,
		" Banned  ": userStatusBanned,
		"  BANNED  ": userStatusBanned,
	}
	for input, want := range cases {
		if got := normalizeUserStatus(input); got != want {
			t.Fatalf("normalizeUserStatus(%q) = %q, want %q —— "+
				"banned 分支被删掉了？这会让「界面提示封禁成功、实际没封」重现", input, got, want)
		}
	}

	// 既有取值不许被这次改动带偏（同一把尺子顺带钉住）。
	if got := normalizeUserStatus("disabled"); got != "disabled" {
		t.Fatalf("disabled must stay disabled, got %q", got)
	}
	if got := normalizeUserStatus(userStatusClosurePending); got != userStatusClosurePending {
		t.Fatalf("closure_pending must stay closure_pending, got %q", got)
	}
	if got := normalizeUserStatus(userStatusEnabled); got != userStatusEnabled {
		t.Fatalf("enabled must stay enabled, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// C 组：端到端 —— 走真实 UpdateUser，落库后读回仍是 banned
// ---------------------------------------------------------------------------

// TestBannedStatusSurvivesAdminProfileUpdate 是本文件**最重要的一条**。
//
// 它刻意不直接调 normalizeUserStatus，而是走 PUT /user/:id 的真实 handler：
// 只有这样才证明了「管理员在用户管理页点封禁」这条路径真的能落库。
// 只测私有函数的话，将来有人把 handler 改成自己拼字段、绕开归一化，
// 单测仍然绿 —— 缺陷会以另一种形式回来。
//
// 变异验证的靶子就是这里：删掉 banned 分支，本用例以
// 「banned must survive the profile update, got "enabled"」变红。
func TestBannedStatusSurvivesAdminProfileUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newArchiveClosureTestDB(t)
	admin := seedArchiveAdmin(t, db, 850, "banned-update-admin")
	// 目标用户初始 enabled，模拟「一个正常用户被管理员封禁」。
	seedClosureUser(t, db, 851, "banned-update-target", phonePtr("13900000851"), 0, 0)
	recorder := doArchiveRequest(t, UpdateUser, http.MethodPut, "/adapter-service/user/851",
		[]byte(`{"nickname":"封禁用户","role":"user","status":"banned"}`), admin,
		gin.Params{{Key: "id", Value: "851"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	// 关键断言：**从库里读回**，不是看响应体。
	// 响应体回显的是 target 结构体，可能被 handler 顺手改过；库才是真相。
	if status := bannedUserStatus(t, db, 851); status != userStatusBanned {
		t.Fatalf("banned must survive the profile update, got %q —— "+
			"normalizeUserStatus 正在把它静默改回 enabled（界面提示封禁成功、实际没封）", status)
	}

	// 落库后该账号应立刻失去全部准入能力（端到端串起来：
	// 「写入成功」还必须真的产生拦截效果，否则只是存了个没人看的字符串）。
	var reloaded entity.User
	if err := db.Where("id = ?", 851).First(&reloaded).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if isUserEnabled(&reloaded) || isUserSessionAllowed(&reloaded) || isUserSpendable(&reloaded) {
		t.Fatalf("a persisted banned user must be rejected by all admission predicates, got %+v", reloaded)
	}

	// 反向：把封禁解除回 enabled，这次也必须真的落库（别把状态改成单向门）。
	recorder = doArchiveRequest(t, UpdateUser, http.MethodPut, "/adapter-service/user/851",
		[]byte(`{"nickname":"封禁用户","role":"user","status":"enabled"}`), admin,
		gin.Params{{Key: "id", Value: "851"}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 on unban, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if status := bannedUserStatus(t, db, 851); status != userStatusEnabled {
		t.Fatalf("unban must persist enabled, got %q", status)
	}

	// 顺带确认 CreateUser 路径也认 banned（两个写路径都要覆盖到）。
	// 这里只断言归一化函数在创建路径上的行为，不重复建用户 ——
	// CreateUser 需要更多前置（模型校验等），与本缺陷无关。
	if got := normalizeUserStatus("banned"); got != userStatusBanned {
		t.Fatalf("create path uses the same normalizer; got %q", got)
	}
}

// ---------------------------------------------------------------------------
// D 组：反向对照 —— 空串仍回落 enabled
// ---------------------------------------------------------------------------

// TestEmptyStatusStillDefaultsToEnabled 保证这次改动没有顺手破坏既有默认行为。
//
// 「空状态默认 enabled」是 CreateUser / UpdateUser 的历史口径：
// 老客户端不带 status 字段时（payload.Status == ""），新用户应当是可用的。
// 加 banned 分支时如果把 default 一起改了（比如改成 disabled），
// 所有老客户端的注册都会变成禁用 —— 这条用来钉住它。
func TestEmptyStatusStillDefaultsToEnabled(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		if got := normalizeUserStatus(input); got != userStatusEnabled {
			t.Fatalf("normalizeUserStatus(%q) = %q, want %q —— "+
				"空状态必须回落到 enabled（老客户端不带 status 字段时注册要能正常用）",
				input, got, userStatusEnabled)
		}
	}

	// 未知/拼错的值同样落回 enabled（保持既有行为，不在本任务里收紧）。
	// 注意：这与「新增状态必须登记」并不矛盾 —— 未知值落 enabled 是历史兼容，
	// 而 banned 是**已知**状态，必须被登记，否则就是缺陷。
	for _, input := range []string{"baned", "bannned", "suspended", "unknown-value"} {
		if got := normalizeUserStatus(input); got != userStatusEnabled {
			t.Fatalf("unknown status %q must fall back to enabled, got %q", input, got)
		}
	}
}

// bannedUserStatus 从库里读回账号状态（不经过任何归一化）。
//
// 与 closureUserStatus 同一写法：刻意用 dao.User 而不是 db.Model().Update，
// 确保读到的是真实落库值。
func bannedUserStatus(t *testing.T, db *gorm.DB, id int32) string {
	t.Helper()
	stored, err := dao.User.Where(dao.User.ID.Eq(id)).First()
	if err != nil {
		t.Fatalf("load user %d: %v", id, err)
	}
	return stored.Status
}
