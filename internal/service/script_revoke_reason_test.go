package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住「会话被撤销时留下可读的原因」（2026-11-17 用户需求）。
//
// # 用户口径
//
//	「如果出现有调用，但是没有心跳的情况下，还需要风控」
//	「需要在我的脚本添加一个通知区，显示因为没有心跳被撤销令牌等信息」
//
// # 为什么需要原因列
//
// 现状 `entity.ScriptRunSession` 只有 `RevokedAt *time.Time`，没有原因。
// 于是「心跳超时被抢占」「用户手动关闭脚本」「管理员强制撤销」三种情况
// 在数据上完全同形。通知区若只能显示「已撤销」，用户无法判断是自己做的、
// 还是客户端出问题了 —— 而「为什么」正是这个功能唯一的价值。
//
// # 判据设计的三条纪律
//
//  1. **必须区分「撤销」与「到寿」**。150 分钟正常结束不是撤销，绝不能报警。
//     通知区每次正常结束都亮红灯，用户很快学会忽略它，真异常时反而看不见。
//  2. **原因必须来自真实触发路径**。所以这些用例驱动真实的
//     ActivateLeasedScriptRunSessionWithDB / CloseUserScriptWithDB，
//     而不是直接调一个「写原因」的助手 —— 后者只能证明助手能用，
//     不能证明触发点接上了。
//  3. **不能只看"字段被写了"**。要断言具体取值，否则实现里写死一个常量
//     （比如永远写 "revoked"）也能过 —— 那样通知区还是说不出原因。
//
// # 为什么用 script_id 反查会话而不是 grant 里的 ID
//
// `ScriptRunSessionGrant` 只有 Token/ExpiresAt/ScriptID/AccessTokenID，
// 没有 session 主键。断言对象是「该脚本名下最新的那条会话」，
// 用 script_id 反查即可，不为此改动生产结构。

// TestScriptRunSessionTakeoverDoesNotMintReasonForExpiredLease 是**经真实抢占路径**的
// 反向对照（上一条只读行，覆盖不到 claimScriptLeaseSlot 的分支）。
//
// 为什么需要这一条：实测发现只读行的用例对「粗暴实现」变绿 ——
// 把 claimScriptLeaseSlot 里「仅到期」那条 UPDATE 也加上 revoked_reason，
// 上一条依然通过，因为到寿会话的 lease_slot 要等下一次抢占才会被清。
// 也就是说：**不驱动抢占，就测不到抢占分支的赋值**。
//
// 场景：会话已到寿（心跳新鲜），随后有人激活同一脚本触发抢占。
// 期望：该会话的 lease_slot 被清空，但**不得**被标记为撤销、不得有原因 ——
// 它是正常到期结束，不是心跳中断。
func TestScriptRunSessionTakeoverDoesNotMintReasonForExpiredLease(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	insertLeaseTicket(t, db, item, "synthetic-old-ticket")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
		"synthetic-old-ticket", "5555555555555555555555555555555555555555555555555555555555555555",
		"127.0.0.1", 0); err != nil {
		t.Fatalf("首次激活应成功: %v", err)
	}
	oldID := scriptRunSessionIDs(t, db, item.ID)[0]

	// 到寿但心跳新鲜 ⇒ 不可用的唯一原因是过期。
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&entity.ScriptRunSession{}).Where("id = ?", oldID).
		Updates(map[string]any{"expires_at": past, "last_heartbeat_at": time.Now()}).Error; err != nil {
		t.Fatalf("拨过期失败: %v", err)
	}

	// 触发抢占：这是让 claimScriptLeaseSlot 真正执行的那一步。
	insertLeaseTicket(t, db, item, "synthetic-new-ticket")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
		"synthetic-new-ticket", "6666666666666666666666666666666666666666666666666666666666666666",
		"127.0.0.1", 0); err != nil {
		t.Fatalf("抢占激活应成功: %v", err)
	}

	var old entity.ScriptRunSession
	if err := db.Where("id = ?", oldID).Take(&old).Error; err != nil {
		t.Fatalf("读回旧会话失败: %v", err)
	}
	// 前置：抢占确实发生了（槽位被清），否则下面两句是空断言。
	if old.LeaseSlot != nil {
		t.Fatal("前置不成立：到寿会话的槽位应已被抢占清空")
	}
	if old.RevokedAt != nil {
		t.Fatalf("到寿会话被抢占时不得标记为撤销（它是正常结束）：%v", old.RevokedAt)
	}
	if old.RevokedReason != "" {
		t.Fatalf("到寿会话被抢占时不得写入原因，实际 %q。"+
			"通知区会因此把每个正常结束的会话都报成「心跳中断」——"+
			"这种误报比不通知更糟，用户会学会忽略整个通知区", old.RevokedReason)
	}
}

// scriptRunSessionIDs 返回某脚本名下所有会话的主键（按 id 升序）。
func scriptRunSessionIDs(t *testing.T, db *gorm.DB, scriptID int32) []int64 {
	t.Helper()
	var ids []int64
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", scriptID).
		Order("id asc").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("读会话主键失败: %v", err)
	}
	return ids
}

func TestScriptRunSessionRevokedReasonDistinguishesCauses(t *testing.T) {
	t.Run("心跳超时被抢占时记录 heartbeat-timeout", func(t *testing.T) {
		db, item := newScriptLeaseFixture(t)

		// 第一个会话：正常激活。
		insertLeaseTicket(t, db, item, "synthetic-stale-ticket")
		if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
			"synthetic-stale-ticket", "1111111111111111111111111111111111111111111111111111111111111111",
			"127.0.0.1", 0); err != nil {
			t.Fatalf("首次激活应成功: %v", err)
		}

		// 把它的心跳拨到远超 ScriptHeartbeatTimeout 之前，模拟「exe 不再发心跳」。
		stale := time.Now().Add(-2 * ScriptHeartbeatTimeout)
		if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).
			UpdateColumn("last_heartbeat_at", stale).Error; err != nil {
			t.Fatalf("拨旧心跳失败: %v", err)
		}
		staleID := scriptRunSessionIDs(t, db, item.ID)[0]

		// 第二个会话抢占同一个槽位 —— 这一步会把第一个判定为心跳超时并撤销。
		insertLeaseTicket(t, db, item, "synthetic-takeover-ticket")
		if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
			"synthetic-takeover-ticket", "2222222222222222222222222222222222222222222222222222222222222222",
			"127.0.0.1", 0); err != nil {
			t.Fatalf("抢占激活应成功: %v", err)
		}

		var revoked entity.ScriptRunSession
		if err := db.Where("id = ?", staleID).Take(&revoked).Error; err != nil {
			t.Fatalf("读回首个会话失败: %v", err)
		}
		if revoked.RevokedAt == nil {
			t.Fatal("心跳超时的会话必须被撤销（revoked_at 非空）")
		}
		// 字面量而非常量：常量漂移时本断言必须变红。
		if revoked.RevokedReason != "heartbeat_timeout" {
			t.Fatalf("心跳超时撤销的原因必须是 %q，实际 %q。"+
				"通知区要靠这个值告诉用户「因为客户端心跳中断已撤销」，"+
				"写成笼统的 revoked 就失去了区分价值",
				"heartbeat_timeout", revoked.RevokedReason)
		}
	})

	t.Run("用户手动关闭时记录 user_closed", func(t *testing.T) {
		db, item := newScriptLeaseFixture(t)
		insertLeaseTicket(t, db, item, "synthetic-close-ticket")
		if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
			"synthetic-close-ticket", "3333333333333333333333333333333333333333333333333333333333333333",
			"127.0.0.1", 0); err != nil {
			t.Fatalf("激活应成功: %v", err)
		}
		sessionID := scriptRunSessionIDs(t, db, item.ID)[0]

		var operator entity.User
		if err := db.Where("id = ?", item.UserID).Take(&operator).Error; err != nil {
			t.Fatalf("读回操作者失败: %v", err)
		}
		if _, err := CloseUserScriptWithDB(db, operator, item.ID, 0); err != nil {
			t.Fatalf("关闭脚本失败: %v", err)
		}

		var closed entity.ScriptRunSession
		if err := db.Where("id = ?", sessionID).Take(&closed).Error; err != nil {
			t.Fatalf("读回会话失败: %v", err)
		}
		if closed.RevokedAt == nil {
			t.Fatal("关闭脚本必须撤销在跑的会话")
		}
		if closed.RevokedReason != "user_closed" {
			t.Fatalf("手动关闭的原因必须是 %q，实际 %q。"+
				"这个原因**不该**在通知区报警（用户自己做的），所以必须与心跳超时区分开",
				"user_closed", closed.RevokedReason)
		}
	})
}

// TestScriptRunSessionExpiryIsNotARevocation 是上一条的反向对照。
//
// 它防的是「把所有不可用都标成撤销」这种粗暴实现 —— 那种实现能让上面两条绿，
// 但会让通知区对每一个正常到寿的会话都报警。
//
// 判据：会话仅仅因为 `expires_at` 过去而不可用时，`revoked_at` 与
// `revoked_reason` 都必须保持为空。到寿是正常结束，不是撤销。
func TestScriptRunSessionExpiryIsNotARevocation(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	insertLeaseTicket(t, db, item, "synthetic-expire-ticket")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
		"synthetic-expire-ticket", "4444444444444444444444444444444444444444444444444444444444444444",
		"127.0.0.1", 0); err != nil {
		t.Fatalf("激活应成功: %v", err)
	}
	sessionID := scriptRunSessionIDs(t, db, item.ID)[0]

	// 让它到寿（拨到过去），但心跳保持新鲜 —— 这样唯一的不可用原因就是过期。
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&entity.ScriptRunSession{}).Where("id = ?", sessionID).
		Updates(map[string]any{"expires_at": past, "last_heartbeat_at": time.Now()}).Error; err != nil {
		t.Fatalf("拨过期失败: %v", err)
	}

	var expired entity.ScriptRunSession
	if err := db.Where("id = ?", sessionID).Take(&expired).Error; err != nil {
		t.Fatalf("读回会话失败: %v", err)
	}
	if expired.RevokedAt != nil {
		t.Fatalf("单纯到寿不得写入 revoked_at（到寿是正常结束，不是撤销）：%v", expired.RevokedAt)
	}
	if expired.RevokedReason != "" {
		t.Fatalf("单纯到寿不得写入 revoked_reason，实际 %q。"+
			"否则通知区会为每个正常结束的会话报警，真异常时反而看不见",
			expired.RevokedReason)
	}
}
