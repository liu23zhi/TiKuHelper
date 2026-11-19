package service

import (
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// 退款收款码承诺书（2026-11-19 用户要求）。
//
// 背景：人工退款要把钱打进用户提供的收款码对应的账户。平台无法在打款前
// 逐一核实该账户是否属于用户本人，所以用一份**本人承诺**把这件事固定下来：
// 一旦打错账户，责任归属有据可依。
//
// 文本存在 system_settings 里而不是写死在代码中：条款措辞会随合规要求变化，
// 每次都改代码发版不合理。管理员可在系统管理界面直接编辑。

// RefundAgreementSettingKey 是协议文本在 system_settings 里的键。
const RefundAgreementSettingKey = "refund_agreement_text"

// RefundAgreementMaxRunes 是协议文本长度上限。
//
// 20000 字足够容纳任何正常条款，同时挡住「把整个文件塞进来」这类误操作 ——
// 协议要在弹窗里**滚动到底**才算阅读完成，过长会让用户根本读不完。
const RefundAgreementMaxRunes = 20000

// ErrRefundAgreementInvalid 协议文本不合法（空或超长）。
var ErrRefundAgreementInvalid = errors.New("退款协议文本无效")

// DefaultRefundAgreementText 是内置的默认承诺书全文。
//
// 管理员把它改成任何内容都不影响本函数 —— 它只在设置行缺失时作为兜底，
// 保证「设置没初始化」不会让退款功能直接不可用。
const DefaultRefundAgreementText = `退款收款码使用承诺书

为保障退款资金安全，本人（以下称「申请人」）就本次退款申请中提交的收款码，向平台作出如下承诺：

一、账户归属
1. 本人承诺，本次提交的收款码所属支付账户（含支付宝、微信等）为本人合法持有并已完成实名认证的账户，本人对该账户拥有完整、排他的控制权与支配权。
2. 本人承诺不提交他人账户、亲友账户，或通过买卖、租借、代办等方式取得的账户作为收款账户。

二、账户与资金状态
3. 本人承诺该账户不存在被冻结、被限制收款，或涉嫌洗钱、赌博、诈骗等异常情形。
4. 本人承诺本次退款所涉资金为本人合法支付的充值款项，不存在代他人充值、代他人申请退款等情形。

三、信息真实性
5. 本人保证所提交的订单信息、退款金额、退款理由及收款码均真实、准确、完整，不存在伪造、变造或隐瞒重要事实的情形。

四、责任承担
6. 如因本人违反上述任一承诺，导致退款资金被打入非本人账户、被第三方主张权利，或引发争议、投诉、行政或司法调查的，由本人自行承担全部责任，并赔偿平台因此遭受的全部损失（包括但不限于退款本金、罚金、诉讼费、律师费及其他合理支出）。
7. 平台发现上述任一承诺不实后，有权暂停或终止本次退款，并保留追究本人责任的权利。

五、其他
8. 本承诺书自本人签署之日起生效。签署记录与签名图像由平台留存，作为本次退款申请的必要凭证；每次申请退款均需重新签署，不沿用上次记录。
9. 本人已完整阅读并理解上述全部条款，对条款含义无异议。

申请人（以电子签名方式确认）：见签名图像
签署日期：提交本次退款申请之日`

// GetRefundAgreementTextWithDB 读取当前的承诺书全文。
//
// 设置行缺失或为空时返回内置默认文本，而不是报错：协议读不到不该让
// 整个退款入口不可用 —— 那会把「配置没初始化」升级成「用户退不了款」。
func GetRefundAgreementTextWithDB(db *gorm.DB) string {
	if db == nil {
		return DefaultRefundAgreementText
	}
	text := strings.TrimSpace(getSystemSettingStringWithDefault(db, RefundAgreementSettingKey, ""))
	if text == "" {
		return DefaultRefundAgreementText
	}
	return text
}

// SetRefundAgreementTextWithDB 更新承诺书全文（管理员）。
func SetRefundAgreementTextWithDB(db *gorm.DB, requested string) error {
	if db == nil {
		return ErrRefundAgreementInvalid
	}
	// 统一换行，避免管理员从不同系统粘贴进来的 \r\n 与 \n 混用，
	// 那会让「滚到底」的判据在两端表现不一致。
	text := strings.ReplaceAll(strings.TrimSpace(requested), "\r\n", "\n")
	if text == "" {
		return ErrRefundAgreementInvalid
	}
	if utf8.RuneCountInString(text) > RefundAgreementMaxRunes {
		return ErrRefundAgreementInvalid
	}
	return saveSystemSettingValue(db, RefundAgreementSettingKey, text, "退款收款码使用承诺书全文")
}

// IsDefaultRefundAgreement 判断当前文本是否仍是内置默认值。
//
// 系统管理界面用它显示「尚未自定义」，避免管理员以为已经改过了。
func IsDefaultRefundAgreement(db *gorm.DB) bool {
	return GetRefundAgreementTextWithDB(db) == DefaultRefundAgreementText
}
