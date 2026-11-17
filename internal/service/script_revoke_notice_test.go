package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 本文件钉住「通知区怎么读撤销原因」，是与前端共享的口径层。
//
// # 为什么要一个纯函数而不是直接在 handler 里拼
//
// 通知区的判据有三态且容易写错：
//
//	心跳超时 → 必须报警（用户没做任何事，是客户端出问题了）
//	用户关闭 → 不报警（自己做的）
//	到寿/在跑 → 不报警（正常）
//
// 更危险的是第四种：**历史遗留行**（revoked_at 非空但 reason 为空，
// 即本列加入之前撤销的会话）。这种既不能报警（不知道原因），
// 也不能当成正常 —— 必须显示「已撤销」但不说原因。猜原因比不显示更糟：
// 猜错了用户会去排查一个不存在的问题。
//
// 纯函数才能把这些分支逐一断言，也不依赖 HTTP 层。
func TestScriptRevokeNotice(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	revokedAt := now.Add(-30 * time.Minute)

	cases := []struct {
		name string
		in   ScriptRevokeNoticeInput
		want ScriptRevokeNotice
	}{
		{
			name: "仍在运行：不通知",
			in: ScriptRevokeNoticeInput{
				RevokedAt: nil,
				ExpiresAt: now.Add(time.Hour),
				Now:       now,
			},
			want: ScriptRevokeNotice{Level: ScriptNoticeNone},
		},
		{
			name: "正常到寿：不通知（到寿不是异常）",
			in: ScriptRevokeNoticeInput{
				RevokedAt: nil,
				ExpiresAt: past,
				Now:       now,
			},
			want: ScriptRevokeNotice{Level: ScriptNoticeNone},
		},
		{
			name: "心跳中断被撤销：必须报警",
			in: ScriptRevokeNoticeInput{
				RevokedAt:     &revokedAt,
				RevokedReason: entity.ScriptRevokeReasonHeartbeatTimeout,
				ExpiresAt:     now.Add(time.Hour),
				Now:           now,
			},
			want: ScriptRevokeNotice{
				Level:  ScriptNoticeWarning,
				Reason: entity.ScriptRevokeReasonHeartbeatTimeout,
			},
		},
		{
			name: "用户自己关闭：不报警（自己做的）",
			in: ScriptRevokeNoticeInput{
				RevokedAt:     &revokedAt,
				RevokedReason: entity.ScriptRevokeReasonUserClosed,
				ExpiresAt:     now.Add(time.Hour),
				Now:           now,
			},
			want: ScriptRevokeNotice{Level: ScriptNoticeNone},
		},
		{
			name: "管理员撤销：提示但不报警",
			in: ScriptRevokeNoticeInput{
				RevokedAt:     &revokedAt,
				RevokedReason: entity.ScriptRevokeReasonAdminRevoked,
				ExpiresAt:     now.Add(time.Hour),
				Now:           now,
			},
			want: ScriptRevokeNotice{
				Level:  ScriptNoticeInfo,
				Reason: entity.ScriptRevokeReasonAdminRevoked,
			},
		},
		{
			name: "历史遗留（撤销但无原因）：显示已撤销，不猜原因",
			in: ScriptRevokeNoticeInput{
				RevokedAt:     &revokedAt,
				RevokedReason: "",
				ExpiresAt:     now.Add(time.Hour),
				Now:           now,
			},
			want: ScriptRevokeNotice{Level: ScriptNoticeInfo},
		},
		{
			name: "未知原因值（将来新增但本端未识别）：按提示处理，不报警",
			in: ScriptRevokeNoticeInput{
				RevokedAt:     &revokedAt,
				RevokedReason: "some_future_reason",
				ExpiresAt:     now.Add(time.Hour),
				Now:           now,
			},
			// Reason 原样透传而不是抹成空串：前端据此可以显示原始枚举值，
			// 运维排查时也能看到后端到底写了什么。设成空会被当成「历史遗留行」，
			// 反而丢了「这是个新值」这个信息。
			want: ScriptRevokeNotice{
				Level:  ScriptNoticeInfo,
				Reason: "some_future_reason",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildScriptRevokeNotice(tc.in)
			if got.Level != tc.want.Level {
				t.Fatalf("Level 期望 %q，实际 %q", tc.want.Level, got.Level)
			}
			if got.Reason != tc.want.Reason {
				t.Fatalf("Reason 期望 %q，实际 %q（不该把未知原因改写成已知值）",
					tc.want.Reason, got.Reason)
			}
		})
	}
}

// TestScriptRevokeNoticeHeartbeatMessageMentionsClient 钉住报警文案**说人话**。
//
// 判据不是「有文案就行」，而是文案里必须能让用户定位到「不是系统无故踢人，
// 是我电脑上的客户端出问题了」。否则用户只会来问「为什么把我踢了」。
func TestScriptRevokeNoticeHeartbeatMessageMentionsClient(t *testing.T) {
	revokedAt := time.Now().Add(-time.Minute)
	notice := BuildScriptRevokeNotice(ScriptRevokeNoticeInput{
		RevokedAt:     &revokedAt,
		RevokedReason: entity.ScriptRevokeReasonHeartbeatTimeout,
		ExpiresAt:     time.Now().Add(time.Hour),
		Now:           time.Now(),
	})
	if notice.Message == "" {
		t.Fatal("心跳中断必须有文案，否则通知区只能显示空白或原始枚举值")
	}
	// 关键词不写死整句：允许将来润色，但必须指向「客户端」而不是「系统/账号」。
	for _, want := range []string{"客户端", "心跳"} {
		if !containsAny(notice.Message, want) {
			t.Fatalf("心跳中断的文案必须提到 %q，让用户知道要去查客户端而不是来问客服。实际：%s",
				want, notice.Message)
		}
	}
}

func containsAny(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
