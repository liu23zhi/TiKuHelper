package service

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const RegisterEnabledSettingKey = "register_enabled"
const NewUserBonusAmountSettingKey = "new_user_bonus_amount"
const SiteNameSettingKey = "site_name"
const PaymentProviderSettingKey = "payment_provider"
const PaymentMethodSettingKey = "payment_method"
const EPayVersionSettingKey = "epay_version"
const EPayGatewayURLSettingKey = "epay_gateway_url"
const EPayPartnerIDSettingKey = "epay_partner_id"
const EPayPlatformPublicKeySettingKey = "epay_platform_public_key"
const EPayMerchantPrivateKeySettingKey = "epay_merchant_private_key"
const EPayNotifyURLSettingKey = "epay_notify_url"
const EPayReturnURLSettingKey = "epay_return_url"
const SMSProviderSettingKey = "sms_provider"
const SMSBaoUsernameSettingKey = "smsbao_username"
const SMSBaoSecretSettingKey = "smsbao_secret"
const SMSCodeSignNameSettingKey = "sms_code_sign_name"
const SMSCodeTemplateSettingKey = "sms_code_template"
const SMSLoginEnabledSettingKey = "sms_login_enabled"
const WechatLoginEnabledSettingKey = "wechat_login_enabled"
const WechatScanLoginEnabledSettingKey = "wechat_scan_login_enabled"
const WechatCodeLoginEnabledSettingKey = "wechat_code_login_enabled"
const WechatDefaultLoginModeSettingKey = "wechat_default_login_mode"
const WechatCodeStaticQRCodeURLSettingKey = "wechat_code_static_qrcode_url"
const WechatAPIBaseURLSettingKey = "wechat_api_base_url"
const WechatAPIBearerTokenSettingKey = "wechat_api_bearer_token"
const CaptchaProviderSettingKey = "captcha_provider"
const CaptchaAPIBaseURLSettingKey = "captcha_api_base_url"
const CaptchaAPIKeySettingKey = "captcha_api_key"
const CaptchaTypeSettingKey = "captcha_type"
const SMSBaoCredentialTypeSettingKey = "smsbao_credential_type"
const SMSBaoRegionModeSettingKey = "smsbao_region_mode"
const MaxLogSizeBytesSettingKey = "max_log_size_bytes"
const MaxAISearchModelCallsSettingKey = "max_ai_search_model_calls"
const MaxAISearchesSettingKey = "max_ai_searches"
const MaxAISearchRetriesSettingKey = "max_ai_search_retries"
const AISearchPricePerCallSettingKey = "ai_search_price_per_call"
const MaxAIWebReadsSettingKey = "max_ai_web_reads"
const WebReadPricePerCallSettingKey = "web_read_price_per_call"
const ImageOCRPricePerCallSettingKey = "image_ocr_price_per_call"
const TikuSearchPricePerCallSettingKey = "tiku_search_price_per_call"
const AIStagedTokenBillingEnabledSettingKey = "enable_ai_staged_token_billing"
const SmoothAIStagedTokenMultiplierSettingKey = "smooth_ai_staged_token_multiplier"
const AIStagedTokenMultiplierPointsSettingKey = "ai_staged_token_multiplier_points"
const MinRechargeAmountSettingKey = "min_recharge_amount"

// 商业化功能总开关。两者默认 false：未获批准的方案不得因为「代码已经写了」
// 而自动生效。
//
//   - RechargePromotionEnabledSettingKey 控制充值阶梯赠送（六档 8.8/28/88/288/888/1688）。
//     关闭时充值只按实付入账，不产生赠金，也不写赠送流水。
//   - DeviceLimitEnabledSettingKey 控制账户设备池上限（1/2/3/5/10/20）。
//     关闭时不登记设备、不校验安装，所有有效 Key 都按原逻辑可用。
//
// 缺记录、缺字段、解析失败一律按关闭处理：配置丢失不能等于悄悄开启。
const RechargePromotionEnabledSettingKey = "recharge_promotion_enabled"
const DeviceLimitEnabledSettingKey = "device_limit_enabled"

// BlacklistEnabledSettingKey / DefaultBlacklistEnabled / GetBlacklistEnabledWithDB
// 定义在 blacklist.go（黑名单规格的唯一落点），这里只把它接进系统设置读写。
//
// 默认 false：接口上线后未显式打开时，中间件零查询、零写日志，全站行为不变。

// ReferralRebateEnabledSettingKey 是邀请码与充值返佣的总开关。
//
// 默认 false（与其它商业化开关一致）：关闭时注册不生成邀请码、支付回调不产生返佣。
// 关闭只影响「是否继续产生新数据」，已存在的邀请码与返佣账本行保持原样可查，
// 不做删除也不改写状态 —— 账本一旦产生就只追加。
const ReferralRebateEnabledSettingKey = "referral_rebate_enabled"

// ReferralRebateRateSettingKey 是返佣比例，取值 [0, 1)，即 0%–100%。
//
// 返佣基数是被邀请账户的**已核验净实付本金，不含赠金**（1688 充值赠 3376 时基数为 1688）。
// 比例在产生返佣时快照进 referral_rebates.rate，改配置不回溯已产生的记录。
const ReferralRebateRateSettingKey = "referral_rebate_rate"

// 评价返现的四项配置。全部默认关闭/保守值：缺表、缺记录、值非法一律回落默认，
// 其中 enabled 必须回落 false —— 配置读不出来绝不能变成「悄悄开活动」。
//
// 规则表本身**不塞进 system_settings**（条数不定），走 review_rebate_rules 表 + 管理页。
const (
	// ReviewRebateEnabledSettingKey 是评价返现总开关，默认 false。
	//
	// 关闭时：提交接口拒绝、Agent 打分拒绝、管理端决策接口拒绝。
	// 关闭只影响「是否继续产生新数据」，已存在的 claim/award 行保持原样可查。
	ReviewRebateEnabledSettingKey = "review_rebate_enabled"
	// ReviewRebateMaxClaimsPerUserSettingKey 是每账号累计提交次数上限，默认 3。
	ReviewRebateMaxClaimsPerUserSettingKey = "review_rebate_max_claims_per_user"
	// ReviewRebateMaxAmountPerUserSettingKey 是每账号累计发放金额上限，默认 5.04。
	ReviewRebateMaxAmountPerUserSettingKey = "review_rebate_max_amount_per_user"
	// ReviewRebateMinAgentConfidenceSettingKey 是 Agent 置信度下限，默认 0.6。
	//
	// 低于此值 → 强制 pending_review，绝不自动发放（16-*.md §1.5）。
	ReviewRebateMinAgentConfidenceSettingKey = "review_rebate_min_agent_confidence"
)

// 评价返现的默认值（16-*.md §1.7）。
const (
	// DefaultReviewRebateEnabled 默认关闭。
	DefaultReviewRebateEnabled = false
	// DefaultReviewRebateMaxClaimsPerUser 默认 3 次。
	DefaultReviewRebateMaxClaimsPerUser = 3
	// DefaultReviewRebateMaxAmountPerUser 默认 5.04 元（= 1.68 × 3，与 04 稿 10.4.2 对齐）。
	DefaultReviewRebateMaxAmountPerUser = float64(5.04)
	// DefaultReviewRebateMinAgentConfidence 默认 0.6。
	DefaultReviewRebateMinAgentConfidence = float64(0.6)
)

// ReviewRebatePolicyVersion 是评价返现当前规则口径版本快照。
//
// 写进 review_rebate_awards.policy_version：基数额度、规则语义变化时必须换值，
// 历史流水保持不变（改版不重算已发放）。
const ReviewRebatePolicyVersion = "v1"

// UAPIAPIKeySettingKey 是 UApiPro（uapis.cn）的 API 密钥。
//
// 用于查询手机号 / IP 归属地（用户管理、日志列表的归属地展示）。
// 与其它密钥一样：只从数据库读取，不硬编码、不拼进 URL、响应里脱敏。
const UAPIAPIKeySettingKey = "uapi_api_key"

const DefaultPaymentProvider = "epay"
const DefaultPaymentMethod = "alipay"
const DefaultEPayVersion = "v2"
const DefaultSiteName = "题库"
const DefaultNewUserBonusAmount = float64(0)
const DefaultSMSProvider = "smsbao"
const DefaultSMSCodeTemplate = "【{sign}】您的验证码是{code}，5分钟内有效。"
const DefaultCaptchaProvider = "remote"
const DefaultCaptchaAPIBaseURL = "https://captcha.api.zelly.cn"
const DefaultCaptchaAPIKey = ""
const DefaultCaptchaType = "RANDOM"
const DefaultSMSBaoCredentialType = "apikey"
const DefaultSMSBaoRegionMode = "auto"
const DefaultWechatLoginMode = "scan"
const DefaultMaxLogSizeBytes int64 = 5 * 1024 * 1024 * 1024
const DefaultMaxAISearchModelCalls = 4
const DefaultMaxAISearches = 3
const DefaultMaxAISearchRetries = 3
const DefaultAISearchPricePerCall = float64(0)
const DefaultMaxAIWebReads = 5
const DefaultWebReadPricePerCall = float64(0)
const DefaultImageOCRPricePerCall = float64(0)
const DefaultTikuSearchPricePerCall = float64(0)
const DefaultMinRechargeAmount = float64(1)

// DefaultReferralRebateEnabled 是邀请返佣总开关的默认值（关闭）。
const DefaultReferralRebateEnabled = false

// DefaultReferralRebateRate 是返佣比例的默认值（0%，即不产生任何金额）。
const DefaultReferralRebateRate = float64(0)

// ReferralRebatePolicyVersion 是当前返佣规则版本快照标识。
//
// 写入 referral_rebates.policy_version，用于区分「按哪一版规则算出的金额」。
// 规则（基数口径、比例来源）发生语义变化时必须换一个值，历史行保持不变。
const ReferralRebatePolicyVersion = "v1"

// JSON numbers must round-trip through the browser without losing precision.
const MaxAISearchCountSafeInteger int64 = 9007199254740991
const MaxAllowedAISearchModelCalls = 20
const MaxAllowedAISearches = 20
const MaxAllowedAISearchRetries = 10

const paymentMethodWXPay = "wxpay"
const wechatLoginModeScan = "scan"
const wechatLoginModeCode = "code"

type SystemSettingDetail struct {
	RegisterEnabled           bool    `json:"registerEnabled"`
	NewUserBonusAmount        float64 `json:"newUserBonusAmount"`
	SiteName                  string  `json:"siteName"`
	AlternativeLoginEnabled   bool    `json:"alternativeLoginEnabled"`
	EPayVersion               string  `json:"epayVersion"`
	EPayGatewayURL            string  `json:"epayGatewayUrl"`
	EPayPartnerID             string  `json:"epayPartnerId"`
	EPayPlatformPublicKey     string  `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey    string  `json:"epayMerchantPrivateKey"`
	EPayNotifyURL             string  `json:"epayNotifyUrl"`
	EPayReturnURL             string  `json:"epayReturnUrl"`
	SMSProvider               string  `json:"smsProvider"`
	SMSBaoUsername            string  `json:"smsbaoUsername"`
	SMSBaoSecret              string  `json:"smsbaoSecret"`
	SMSCodeSignName           string  `json:"smsCodeSignName"`
	SMSCodeTemplate           string  `json:"smsCodeTemplate"`
	SMSLoginEnabled           bool    `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool    `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool    `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool    `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string  `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string  `json:"wechatCodeStaticQrcodeUrl"`
	WechatAPIBaseURL          string  `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken      string  `json:"wechatApiBearerToken"`
	CaptchaProvider           string  `json:"captchaProvider"`
	CaptchaAPIBaseURL         string  `json:"captchaApiBaseUrl"`
	CaptchaAPIKey             string  `json:"captchaApiKey"`
	CaptchaType               string  `json:"captchaType"`
	SMSBaoCredentialType      string  `json:"smsbaoCredentialType"`
	SMSBaoRegionMode          string  `json:"smsbaoRegionMode"`
	// UAPIAPIKey 是 UApiPro 的 API 密钥；与其它密钥一样**明文**返回。
	UAPIAPIKey            string  `json:"uapiApiKey"`
	MaxLogSizeBytes       int64   `json:"maxLogSizeBytes"`
	MaxAISearchModelCalls int     `json:"maxAISearchModelCalls"`
	MaxAISearches         int     `json:"maxAISearches"`
	MaxAISearchRetries    int     `json:"maxAISearchRetries"`
	MaxAIWebReads         int     `json:"maxAIWebReads"`
	WebReadPricePerCall   float64 `json:"webReadPricePerCall"`
	MinRechargeAmount     float64 `json:"minRechargeAmount"`
	// 商业化总开关，默认关闭。语义见 RechargePromotionEnabledSettingKey 注释。
	RechargePromotionEnabled bool `json:"rechargePromotionEnabled"`
	DeviceLimitEnabled       bool `json:"deviceLimitEnabled"`
	// 邀请返佣：总开关默认关闭，比例默认 0（0%–100%，上界开区间）。
	ReferralRebateEnabled bool    `json:"referralRebateEnabled"`
	ReferralRebateRate    float64 `json:"referralRebateRate"`
	// 评价返现：开关默认关闭，三条上限/门槛都有保守默认值。
	// 规则表本身不在这里（条数不定），走 review_rebate_rules 独立表。
	ReviewRebateEnabled            bool    `json:"reviewRebateEnabled"`
	ReviewRebateMaxClaimsPerUser   int     `json:"reviewRebateMaxClaimsPerUser"`
	ReviewRebateMaxAmountPerUser   float64 `json:"reviewRebateMaxAmountPerUser"`
	ReviewRebateMinAgentConfidence float64 `json:"reviewRebateMinAgentConfidence"`
	// 黑名单总开关，默认关闭（46-黑名单设计.md）。关闭时中间件零查询、零写日志。
	BlacklistEnabled bool `json:"blacklistEnabled"`
	// 两套登录限流（45-*.md §0.1.4）。普通模式按账号、可关闭；
	// 被拉黑 IP 的管理员入口按 IP、**不可关闭**（没有 enabled 键，见 login_rate_limit.go）。
	LoginRateLimitEnabled             bool `json:"loginRateLimitEnabled"`
	LoginMaxAttempts                  int  `json:"loginMaxAttempts"`
	LoginAttemptWindowMinutes         int  `json:"loginAttemptWindowMinutes"`
	LoginLockoutMinutes               int  `json:"loginLockoutMinutes"`
	BlacklistAdminLoginWindowMinutes  int  `json:"blacklistAdminLoginWindowMinutes"`
	BlacklistAdminLoginMaxAttempts    int  `json:"blacklistAdminLoginMaxAttempts"`
	BlacklistAdminLoginLockoutMinutes int  `json:"blacklistAdminLoginLockoutMinutes"`
	// 普通模式 IP 维度阈值（默认 20）与 IP 白名单（CIDR 列表，默认空）。
	// 见 45-*.md Q67：账号维度拦不住 password spraying，需 IP 维度兜底。
	LoginIPMaxAttempts      int    `json:"loginIPMaxAttempts"`
	LoginRateLimitExemptIPs string `json:"loginRateLimitExemptIPs"`
	// SensitiveWordCheckEnabled 是敏感词检测总开关，默认 false。
	//
	// 关闭时完全不调外部 API、不写 logs 的 sensitive_* 四列；
	// 开启只影响「打标记」，答题流程与返回结构在任何取值下都完全一致。
	SensitiveWordCheckEnabled bool `json:"sensitiveWordCheckEnabled"`
	// SensitiveWordCheckConfigured 报告「开关已开 + 密钥可用」。
	//
	// 只读派生值：开关开着但没配 UAPI 密钥时它是 false，管理页据此提示
	// 「已启用但未配置」，避免用户以为打开开关就生效了。
	SensitiveWordCheckConfigured bool `json:"sensitiveWordCheckConfigured"`
}

type UpdateSystemSettingInput struct {
	RegisterEnabled           bool    `json:"registerEnabled"`
	NewUserBonusAmount        float64 `json:"newUserBonusAmount"`
	SiteName                  string  `json:"siteName"`
	EPayVersion               string  `json:"epayVersion"`
	EPayGatewayURL            string  `json:"epayGatewayUrl"`
	EPayPartnerID             string  `json:"epayPartnerId"`
	EPayPlatformPublicKey     string  `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey    string  `json:"epayMerchantPrivateKey"`
	EPayNotifyURL             string  `json:"epayNotifyUrl"`
	EPayReturnURL             string  `json:"epayReturnUrl"`
	SMSProvider               string  `json:"smsProvider"`
	SMSBaoUsername            string  `json:"smsbaoUsername"`
	SMSBaoSecret              string  `json:"smsbaoSecret"`
	SMSCodeSignName           string  `json:"smsCodeSignName"`
	SMSCodeTemplate           string  `json:"smsCodeTemplate"`
	SMSLoginEnabled           bool    `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool    `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool    `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool    `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string  `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string  `json:"wechatCodeStaticQrcodeUrl"`
	WechatAPIBaseURL          string  `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken      string  `json:"wechatApiBearerToken"`
	CaptchaProvider           string  `json:"captchaProvider"`
	CaptchaAPIBaseURL         string  `json:"captchaApiBaseUrl"`
	CaptchaAPIKey             string  `json:"captchaApiKey"`
	CaptchaType               string  `json:"captchaType"`
	SMSBaoCredentialType      string  `json:"smsbaoCredentialType"`
	SMSBaoRegionMode          string  `json:"smsbaoRegionMode"`
	// UAPIAPIKey 是 UApiPro 的 API 密钥；与其它密钥一样**明文**返回。
	// 写入语义与其它密钥一致：空值或 ******** 表示保持原值，__CLEAR__ 表示清空。
	UAPIAPIKey            string  `json:"uapiApiKey"`
	MaxLogSizeBytes       int64   `json:"maxLogSizeBytes"`
	MaxAISearchModelCalls int     `json:"maxAISearchModelCalls"`
	MaxAISearches         int     `json:"maxAISearches"`
	MaxAISearchRetries    int     `json:"maxAISearchRetries"`
	MaxAIWebReads         int     `json:"maxAIWebReads"`
	WebReadPricePerCall   float64 `json:"webReadPricePerCall"`
	MinRechargeAmount     float64 `json:"minRechargeAmount"`
	// 指针语义：nil = 旧客户端未提交该字段，保持库中现值；非 nil 才覆盖。
	// 若用裸 bool，任何省略字段的旧客户端都会把开关静默关掉。
	RechargePromotionEnabled *bool `json:"rechargePromotionEnabled"`
	DeviceLimitEnabled       *bool `json:"deviceLimitEnabled"`
	// 邀请返佣：nil = 本次没有提交该字段 = 保持现值。
	// 布尔一律用指针，避免老前端（不认识这两个字段）把开关静默关掉。
	ReferralRebateEnabled *bool    `json:"referralRebateEnabled"`
	ReferralRebateRate    *float64 `json:"referralRebateRate"`
	// 评价返现：同样是指针语义。nil = 未提交 = 保持现值。
	// 整数上限也用指针 —— 0 是合法输入（等于关闭该活动），不能拿来当「未提交」。
	ReviewRebateEnabled            *bool    `json:"reviewRebateEnabled"`
	ReviewRebateMaxClaimsPerUser   *int     `json:"reviewRebateMaxClaimsPerUser"`
	ReviewRebateMaxAmountPerUser   *float64 `json:"reviewRebateMaxAmountPerUser"`
	ReviewRebateMinAgentConfidence *float64 `json:"reviewRebateMinAgentConfidence"`
	// 黑名单总开关：同样是指针语义，nil = 未提交 = 保持现值。
	// 旧客户端（不带该字段）不会因为一次「只改站点名」的 PUT 把黑名单打开或关掉。
	BlacklistEnabled *bool `json:"blacklistEnabled"`
	// 两套登录限流：指针语义，nil = 未提交 = 保持现值。
	// ⚠️ 刻意**没有** BlacklistAdminLoginEnabled —— 「不允许关闭」是结构性保证，
	// 不接受该键，也不存在对应的 system_settings 键（见 login_rate_limit.go）。
	LoginRateLimitEnabled             *bool `json:"loginRateLimitEnabled"`
	LoginMaxAttempts                  *int  `json:"loginMaxAttempts"`
	LoginAttemptWindowMinutes         *int  `json:"loginAttemptWindowMinutes"`
	LoginLockoutMinutes               *int  `json:"loginLockoutMinutes"`
	BlacklistAdminLoginWindowMinutes  *int  `json:"blacklistAdminLoginWindowMinutes"`
	BlacklistAdminLoginMaxAttempts    *int  `json:"blacklistAdminLoginMaxAttempts"`
	BlacklistAdminLoginLockoutMinutes *int  `json:"blacklistAdminLoginLockoutMinutes"`
	// 普通模式 IP 维度阈值 + IP 白名单：指针语义，nil = 未提交 = 保持现值。
	LoginIPMaxAttempts      *int    `json:"loginIPMaxAttempts"`
	LoginRateLimitExemptIPs *string `json:"loginRateLimitExemptIPs"`
	// 敏感词检测开关：指针语义，nil = 未提交 = 保持库中现值。
	//
	// 若用裸 bool，任何不认识该字段的旧客户端提交设置都会把开关静默关掉。
	SensitiveWordCheckEnabled *bool `json:"sensitiveWordCheckEnabled"`
}

var ErrInvalidNewUserBonusAmount = errors.New("新用户赠送金额不能为负数")
var ErrInvalidMinRechargeAmount = errors.New("最低充值金额必须是大于 0 的有效金额")
var ErrInvalidAISearchCount = errors.New("AI 调用与工具次数必须是可精确表示的正整数")
var ErrInvalidReferralRebateRate = errors.New("返佣比例必须在 0（含）到 1（不含）之间")

// 评价返现配置的输入校验错误（都是 400，不是 500）。
var (
	// ErrInvalidReviewRebateAmount 金额为负或非有限数。
	ErrInvalidReviewRebateAmount = errors.New("评价返现金额必须是不小于 0 的有效金额")
	// ErrInvalidReviewRebateConfidence 置信度门槛不在 [0,1]。
	ErrInvalidReviewRebateConfidence = errors.New("Agent 置信度门槛必须在 0 到 1 之间")
	// ErrInvalidReviewRebateMaxClaims 次数上限为负。
	ErrInvalidReviewRebateMaxClaims = errors.New("评价返现次数上限必须是不小于 0 的整数")
)

type PublicAuthSettings struct {
	RegisterEnabled           bool   `json:"registerEnabled"`
	SiteName                  string `json:"siteName"`
	SMSLoginEnabled           bool   `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool   `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool   `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool   `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string `json:"wechatCodeStaticQrcodeUrl"`
	AlternativeLoginEnabled   bool   `json:"alternativeLoginEnabled"`
	CaptchaProvider           string `json:"captchaProvider"`
	CaptchaAPIBaseURL         string `json:"captchaApiBaseUrl"`
	CaptchaType               string `json:"captchaType"`
}

func GetRegisterEnabledWithDB(db *gorm.DB) (bool, error) {
	setting, exists, err := getSystemSettingByKey(db, RegisterEnabledSettingKey)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(setting.Value), "true"), nil
}

func SetRegisterEnabledWithDB(db *gorm.DB, enabled bool) (bool, error) {
	value := "false"
	if enabled {
		value = "true"
	}

	setting, _, err := getSystemSettingByKey(db, RegisterEnabledSettingKey)
	if err != nil {
		return false, err
	}
	if setting.Key == "" {
		setting.Key = RegisterEnabledSettingKey
		setting.Remark = "是否允许公开注册"
	}
	setting.Value = value

	freshDB := db.Session(&gorm.Session{NewDB: true})
	if err := freshDB.Save(&setting).Error; err != nil {
		return false, err
	}
	return enabled, nil
}

func GetNewUserBonusAmountWithDB(db *gorm.DB) (float64, error) {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, NewUserBonusAmountSettingKey, "0"))
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultNewUserBonusAmount, nil
	}
	normalized, err := normalizeNewUserBonusAmount(amount)
	if err != nil {
		return DefaultNewUserBonusAmount, nil
	}
	return normalized, nil
}

func normalizeNewUserBonusAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrInvalidNewUserBonusAmount
	}
	return util.RoundMoney(amount), nil
}

func GetPublicAuthSettingsWithDB(db *gorm.DB) (PublicAuthSettings, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return PublicAuthSettings{}, err
	}
	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatAPIBaseURL := trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, ""))
	wechatAPIBearerToken := strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, ""))
	wechatSettings := getNormalizedWechatLoginSettingsWithDB(db)
	wechatConfigReady := wechatSettings.WechatLoginEnabled && wechatAPIBaseURL != "" && wechatAPIBearerToken != ""
	publicWechatScanLoginEnabled := wechatConfigReady && wechatSettings.WechatScanLoginEnabled
	publicWechatCodeLoginEnabled := wechatConfigReady && wechatSettings.WechatCodeLoginEnabled && wechatSettings.WechatCodeStaticQRCodeURL != ""
	publicWechatLoginEnabled := publicWechatScanLoginEnabled || publicWechatCodeLoginEnabled
	publicWechatDefaultLoginMode := normalizeEnabledWechatLoginMode(wechatSettings.WechatDefaultLoginMode, publicWechatScanLoginEnabled, publicWechatCodeLoginEnabled)
	return PublicAuthSettings{
		RegisterEnabled:           registerEnabled,
		SiteName:                  normalizeSiteName(getSystemSettingStringWithDefault(db, SiteNameSettingKey, DefaultSiteName)),
		SMSLoginEnabled:           smsLoginEnabled,
		WechatLoginEnabled:        publicWechatLoginEnabled,
		WechatScanLoginEnabled:    publicWechatScanLoginEnabled,
		WechatCodeLoginEnabled:    publicWechatCodeLoginEnabled,
		WechatDefaultLoginMode:    publicWechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL: wechatSettings.WechatCodeStaticQRCodeURL,
		AlternativeLoginEnabled:   smsLoginEnabled || publicWechatLoginEnabled,
		CaptchaProvider:           normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:         trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaType:               normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
	}, nil
}

func GetSystemSettingDetailWithDB(db *gorm.DB) (SystemSettingDetail, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}

	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatSettings := getNormalizedWechatLoginSettingsWithDB(db)
	detail := SystemSettingDetail{
		RegisterEnabled:                   registerEnabled,
		NewUserBonusAmount:                DefaultNewUserBonusAmount,
		SiteName:                          normalizeSiteName(getSystemSettingStringWithDefault(db, SiteNameSettingKey, DefaultSiteName)),
		AlternativeLoginEnabled:           smsLoginEnabled || wechatSettings.WechatLoginEnabled,
		EPayVersion:                       DefaultEPayVersion,
		EPayGatewayURL:                    getSystemSettingStringWithDefault(db, EPayGatewayURLSettingKey, ""),
		EPayPartnerID:                     getSystemSettingStringWithDefault(db, EPayPartnerIDSettingKey, ""),
		EPayPlatformPublicKey:             getSystemSettingStringWithDefault(db, EPayPlatformPublicKeySettingKey, ""),
		EPayMerchantPrivateKey:            getSystemSettingStringWithDefault(db, EPayMerchantPrivateKeySettingKey, ""),
		EPayNotifyURL:                     getSystemSettingStringWithDefault(db, EPayNotifyURLSettingKey, ""),
		EPayReturnURL:                     getSystemSettingStringWithDefault(db, EPayReturnURLSettingKey, ""),
		SMSProvider:                       getSystemSettingStringWithDefault(db, SMSProviderSettingKey, DefaultSMSProvider),
		SMSBaoUsername:                    getSystemSettingStringWithDefault(db, SMSBaoUsernameSettingKey, ""),
		SMSBaoSecret:                      getSystemSettingStringWithDefault(db, SMSBaoSecretSettingKey, ""),
		SMSCodeSignName:                   getSystemSettingStringWithDefault(db, SMSCodeSignNameSettingKey, ""),
		SMSCodeTemplate:                   getSystemSettingStringWithDefault(db, SMSCodeTemplateSettingKey, DefaultSMSCodeTemplate),
		SMSLoginEnabled:                   smsLoginEnabled,
		WechatLoginEnabled:                wechatSettings.WechatLoginEnabled,
		WechatScanLoginEnabled:            wechatSettings.WechatScanLoginEnabled,
		WechatCodeLoginEnabled:            wechatSettings.WechatCodeLoginEnabled,
		WechatDefaultLoginMode:            wechatSettings.WechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL:         wechatSettings.WechatCodeStaticQRCodeURL,
		WechatAPIBaseURL:                  trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, "")),
		WechatAPIBearerToken:              strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, "")),
		CaptchaProvider:                   normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:                 trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:                     strings.TrimSpace(getSystemSettingStringWithDefault(db, CaptchaAPIKeySettingKey, DefaultCaptchaAPIKey)),
		CaptchaType:                       normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
		SMSBaoCredentialType:              normalizeSMSBaoCredentialType(getSystemSettingStringWithDefault(db, SMSBaoCredentialTypeSettingKey, DefaultSMSBaoCredentialType)),
		SMSBaoRegionMode:                  normalizeSMSBaoRegionMode(getSystemSettingStringWithDefault(db, SMSBaoRegionModeSettingKey, DefaultSMSBaoRegionMode)),
		UAPIAPIKey:                        strings.TrimSpace(getSystemSettingStringWithDefault(db, UAPIAPIKeySettingKey, "")),
		MaxLogSizeBytes:                   getPositiveInt64SystemSetting(db, MaxLogSizeBytesSettingKey, DefaultMaxLogSizeBytes),
		MaxAISearchModelCalls:             getPositiveIntSystemSetting(db, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls),
		MaxAISearches:                     getPositiveIntSystemSetting(db, MaxAISearchesSettingKey, DefaultMaxAISearches),
		MaxAISearchRetries:                getPositiveIntSystemSetting(db, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries),
		MaxAIWebReads:                     getPositiveIntSystemSetting(db, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads),
		WebReadPricePerCall:               GetWebReadPricePerCallWithDB(db),
		MinRechargeAmount:                 GetMinRechargeAmountWithDB(db),
		RechargePromotionEnabled:          GetRechargePromotionEnabledWithDB(db),
		DeviceLimitEnabled:                GetDeviceLimitEnabledWithDB(db),
		ReferralRebateEnabled:             GetReferralRebateEnabledWithDB(db),
		ReferralRebateRate:                GetReferralRebateRateWithDB(db),
		ReviewRebateEnabled:               GetReviewRebateEnabledWithDB(db),
		ReviewRebateMaxClaimsPerUser:      GetReviewRebateMaxClaimsPerUserWithDB(db),
		ReviewRebateMaxAmountPerUser:      GetReviewRebateMaxAmountPerUserWithDB(db),
		ReviewRebateMinAgentConfidence:    GetReviewRebateMinAgentConfidenceWithDB(db),
		BlacklistEnabled:                  GetBlacklistEnabledWithDB(db),
		LoginRateLimitEnabled:             GetLoginRateLimitEnabledWithDB(db),
		LoginMaxAttempts:                  GetLoginMaxAttemptsWithDB(db),
		LoginAttemptWindowMinutes:         GetLoginAttemptWindowMinutesWithDB(db),
		LoginLockoutMinutes:               GetLoginLockoutMinutesWithDB(db),
		BlacklistAdminLoginWindowMinutes:  GetBlacklistAdminLoginWindowMinutesWithDB(db),
		BlacklistAdminLoginMaxAttempts:    GetBlacklistAdminLoginMaxAttemptsWithDB(db),
		BlacklistAdminLoginLockoutMinutes: GetBlacklistAdminLoginLockoutMinutesWithDB(db),
		LoginIPMaxAttempts:                GetLoginIPMaxAttemptsWithDB(db),
		LoginRateLimitExemptIPs:           GetLoginRateLimitExemptIPsWithDB(db),
		SensitiveWordCheckEnabled:         GetSensitiveWordCheckEnabledWithDB(db),
		SensitiveWordCheckConfigured:      IsSensitiveWordDetectorConfiguredWithDB(db),
	}
	bonusAmount, err := GetNewUserBonusAmountWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	detail.NewUserBonusAmount = bonusAmount
	return detail, nil
}

func UpdateSystemSettingDetailWithDB(db *gorm.DB, input UpdateSystemSettingInput) (SystemSettingDetail, error) {
	if err := validateAISearchCountInputs(input.MaxAISearchModelCalls, input.MaxAISearches, input.MaxAISearchRetries, input.MaxAIWebReads); err != nil {
		return SystemSettingDetail{}, err
	}
	if _, err := SetRegisterEnabledWithDB(db, input.RegisterEnabled); err != nil {
		return SystemSettingDetail{}, err
	}
	bonusAmount, err := normalizeNewUserBonusAmount(input.NewUserBonusAmount)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	minRechargeAmount, err := resolveMinRechargeAmount(db, input.MinRechargeAmount)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	referralRebateRate, err := resolveReferralRebateRate(db, input.ReferralRebateRate)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	reviewRebateMaxClaims, err := resolveReviewRebateMaxClaims(db, input.ReviewRebateMaxClaimsPerUser)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	reviewRebateMaxAmount, err := resolveReviewRebateMaxAmount(db, input.ReviewRebateMaxAmountPerUser)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	reviewRebateMinConfidence, err := resolveReviewRebateMinConfidence(db, input.ReviewRebateMinAgentConfidence)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	// 两套登录限流：边界校验**必须在写库之前**（越界拒绝保存，不静默 clamp）。
	if err := ValidateLoginRateLimitInput(input); err != nil {
		return SystemSettingDetail{}, err
	}

	normalizedWechatLoginEnabled := input.WechatLoginEnabled && (input.WechatScanLoginEnabled || input.WechatCodeLoginEnabled)
	normalizedWechatScanLoginEnabled := normalizedWechatLoginEnabled && input.WechatScanLoginEnabled
	normalizedWechatCodeLoginEnabled := normalizedWechatLoginEnabled && input.WechatCodeLoginEnabled
	normalizedWechatDefaultLoginMode := normalizeEnabledWechatLoginMode(input.WechatDefaultLoginMode, normalizedWechatScanLoginEnabled, normalizedWechatCodeLoginEnabled)
	if normalizedWechatDefaultLoginMode == "" {
		normalizedWechatDefaultLoginMode = DefaultWechatLoginMode
	}
	normalized := SystemSettingDetail{
		RegisterEnabled:           input.RegisterEnabled,
		NewUserBonusAmount:        bonusAmount,
		SiteName:                  normalizeSiteName(input.SiteName),
		EPayVersion:               normalizeEPayVersion(input.EPayVersion),
		EPayGatewayURL:            strings.TrimSpace(input.EPayGatewayURL),
		EPayPartnerID:             strings.TrimSpace(input.EPayPartnerID),
		EPayPlatformPublicKey:     strings.TrimSpace(input.EPayPlatformPublicKey),
		EPayMerchantPrivateKey:    mergeStoredSecretInput(db, EPayMerchantPrivateKeySettingKey, input.EPayMerchantPrivateKey),
		EPayNotifyURL:             strings.TrimSpace(input.EPayNotifyURL),
		EPayReturnURL:             strings.TrimSpace(input.EPayReturnURL),
		SMSProvider:               strings.TrimSpace(input.SMSProvider),
		SMSBaoUsername:            strings.TrimSpace(input.SMSBaoUsername),
		SMSBaoSecret:              mergeStoredSecretInput(db, SMSBaoSecretSettingKey, input.SMSBaoSecret),
		SMSCodeSignName:           strings.TrimSpace(input.SMSCodeSignName),
		SMSCodeTemplate:           strings.TrimSpace(input.SMSCodeTemplate),
		SMSLoginEnabled:           input.SMSLoginEnabled,
		WechatLoginEnabled:        normalizedWechatLoginEnabled,
		WechatScanLoginEnabled:    normalizedWechatScanLoginEnabled,
		WechatCodeLoginEnabled:    normalizedWechatCodeLoginEnabled,
		WechatDefaultLoginMode:    normalizedWechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL: strings.TrimSpace(input.WechatCodeStaticQRCodeURL),
		WechatAPIBaseURL:          trimTrailingSlash(input.WechatAPIBaseURL),
		WechatAPIBearerToken:      mergeStoredSecretInput(db, WechatAPIBearerTokenSettingKey, input.WechatAPIBearerToken),
		CaptchaProvider:           normalizeCaptchaProvider(input.CaptchaProvider),
		CaptchaAPIBaseURL:         trimTrailingSlash(defaultString(input.CaptchaAPIBaseURL, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:             mergeStoredSecretInput(db, CaptchaAPIKeySettingKey, input.CaptchaAPIKey),
		CaptchaType:               normalizeCaptchaType(input.CaptchaType),
		SMSBaoCredentialType:      normalizeSMSBaoCredentialType(input.SMSBaoCredentialType),
		SMSBaoRegionMode:          normalizeSMSBaoRegionMode(input.SMSBaoRegionMode),
		// UApiPro 密钥不脱敏：系统设置页本身只有管理员能打开（与「默认账号配置」
		// 的 token 处理一致），明文存取、由前端密码框隐藏显示。
		// 直接 TrimSpace 而不是 mergeStoredSecretInput —— 留空即清空，语义直观。
		UAPIAPIKey:            strings.TrimSpace(input.UAPIAPIKey),
		MaxLogSizeBytes:       resolveMaxLogSizeBytes(db, input.MaxLogSizeBytes),
		MaxAISearchModelCalls: resolveMaxAISearchBudget(db, input.MaxAISearchModelCalls, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls),
		MaxAISearches:         resolveMaxAISearchBudget(db, input.MaxAISearches, MaxAISearchesSettingKey, DefaultMaxAISearches),
		MaxAISearchRetries:    resolveMaxAISearchBudget(db, input.MaxAISearchRetries, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries),
		MaxAIWebReads:         resolveMaxAISearchBudget(db, input.MaxAIWebReads, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads),
		WebReadPricePerCall:   resolveWebReadPrice(db, input.WebReadPricePerCall),
		MinRechargeAmount:     minRechargeAmount,
		// 省略字段保持库中现值：旧客户端（或只改其它字段的请求）不会误关开关。
		RechargePromotionEnabled: resolveBoolSettingInput(db, input.RechargePromotionEnabled, RechargePromotionEnabledSettingKey),
		DeviceLimitEnabled:       resolveBoolSettingInput(db, input.DeviceLimitEnabled, DeviceLimitEnabledSettingKey),
		// nil = 未提交 = 保持现值；读取失败一律 false / 0（不能让配置读错变成开活动）。
		ReferralRebateEnabled: resolveBoolSettingInput(db, input.ReferralRebateEnabled, ReferralRebateEnabledSettingKey),
		ReferralRebateRate:    referralRebateRate,
		// 评价返现：同上，nil = 未提交 = 保持现值；读取失败一律回落保守默认（开关 false）。
		ReviewRebateEnabled:            resolveBoolSettingInput(db, input.ReviewRebateEnabled, ReviewRebateEnabledSettingKey),
		ReviewRebateMaxClaimsPerUser:   reviewRebateMaxClaims,
		ReviewRebateMaxAmountPerUser:   reviewRebateMaxAmount,
		ReviewRebateMinAgentConfidence: reviewRebateMinConfidence,
		// 黑名单：nil = 未提交 = 保持现值；读取失败一律 false（不能让配置读错变成开始拦人）。
		BlacklistEnabled: resolveBoolSettingInput(db, input.BlacklistEnabled, BlacklistEnabledSettingKey),
		// 两套登录限流：越界**拒绝保存**（ValidateLoginRateLimitInput 已在函数入口校验）。
		// nil = 未提交 = 保持现值。
		LoginRateLimitEnabled: resolveBoolSettingInput(db, input.LoginRateLimitEnabled, LoginRateLimitEnabledSettingKey),
		LoginMaxAttempts: resolveLoginRateLimitInt(db, input.LoginMaxAttempts,
			GetLoginMaxAttemptsWithDB(db)),
		LoginAttemptWindowMinutes: resolveLoginRateLimitInt(db, input.LoginAttemptWindowMinutes,
			GetLoginAttemptWindowMinutesWithDB(db)),
		LoginLockoutMinutes: resolveLoginRateLimitInt(db, input.LoginLockoutMinutes,
			GetLoginLockoutMinutesWithDB(db)),
		BlacklistAdminLoginWindowMinutes: resolveLoginRateLimitInt(db, input.BlacklistAdminLoginWindowMinutes,
			GetBlacklistAdminLoginWindowMinutesWithDB(db)),
		BlacklistAdminLoginMaxAttempts: resolveLoginRateLimitInt(db, input.BlacklistAdminLoginMaxAttempts,
			GetBlacklistAdminLoginMaxAttemptsWithDB(db)),
		BlacklistAdminLoginLockoutMinutes: resolveLoginRateLimitInt(db, input.BlacklistAdminLoginLockoutMinutes,
			GetBlacklistAdminLoginLockoutMinutesWithDB(db)),
		LoginIPMaxAttempts: resolveLoginRateLimitInt(db, input.LoginIPMaxAttempts,
			GetLoginIPMaxAttemptsWithDB(db)),
		LoginRateLimitExemptIPs: resolveLoginRateLimitExemptIPs(db, input.LoginRateLimitExemptIPs),
		// 敏感词检测：nil = 未提交 = 保持现值；读取失败一律 false（不能悄悄开外呼）。
		SensitiveWordCheckEnabled: ResolveSensitiveWordSettingInput(db, input.SensitiveWordCheckEnabled),
	}
	normalized.AlternativeLoginEnabled = normalized.SMSLoginEnabled || normalized.WechatLoginEnabled

	settings := []struct {
		key    string
		value  string
		remark string
	}{
		{SiteNameSettingKey, normalized.SiteName, "网站名称"},
		{NewUserBonusAmountSettingKey, strconv.FormatFloat(normalized.NewUserBonusAmount, 'f', 2, 64), "新用户默认赠送金额"},
		{PaymentProviderSettingKey, normalizePaymentProvider(DefaultPaymentProvider), "支付提供方"},
		{EPayVersionSettingKey, normalized.EPayVersion, "易支付接口版本"},
		{EPayGatewayURLSettingKey, normalized.EPayGatewayURL, "易支付网关地址"},
		{EPayPartnerIDSettingKey, normalized.EPayPartnerID, "易支付商户编号"},
		{EPayPlatformPublicKeySettingKey, normalized.EPayPlatformPublicKey, "易支付平台公钥"},
		{EPayMerchantPrivateKeySettingKey, normalized.EPayMerchantPrivateKey, "易支付商户私钥"},
		{EPayNotifyURLSettingKey, normalized.EPayNotifyURL, "易支付异步通知地址"},
		{EPayReturnURLSettingKey, normalized.EPayReturnURL, "易支付同步跳转地址"},
		{SMSProviderSettingKey, normalized.SMSProvider, "短信提供方"},
		{SMSBaoUsernameSettingKey, normalized.SMSBaoUsername, "短信宝用户名"},
		{SMSBaoSecretSettingKey, normalized.SMSBaoSecret, "短信宝密钥"},
		{SMSCodeSignNameSettingKey, normalized.SMSCodeSignName, "短信签名"},
		{SMSCodeTemplateSettingKey, normalized.SMSCodeTemplate, "短信验证码模板"},
		{SMSLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.SMSLoginEnabled], "是否开启短信验证码登录"},
		{WechatLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatLoginEnabled], "是否开启微信登录"},
		{WechatScanLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatScanLoginEnabled], "是否开启微信扫描登录"},
		{WechatCodeLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatCodeLoginEnabled], "是否开启微信验证码登录"},
		{WechatDefaultLoginModeSettingKey, normalized.WechatDefaultLoginMode, "默认微信登录方式"},
		{WechatCodeStaticQRCodeURLSettingKey, normalized.WechatCodeStaticQRCodeURL, "微信验证码登录静态二维码"},
		{WechatAPIBaseURLSettingKey, normalized.WechatAPIBaseURL, "微信登录服务地址"},
		{WechatAPIBearerTokenSettingKey, normalized.WechatAPIBearerToken, "微信登录服务 Bearer Token"},
		{CaptchaProviderSettingKey, normalized.CaptchaProvider, "验证码提供方"},
		{CaptchaAPIBaseURLSettingKey, normalized.CaptchaAPIBaseURL, "远程验证码服务地址"},
		{CaptchaAPIKeySettingKey, normalized.CaptchaAPIKey, "远程验证码 X-Captcha-Key"},
		{CaptchaTypeSettingKey, normalized.CaptchaType, "验证码类型"},
		{SMSBaoCredentialTypeSettingKey, normalized.SMSBaoCredentialType, "短信宝凭证类型"},
		{SMSBaoRegionModeSettingKey, normalized.SMSBaoRegionMode, "短信宝发送区域模式"},
		{UAPIAPIKeySettingKey, normalized.UAPIAPIKey, "UApiPro API 密钥"},
		{MaxLogSizeBytesSettingKey, strconv.FormatInt(normalized.MaxLogSizeBytes, 10), "日志文件总大小上限（字节）"},
		{MaxAISearchModelCallsSettingKey, strconv.Itoa(normalized.MaxAISearchModelCalls), "AI 搜索模型最大调用次数"},
		{MaxAISearchesSettingKey, strconv.Itoa(normalized.MaxAISearches), "AI 搜索最大搜索次数"},
		{MaxAISearchRetriesSettingKey, strconv.Itoa(normalized.MaxAISearchRetries), "AI 搜索每轮失败重试次数（含首次调用）"},
		{MaxAIWebReadsSettingKey, strconv.Itoa(normalized.MaxAIWebReads), "AI 搜索网页读取最大次数"},
		{WebReadPricePerCallSettingKey, strconv.FormatFloat(normalized.WebReadPricePerCall, 'f', -1, 64), "网页读取单价（元/次）"},
		{MinRechargeAmountSettingKey, formatRechargeSettingAmount(normalized.MinRechargeAmount), "充值最低金额（元）"},
		{RechargePromotionEnabledSettingKey, strconv.FormatBool(normalized.RechargePromotionEnabled), "启用充值阶梯赠送（默认关闭）"},
		{DeviceLimitEnabledSettingKey, strconv.FormatBool(normalized.DeviceLimitEnabled), "启用账户设备池上限（默认关闭）"},
		{ReferralRebateEnabledSettingKey, strconv.FormatBool(normalized.ReferralRebateEnabled), "启用邀请码与充值返佣（默认关闭）"},
		{ReferralRebateRateSettingKey, strconv.FormatFloat(normalized.ReferralRebateRate, 'f', -1, 64), "返佣比例（0–1，不含 1）"},
		{ReviewRebateEnabledSettingKey, strconv.FormatBool(normalized.ReviewRebateEnabled), "启用评价返现（默认关闭）"},
		{ReviewRebateMaxClaimsPerUserSettingKey, strconv.Itoa(normalized.ReviewRebateMaxClaimsPerUser), "评价返现每账号累计次数上限"},
		{ReviewRebateMaxAmountPerUserSettingKey, strconv.FormatFloat(normalized.ReviewRebateMaxAmountPerUser, 'f', -1, 64), "评价返现每账号累计金额上限（元）"},
		{ReviewRebateMinAgentConfidenceSettingKey, strconv.FormatFloat(normalized.ReviewRebateMinAgentConfidence, 'f', -1, 64), "评价返现 Agent 置信度门槛（低于则强制人工）"},
		{BlacklistEnabledSettingKey, strconv.FormatBool(normalized.BlacklistEnabled), "启用黑名单（默认关闭）"},
		{LoginRateLimitEnabledSettingKey, strconv.FormatBool(normalized.LoginRateLimitEnabled), "启用普通登录限流（默认开启，可关闭）"},
		{LoginMaxAttemptsSettingKey, strconv.Itoa(normalized.LoginMaxAttempts), "普通登录连续失败阈值（次，默认 10，无上限）"},
		{LoginAttemptWindowMinutesSettingKey, strconv.Itoa(normalized.LoginAttemptWindowMinutes), "普通登录失败计数窗口（分钟，默认 15，无上限）"},
		{LoginLockoutMinutesSettingKey, strconv.Itoa(normalized.LoginLockoutMinutes), "普通登录锁定时长（分钟，默认 15）"},
		// ⚠️ 以下三键**没有**对应的 enabled 键：「不允许关闭」是结构性保证。
		{BlacklistAdminLoginWindowMinutesSettingKey, strconv.Itoa(normalized.BlacklistAdminLoginWindowMinutes), "被拉黑 IP 的管理员登录计数窗口（分钟，默认 15，最低 15）"},
		{BlacklistAdminLoginMaxAttemptsSettingKey, strconv.Itoa(normalized.BlacklistAdminLoginMaxAttempts), "被拉黑 IP 的管理员登录连续失败阈值（次，默认 5，最高 10）"},
		{BlacklistAdminLoginLockoutMinutesSettingKey, strconv.Itoa(normalized.BlacklistAdminLoginLockoutMinutes), "被拉黑 IP 的管理员登录锁定时长（分钟，默认 30）"},
		{LoginIPMaxAttemptsSettingKey, strconv.Itoa(normalized.LoginIPMaxAttempts), "普通登录 IP 维度连续失败阈值（次，默认 20，无上限）"},
		{LoginRateLimitExemptIPsSettingKey, normalized.LoginRateLimitExemptIPs, "登录限流 IP 白名单（IP/CIDR，逗号或换行分隔；白名单不参与 IP 维度限流）"},
		{SensitiveWordCheckEnabledSettingKey, strconv.FormatBool(normalized.SensitiveWordCheckEnabled), "启用敏感词检测打标（默认关闭，不影响答题）"},
	}
	for _, item := range settings {
		if err := saveSystemSettingValue(db, item.key, item.value, item.remark); err != nil {
			return SystemSettingDetail{}, err
		}
	}

	return GetSystemSettingDetailWithDB(db)
}

func resolveMaxLogSizeBytes(db *gorm.DB, requested int64) int64 {
	if requested > 0 {
		return requested
	}
	return getPositiveInt64SystemSetting(db, MaxLogSizeBytesSettingKey, DefaultMaxLogSizeBytes)
}

// GetMinRechargeAmountWithDB returns the configured minimum recharge amount.
// Invalid or missing persisted values fall back to the one-yuan default so a
// malformed setting cannot accidentally disable the server-side restriction.
func GetMinRechargeAmountWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultMinRechargeAmount
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, MinRechargeAmountSettingKey, formatRechargeSettingAmount(DefaultMinRechargeAmount)))
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultMinRechargeAmount
	}
	normalized, err := normalizeMinRechargeAmount(amount)
	if err != nil {
		return DefaultMinRechargeAmount
	}
	return normalized
}

func normalizeMinRechargeAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, ErrInvalidMinRechargeAmount
	}
	return util.RoundMoneyToDecimals(amount, util.RechargePrecision), nil
}

func resolveMinRechargeAmount(db *gorm.DB, requested float64) (float64, error) {
	if requested > 0 {
		return normalizeMinRechargeAmount(requested)
	}
	if requested < 0 || math.IsNaN(requested) || math.IsInf(requested, 0) {
		return 0, ErrInvalidMinRechargeAmount
	}
	// A zero value means the field was omitted by an older frontend. Preserve
	// the previously stored value instead of resetting it during unrelated
	// settings updates.
	return GetMinRechargeAmountWithDB(db), nil
}

func formatRechargeSettingAmount(amount float64) string {
	return strconv.FormatFloat(util.RoundMoneyToDecimals(amount, util.RechargePrecision), 'f', util.RechargePrecision, 64)
}

func getPositiveInt64SystemSetting(db *gorm.DB, key string, defaultValue int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10))), 10, 64)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func resolveMaxAISearchBudget(db *gorm.DB, requested int, key string, defaultValue int) int {
	if requested > 0 {
		return normalizeAISearchBudgetForKey(requested, key)
	}
	return getPositiveIntSystemSetting(db, key, defaultValue)
}

// GetAISearchBudgetsWithDB returns the model-round and web-search limits used
// by every AI search entry point. Invalid or missing settings fall back to the
// safe defaults so a settings row cannot disable the search path accidentally.
func GetAISearchBudgetsWithDB(db *gorm.DB) (modelCalls int, searches int) {
	return getPositiveIntSystemSetting(db, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls),
		getPositiveIntSystemSetting(db, MaxAISearchesSettingKey, DefaultMaxAISearches)
}

// GetAISearchRetriesWithDB returns the total number of attempts allowed for
// each AI model round. The first request is included in this count.
func GetAISearchRetriesWithDB(db *gorm.DB) int {
	return getPositiveIntSystemSetting(db, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries)
}

// SetAISearchRetriesWithDB updates the total per-round attempt count. A value
// of zero is treated as omitted by callers and therefore keeps the current
// setting; invalid persisted values fall back to the default on read.
func SetAISearchRetriesWithDB(db *gorm.DB, requested int) error {
	if err := validateAISearchCountInputs(requested); err != nil {
		return err
	}
	retries := GetAISearchRetriesWithDB(db)
	if requested > 0 {
		retries = normalizeAISearchBudgetForKey(requested, MaxAISearchRetriesSettingKey)
	}
	return saveSystemSettingValue(db, MaxAISearchRetriesSettingKey, strconv.Itoa(retries), "AI 搜索每轮失败重试次数（含首次调用）")
}

// SetAISearchBudgetsWithDB updates only the AI search budget rows. It is used
// by the AI search configuration endpoint so that changing the AI account does
// not overwrite unrelated system settings.
func SetAISearchBudgetsWithDB(db *gorm.DB, requestedModelCalls int, requestedSearches int) error {
	if err := validateAISearchCountInputs(requestedModelCalls, requestedSearches); err != nil {
		return err
	}
	modelCalls, searches := GetAISearchBudgetsWithDB(db)
	if requestedModelCalls > 0 {
		modelCalls = normalizeAISearchBudgetForKey(requestedModelCalls, MaxAISearchModelCallsSettingKey)
	}
	if requestedSearches > 0 {
		searches = normalizeAISearchBudgetForKey(requestedSearches, MaxAISearchesSettingKey)
	}
	if err := saveSystemSettingValue(db, MaxAISearchModelCallsSettingKey, strconv.Itoa(modelCalls), "AI 搜索模型最大调用次数"); err != nil {
		return err
	}
	return saveSystemSettingValue(db, MaxAISearchesSettingKey, strconv.Itoa(searches), "AI 搜索最大搜索次数")
}

func GetAISearchPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultAISearchPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, AISearchPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultAISearchPricePerCall
	}
	return util.RoundMoney(price)
}

func SetAISearchPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("联网搜索单价必须是大于或等于 0 的有效金额")
	}
	price := util.RoundMoney(requested)
	return saveSystemSettingValue(db, AISearchPricePerCallSettingKey, strconv.FormatFloat(price, 'f', -1, 64), "联网搜索单价（元/次）")
}

func GetWebReadPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultWebReadPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, WebReadPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultWebReadPricePerCall
	}
	return util.RoundMoney(price)
}

func SetWebReadPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("网页读取单价必须是大于或等于 0 的有效金额")
	}
	return saveSystemSettingValue(db, WebReadPricePerCallSettingKey, strconv.FormatFloat(util.RoundMoney(requested), 'f', -1, 64), "网页读取单价（元/次）")
}

func GetImageOCRPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultImageOCRPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ImageOCRPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultImageOCRPricePerCall
	}
	return util.RoundMoney(price)
}

func SetImageOCRPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("图片 OCR 单价必须是大于或等于 0 的有效金额")
	}
	return saveSystemSettingValue(db, ImageOCRPricePerCallSettingKey, strconv.FormatFloat(util.RoundMoney(requested), 'f', -1, 64), "图片 OCR 单价（元/次）")
}

func GetTikuSearchPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultTikuSearchPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, TikuSearchPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultTikuSearchPricePerCall
	}
	return util.RoundMoney(price)
}

func SetTikuSearchPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("本地题库检索单价必须是大于或等于 0 的有效金额")
	}
	return saveSystemSettingValue(db, TikuSearchPricePerCallSettingKey, strconv.FormatFloat(util.RoundMoney(requested), 'f', -1, 64), "本地题库检索单价（元/次）")
}

type AIStagedTokenBillingSettings struct {
	Enabled          bool
	SmoothFactor     bool
	MultiplierPoints []AIStagedTokenMultiplierPoint
}

type AIStagedTokenMultiplierPoint struct {
	Tokens     int64   `json:"tokens"`
	Multiplier float64 `json:"multiplier"`
}

func (point AIStagedTokenMultiplierPoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Tokens     string  `json:"tokens"`
		Multiplier float64 `json:"multiplier"`
	}{Tokens: strconv.FormatInt(point.Tokens, 10), Multiplier: point.Multiplier})
}

func (point *AIStagedTokenMultiplierPoint) UnmarshalJSON(data []byte) error {
	var payload struct {
		Tokens     json.RawMessage `json:"tokens"`
		Multiplier float64         `json:"multiplier"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	tokenValue := strings.TrimSpace(string(payload.Tokens))
	if tokenValue == "" || tokenValue == "null" {
		return errors.New("阶段倍率 token 阈值必须是十进制整数")
	}
	if strings.HasPrefix(tokenValue, `"`) {
		var tokenString string
		if err := json.Unmarshal(payload.Tokens, &tokenString); err != nil {
			return err
		}
		tokenValue = strings.TrimSpace(tokenString)
	}
	tokens, err := strconv.ParseInt(tokenValue, 10, 64)
	if err != nil {
		return errors.New("阶段倍率 token 阈值超出 int64 整数范围")
	}
	point.Tokens = tokens
	point.Multiplier = payload.Multiplier
	return nil
}

func DefaultAIStagedTokenMultiplierPoints() []AIStagedTokenMultiplierPoint {
	return []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 4},
		{Tokens: 3000, Multiplier: 2},
		{Tokens: 10000, Multiplier: 1},
		{Tokens: 30000, Multiplier: 0.65},
		{Tokens: 100000, Multiplier: 0.3},
	}
}

func NormalizeAIStagedTokenMultiplierPoints(points []AIStagedTokenMultiplierPoint) ([]AIStagedTokenMultiplierPoint, error) {
	if len(points) < 1 {
		return nil, errors.New("阶段倍率价格表至少需要保留首个 0 token 节点")
	}
	normalized := make([]AIStagedTokenMultiplierPoint, len(points))
	copy(normalized, points)
	for index := range normalized {
		point := &normalized[index]
		if point.Tokens < 0 {
			return nil, errors.New("阶段倍率 token 阈值不能小于 0")
		}
		if math.IsNaN(point.Multiplier) || math.IsInf(point.Multiplier, 0) {
			return nil, errors.New("阶段倍率必须是有限数值")
		}
		point.Multiplier = RoundAIStagedTokenMultiplier(point.Multiplier)
		if index > 0 {
			previous := normalized[index-1]
			if point.Tokens <= previous.Tokens {
				return nil, errors.New("阶段倍率 token 阈值必须严格递增")
			}
			if point.Multiplier > previous.Multiplier {
				return nil, errors.New("阶段倍率必须随 token 数量增加而不升高")
			}
		}
	}
	if normalized[0].Tokens != 0 {
		return nil, errors.New("阶段倍率价格表首个节点的 token 阈值必须为 0")
	}
	return normalized, nil
}

func RoundAIStagedTokenMultiplier(multiplier float64) float64 {
	if math.Abs(multiplier) > math.MaxFloat64/1_000_000 {
		return multiplier
	}
	return util.RoundMoney(multiplier)
}

func cloneAIStagedTokenMultiplierPoints(points []AIStagedTokenMultiplierPoint) []AIStagedTokenMultiplierPoint {
	cloned := make([]AIStagedTokenMultiplierPoint, len(points))
	copy(cloned, points)
	return cloned
}

func GetAIStagedTokenBillingSettingsWithDB(db *gorm.DB) AIStagedTokenBillingSettings {
	points := DefaultAIStagedTokenMultiplierPoints()
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, AIStagedTokenMultiplierPointsSettingKey, ""))
	if raw != "" {
		var stored []AIStagedTokenMultiplierPoint
		if json.Unmarshal([]byte(raw), &stored) == nil {
			if normalized, err := NormalizeAIStagedTokenMultiplierPoints(stored); err == nil {
				points = normalized
			}
		}
	}
	return AIStagedTokenBillingSettings{
		Enabled:          getBoolSystemSetting(db, AIStagedTokenBillingEnabledSettingKey),
		SmoothFactor:     getBoolSystemSetting(db, SmoothAIStagedTokenMultiplierSettingKey),
		MultiplierPoints: cloneAIStagedTokenMultiplierPoints(points),
	}
}

func SetAIStagedTokenBillingSettingsWithDB(db *gorm.DB, enabled *bool, smoothFactor *bool, points ...*[]AIStagedTokenMultiplierPoint) error {
	if len(points) > 0 && points[0] != nil {
		normalized, err := NormalizeAIStagedTokenMultiplierPoints(*points[0])
		if err != nil {
			return err
		}
		payload, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		if err := saveSystemSettingValue(db, AIStagedTokenMultiplierPointsSettingKey, string(payload), "AI 单次模型调用阶段倍率价格表"); err != nil {
			return err
		}
	}
	if enabled != nil {
		if err := saveSystemSettingValue(db, AIStagedTokenBillingEnabledSettingKey, strconv.FormatBool(*enabled), "启用 AI 单次模型调用分阶段计费"); err != nil {
			return err
		}
	}
	if smoothFactor != nil {
		if err := saveSystemSettingValue(db, SmoothAIStagedTokenMultiplierSettingKey, strconv.FormatBool(*smoothFactor), "AI 阶段计费使用平滑倍率"); err != nil {
			return err
		}
	}
	return nil
}

func getBoolSystemSetting(db *gorm.DB, key string) bool {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return false
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, "false"))
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

// GetRechargePromotionEnabledWithDB 返回充值阶梯赠送开关。默认关闭：
// 缺表、缺记录、值非法都按关闭处理，不能让配置问题变成「悄悄开活动」。
func GetRechargePromotionEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, RechargePromotionEnabledSettingKey)
}

// GetDeviceLimitEnabledWithDB 返回账户设备池开关。默认关闭：
// 关闭时所有有效 Key 按原逻辑使用，不做设备登记与安装校验。
func GetDeviceLimitEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, DeviceLimitEnabledSettingKey)
}

// resolveBoolSettingInput 处理「省略 = 保持现值」的写入语义。
// GetReferralRebateEnabledWithDB 返回邀请返佣总开关，默认关闭。
//
// 缺表、缺记录、值非法都按关闭处理：配置读不出来时绝不能变成「悄悄开活动」。
func GetReferralRebateEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, ReferralRebateEnabledSettingKey)
}

// GetReferralRebateRateWithDB 返回返佣比例（0–1 之间的分数）。
//
// 默认 0；越界或非数值一律回落到 0，同样是为了「读不到就不要发钱」。
func GetReferralRebateRateWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultReferralRebateRate
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ReferralRebateRateSettingKey, "0"))
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultReferralRebateRate
	}
	normalized, err := normalizeReferralRebateRate(rate)
	if err != nil {
		return DefaultReferralRebateRate
	}
	return normalized
}

// normalizeReferralRebateRate 校验并规范化返佣比例：必须在 [0, 1) 内。
//
// 1（100%）不允许：返佣 100% 等于把实付本金原额返出，超出「佣金」语义，
// 也更容易被刷单套利；上界是开区间。
func normalizeReferralRebateRate(rate float64) (float64, error) {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate >= 1 {
		return 0, ErrInvalidReferralRebateRate
	}
	return rate, nil
}

// resolveReferralRebateRate 处理「省略 = 保持现值」的写入语义。
// nil 表示调用方没有提交该字段，此时读库现值（读不到就是 0）。
func resolveReferralRebateRate(db *gorm.DB, requested *float64) (float64, error) {
	if requested == nil {
		return GetReferralRebateRateWithDB(db), nil
	}
	return normalizeReferralRebateRate(*requested)
}

// resolveBoolSettingInput 处理「省略 = 保持现值」的布尔写入语义。
// 指针为 nil 表示调用方没有提交该字段；此时读库现值，读不到就是 false。
func resolveBoolSettingInput(db *gorm.DB, requested *bool, key string) bool {
	if requested != nil {
		return *requested
	}
	return getBoolSystemSetting(db, key)
}

// GetReviewRebateEnabledWithDB 返回评价返现总开关，默认关闭。
// 缺表、缺记录、值非法一律按关闭处理：配置读不出来时绝不能变成「悄悄开活动」。
func GetReviewRebateEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, ReviewRebateEnabledSettingKey)
}

// GetReviewRebateMaxClaimsPerUserWithDB 返回每账号累计提交次数上限，默认 3。
//
// 0 是合法值（表示不允许提交），只有「解析失败 / 负数」才回落默认。
func GetReviewRebateMaxClaimsPerUserWithDB(db *gorm.DB) int {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultReviewRebateMaxClaimsPerUser
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ReviewRebateMaxClaimsPerUserSettingKey,
		strconv.Itoa(DefaultReviewRebateMaxClaimsPerUser)))
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return DefaultReviewRebateMaxClaimsPerUser
	}
	return value
}

// GetReviewRebateMaxAmountPerUserWithDB 返回每账号累计发放金额上限，默认 5.04。
//
// 负数、NaN、Inf 一律回落默认；0 是合法值（表示不发放）。
func GetReviewRebateMaxAmountPerUserWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultReviewRebateMaxAmountPerUser
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ReviewRebateMaxAmountPerUserSettingKey,
		strconv.FormatFloat(DefaultReviewRebateMaxAmountPerUser, 'f', -1, 64)))
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultReviewRebateMaxAmountPerUser
	}
	normalized, err := normalizeReviewRebateAmount(amount)
	if err != nil {
		return DefaultReviewRebateMaxAmountPerUser
	}
	return normalized
}

// GetReviewRebateMinAgentConfidenceWithDB 返回 Agent 置信度下限，默认 0.6。
//
// 取值必须落在 [0,1]；越界或非数值回落 0.6 —— 这是「读不到就要求人工看」的保守方向：
// 门槛被读成 0 等于关掉人工复核，而读成 1 只是让所有单子都进人工，后者更安全。
func GetReviewRebateMinAgentConfidenceWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultReviewRebateMinAgentConfidence
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, ReviewRebateMinAgentConfidenceSettingKey,
		strconv.FormatFloat(DefaultReviewRebateMinAgentConfidence, 'f', -1, 64)))
	confidence, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultReviewRebateMinAgentConfidence
	}
	normalized, err := normalizeReviewRebateConfidence(confidence)
	if err != nil {
		return DefaultReviewRebateMinAgentConfidence
	}
	return normalized
}

// normalizeReviewRebateAmount 校验并规范化赠金金额：必须非负且有限。
func normalizeReviewRebateAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrInvalidReviewRebateAmount
	}
	return util.RoundMoney(amount), nil
}

// normalizeReviewRebateConfidence 校验置信度：必须落在 [0,1]。
func normalizeReviewRebateConfidence(confidence float64) (float64, error) {
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return 0, ErrInvalidReviewRebateConfidence
	}
	return util.RoundMoney(confidence), nil
}

// resolveReviewRebateMaxClaims 处理「省略 = 保持现值」的次数上限写入语义。
func resolveReviewRebateMaxClaims(db *gorm.DB, requested *int) (int, error) {
	if requested == nil {
		return GetReviewRebateMaxClaimsPerUserWithDB(db), nil
	}
	if *requested < 0 {
		return 0, ErrInvalidReviewRebateMaxClaims
	}
	return *requested, nil
}

// resolveReviewRebateMaxAmount 处理「省略 = 保持现值」的金额上限写入语义。
func resolveReviewRebateMaxAmount(db *gorm.DB, requested *float64) (float64, error) {
	if requested == nil {
		return GetReviewRebateMaxAmountPerUserWithDB(db), nil
	}
	return normalizeReviewRebateAmount(*requested)
}

// resolveReviewRebateMinConfidence 处理「省略 = 保持现值」的置信度门槛写入语义。
func resolveReviewRebateMinConfidence(db *gorm.DB, requested *float64) (float64, error) {
	if requested == nil {
		return GetReviewRebateMinAgentConfidenceWithDB(db), nil
	}
	return normalizeReviewRebateConfidence(*requested)
}

func resolveWebReadPrice(db *gorm.DB, requested float64) float64 {
	if requested > 0 {
		return util.RoundMoney(requested)
	}
	return GetWebReadPricePerCallWithDB(db)
}

func getPositiveIntSystemSetting(db *gorm.DB, key string, defaultValue int) int {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.Itoa(defaultValue)))
	value, err := strconv.Atoi(raw)
	if err != nil || !isValidAISearchCount(value) {
		return defaultValue
	}
	return normalizeAISearchBudgetForKey(value, key)
}

func normalizeAISearchBudgetForKey(value int, key string) int {
	switch key {
	case MaxAISearchModelCallsSettingKey, MaxAISearchesSettingKey, MaxAIWebReadsSettingKey:
		return min(value, MaxAllowedAISearchModelCalls)
	case MaxAISearchRetriesSettingKey:
		return min(value, MaxAllowedAISearchRetries)
	}
	return value
}

func isValidAISearchCount(value int) bool {
	return value > 0 && int64(value) <= MaxAISearchCountSafeInteger
}

func validateAISearchCountInputs(values ...int) error {
	for _, value := range values {
		// Zero is the legacy omitted-field value for non-pointer settings.
		if value != 0 && !isValidAISearchCount(value) {
			return ErrInvalidAISearchCount
		}
	}
	return nil
}

func getSystemSettingStringWithDefault(db *gorm.DB, key string, defaultValue string) string {
	setting, exists, err := getSystemSettingByKey(db, key)
	if err != nil || !exists {
		return defaultValue
	}
	value := strings.TrimSpace(setting.Value)
	if value == "" {
		return defaultValue
	}
	return value
}

func saveSystemSettingValue(db *gorm.DB, key string, value string, remark string) error {
	setting, _, err := getSystemSettingByKey(db, key)
	if err != nil {
		return err
	}
	if setting.Key == "" {
		setting.Key = key
		setting.Remark = remark
	}
	setting.Value = value
	freshDB := db.Session(&gorm.Session{NewDB: true})
	return freshDB.Save(&setting).Error
}

func normalizePaymentProvider(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), DefaultPaymentProvider) {
		return DefaultPaymentProvider
	}
	return DefaultPaymentProvider
}

func normalizePaymentMethod(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", DefaultPaymentMethod:
		return DefaultPaymentMethod
	case "wechat", paymentMethodWXPay:
		return paymentMethodWXPay
	default:
		return normalized
	}
}

func defaultString(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func normalizeSiteName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultSiteName
	}
	return trimmed
}

func trimTrailingSlash(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func normalizeCaptchaProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", DefaultCaptchaProvider:
		return DefaultCaptchaProvider
	case "local":
		return "local"
	default:
		return DefaultCaptchaProvider
	}
}

func normalizeCaptchaType(value string) string {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "随机":
		return "RANDOM"
	case "滑块", "滑块验证", "滑块验证码":
		return "SLIDER"
	case "旋转", "旋转验证", "旋转验证码":
		return "ROTATE"
	case "滑动还原", "滑动还原验证码":
		return "CONCAT"
	case "文字点选", "点选", "点选验证码", "文字点选验证码":
		return "WORD_IMAGE_CLICK"
	}
	normalized := strings.ToUpper(strings.TrimSpace(value))
	switch normalized {
	case "RANDOM", "SLIDER", "ROTATE", "CONCAT", "WORD_IMAGE_CLICK":
		return normalized
	default:
		return DefaultCaptchaType
	}
}

func normalizeSMSBaoCredentialType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "password" {
		return "password"
	}
	return DefaultSMSBaoCredentialType
}

func normalizeSMSBaoRegionMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "domestic", "international":
		return normalized
	default:
		return DefaultSMSBaoRegionMode
	}
}

func normalizeEPayVersion(value string) string {
	return DefaultEPayVersion
}

func getNormalizedWechatLoginSettingsWithDB(db *gorm.DB) SystemSettingDetail {
	wechatLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatLoginEnabledSettingKey, "false")), "true")
	legacyScanDefault := "false"
	if wechatLoginEnabled {
		legacyScanDefault = "true"
	}
	wechatScanLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatScanLoginEnabledSettingKey, legacyScanDefault)), "true")
	wechatCodeLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatCodeLoginEnabledSettingKey, "false")), "true")
	if !wechatScanLoginEnabled && !wechatCodeLoginEnabled {
		wechatLoginEnabled = false
	}
	if !wechatLoginEnabled {
		wechatScanLoginEnabled = false
		wechatCodeLoginEnabled = false
	}
	defaultMode := normalizeEnabledWechatLoginMode(
		getSystemSettingStringWithDefault(db, WechatDefaultLoginModeSettingKey, DefaultWechatLoginMode),
		wechatScanLoginEnabled,
		wechatCodeLoginEnabled,
	)
	if defaultMode == "" {
		defaultMode = DefaultWechatLoginMode
	}
	return SystemSettingDetail{
		WechatLoginEnabled:        wechatLoginEnabled,
		WechatScanLoginEnabled:    wechatScanLoginEnabled,
		WechatCodeLoginEnabled:    wechatCodeLoginEnabled,
		WechatDefaultLoginMode:    defaultMode,
		WechatCodeStaticQRCodeURL: strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatCodeStaticQRCodeURLSettingKey, "")),
	}
}

func normalizeWechatLoginMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", wechatLoginModeScan, "qrcode", "qr", "scan_qr":
		return wechatLoginModeScan
	case wechatLoginModeCode, "captcha", "verification_code":
		return wechatLoginModeCode
	default:
		return DefaultWechatLoginMode
	}
}

func normalizeEnabledWechatLoginMode(value string, scanEnabled bool, codeEnabled bool) string {
	normalized := normalizeWechatLoginMode(value)
	if normalized == wechatLoginModeCode && codeEnabled {
		return wechatLoginModeCode
	}
	if normalized == wechatLoginModeScan && scanEnabled {
		return wechatLoginModeScan
	}
	if scanEnabled {
		return wechatLoginModeScan
	}
	if codeEnabled {
		return wechatLoginModeCode
	}
	return ""
}

func getSystemSettingByKey(db *gorm.DB, key string) (entity.SystemSetting, bool, error) {
	var setting entity.SystemSetting
	freshDB := db.Session(&gorm.Session{NewDB: true})
	if err := freshDB.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).First(&setting).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.SystemSetting{}, false, nil
		}
		return entity.SystemSetting{}, false, err
	}
	return setting, true, nil
}
