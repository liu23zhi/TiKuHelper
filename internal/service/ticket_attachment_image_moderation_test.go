package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// withTicketImageModerator 注入假审核通道，并断言它被调用多少次。
//
// 与 withTicketAttachmentNSFWURL 一样是测试专用钩子：t.Cleanup 恢复，
// 避免用例之间互相污染（本包的所有测试共用同一个包级变量）。
func withTicketImageModerator(t *testing.T, mod TicketImageModerator) {
	t.Helper()
	previous := ticketImageModeratorOverride
	ticketImageModeratorOverride = mod
	t.Cleanup(func() { ticketImageModeratorOverride = previous })
}

// enableTicketImageModeration 写入开关键（默认是关闭的，测试里必须显式打开）。
func enableTicketImageModeration(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	if err := SetTicketImageModerationEnabledWithDB(db, &enabled); err != nil {
		t.Fatalf("enable moderation: %v", err)
	}
}

// countingModerator 返回一个纯计数器通道，verdict/err 是它每次的固定回答。
func countingModerator(calls *int32, verdict TicketImageModeration, err error) TicketImageModerator {
	return func(context.Context, []byte, string) (TicketImageModeration, error) {
		atomic.AddInt32(calls, 1)
		return verdict, err
	}
}

// newModerationTestUser 建一个普通用户，返回它（多数用例都要先有工单归属）。
func newModerationTestUser(t *testing.T, db *gorm.DB) *entity.User {
	t.Helper()
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

// TestTicketImageModerationDisabledMakesNoUpstreamCall 是本需求最核心的一条：
// 开关**默认关闭**，且关闭时**一次外呼都不发**（uapi 的 /image/nsfw 是计费 POST）。
func TestTicketImageModerationDisabledMakesNoUpstreamCall(t *testing.T) {
	db := newTicketAttachmentTestDB(t)

	// 默认值断言：从来没有写过这个键时，开关必须是关闭的。
	if TicketImageModerationEnabledWithDB(db) {
		t.Fatal("ticket_image_moderation_enabled must default to false")
	}

	var calls int32
	withTicketImageModerator(t, countingModerator(&calls, TicketImageModeration{Approved: false, Reason: "不该被调用"}, nil))

	user := newModerationTestUser(t, db)
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	// 即使通道「永远判定违规」，关闭状态下也必须正常入库、且状态是 pending
	// （没有被同步审核打标）。
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatalf("disabled moderation must not block upload: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("disabled moderation must make 0 upstream calls, got %d", got)
	}
	if info.ScanState != entity.TicketAttachmentScanPending {
		t.Fatalf("upload must still queue async scan, got %q", info.ScanState)
	}

	// 显式置 false 也一样零外呼（*bool 语义：nil = 不修改）。
	enableTicketImageModeration(t, db, false)
	if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16)); err != nil {
		t.Fatalf("explicitly disabled moderation must not block upload: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("explicitly disabled moderation must make 0 upstream calls, got %d", got)
	}

	// nil 指针 = 不修改：保持关闭。
	if err := SetTicketImageModerationEnabledWithDB(db, nil); err != nil {
		t.Fatalf("nil update must be a no-op: %v", err)
	}
	if TicketImageModerationEnabledWithDB(db) {
		t.Fatal("nil *bool must not change the switch")
	}
}

// TestTicketImageModerationFlagsSensitiveUploadWithoutBlocking 是**用户明确要求**的语义：
// 判定违规的图**照常落库**（不拦上传，用户仍能开单说明情况），只把 scan_state 置成
// sensitive —— 用户端隐藏、管理端可见（复用 #32 已有的展示链路）。
//
// 这条用例替换了旧版「Approved=false → 拒绝入库」的断言。
func TestTicketImageModerationFlagsSensitiveUploadWithoutBlocking(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	enableTicketImageModeration(t, db, true)

	var calls int32
	withTicketImageModerator(t, countingModerator(&calls,
		TicketImageModeration{Approved: false, Reason: "上游判定为敏感图片"}, nil))

	user := newModerationTestUser(t, db)
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatalf("flagged image must still be stored, got error %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("enabled moderation must call the channel exactly once, got %d", got)
	}

	// 1) 落库了，且状态直接是 sensitive（不是 pending）。
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatalf("flagged upload must be persisted: %v", err)
	}
	if row.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("flagged upload must be marked sensitive, got %q", row.ScanState)
	}
	var count int64
	if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("flagged upload must persist exactly 1 row, found %d", count)
	}

	// 2) 返回给上传者的 info 里就已经是隐藏态（前端不需要自己推导）。
	if info.Visible {
		t.Fatal("flagged attachment must report visible=false to the uploader")
	}
	if info.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("info.scanState=%q want sensitive", info.ScanState)
	}

	// 3) 普通用户读不到字节，且拿到的是「已隐藏」而不是「不存在」。
	if _, data, _, err := ReadTicketAttachmentWithDB(db, user, info.ID); !errors.Is(err, ErrTicketAttachmentHidden) || len(data) != 0 {
		t.Fatalf("owner must be denied sensitive bytes, got len=%d err=%v", len(data), err)
	}

	// 4) 管理员能读到字节，且 allowSensitive=true（handler 据此写审计）。
	admin := &entity.User{ID: 2, Username: "root", Role: "admin", Status: "enabled"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	mimeType, data, allowSensitive, err := ReadTicketAttachmentWithDB(db, admin, info.ID)
	if err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if !allowSensitive {
		t.Fatal("admin read of flagged attachment must report allowSensitive=true")
	}
	if mimeType != "image/png" || len(data) == 0 {
		t.Fatalf("admin must receive bytes, got mime=%q len=%d", mimeType, len(data))
	}

	// 5) 审核收到的是重编码后的字节（这里退化为「非空且是 PNG」）。
	var seenBytes int
	var seenMime string
	withTicketImageModerator(t, func(_ context.Context, payload []byte, mimeType string) (TicketImageModeration, error) {
		seenBytes, seenMime = len(payload), mimeType
		return TicketImageModeration{Approved: true}, nil
	})
	if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16)); err != nil {
		t.Fatalf("approved moderation must allow upload: %v", err)
	}
	if seenBytes == 0 || seenMime != "image/png" {
		t.Fatalf("moderator must receive the re-encoded PNG, got mime=%q len=%d", seenMime, seenBytes)
	}
}

// TestTicketImageModerationUnavailableRejectsUpload 覆盖「审核**没跑成**」的三类情形：
// 通道缺失 / 上游报错 / 结论不可用（Approved=true 但带 Reason 的中间结论）。
//
// 这三类**仍然拒绝入库**（fail-closed）：它们不是「审核说不违规」，而是「审核没结论」。
// 放行等于把上游打挂就能绕过审核。与 flagged 的区别是：flagged 是**成功判定出的**
// 业务结果（图确实违规），照常落库只打标。
func TestTicketImageModerationUnavailableRejectsUpload(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	enableTicketImageModeration(t, db, true)
	user := newModerationTestUser(t, db)
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	cases := []struct {
		name string
		mod  TicketImageModerator
	}{
		{name: "upstream_error", mod: func(context.Context, []byte, string) (TicketImageModeration, error) {
			return TicketImageModeration{}, errors.New("upstream 500")
		}},
		// 上游「200 但没给出结论」在生产实现里被翻译成 error；这里直接模拟。
		{name: "no_conclusion", mod: func(context.Context, []byte, string) (TicketImageModeration, error) {
			return TicketImageModeration{}, errors.New("检测结论不可用")
		}},
		// Approved=true 但带着 Reason：既没说违规也没说正常 ⇒ 不放行，
		// 且必须报成「结论不可用」而不是「敏感」—— 后者是对用户的明确指控。
		{name: "inconclusive", mod: func(context.Context, []byte, string) (TicketImageModeration, error) {
			return TicketImageModeration{Approved: true, Reason: "上游建议 review"}, nil
		}},
		// 通道缺失（未注入、也没配密钥）⇒ 不可用。
		{name: "unconfigured", mod: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTicketImageModerator(t, tc.mod)
			_, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
			if !errors.Is(err, ErrTicketImageModerationUnavailable) {
				t.Fatalf("expected fail-closed ErrTicketImageModerationUnavailable, got %v", err)
			}
			// 「没结论」的拒绝必须与「判定违规」区分：后者不再拒绝入库，
			// 因此这里只允许 unavailable 一种错误语义，不能是别的业务错误。
			var count int64
			if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("fail-closed must not persist, found %d rows", count)
			}
		})
	}
}

// TestTicketImageModerationPassKeepsPending 审核明确通过时**不**预先打标：
// scan_state 保持 pending，交给 #32 的异步扫描继续复核（同步审核只做提前拦截，
// 不取代异步链路）。
func TestTicketImageModerationPassKeepsPending(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	enableTicketImageModeration(t, db, true)

	var calls int32
	withTicketImageModerator(t, countingModerator(&calls, TicketImageModeration{Approved: true}, nil))

	user := newModerationTestUser(t, db)
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatalf("approved moderation must allow upload: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 moderation call, got %d", got)
	}
	if info.ScanState != entity.TicketAttachmentScanPending {
		t.Fatalf("pass must leave scan_state=pending, got %q", info.ScanState)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ScanState != entity.TicketAttachmentScanPending {
		t.Fatalf("persisted scan_state=%q want pending", row.ScanState)
	}
}

// TestTicketImageModerationAsyncScanCannotDowngradeSensitive 是「上传前同步审核」与
// 「异步扫描」的**优先级规则**：上传前已判 sensitive 的行，异步扫描不得把它改回 approved。
//
// 三层保障，本用例逐层验证：
//  1. 上传时直接写 sensitive（不经 pending），worker 的取数条件是 scan_state=pending，
//     因此根本不会选中它 —— 断言 worker 跑完后**一次外呼都没发**；
//  2. persistTicketAttachmentScanResult 的 UPDATE 带 `scan_state = scanning` 条件，
//     即使被外部改回 scanning 再拿到 approved 结论，也不会覆盖 sensitive；
//  3. 状态保持 sensitive、普通用户依旧读不到。
func TestTicketImageModerationAsyncScanCannotDowngradeSensitive(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	enableTicketImageModeration(t, db, true)

	withTicketImageModerator(t, countingModerator(new(int32),
		TicketImageModeration{Approved: false, Reason: "上游判定为敏感图片"}, nil))

	user := newModerationTestUser(t, db)
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatalf("flagged upload must be stored: %v", err)
	}
	if info.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("flagged upload must be sensitive, got %q", info.ScanState)
	}

	// --- 第 1 层：worker 不会选中 sensitive 行 ---
	// 上游（异步通道）会回答 pass，如果 worker 选中了这张图，它就会被改成 approved。
	var asyncCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&asyncCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nsfw_score":0.01,"is_nsfw":false,"suggestion":"pass","risk_level":"low"}`))
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, "uapi-test-only-key", "test"); err != nil {
		t.Fatal(err)
	}
	// 让注入的审核通道失效，worker 才会走真实（httptest）通道。
	withTicketImageModerator(t, nil)
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := atomic.LoadInt32(&asyncCalls); got != 0 {
		t.Fatalf("async worker must not scan an already-sensitive row, got %d upstream calls", got)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("worker must not change sensitive state, got %q", row.ScanState)
	}

	// --- 第 2 层：即便状态被改回 scanning，approved 结论也不能覆盖 sensitive ---
	// 模拟「行被外部改成 sensitive 之后，异步结果才回来」：直接把状态置 scanning
	// 让 UPDATE 的条件成立，但把 scan_state 再改回 sensitive 才是真实场景 ——
	// 因此这里直接对 sensitive 行调用 persist，断言它被丢弃。
	err = persistTicketAttachmentScanResult(db, row, TicketAttachmentScanResult{
		State: entity.TicketAttachmentScanApproved,
	}, time.Now())
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("approved async result must not overwrite sensitive, got %q", row.ScanState)
	}

	// --- 第 3 层：展示判据仍然隐藏 ---
	if ticketAttachmentVisible(row) {
		t.Fatal("sensitive attachment must stay hidden")
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, user, info.ID); !errors.Is(err, ErrTicketAttachmentHidden) {
		t.Fatalf("owner must still be denied, got %v", err)
	}
}

// TestTicketImageModerationProductionChannelUsesJudgeTable 验证生产实现走的是
// uapi 的 /image/nsfw 通道、且判定规则**复用** judgeTicketAttachmentScan
// （不另写一份 is_nsfw/suggestion 分支），并验证三态各自的落库后果。
func TestTicketImageModerationProductionChannelUsesJudgeTable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		// wantState 是上传结束后库里的 scan_state；空串表示「不允许落库」。
		wantState  string
		wantReject error
		wantCall   bool
	}{
		{name: "pass", status: 200,
			body:      `{"nsfw_score":0.02,"is_nsfw":false,"suggestion":"pass","risk_level":"low"}`,
			wantState: entity.TicketAttachmentScanPending, wantCall: true},
		{name: "block_is_flagged_not_rejected", status: 200,
			body:      `{"nsfw_score":0.99,"is_nsfw":true,"suggestion":"block","risk_level":"high"}`,
			wantState: entity.TicketAttachmentScanSensitive, wantCall: true},
		{name: "review_is_flagged_not_rejected", status: 200,
			body:      `{"is_nsfw":false,"suggestion":"review","risk_level":"medium"}`,
			wantState: entity.TicketAttachmentScanSensitive, wantCall: true},
		{name: "server_error_is_rejected", status: 500, body: `{"error":"internal"}`,
			wantReject: ErrTicketImageModerationUnavailable, wantCall: true},
		{name: "missing_fields_is_rejected", status: 200, body: `{"label":"normal"}`,
			wantReject: ErrTicketImageModerationUnavailable, wantCall: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTicketAttachmentTestDB(t)
			enableTicketImageModeration(t, db, true)
			if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, "uapi-test-only-key", "test"); err != nil {
				t.Fatal(err)
			}
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			withTicketAttachmentNSFWURL(t, server.URL)

			user := newModerationTestUser(t, db)
			ticket := seedTicketAttachmentTicket(t, db, user.ID)

			info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
			if tc.wantReject != nil {
				if !errors.Is(err, tc.wantReject) {
					t.Fatalf("expected %v, got %v", tc.wantReject, err)
				}
			} else if err != nil {
				t.Fatalf("expected approval/flagging, got %v", err)
			}
			if tc.wantCall && atomic.LoadInt32(&calls) != 1 {
				t.Fatalf("expected exactly 1 upstream call, got %d", atomic.LoadInt32(&calls))
			}
			if tc.wantState != "" {
				if info.ScanState != tc.wantState {
					t.Fatalf("scan_state=%q want %q", info.ScanState, tc.wantState)
				}
				var row entity.TicketAttachment
				if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
					t.Fatalf("expected persisted row: %v", err)
				}
				if row.ScanState != tc.wantState {
					t.Fatalf("persisted scan_state=%q want %q", row.ScanState, tc.wantState)
				}
			} else {
				var count int64
				if err := db.Model(&entity.TicketAttachment{}).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("rejected upload must not persist, found %d rows", count)
				}
			}
		})
	}
}

// TestTicketTextWithSensitiveWordsIsNotBlocked 确认用户要求的另一半：
// 工单**文本**里出现敏感词不阻止提交。
//
// 本仓库的工单文本只做「非空 + 长度」校验（normalizeTicketText），没有任何
// 敏感词表或内容审核外呼；用户端遮挡由前端负责（那是另一个工作项）。这条用例
// 钉住「服务端不拦文本」这个事实，避免将来有人顺手加一道会误伤正常提问的过滤。
func TestTicketTextWithSensitiveWordsIsNotBlocked(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := newModerationTestUser(t, db)

	// 故意用最容易被「敏感词」规则误伤的写法：既要能描述问题，又不该被拦。
	subject := "账号被盗，收到大量赌博/色情推广私信，想举报并解绑"
	body := "我的账号被人拿去发赌博和色情广告，现在登录异常。" +
		"附上截图（图里可能有裸露内容）作为证据，请帮忙处理，也请告诉我怎么申诉。"

	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: subject, Body: body})
	if err != nil {
		t.Fatalf("ticket text with sensitive words must not be blocked, got %v", err)
	}
	if detail.Ticket.ID <= 0 {
		t.Fatalf("ticket must be created, got id=%d", detail.Ticket.ID)
	}

	// 落库的正文必须**原样保留**（不能被静默改写/打码），否则用户无法描述问题、
	// 管理员也看不到原始证据。
	var stored entity.Ticket
	if err := db.Where("id = ?", detail.Ticket.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Subject != subject {
		t.Fatalf("subject must be preserved verbatim, got %q", stored.Subject)
	}
	var message entity.TicketMessage
	if err := db.Where("ticket_id = ?", detail.Ticket.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.Body != body {
		t.Fatalf("body must be preserved verbatim, got %q", message.Body)
	}
}
