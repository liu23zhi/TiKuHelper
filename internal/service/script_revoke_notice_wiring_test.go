package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 本文件钉住「脚本列表把撤销通知带给前端」这条接线。
//
// # 为什么要单独一条接线用例
//
// `BuildScriptRevokeNotice` 本身已有单测（见 script_revoke_notice_test.go），
// 但那只证明「给定会话状态能算出正确结论」。它**完全不能证明**
// 「列表返回里带了这个结论」—— 这正是本仓反复出现的
// 「实现层对、接线层空」形态（33 个 admin handler 未挂路由同源）。
//
// 判据：真实走 ListUserScriptsWithDB（或当前列表入口），断言返回项里
// 有 Notice 字段且取值正确。不直接调纯函数。
func TestScriptListCarriesRevokeNotice(t *testing.T) {
	db, item := newScriptLeaseFixture(t)

	// 造一条「心跳中断被撤销」的会话：这是唯一需要报警的形态。
	insertLeaseTicket(t, db, item, "synthetic-notice-ticket")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
		"synthetic-notice-ticket", "7777777777777777777777777777777777777777777777777777777777777777",
		"127.0.0.1", 0); err != nil {
		t.Fatalf("激活应成功: %v", err)
	}
	stale := time.Now().Add(-2 * ScriptHeartbeatTimeout)
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).
		Updates(map[string]any{
			"last_heartbeat_at": stale,
			"revoked_at":        time.Now(),
			"revoked_reason":    entity.ScriptRevokeReasonHeartbeatTimeout,
		}).Error; err != nil {
		t.Fatalf("构造撤销状态失败: %v", err)
	}

	var operator entity.User
	if err := db.Where("id = ?", item.UserID).Take(&operator).Error; err != nil {
		t.Fatalf("读回操作者失败: %v", err)
	}
	items, err := ListUserScriptsWithDB(db, operator, false)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应只有 1 个脚本，实际 %d", len(items))
	}

	notice := items[0].RevokeNotice
	if notice == nil {
		t.Fatal("列表项必须携带 RevokeNotice —— 否则前端通知区拿不到任何数据，" +
			"用户永远看不到「因心跳中断已撤销」这条提示")
	}
	// 字面量而非常量：常量漂移时本断言必须变红。
	if notice.Level != "warning" {
		t.Fatalf("心跳中断的通知级别必须是 %q（需要用户行动），实际 %q", "warning", notice.Level)
	}
	if notice.Reason != "heartbeat_timeout" {
		t.Fatalf("通知原因必须是 %q，实际 %q", "heartbeat_timeout", notice.Reason)
	}
}

// TestScriptListNoticeAbsentWhenNoRevocation 是同一接线的反向对照。
//
// 防的是「实现里无条件返回一个 Notice」——那种实现能让上面那条绿，
// 但会让每个正常脚本都挂一条提示。
func TestScriptListNoticeAbsentWhenNoRevocation(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	var operator entity.User
	if err := db.Where("id = ?", item.UserID).Take(&operator).Error; err != nil {
		t.Fatalf("读回操作者失败: %v", err)
	}
	items, err := ListUserScriptsWithDB(db, operator, false)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应只有 1 个脚本，实际 %d", len(items))
	}
	if items[0].RevokeNotice != nil {
		t.Fatalf("从未运行过的脚本不得带撤销通知，实际 %+v", items[0].RevokeNotice)
	}
}
