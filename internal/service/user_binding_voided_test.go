package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 作废行（state='voided'）必须从**所有**绑定判定里消失（需求 7 / task_doc/70 §3.7）。
//
// 为什么单独一个文件：这是 A3 唯一的业务风险点。作废是「不物理删除」的删除，
// 只要有一条判定查询漏了 `state != 'voided'`，一条被管理员作废的记录就会
// 继续被当成「真实发生过的绑定/释放」—— 表现为
//   · 二次注册判定误判（本该不赠送的份被挡住，或该挡的没挡住）；
//   · 追缴误触发（依据是已经作废的释放记录）。
// 两种都是静默的：没有报错、没有日志，只有账错了才被发现。
//
// 因此这里逐个判定入口各一条断言，而不是只测其中一个：4 处查询是 4 个
// 独立的 Where 子句，漏一处在测试上就是一次遗漏。

// seedVoidedBinding 造一条**已作废**的释放记录。
//
// 与 seedReleasedBinding 的唯一差别是 state='voided' 且带作废审计字段；
// active_key 必须是 nil（作废时置 NULL —— 见 VoidBindingRecordWithDB）。
func seedVoidedBinding(t *testing.T, db *gorm.DB, ownerID int32, kind, value, releaseReason string) entity.UserBindingRecord {
	t.Helper()

	now := time.Now()
	record := entity.UserBindingRecord{
		UserID:           ownerID,
		UsernameSnapshot: "voided-owner",
		Kind:             kind,
		Value:            value,
		ValueHash:        BindingValueHash(value),
		State:            entity.BindingStateVoided,
		ActiveKey:        nil,
		Source:           entity.BindingSourceUserBind,
		ReleasedAt:       &now,
		ReleaseReason:    releaseReason,
		ReleaseSource:    entity.BindingReleaseSourceUserUnbind,
		VoidReason:       "测试：作废不应参与任何判定",
		VoidedByUserID:   9,
		VoidedAt:         &now,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create voided binding: %v", err)
	}
	return record
}

// TestVoidedBindingExcludedFromLookup 覆盖保留区回看（bindingReleasedExists）
// 与「按值取 released 记录」（FindReleasedBindingByValueWithDB）两条入口。
func TestVoidedBindingExcludedFromLookup(t *testing.T) {
	db := newClawbackTestDB(t)

	// 同一类、同一释放原因下放两条：一条真的 released，一条已作废。
	// 值不同以便分别断言 —— 作废那条必须**单独**查不到任何东西。
	liveValue := "13900000001"
	voidedValue := "13900000002"
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, liveValue, entity.BindingReleaseReasonRebind)
	seedVoidedBinding(t, db, 1, entity.BindingKindPhone, voidedValue, entity.BindingReleaseReasonRebind)

	// ① 保留区回看：作废值必须**不**算「历史上出现过」。
	//    history 开关靠的就是这个结论，漏判会让作废记录继续阻塞新用户注册。
	voidedLookup, err := LookupBindingHistoryWithDB(db, voidedValue, "")
	if err != nil {
		t.Fatalf("lookup voided value: %v", err)
	}
	if voidedLookup.HistoryPhone || voidedLookup.CurrentPhone {
		t.Fatalf("作废记录不得计入任何绑定命中，got history=%v current=%v",
			voidedLookup.HistoryPhone, voidedLookup.CurrentPhone)
	}

	// ② 反向对照：真实 released 的那条必须照常命中 history。
	//    没有这条，上面 ① 在「整个查询被改坏、恒返回空」时也会绿。
	liveLookup, err := LookupBindingHistoryWithDB(db, liveValue, "")
	if err != nil {
		t.Fatalf("lookup live value: %v", err)
	}
	if !liveLookup.HistoryPhone {
		t.Fatalf("未作废的 rebind 记录必须仍命中 history，got %+v", liveLookup)
	}

	// ③ 按值取 released 记录（追缴取触发行 + FindReleasedBindingByValueWithDB）：
	//    作废值必须返回 nil（没有可用触发行 → 不追缴）。
	trigger, err := FindReleasedBindingByValueWithDB(db, entity.BindingKindPhone, voidedValue)
	if err != nil {
		t.Fatalf("find released by voided value: %v", err)
	}
	if trigger != nil {
		t.Fatalf("作废记录不得被当作追缴触发行，got id=%d state=%s", trigger.ID, trigger.State)
	}
	liveTrigger, err := FindReleasedBindingByValueWithDB(db, entity.BindingKindPhone, liveValue)
	if err != nil {
		t.Fatalf("find released by live value: %v", err)
	}
	if liveTrigger == nil {
		t.Fatal("未作废的 released 记录必须仍可取到（追缴依赖它）")
	}
}

// TestVoidedBindingExcludedFromActiveClaims 覆盖活跃侧两处入口：
// findActiveBindingByValue 与 FindActiveBindingByUser。
//
// 作废一条状态为 active 的行是业务上被禁止的（VoidBindingRecordWithDB 返回
// ErrBindingActiveNotVoidable），但**历史数据**可能因为旧版本/人工改库出现
// 「voided 且 active_key 非空」的残留。这种情况下唯一能挡住它继续被当成
// 有效绑定的就是查询里的 `state != 'voided'`，所以必须测。
func TestVoidedBindingExcludedFromActiveClaims(t *testing.T) {
	db := newClawbackTestDB(t)

	const value = "13700000009"
	// 刻意造一条「state=voided 但 active_key 有值」的行：
	// 这是查询条件失守时最危险的状态（唯一索引层面它还会占位）。
	stale := seedVoidedBinding(t, db, 1, entity.BindingKindPhone, value, "")
	staleKey := BindingValueHash(value)
	if err := db.Model(&entity.UserBindingRecord{}).Where("id = ?", stale.ID).
		UpdateColumn("active_key", staleKey).Error; err != nil {
		t.Fatalf("set stale active_key: %v", err)
	}

	// ① findActiveBindingByValue：按值找活跃行必须找不到它。
	found, err := findActiveBindingByValue(db, entity.BindingKindPhone, staleKey)
	if err != nil {
		t.Fatalf("find active by value: %v", err)
	}
	if found != nil {
		t.Fatalf("作废行不得被当作活跃绑定（按值），got id=%d state=%s", found.ID, found.State)
	}

	// ② CurrentActiveBindingsWithDB：按用户取「当前生效」列表时也必须看不到它。
	rows, err := CurrentActiveBindingsWithDB(db, 1)
	if err != nil {
		t.Fatalf("find active by user: %v", err)
	}
	for _, row := range rows {
		if row.ID == stale.ID {
			t.Fatalf("作废行不得出现在用户的活跃绑定列表里，got id=%d state=%s", row.ID, row.State)
		}
	}

	// ③ 反向对照：插一条真正 active 的行，两个入口都必须能看到它。
	live := entity.UserBindingRecord{
		UserID: 1, UsernameSnapshot: "live-owner", Kind: entity.BindingKindPhone,
		Value: "13700000010", ValueHash: BindingValueHash("13700000010"),
		State: entity.BindingStateActive, ActiveKey: strPtr(BindingValueHash("13700000010")),
		Source: entity.BindingSourceUserBind, BoundAt: timePtr(time.Now()),
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatalf("create live active binding: %v", err)
	}
	liveFound, err := findActiveBindingByValue(db, entity.BindingKindPhone, BindingValueHash("13700000010"))
	if err != nil {
		t.Fatalf("find live active by value: %v", err)
	}
	if liveFound == nil || liveFound.ID != live.ID {
		t.Fatalf("真正的活跃行必须能查到，got %+v", liveFound)
	}
	liveRows, err := CurrentActiveBindingsWithDB(db, 1)
	if err != nil {
		t.Fatalf("find live active by user: %v", err)
	}
	seenLive := false
	for _, row := range liveRows {
		if row.ID == live.ID {
			seenLive = true
		}
	}
	if !seenLive {
		t.Fatal("真正的活跃行必须出现在用户的活跃绑定列表里")
	}
}

// TestVoidedBindingKeepsActiveKeyNil 是红线 1 的行为级钉子。
//
// 只改 state 不置 active_key = NULL 的后果：该行继续占 idx_binding_active
// 唯一索引，别人再绑同值时报唯一约束冲突 —— 而报错文案与手机号毫无字面
// 关系，排查成本极高（entity/user_binding_record.go:57-59 的注释）。
//
// 这里断言的是**写入路径真的把 active_key 置了 nil**，不是「代码里写了这行」。
func TestVoidedBindingKeepsActiveKeyNil(t *testing.T) {
	db := newClawbackTestDB(t)

	const value = "13600000001"
	row := entity.UserBindingRecord{
		UserID: 1, UsernameSnapshot: "void-target", Kind: entity.BindingKindPhone,
		Value: value, ValueHash: BindingValueHash(value),
		State: entity.BindingStateReleased, ActiveKey: nil,
		Source: entity.BindingSourceUserBind, BoundAt: timePtr(time.Now()),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create released row: %v", err)
	}
	// 人为把 active_key 塞上（模拟历史脏数据），再走一次真实作废路径，
	// 确认作废后它被清成 NULL 而不是原样留着。
	if err := db.Model(&entity.UserBindingRecord{}).Where("id = ?", row.ID).
		UpdateColumn("active_key", BindingValueHash(value)).Error; err != nil {
		t.Fatalf("seed dirty active_key: %v", err)
	}

	if _, _, err := VoidBindingRecordWithDB(db, row.ID, 9, "测试作废"); err != nil {
		t.Fatalf("void binding: %v", err)
	}

	var reloaded entity.UserBindingRecord
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", row.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload voided row: %v", err)
	}
	if reloaded.State != entity.BindingStateVoided {
		t.Fatalf("state = %q, want %q", reloaded.State, entity.BindingStateVoided)
	}
	if reloaded.ActiveKey != nil {
		t.Fatalf("作废行必须把 active_key 置 NULL，否则继续占唯一索引；got %q", *reloaded.ActiveKey)
	}

	// 直接验后果：同值再插一条 active 行必须**成功**。
	// 若上一行的 active_key 没清，这里会撞 idx_binding_active 唯一索引。
	again := entity.UserBindingRecord{
		UserID: 2, UsernameSnapshot: "rebind-after-void", Kind: entity.BindingKindPhone,
		Value: value, ValueHash: BindingValueHash(value),
		State: entity.BindingStateActive, ActiveKey: strPtr(BindingValueHash(value)),
		Source: entity.BindingSourceWechatRegister, BoundAt: timePtr(time.Now()),
	}
	if err := db.Create(&again).Error; err != nil {
		t.Fatalf("作废后同值必须可以被重新绑定（active_key 未清会撞唯一索引）: %v", err)
	}
}

// timePtr 返回时间指针（本文件的 fixture 需要指针字段）。
func timePtr(value time.Time) *time.Time { return &value }
