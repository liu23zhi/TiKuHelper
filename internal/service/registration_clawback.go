package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件实现 `44-二次注册四情形与追缴定案.md` §3 / §5 的**追缴（Clawback）**机制。
//
// # 它解决什么问题
//
// 注册时的四个 no_bonus 开关只能拦「注册那一刻就带着旧值」的人。若用户先以全新值
// （BBBB/CCCC）注册拿到赠送，**事后再换绑**到别人换绑/注销时释放过的旧值（BB/CC 或
// BBB/CCC），注册判定早已过去 —— 必须有一个**换绑时**的机制把钱追回来。
//
// # 三条不可协商的口径
//
//  1. **扣减顺序复用 43 号规则 2**：先扣 gift_balance（扣到 0 为止，下限定 0），
//     不足部分扣 balance（**允许变负**，本金是欠款载体）。
//  2. **追缴总能全额执行**（44 §5.3）：不存在「扣不到」，最差结果是
//     `gift_balance=0 && balance<0`。**不提供「放弃绑定」选项**。
//  3. **追缴是一个独立 bool 开关，默认关闭**（44 §5.4）。关闭时零副作用、零记账、
//     零额外查询 —— 连 binding_records 都不查。
//
// # 与返佣冲回的区别（19 号 §3.6 第 6 点）
//
// 返佣冲回（debitReferralRebateWalletInTx）是「只扣赠金、clamp 0、本金不动」，
// 因为它冲的是一笔**从未进过本金桶**的赠金。追缴不同：赠金可能早已被消费掉，
// 钱必须真的回来，所以赠金扣光后继续扣本金。

// RegistrationBonusClawbackEnabledSettingKey 是追缴总开关，**默认关闭**（44-*.md §5.4）。
//
// 关闭时：换绑到任何历史值都不追缴、不写 `registration_bonus_clawback` 流水。
// 缺表 / 缺记录 / 解析失败一律按 false（fail-open，与项目既有 `*_enabled` 约定一致）：
// 读不到配置绝不能变成「悄悄扣用户的钱」。
const RegistrationBonusClawbackEnabledSettingKey = "registration_bonus_clawback_enabled"

// RegistrationBonusClawbackBillingRecordType 是追缴流水的账单类型。
//
// 与 `registration_bonus_revoked`（42-*.md：注册时的**即时**扣减）**不是一回事**：
// 那个发生在注册事务里、拦的是「注册就带旧值」；这个发生在**换绑**路径、追的是
// 「注册时合法拿到、事后换绑到旧值」。两者语义与触发点都不同，不能复用同一个 type，
// 否则账单上一行「退回赠送」看不出到底是哪种情形，追缴审计会失效。
const RegistrationBonusClawbackBillingRecordType = "registration_bonus_clawback"

// registrationBonusRevokedBillingRecordType 是 42-*.md 的注册时即时扣减类型。
//
// ⚠️ 该机制（19 号四开关 + 42 号先发再扣）**本分支尚未实现**，这里只声明类型常量
// 用于「计算实际到手赠送额」时把将来可能存在的扣减行减掉。**这是推断**：
// 44 §3.3 只说「按 type='registration_bonus' 取」，没有提 registration_bonus_revoked；
// 但若不同时减去它，「注册被判不赠送、随后换绑」的用户会被追缴一笔他从未真正拿到的钱。
// 依据：42-*.md §5「净效果 gift_balance 不变」——净额才是「实际获得」。
const registrationBonusRevokedBillingRecordType = "registration_bonus_revoked"

// ErrRegistrationBonusClawbackPending 表示同一用户的追缴正在另一事务中执行。
var ErrRegistrationBonusClawbackPending = errors.New("追缴正在处理中，请勿重复提交")

// GetRegistrationBonusClawbackEnabledWithDB 返回追缴开关。默认关闭。
//
// 缺表、缺记录、值非法都按关闭处理 —— 与 GetReferralRebateEnabledWithDB 同构。
func GetRegistrationBonusClawbackEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, RegistrationBonusClawbackEnabledSettingKey)
}

// SetRegistrationBonusClawbackEnabledWithDB 写入追缴开关（管理员系统设置用）。
func SetRegistrationBonusClawbackEnabledWithDB(db *gorm.DB, enabled bool) error {
	return saveSystemSettingValue(db, RegistrationBonusClawbackEnabledSettingKey,
		strconv.FormatBool(enabled), "用户换绑到曾获赠过的手机号/微信时追回新用户赠送")
}

// RegistrationBonusClawbackRequestID 是追缴流水的 request_id，同时充当**持久幂等锚点**。
//
// 幂等键只含 userID，**不含 bindingRecordID**（44-*.md §5.6 的修正）：
// 若键里带 bindingRecordID，用户「先换绑到 BB、再换绑到 CC」会产生两个新键 →
// **重复追缴两次**。一个用户只应被追缴一次（他当初只拿过一次新用户赠送）。
//
// ⚠️ 与任务书的差异：任务书写的是 `registration_bonus_clawback_<userID>_<bindingRecordID>`，
// 但 44 §5.6 明确判定那个形态是**设计错误**并给出修正（本节即修正后的形态）。
// 冲突时按 44 号文档执行；触发它的 bindingRecordID 仍然写进 unit_price_snapshot
// 与 remark，审计上不缺信息。
func RegistrationBonusClawbackRequestID(userID int32) string {
	return fmt.Sprintf("%s_%d", RegistrationBonusClawbackBillingRecordType, userID)
}

// RegistrationBonusClawbackResult 是一次追缴的实际结果。
//
// Executed=false 表示「没有扣款」—— 原因可能是开关关闭、没有可追的赠送、
// 或该用户已被追缴过。调用方**不应该**把 false 当成错误。
type RegistrationBonusClawbackResult struct {
	Executed bool
	// RequestID 是幂等键（未执行时为空）。
	RequestID string
	// Amount 是应追缴额（= 该账号实际到手的新用户赠送净额）。
	Amount float64
	// FromGift / FromBalance 是实际从两个桶各扣了多少；执行时 FromGift+FromBalance == Amount。
	FromGift    float64
	FromBalance float64
	// BalanceAfter / GiftAfter 是扣减后的两桶值（未执行时为零值，不可当真实余额）。
	BalanceAfter float64
	GiftAfter    float64
	// AlreadyClawedBack 表示检测到该用户已有追缴流水，本次是**幂等命中**。
	AlreadyClawedBack bool
}

// RegistrationBonusReceivedWithDB 返回某账号**实际到手**的新用户赠送净额（44-*.md §3.3）。
//
// 口径：sum(registration_bonus) − sum(registration_bonus_revoked)。
// 第二条是 42-*.md 的「注册时立即扣减」，本分支尚未实现（表里不会有这类行，
// 但算式先留着，实现落地后无需再改这里 —— 见 registrationBonusRevokedBillingRecordType 的注释）。
//
// 只读一次聚合：金额量级与账单行数无关，不需要分批。
func RegistrationBonusReceivedWithDB(db *gorm.DB, userID int32) (float64, error) {
	if db == nil || userID <= 0 {
		return 0, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return 0, nil
	}
	var row struct {
		Granted float64 `gorm:"column:granted"`
		Revoked float64 `gorm:"column:revoked"`
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Where("user_id = ? AND type IN ?", userID, []string{
			registrationBonusBillingRecordType, registrationBonusRevokedBillingRecordType,
		}).
		Select("COALESCE(SUM(CASE WHEN type = ? THEN amount ELSE 0 END), 0) AS granted, "+
			"COALESCE(SUM(CASE WHEN type = ? THEN amount ELSE 0 END), 0) AS revoked",
			registrationBonusBillingRecordType, registrationBonusRevokedBillingRecordType).
		Scan(&row).Error; err != nil {
		return 0, err
	}
	net := util.RoundMoney(row.Granted - row.Revoked)
	if net < 0 {
		// 脏数据：已扣的比发的多。追缴额按 0 处理，绝不反过来「退」给用户。
		return 0, nil
	}
	return net, nil
}

// registrationBonusClawbackTrigger 描述「哪条 released 绑定记录触发了本次追缴」。
// 只用于账单留痕与幂等 request 内容，不参与扣款金额计算。
type registrationBonusClawbackTrigger struct {
	BindingRecordID int64
	Kind            string
	// MaskedValue 是脱敏后的触发值（账单 remark 用，用户可见）。
	MaskedValue string
	// ReleaseReason 是触发的二分类（rebind / closure / admin，44 §5.2 覆盖全部三类）。
	ReleaseReason string
}

// ClawbackRegistrationBonusOnRebindWithDB 在**换绑路径**上按需追缴该用户的新用户赠送。
//
// 参数 binding 是「换绑后新值」对应的 released 绑定记录（由调用方查好传入）；
// binding 为 nil 表示新值在 binding_records 里没有 released 记录 → 不追缴（情形 1/3 的常态）。
//
// 返回值语义见 RegistrationBonusClawbackResult。**任何情况下都不返回「扣不到」**：
// 本金可为负，追缴总能全额执行（44 §5.3）。
//
// 换绑链路内部请用 ClawbackRegistrationBonusOnRebindInTx，避免在已有事务里再开一层。
func ClawbackRegistrationBonusOnRebindWithDB(db *gorm.DB, userID int32, binding *entity.UserBindingRecord) (RegistrationBonusClawbackResult, error) {
	var result RegistrationBonusClawbackResult
	if db == nil || userID <= 0 || binding == nil {
		return result, nil
	}
	// 开关最先判：关闭时零查询、零副作用（44 §5.4）。
	if !GetRegistrationBonusClawbackEnabledWithDB(db) {
		return result, nil
	}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		applied, err := clawbackRegistrationBonusInTx(tx, userID, buildClawbackTrigger(*binding), &result)
		result.Executed = applied
		return err
	})
	return result, err
}

// ClawbackRegistrationBonusOnRebindInTx 是换绑事务内的版本：**不新开事务**。
//
// 为什么必须共用调用方的 tx：换绑 + 释放旧绑定 + 写新绑定 + 追缴是**同一笔业务动作**。
// 若追缴另开事务，换绑成功而追缴失败时会留下「已换到旧值但没追钱」的窗口，
// 而幂等键又让下次换绑不再触发 —— 这笔钱就永久追不回来了。
func ClawbackRegistrationBonusOnRebindInTx(tx *gorm.DB, userID int32, binding *entity.UserBindingRecord) (RegistrationBonusClawbackResult, error) {
	var result RegistrationBonusClawbackResult
	if tx == nil || userID <= 0 || binding == nil {
		return result, nil
	}
	applied, err := clawbackRegistrationBonusInTx(tx, userID, buildClawbackTrigger(*binding), &result)
	result.Executed = applied
	return result, err
}

// buildClawbackTrigger 把绑定记录折成账单留痕需要的触发信息。
func buildClawbackTrigger(binding entity.UserBindingRecord) registrationBonusClawbackTrigger {
	return registrationBonusClawbackTrigger{
		BindingRecordID: binding.ID,
		Kind:            binding.Kind,
		MaskedValue:     MaskBindingValue(binding.Kind, binding.Value),
		ReleaseReason:   binding.ReleaseReason,
	}
}

// clawbackAfterRebindInTx 是换绑路径的**唯一接点**：查触发记录 → 按需追缴。
//
// 三条纪律：
//   - 开关关闭时**一次查询都不发**（GetRegistrationBonusClawbackEnabledWithDB 早退）；
//   - 新值没有 released 记录 → 直接放过（情形 1/3 的常态，不是错误）；
//   - 追缴失败必须让换绑一起回滚 —— 见 ClawbackRegistrationBonusOnRebindInTx 的注释。
func clawbackAfterRebindInTx(tx *gorm.DB, userID int32, kind, value string) (RegistrationBonusClawbackResult, error) {
	var result RegistrationBonusClawbackResult
	if tx == nil || userID <= 0 || strings.TrimSpace(value) == "" {
		return result, nil
	}
	if !GetRegistrationBonusClawbackEnabledWithDB(tx) {
		return result, nil
	}
	if !tx.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return result, nil
	}
	binding, err := FindReleasedBindingByValueWithDB(tx, kind, value)
	if err != nil {
		return result, err
	}
	if binding == nil {
		return result, nil
	}
	// 【推断】**排除自己释放掉的记录**（44 号文档没有覆盖这个边界，按其意图推导）。
	//
	// 反例：D 用全新值 BBBB 注册获赠 → 换绑到 CCCC（BBBB 转 released/rebind）→
	// 又换回 BBBB。此时 BBBB 的 released 记录**属于 D 自己**，D 当年正是靠 BBBB 合法拿到
	// 赠送的，再追缴等于把自己本来应得的钱收回去。
	//
	// 追缴的目的是「D 用了别人（A）曾获赠过的值」，所以要求绑定的原持有人 ≠ 本次追缴对象。
	// 依据：44 §3.1「用户 D 已因注册时无匹配获得赠送，之后主动换绑到某个 history_*/current_*
	// 命中的值」—— 那两个开关的语义（19 §3.3 / 44 §1.1）都是「**A** 释放的值」，
	// 从来没有「自己释放给自己」这一情形。
	if binding.UserID == userID {
		return result, nil
	}
	return ClawbackRegistrationBonusOnRebindInTx(tx, userID, binding)
}

// clawbackRegistrationBonusInTx 是追缴的唯一实现（事务内）。
//
// 幂等是**两层**的，缺一不可：
//
//  1. **持久层**：`billing_records.request_id = registration_bonus_clawback_<userID>`
//     已存在 → 直接返回 AlreadyClawedBack。`billing_records.request_id` 只是普通索引
//     （entity/billing_record.go:41），所以它只负责「跨时间」的幂等；
//  2. **并发层**：`idempotency_records` 的 `(scope_hash, operation, key_hash)` **唯一索引**
//     负责「同一瞬间两个事务」的幂等。
//
// ⚠️ 为什么不能只用 idempotency_records：它有 24h 保留期（idempotencyRetention），
// 过期记录会被清理，**过期后同样的请求会被当成新请求再次执行** —— 用户一周后
// 再换绑一次就会被追缴第二次。所以第 1 层（账单行）才是真正的持久锚点。
func clawbackRegistrationBonusInTx(tx *gorm.DB, userID int32, trigger registrationBonusClawbackTrigger, result *RegistrationBonusClawbackResult) (bool, error) {
	requestID := RegistrationBonusClawbackRequestID(userID)

	// ① 持久幂等：已有追缴流水 → 不重复扣。
	exists, err := registrationClawbackRecordExists(tx, requestID)
	if err != nil {
		return false, err
	}
	if exists {
		result.AlreadyClawedBack = true
		result.RequestID = requestID
		return false, nil
	}

	// ② 金额：该账号实际到手的新用户赠送净额。没有赠送 → 无从追缴。
	amount, err := RegistrationBonusReceivedWithDB(tx, userID)
	if err != nil {
		return false, err
	}
	if amount <= 0 {
		return false, nil
	}

	// ③ 并发幂等：唯一索引兜底。request 内容**只含 userID**（不含触发记录），
	// 这样「同一用户被不同旧值触发」会命中同一条记录而不是冲突。
	request, err := json.Marshal(map[string]any{"userId": userID})
	if err != nil {
		return false, err
	}
	record, fresh, err := ClaimIdempotencyWithDB(tx,
		fmt.Sprintf("user:%d", userID), "registration-bonus-clawback",
		strconv.FormatInt(int64(userID), 10), request)
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			// 同一用户已在执行（或已完成）另一笔追缴 → 幂等命中，不重复扣。
			result.AlreadyClawedBack = true
			result.RequestID = requestID
			return false, nil
		}
		return false, err
	}
	if !fresh {
		result.AlreadyClawedBack = true
		result.RequestID = requestID
		if record.Status != IdempotencyStatusSucceeded {
			// 前一笔还没结束：明确告知调用方「待定」，不要当成已完成。
			return false, ErrRegistrationBonusClawbackPending
		}
		return false, nil
	}

	// ④ 锁钱包行并计算两桶扣减（43 号规则 2）。
	wallet, err := lockClawbackWalletInTx(tx, userID)
	if err != nil {
		return false, err
	}
	balanceBefore := util.RoundMoney(wallet.Balance)
	giftBefore := util.RoundMoney(wallet.GiftBalance)
	balanceAfter, giftAfter := ConsumeWalletBuckets(balanceBefore, giftBefore, amount)
	fromGift := util.RoundMoney(giftBefore - giftAfter)
	fromBalance := util.RoundMoney(balanceBefore - balanceAfter)

	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("id = ?", wallet.ID).Updates(map[string]any{
		"balance":      balanceAfter,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return false, err
	}

	// ⑤ 写一条独立可读的追缴流水（44 §3.4 / 42 号「账务流即留痕」）。
	record2 := entity.BillingRecord{
		UserID:        wallet.ID,
		Type:          RegistrationBonusClawbackBillingRecordType,
		Provider:      "system",
		Amount:        amount,
		BalanceBefore: balanceBefore,
		BalanceAfter:  balanceAfter,
		// 本金桶的前后值进 balance_before/after；赠金桶前后值沿用 key=value 快照约定
		// （billing_records 没有 gift 列，加列要走迁移，见 42 号 §3.3 / referral 同款做法）。
		UnitPriceSnapshot: fmt.Sprintf(
			"walletBucket=gift+before_gift=%s giftBalanceBefore=%s giftBalanceAfter=%s principalCharged=%s giftCharged=%s bindingRecordId=%d bindingKind=%s releaseReason=%s",
			balanceMoneyText(fromGift), balanceMoneyText(giftBefore), balanceMoneyText(giftAfter),
			balanceMoneyText(fromBalance), balanceMoneyText(fromGift),
			trigger.BindingRecordID, trigger.Kind, trigger.ReleaseReason),
		RequestID: requestID,
		Remark:    buildRegistrationClawbackRemark(trigger, amount, fromGift, fromBalance),
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record2).Error; err != nil {
		return false, err
	}

	response, err := json.Marshal(map[string]any{
		"userId": wallet.ID, "amount": amount, "giftCharged": fromGift, "principalCharged": fromBalance,
		"balanceAfter": balanceAfter, "giftBalanceAfter": giftAfter, "billingRecordId": record2.ID,
	})
	if err != nil {
		return false, err
	}
	if err := CompleteIdempotencyWithDB(tx, record.ID, 200, response); err != nil {
		return false, err
	}

	result.RequestID = requestID
	result.Amount = amount
	result.FromGift = fromGift
	result.FromBalance = fromBalance
	result.BalanceAfter = balanceAfter
	result.GiftAfter = giftAfter
	return true, nil
}

// buildRegistrationClawbackRemark 拼出 44 §3.4 要求的备注文案。
//
// 必含三件事：① 换绑到哪个（脱敏）值；② 追回多少钱；③ 其中赠金/本金各扣多少。
// 用户在账单里要能自己看懂「为什么这笔钱被扣了」。
func buildRegistrationClawbackRemark(trigger registrationBonusClawbackTrigger, amount, fromGift, fromBalance float64) string {
	kindLabel := "手机号"
	if strings.EqualFold(trigger.Kind, entity.BindingKindWechat) {
		kindLabel = "微信"
	}
	reasonLabel := "曾获赠过"
	switch trigger.ReleaseReason {
	case entity.BindingReleaseReasonClosure:
		reasonLabel = "原账号注销时释放、曾获赠过"
	case entity.BindingReleaseReasonAdmin:
		reasonLabel = "管理员释放、曾获赠过"
	}
	parts := []string{
		fmt.Sprintf("用户换绑到%s（%s，%s），追回新用户赠送 ¥%s",
			kindLabel, reasonLabel, trigger.MaskedValue, moneyTextForReferral(amount)),
		fmt.Sprintf("其中赠金扣 ¥%s、本金扣 ¥%s", moneyTextForReferral(fromGift), moneyTextForReferral(fromBalance)),
	}
	if util.RoundMoney(fromBalance) > 0 && fromBalance > 0 {
		parts = append(parts, "赠金不足部分已扣本金（本金可为负，欠款留存）")
	}
	return strings.Join(parts, "；")
}

// registrationClawbackRecordExists 判断某用户的追缴流水是否已落账（持久幂等层）。
func registrationClawbackRecordExists(tx *gorm.DB, requestID string) (bool, error) {
	if strings.TrimSpace(requestID) == "" {
		return false, nil
	}
	var count int64
	if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Where("request_id = ?", requestID).Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// lockClawbackWalletInTx 取钱包行并加行锁（MySQL 下 FOR UPDATE；SQLite 下事务已是
// immediate 写事务，clause 会被忽略但语义不变）。
type clawbackWallet struct {
	ID          int32   `gorm:"column:id"`
	Balance     float64 `gorm:"column:balance"`
	GiftBalance float64 `gorm:"column:gift_balance"`
}

func lockClawbackWalletInTx(tx *gorm.DB, userID int32) (clawbackWallet, error) {
	var wallet clawbackWallet
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Select("id, balance, gift_balance").
		Where("id = ?", userID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&wallet).Error
	return wallet, err
}

// ClawbackTriggerBindingForRebindWithDB 在换绑写入**之后**取出「新值」的 released 记录。
//
// 只在追缴开关打开时才查（关闭时零查询）。取不到 → nil → 不追缴。
// 注意：这里必须用**新值**去查，而不是用户原来的旧值 —— 追的是「换绑到哪个值」。
func ClawbackTriggerBindingForRebindWithDB(db *gorm.DB, kind, value string) (*entity.UserBindingRecord, error) {
	if db == nil || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if !GetRegistrationBonusClawbackEnabledWithDB(db) {
		return nil, nil
	}
	trigger, err := FindReleasedBindingByValueWithDB(db, kind, value)
	if err != nil || trigger == nil {
		return nil, err
	}
	// 显式再挡一次作废行（需求 7 / task_doc/70 §3.7.1）：
	// 追缴的依据必须是「真实发生过的释放」。虽然 FindReleasedBindingByValueWithDB
	// 已带 `state != 'voided'`，但这里是**钱**的入口，多一道判断的成本是一次字符串比较 ——
	// 一旦上面的查询将来被人改成「不限 state」，这条守卫仍然成立。
	if trigger.State == entity.BindingStateVoided {
		return nil, nil
	}
	return trigger, nil
}

// BindingRecordTimestamp 是「绑定写入时间」的统一取法：优先用 users 上的 bound_at，
// 缺失时回落当前时间。抽出来是为了让注册/绑定/抢号三条路径写同一列时不会各写一套。
func BindingRecordTimestamp(boundAt *time.Time) time.Time {
	if boundAt != nil {
		return *boundAt
	}
	return time.Now()
}
