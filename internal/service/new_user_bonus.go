package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	RegistrationBonusSourcePublicRegister = "public_register"
	RegistrationBonusSourceAdminCreate    = "admin_create"
)

type NewUserBonusOptions struct {
	Enabled bool
	Amount  float64
	Source  string
}

func CreateUserWithNewUserBonusWithDB(db *gorm.DB, user *entity.User, options NewUserBonusOptions) error {
	return db.Transaction(func(tx *gorm.DB) error {
		return CreateUserWithNewUserBonusInTx(tx, user, options)
	})
}

func CreateUserWithNewUserBonusInTx(tx *gorm.DB, user *entity.User, options NewUserBonusOptions) error {
	if user == nil {
		return fmt.Errorf("用户不能为空")
	}
	if err := util.ValidateRestrictedName(user.Username, util.UsernameMaxLength, "用户名"); err != nil {
		return err
	}
	if user.Phone != nil && *user.Phone != "" {
		phone, err := NormalizeMainlandPhoneInput(*user.Phone)
		if err != nil {
			return err
		}
		owners, err := FindUsersByPhoneAliases(tx, phone)
		if err != nil {
			return err
		}
		if len(owners) > 1 {
			return ErrPhoneBindingConflict
		}
		if len(owners) > 0 {
			return ErrPhoneAlreadyBound
		}
		user.Phone = &phone
	}
	var bonusAmount float64
	if options.Enabled {
		normalized, err := normalizeNewUserBonusAmount(options.Amount)
		if err != nil {
			return err
		}
		bonusAmount = normalized
	}

	if err := tx.Create(user).Error; err != nil {
		return err
	}
	// 绑定记录：注册时带的 phone / wechat 必须留 active 行。
	//
	// # 为什么在这里补（此前是接线缺陷）
	//
	// `updateLoginUserPhone` 那条「改手机号」路径早就会写（UpdateUserPhoneWithDB 内），
	// 但**注册**这条完全没写 —— 于是 `user_binding_records` 只有「改过号的人」才有行，
	// 表现为「表里只有被释放过的记录」。R14 binding_churn / 44 号追缴 /
	// 19 号 CurrentPhone·CurrentWechat 四个开关都依赖这张表，缺了它们全是死的。
	//
	// # 位置与语义
	//
	//   - 必须在 `db.Create(user)` **之后**：此前 user.ID 为 0，
	//     recordActiveBinding 会因 `user.ID <= 0` 直接放过（fail-safe，不报错）。
	//   - 必须放在「未启用赠送就 return nil」那个早退分支**之前** ——
	//     「不赠送」才是默认配置（options.Enabled 默认 false），
	//     放在早退之后等于对绝大多数注册完全没生效。
	//   - 用 `tx` 而不是新句柄：注册事务回滚时绑定记录必须一起回滚，
	//     否则会出现「用户没建成、绑定记录却留下」的脏数据。
	//   - recordActiveBinding 本身是幂等 + fail-safe 的（表不存在/值空/ID<=0 都放过），
	//     且 RecordBindingActiveWithDB 对同值已存在会直接返回既有行、不重复插入。
	if err := recordActiveBindingsForNewUser(tx, user); err != nil {
		return err
	}
	if !options.Enabled || bonusAmount <= 0 {
		return nil
	}

	balanceBefore := util.RoundMoney(user.Balance)
	giftBefore := util.RoundMoney(user.GiftBalance)
	// 注册赠送是「送的钱」，按双桶模型进**赠送余额** gift_balance（可消费、不可退），
	// 不进可退本金 balance —— 否则新号注册就能立刻退款套现。
	// 入账按规则 3：先抵欠款，剩余才进赠金桶（43-*.md）。
	newBalance, giftAfter := CreditGiftBuckets(user.Balance, user.GiftBalance, bonusAmount)
	if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"balance":      newBalance,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return err
	}
	user.Balance = newBalance
	user.GiftBalance = giftAfter

	record := entity.BillingRecord{
		UserID:            user.ID,
		Type:              registrationBonusBillingRecordType,
		Provider:          "system",
		Amount:            bonusAmount,
		BalanceBefore:     balanceBefore,
		BalanceAfter:      newBalance,
		RequestID:         fmt.Sprintf("registration_bonus_%d", user.ID),
		UnitPriceSnapshot: fmt.Sprintf("source=%s giftBalanceBefore=%s giftBalanceAfter=%s", options.Source, moneyTextForReferral(giftBefore), moneyTextForReferral(giftAfter)),
		Remark:            "新用户赠送",
	}
	return tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error
}

// recordActiveBindingsForNewUser 为新建用户补写 **active** 绑定记录（phone / wechat 各一条）。
//
// # 为什么独立成函数
//
// ① 注册（public / wechat）与管理员建号（admin_create）都走 CreateUserWithNewUserBonusInTx，
//
//	三条入口共用同一份逻辑，避免某一条又漏；
//
// ② boundAt 的取值有讲究（见下），集中一处才能保证三条入口一致。
//
// # boundAt 为什么优先取 user.PhoneBoundAt / WechatBoundAt
//
// 调用方在构造 user 时**已经**写好了 `PhoneBoundAt = &now`（public 注册 user.go:541、
// admin 建号 user.go:1304-1305）。若这里另取 `time.Now()`，绑定记录的 bound_at
// 会比 users.phone_bound_at 晚几毫秒 —— 而 R14「24h 换绑」、19 号「history 号不赠送」
// 都按 bound_at 排时序，两列不一致会让「绑定时刻」出现两个版本。
// 只有在调用方没设时才回落到 time.Now()（覆盖「先建 user 再补 phone」这类未来路径）。
func recordActiveBindingsForNewUser(tx *gorm.DB, user *entity.User) error {
	if tx == nil || user == nil || user.ID <= 0 {
		return nil
	}
	if user.Phone != nil && strings.TrimSpace(*user.Phone) != "" {
		boundAt := riskBindingTimeOrNow(user.PhoneBoundAt)
		if err := recordActivePhoneBinding(tx, *user, *user.Phone, boundAt); err != nil {
			return err
		}
	}
	if user.WechatID != nil && strings.TrimSpace(*user.WechatID) != "" {
		wechatID := strings.TrimSpace(*user.WechatID)
		boundAt := riskBindingTimeOrNow(user.WechatBoundAt)
		if err := recordActiveWechatBinding(tx, *user, wechatID, boundAt); err != nil {
			return err
		}
	}
	return nil
}

// riskBindingTimeOrNow 取调用方给的绑定时刻，**为零值时兜底为 time.Now()**。
//
// ⚠️ 必须同时判 nil 与零值 —— 只判 nil 会漏掉「指针非 nil 但指向零值 time.Time」：
// 实测（探针 TestProbeZeroValueBoundAt）：
//
//	PhoneBoundAt = nil            → bound_at = 2026-11-14 ...（兜底 time.Now()，正确）
//	PhoneBoundAt = &time.Time{}   → bound_at = **0001-01-01 00:00:00**（写进库了）
//
// 后果很隐蔽：`recordActiveBinding` 是 `when := boundAt` 直接赋值、**不做任何兜底**
// （user_binding_record.go），所以零值会原样落库且**不报错**；
// 而 R14「24h 换绑」与 19 号「history 号不赠送」都按 bound_at 排时序，
// `ORDER BY bound_at` 还会把这些行排到最后 —— 属于「测试全绿、线上排序全乱」那一类。
//
// 当前三条入口（public 注册 / wechat 注册 / admin 建号）都显式写了 `PhoneBoundAt = &now`，
// 所以零值不会发生；但这条兜底是为「将来新增入口忘了设」准备的，
// 且**只有同时判 nil 与零值才真的防住**。
func riskBindingTimeOrNow(boundAt *time.Time) time.Time {
	if boundAt == nil || boundAt.IsZero() {
		return time.Now()
	}
	return *boundAt
}
