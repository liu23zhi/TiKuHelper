package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 令牌管理拆双入口（2026-11-14 用户诉求）的服务端数据范围判据。
//
// # 诉求原文
//
//	「脚本管理 就只显示所有脚本，因为有 我的脚本。
//	 请同步修改 令牌管理。添加我的令牌。我的令牌只显示当前账号的令牌，
//	 令牌管理是管理员的，显示所有令牌。」
//
// # 这一层要钉的是什么
//
// 前端 viewMode 只决定「看到哪个视图」，**数据范围不能由前端决定** ——
// 否则普通用户改一个查询参数就能看全站。所以判据落在 service 层：
//   ListAccessTokensWithDB    → 无论调用方是谁，只回 operator.ID 自己的
//   ListAllAccessTokensWithDB → 仅管理员回全站；非管理员**退化成只看自己**
//
// ⚠️ 用**两个用户的真实数据集**断言，不是断言 SQL 字符串。
// 断言 SQL 只能证明"写了 WHERE"，证明不了过滤到的是正确的人：
// 参数写反、绑错列、JOIN 放大都会让 SQL 看起来很对而结果全错。

// seedTokenPair 建两个用户，各挂 2 条令牌，返回 (alice, bob)。
//
// 故意交错插入（a1,b1,a2,b2）并让 id 顺序与用户顺序不同 ——
// 若实现漏掉用户过滤，返回的会是交集；若按 id 范围截断，会先拿到 a1 再拿到 b1。
func seedTokenPair(t *testing.T) (db *gorm.DB, alice, bob entity.User) {
	t.Helper()

	db = newAccessTokenTestDB(t)
	alice = seedAccessTokenOwner(t, db, 11, "user")
	bob = seedAccessTokenOwner(t, db, 12, "user")
	// seedAccessTokenOwner 按角色拼用户名（两个 user 会都叫 "user-user"），
	// 这里改成不同的 —— 否则「管理员视图的用户名能否区分到人」就无法断言：
	// JOIN 错位到同一行时两条都显示同一个名字，测试却仍然绿。
	if err := db.Model(&entity.User{}).Where("id = ?", alice.ID).Update("username", "alice").Error; err != nil {
		t.Fatalf("rename alice: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", bob.ID).Update("username", "bob").Error; err != nil {
		t.Fatalf("rename bob: %v", err)
	}
	alice.Username, bob.Username = "alice", "bob"

	rows := []entity.AccessToken{
		{UserID: alice.ID, Name: "alice-1", Status: "enabled"},
		{UserID: bob.ID, Name: "bob-1", Status: "enabled"},
		{UserID: alice.ID, Name: "alice-2", Status: "disabled"},
		{UserID: bob.ID, Name: "bob-2", Status: "enabled"},
	}
	for i := range rows {
		// token_hash 上有唯一索引（真实表结构），空串会互相冲突。
		// 这里给每条一个唯一值，但**不参与本文件的任何断言** —— 被测的是 user_id 过滤。
		rows[i].TokenHash = "hash-" + rows[i].Name
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create token %s: %v", rows[i].Name, err)
		}
	}
	return db, alice, bob
}

func namesOf(items []AccessTokenListItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

// TestListAccessTokensScopesToOperatorOwnTokens 是「我的令牌只显示当前账号的令牌」的核心判据。
//
// 对**每个用户**分别调用同一个函数，断言各自只拿到自己的两条 ——
// 只测一个用户是不够的：若过滤条件写成 `user_id = <某个固定值>`，
// 恰好测到的那个用户会通过，另一个才暴露问题。
func TestListAccessTokensScopesToOperatorOwnTokens(t *testing.T) {
	db, alice, bob := seedTokenPair(t)

	aliceTokens, err := ListAccessTokensWithDB(db, alice)
	if err != nil {
		t.Fatalf("list alice tokens: %v", err)
	}
	if got := namesOf(aliceTokens); len(got) != 2 || got[0] != "alice-2" || got[1] != "alice-1" {
		// 按 id desc：alice-2 先创建后，所以 id 更大、排在前面。
		t.Fatalf("alice 只应看到自己的两条（id desc），实际 %v", got)
	}

	bobTokens, err := ListAccessTokensWithDB(db, bob)
	if err != nil {
		t.Fatalf("list bob tokens: %v", err)
	}
	if got := namesOf(bobTokens); len(got) != 2 || got[0] != "bob-2" || got[1] != "bob-1" {
		t.Fatalf("bob 只应看到自己的两条（id desc），实际 %v", got)
	}

	// 反向防线：两个用户的集合必须**完全不交**。
	// 若某天实现退化成"返回全站"，两个调用会返回同样的 4 条，这里立刻红。
	for _, a := range aliceTokens {
		for _, b := range bobTokens {
			if a.ID == b.ID {
				t.Fatalf("alice 与 bob 拿到了同一条令牌 id=%d —— 用户过滤失效", a.ID)
			}
		}
	}
}

// TestListAllAccessTokensRefusesNonAdmin 是越权防线的核心判据。
//
// 普通用户即使显式走「全站」路径，也必须只拿到自己的 ——
// 这是「服务端强制过滤，不能靠前端传参」的落点。
func TestListAllAccessTokensRefusesNonAdmin(t *testing.T) {
	db, alice, bob := seedTokenPair(t)

	tokens, err := ListAllAccessTokensWithDB(db, alice)
	if err != nil {
		t.Fatalf("list all as non-admin: %v", err)
	}
	if got := namesOf(tokens); len(got) != 2 || got[0] != "alice-2" || got[1] != "alice-1" {
		t.Fatalf("非管理员调用全站接口必须退化成只看自己，实际拿到 %v", got)
	}
	for _, token := range tokens {
		if token.UserID != alice.ID {
			t.Fatalf("非管理员拿到了别人的令牌 user_id=%d（期望 %d）", token.UserID, alice.ID)
		}
		if token.Username != "" {
			// username 只在管理员的全站视图里 JOIN 出来；出现了说明走了管理员分支。
			t.Fatalf("非管理员视图不应带出 username（说明走了全站分支）：%q", token.Username)
		}
	}

	// bob 同样。两个用户都验，避免"只对某一个用户生效"的假绿。
	bobTokens, err := ListAllAccessTokensWithDB(db, bob)
	if err != nil {
		t.Fatalf("list all as bob: %v", err)
	}
	if len(bobTokens) != 2 {
		t.Fatalf("bob 走全站接口也必须只看到自己的 2 条，实际 %d 条", len(bobTokens))
	}
	for _, token := range bobTokens {
		if token.UserID != bob.ID {
			t.Fatalf("bob 拿到了别人的令牌 user_id=%d", token.UserID)
		}
	}
}

// TestListAllAccessTokensAdminSeesEveryoneWithUsernames 是「令牌管理显示所有令牌」的正向判据。
//
// 同时钉住 username 列：管理员视图要靠它区分"这是谁的令牌"，
// 缺了它页面上的用户列就是空白（数据对但展示错，用户会以为令牌没归属）。
func TestListAllAccessTokensAdminSeesEveryoneWithUsernames(t *testing.T) {
	db, alice, bob := seedTokenPair(t)
	admin := seedAccessTokenOwner(t, db, 99, "admin")

	tokens, err := ListAllAccessTokensWithDB(db, admin)
	if err != nil {
		t.Fatalf("list all as admin: %v", err)
	}
	if len(tokens) != 4 {
		t.Fatalf("管理员应看到全部 4 条令牌，实际 %d 条：%v", len(tokens), namesOf(tokens))
	}

	byUser := map[int32]int{}
	for _, token := range tokens {
		byUser[token.UserID]++
		if token.Username == "" {
			t.Fatalf("管理员视图必须带出 username（令牌 id=%d），否则用户列是空白", token.ID)
		}
	}
	if byUser[alice.ID] != 2 || byUser[bob.ID] != 2 {
		t.Fatalf("管理员应按用户分别看到 2 条，实际分布 %v", byUser)
	}
	// 用户名必须真的对上人，不能是同一个（JOIN 写错会全表放大或错位）。
	seen := map[string]bool{}
	for _, token := range tokens {
		seen[token.Username] = true
	}
	if len(seen) != 2 {
		t.Fatalf("管理员视图应出现两个不同用户名，实际 %v", seen)
	}
}

// TestListAccessTokensExcludesScriptBoundTokens 守住既有语义，避免本轮改动把它弄丢。
//
// 脚本派生的令牌（script_id != 0）由脚本页管理，不该出现在令牌列表里 ——
// 否则用户在「我的令牌」看到一堆自己没创建过的令牌。
func TestListAccessTokensExcludesScriptBoundTokens(t *testing.T) {
	db, alice, _ := seedTokenPair(t)

	scriptToken := entity.AccessToken{UserID: alice.ID, Name: "script-bound", Status: "enabled", ScriptID: 7, TokenHash: "hash-script-bound"}
	if err := db.Create(&scriptToken).Error; err != nil {
		t.Fatalf("create script-bound token: %v", err)
	}

	tokens, err := ListAccessTokensWithDB(db, alice)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, token := range tokens {
		if token.Name == "script-bound" {
			t.Fatal("脚本派生的令牌不得出现在令牌列表里")
		}
	}
	if len(tokens) != 2 {
		t.Fatalf("加入脚本令牌后仍应只看到自己的 2 条，实际 %d 条", len(tokens))
	}
}
