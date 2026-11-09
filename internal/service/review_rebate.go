package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 评价返现的哨兵错误。
//
// 与 referral.go 的分工一致：这些是**预期业务结果**（开关关闭、超上限、重复提交），
// 调用方按 4xx 处理即可，不要写 error 日志。
var (
	// ErrReviewRebateDisabled 总开关关闭（或活动行被禁用）：入口直接拒绝。
	ErrReviewRebateDisabled = errors.New("评价返现未启用")
	// ErrReviewRebateProgramNotFound 活动不存在。
	ErrReviewRebateProgramNotFound = errors.New("评价返现活动不存在")
	// ErrReviewRebateClaimNotFound 提交单不存在。
	ErrReviewRebateClaimNotFound = errors.New("评价返现提交记录不存在")
	// ErrReviewRebateRuleNotFound 规则不存在。
	ErrReviewRebateRuleNotFound = errors.New("评价返现规则不存在")
	// ErrReviewRebateDuplicate 同一账号同一评价（canonical_claim_id）重复提交。
	ErrReviewRebateDuplicate = errors.New("该评价已提交过，请勿重复提交")
	// ErrReviewRebateLimitReached 超过每账号次数或金额上限。
	ErrReviewRebateLimitReached = errors.New("已达评价返现领取上限")
	// ErrReviewRebateClaimState 当前状态不允许该操作（例如已批准的再拒）。
	ErrReviewRebateClaimState = errors.New("提交记录状态不允许该操作")
	// ErrReviewRebateSchemaUnavailable 表/列还没迁移到位。
	ErrReviewRebateSchemaUnavailable = errors.New("评价返现数据表未就绪")
	// ErrReviewRebateInvalidInput 入参本身不合法（星数越界、区间颠倒等）。
	ErrReviewRebateInvalidInput = errors.New("评价返现参数不合法")
	// ErrReviewRebateNoRuleMatched 规则表未命中（金额为 0）。
	ErrReviewRebateNoRuleMatched = errors.New("没有匹配的返现规则")
	// ErrReviewRebateReviewerInvalid 决策人不是有效管理员账户。
	ErrReviewRebateReviewerInvalid = errors.New("复核人无效")
	// ErrReviewRebateScreenshotInvalid 截图不是真实的 PNG/JPEG（服务端解码失败）。
	ErrReviewRebateScreenshotInvalid = errors.New("评价截图不是有效的 PNG/JPEG 图片")
	// ErrReviewRebateScreenshotTooLarge 截图超过 5 MB。
	ErrReviewRebateScreenshotTooLarge = errors.New("评价截图超过允许大小")
)

const (
	// reviewRebateStarsMin/Max 是星数取值域（1–5）。
	reviewRebateStarsMin = 1
	reviewRebateStarsMax = 5
	// reviewRebateContentScoreMin/Max 是内容分取值域（1–10）。
	reviewRebateContentScoreMin = 1
	reviewRebateContentScoreMax = 10
	// reviewRebateDefaultPageSize 是列表默认每页条数。
	reviewRebateDefaultPageSize = 20
	// reviewRebateMaxPageSize 是列表每页条数硬上限。
	reviewRebateMaxPageSize = 100
)

// ReviewRebateScore 是 Agent 打分的结构化输出。
//
// 它是**建议**，不是发放指令：ScoreReviewRebateClaimWithDB 只把它写进 claim，
// 真正发钱必须走 DecideReviewRebateClaimWithDB（人工复核）。
type ReviewRebateScore struct {
	Stars        int     `json:"stars"`        // 1–5
	ContentScore int     `json:"contentScore"` // 1=很差，10=很好
	Confidence   float64 `json:"confidence"`   // 0–1，低于活动门槛 → 强制人工
	Rationale    string  `json:"rationale"`    // 理由（给管理员看的可读解释）
	// Scorer 标记分数的来源：agent=真实模型，stub=未配置模型时的占位实现。
	//
	// 这个字段是给管理端看的：占位实现的分数由截图摘要派生，**不构成任何核验**，
	// 界面上必须能与真实打分区分开，否则运营会把「哈希算出来的 5 星」当成模型结论。
	Scorer string `json:"scorer,omitempty"`
}

// ReviewRebateScorer 是打分器接口。
//
// 两个实现：
//   - agentReviewRebateScorer（review_rebate_scorer.go）：真实模型打分，读截图 + 活动配置
//     调既有模型客户端，按配置的地址/密钥/模型运行；
//   - deterministicReviewRebateScorer：未配置模型时的占位实现，分数由截图摘要派生。
//
// 用 NewReviewRebateScorerWithDB 按配置选择，不要直接引用某个实现：
// 部署侧只改 AI 配置就该切换，不该改代码。
type ReviewRebateScorer interface {
	Score(claim entity.ReviewRebateClaim) (ReviewRebateScore, error)
}

// deterministicReviewRebateScorer 是**占位实现**，输出完全由截图 SHA-256 决定。
//
// 为什么必须确定性：单测要能钉住「规则表 × Agent 输出 → 金额」这条链路，
//
//	随机或依赖外部服务的打分会让测试无法复现。
//
// 映射规则（写死在这里，改它等于改业务语义）：
//   - 摘要前 2 个十六进制字符（0–255）决定星数：1 + (b % 5) → 1..5
//   - 接下来的 2 个字符决定内容分：1 + (b % 10) → 1..10
//   - 置信度固定 1.0：占位实现不假装有「把握」，真实模型才会给中间值
//   - 摘要为空（历史行/未落盘）→ 返回无法判定，confidence=0，强制人工
//
// ⚠️ 它的分数**不代表任何核验**：既没有读截图内容，也没有比对公开评价。
// 因此每条输出都带 Scorer="stub"，rationale 里也写明是占位打分器 ——
// 管理端据此判断「这条要不要真的看截图」，而不是直接信这个分数。
type deterministicReviewRebateScorer struct{}

// DefaultReviewRebateScorer 是未配置真实模型时使用的打分器。
var DefaultReviewRebateScorer ReviewRebateScorer = deterministicReviewRebateScorer{}

func (deterministicReviewRebateScorer) Score(claim entity.ReviewRebateClaim) (ReviewRebateScore, error) {
	return deterministicReviewRebateScore(claim)
}

// NewDeterministicReviewRebateScorer 显式构造占位打分器（测试用）。
func NewDeterministicReviewRebateScorer() ReviewRebateScorer {
	return deterministicReviewRebateScorer{}
}

// deterministicReviewRebateScore 是占位打分的纯函数版本，便于单测直接断言。
//
// ⚠️ 占位实现：真实打分走 agentReviewRebateScorer（review_rebate_scorer.go）。
// 输出带 Scorer="stub"，让管理端能区分「真打分」与「按哈希派生」。
func deterministicReviewRebateScore(claim entity.ReviewRebateClaim) (ReviewRebateScore, error) {
	digest := strings.ToLower(strings.TrimSpace(claim.ScreenshotSHA256))
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) < 4 {
		return ReviewRebateScore{
			Stars:        0,
			ContentScore: 0,
			Confidence:   0,
			Rationale:    "占位打分器：截图摘要不可用，无法判定（强制人工复核）",
			Scorer:       ReviewRebateScorerKindStub,
		}, nil
	}
	stars := reviewRebateStarsMin + int(raw[0])%reviewRebateStarsMax
	contentScore := reviewRebateContentScoreMin + int(raw[1])%reviewRebateContentScoreMax
	return ReviewRebateScore{
		Stars:        stars,
		ContentScore: contentScore,
		Confidence:   1,
		Rationale: fmt.Sprintf(
			"占位打分器（未配置模型服务）：按截图摘要派生 stars=%d contentScore=%d confidence=1；"+
				"未读取截图内容、未比对公开评价，不代表任何核验结论，请管理员以截图为准",
			stars, contentScore),
		Scorer: ReviewRebateScorerKindStub,
	}, nil
}

// reviewRebateSession 返回一个「干净语句」的会话。
//
// 与 referralSession 同因：评价返现的入口可能拿到 `dao.User.UnderlyingDB()` 之类的
// 派生会话，Statement 里还挂着上一个模型；NewDB 清掉继承的模型，同时保留事务连接。
func reviewRebateSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// ReviewRebateSchemaAvailable 判断评价返现相关表/列是否已就绪。
//
// 存在的意义：请求链路不能因为迁移还没跑过（老库升级、单测里的最小 schema）
// 就整体 500。结构没准备好时一律降级为「功能不可用」（入口返回业务错误），
// 绝不 panic —— 这也是 tests 里「表未迁移时降级不 panic」的判据。
func ReviewRebateSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	for _, table := range []string{
		entity.TableNameReviewRebateProgram,
		entity.TableNameReviewRebateRule,
		entity.TableNameReviewRebateClaim,
		entity.TableNameReviewRebateAward,
	} {
		if !db.Migrator().HasTable(table) {
			return false
		}
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameUser, "gift_balance")
}

// RequiresReviewRebateSchema 是给 controller 的便捷判据（语义更直白）。
func RequiresReviewRebateSchema(db *gorm.DB) bool { return ReviewRebateSchemaAvailable(db) }

// MatchReviewRebateRule 按星数与内容分匹配规则表。
//
// 匹配逻辑（16-*.md §1.2）：
//  1. 筛出 Enabled 且 stars ∈ [MinStars,MaxStars] 且 contentScore ∈ [MinContentScore,MaxContentScore]
//  2. 按 Priority **降序**取第一条；Priority 相同时按 ID 升序（后插入的不会意外盖住先配置的，
//     这样「同等优先级」是可预测的，不会因为插入顺序抖动而改变金额）
//  3. 都不命中返回 nil
//
// 注意：本函数**不**校验 stars/contentScore 是否在 1–5 / 1–10 内 ——
// 越界输入应该由调用方先拒绝，而不是靠「恰好没规则命中」来兜（那会把参数错误
// 静默变成「金额 0」，运营查不出问题）。
func MatchReviewRebateRule(rules []entity.ReviewRebateRule, stars int, contentScore int) *entity.ReviewRebateRule {
	matched := make([]entity.ReviewRebateRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if stars < rule.MinStars || stars > rule.MaxStars {
			continue
		}
		if contentScore < rule.MinContentScore || contentScore > rule.MaxContentScore {
			continue
		}
		matched = append(matched, rule)
	}
	if len(matched) == 0 {
		return nil
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority > matched[j].Priority
		}
		return matched[i].ID < matched[j].ID
	})
	best := matched[0]
	return &best
}

// validateReviewRebateRule 校验一条规则的取值域与金额。
func validateReviewRebateRule(rule entity.ReviewRebateRule) error {
	if rule.ProgramID <= 0 {
		return fmt.Errorf("%w: 活动 ID 必须大于 0", ErrReviewRebateInvalidInput)
	}
	if rule.MinStars < reviewRebateStarsMin || rule.MaxStars > reviewRebateStarsMax || rule.MinStars > rule.MaxStars {
		return fmt.Errorf("%w: 星数区间必须在 %d–%d 且下限不大于上限",
			ErrReviewRebateInvalidInput, reviewRebateStarsMin, reviewRebateStarsMax)
	}
	if rule.MinContentScore < reviewRebateContentScoreMin || rule.MaxContentScore > reviewRebateContentScoreMax ||
		rule.MinContentScore > rule.MaxContentScore {
		return fmt.Errorf("%w: 内容分区间必须在 %d–%d 且下限不大于上限",
			ErrReviewRebateInvalidInput, reviewRebateContentScoreMin, reviewRebateContentScoreMax)
	}
	if _, err := normalizeReviewRebateAmount(rule.Amount); err != nil {
		return err
	}
	return nil
}

// validateReviewRebateProgram 校验活动配置的取值域。
func validateReviewRebateProgram(program entity.ReviewRebateProgram) error {
	if strings.TrimSpace(program.Name) == "" {
		return fmt.Errorf("%w: 活动名称不能为空", ErrReviewRebateInvalidInput)
	}
	if program.MaxClaimsPerUser < 0 {
		return fmt.Errorf("%w: 每账号次数上限不能为负", ErrReviewRebateInvalidInput)
	}
	if _, err := normalizeReviewRebateAmount(program.MaxAmountPerUser); err != nil {
		return err
	}
	if _, err := normalizeReviewRebateConfidence(program.MinAgentConfidence); err != nil {
		return err
	}
	return nil
}

// normalizeReviewRebateClaimStars 校验用户自填星数：0 表示未填，其余必须 1–5。
func normalizeReviewRebateClaimStars(stars int) (int, error) {
	if stars == 0 {
		return 0, nil
	}
	if stars < reviewRebateStarsMin || stars > reviewRebateStarsMax {
		return 0, fmt.Errorf("%w: 星数必须在 %d–%d", ErrReviewRebateInvalidInput, reviewRebateStarsMin, reviewRebateStarsMax)
	}
	return stars, nil
}

// BuildReviewRebateCanonicalClaimID 计算账务去重键。
//
// 口径（04 稿 10.4.4）：
//   - 参与摘要的是「用户能改的描述性内容」：用户自填星数（0=未填）、评价链接、
//     以及截图 SHA-256 —— 三者都进 SHA-256 后取前 32 个十六进制字符；
//   - 它**不是**作者归属证明，也不可能稳定覆盖「同一评价换个链接再传一遍」，
//     因此下游还必须有次数上限与人工复核兜底；
//   - 唯一索引 (program_id, user_id, canonical_claim_id) 保证同账号同评价只落一行，
//     换活动（program 换版）是**新行** —— 这正是「换版不重置已领次数」的相反面：
//     次数统计按 user 跨活动累计，去重按 (program, user, canonical) 计。
func BuildReviewRebateCanonicalClaimID(claimedStars int, screenshotSHA256 string, reviewURL string) string {
	payload := fmt.Sprintf("stars=%d|sha256=%s|url=%s",
		claimedStars,
		strings.ToLower(strings.TrimSpace(screenshotSHA256)),
		normalizeReviewRebateClaimURL(reviewURL))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:32]
}

// normalizeReviewRebateClaimURL 归一化用户填写的评价链接：去空白、转小写、去尾斜杠。
func normalizeReviewRebateClaimURL(value string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), "/")
}

// GetReviewRebateProgramWithDB 取「当前生效」的活动（用户端入口用）。
//
// 选择口径：开关开启 + program.Enabled 的最新一行（ID 最大）。
// 一行都没有 → ErrReviewRebateProgramNotFound（不是 500）。
func GetReviewRebateProgramWithDB(db *gorm.DB) (entity.ReviewRebateProgram, error) {
	if !GetReviewRebateEnabledWithDB(db) {
		return entity.ReviewRebateProgram{}, ErrReviewRebateDisabled
	}
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateProgram{}, ErrReviewRebateSchemaUnavailable
	}
	var program entity.ReviewRebateProgram
	err := reviewRebateSession(db).Model(&entity.ReviewRebateProgram{}).
		Where("enabled = ?", true).Order("id desc").Take(&program).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ReviewRebateProgram{}, ErrReviewRebateProgramNotFound
		}
		return entity.ReviewRebateProgram{}, err
	}
	return program, nil
}

// ListReviewRebateProgramsWithDB 返回全部活动（管理员），按 ID 升序。
func ListReviewRebateProgramsWithDB(db *gorm.DB) ([]entity.ReviewRebateProgram, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return nil, ErrReviewRebateSchemaUnavailable
	}
	programs := make([]entity.ReviewRebateProgram, 0)
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateProgram{}).
		Order("id asc").Find(&programs).Error; err != nil {
		return nil, err
	}
	return programs, nil
}

// ReviewRebateProgramUpdate 是活动配置的部分更新入参（指针语义：nil = 保持现值）。
type ReviewRebateProgramUpdate struct {
	Name               *string
	StoreURL           *string
	ExtensionID        *string
	Enabled            *bool
	MaxClaimsPerUser   *int
	MaxAmountPerUser   *float64
	MinAgentConfidence *float64
	TutorialVersionID  *int32
	PolicyVersion      *string
}

// UpdateReviewRebateProgramWithDB 部分更新活动配置。
//
// 指针语义与系统设置一致：nil = 本次没有提交该字段 = 保持现值。
// 「没有活动行」时不做隐式创建 —— 隐式创建会让运营在管理页上「改一个 id」时
// 意外造出一条默认配置，不如明确报 404 让人先建。
func UpdateReviewRebateProgramWithDB(db *gorm.DB, id int32, update ReviewRebateProgramUpdate) (entity.ReviewRebateProgram, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateProgram{}, ErrReviewRebateSchemaUnavailable
	}
	if id <= 0 {
		return entity.ReviewRebateProgram{}, ErrReviewRebateProgramNotFound
	}
	var program entity.ReviewRebateProgram
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateProgram{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&program).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReviewRebateProgramNotFound
			}
			return err
		}
		updates := map[string]any{}
		if update.Name != nil {
			name := strings.TrimSpace(*update.Name)
			if name == "" {
				return fmt.Errorf("%w: 活动名称不能为空", ErrReviewRebateInvalidInput)
			}
			updates["name"] = name
		}
		if update.StoreURL != nil {
			updates["store_url"] = strings.TrimSpace(*update.StoreURL)
		}
		if update.ExtensionID != nil {
			updates["extension_id"] = strings.TrimSpace(*update.ExtensionID)
		}
		if update.Enabled != nil {
			updates["enabled"] = *update.Enabled
		}
		if update.MaxClaimsPerUser != nil {
			if *update.MaxClaimsPerUser < 0 {
				return fmt.Errorf("%w: 每账号次数上限不能为负", ErrReviewRebateInvalidInput)
			}
			updates["max_claims_per_user"] = *update.MaxClaimsPerUser
		}
		if update.MaxAmountPerUser != nil {
			amount, err := normalizeReviewRebateAmount(*update.MaxAmountPerUser)
			if err != nil {
				return err
			}
			updates["max_amount_per_user"] = amount
		}
		if update.MinAgentConfidence != nil {
			confidence, err := normalizeReviewRebateConfidence(*update.MinAgentConfidence)
			if err != nil {
				return err
			}
			updates["min_agent_confidence"] = confidence
		}
		if update.TutorialVersionID != nil {
			if *update.TutorialVersionID < 0 {
				return fmt.Errorf("%w: 教程版本 ID 不能为负", ErrReviewRebateInvalidInput)
			}
			updates["tutorial_version_id"] = *update.TutorialVersionID
		}
		if update.PolicyVersion != nil {
			updates["policy_version"] = strings.TrimSpace(*update.PolicyVersion)
		}
		if len(updates) == 0 {
			return nil
		}
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateProgram{}).
			Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return reviewRebateSession(tx).Model(&entity.ReviewRebateProgram{}).Where("id = ?", id).Take(&program).Error
	})
	if err != nil {
		return entity.ReviewRebateProgram{}, err
	}
	return program, nil
}

// CreateReviewRebateProgramWithDB 新建活动（管理初始化用；幂等性由调用方保证）。
func CreateReviewRebateProgramWithDB(db *gorm.DB, program entity.ReviewRebateProgram) (entity.ReviewRebateProgram, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateProgram{}, ErrReviewRebateSchemaUnavailable
	}
	if err := validateReviewRebateProgram(program); err != nil {
		return entity.ReviewRebateProgram{}, err
	}
	program.Name = strings.TrimSpace(program.Name)
	program.StoreURL = strings.TrimSpace(program.StoreURL)
	program.ExtensionID = strings.TrimSpace(program.ExtensionID)
	program.PolicyVersion = strings.TrimSpace(program.PolicyVersion)
	if program.PolicyVersion == "" {
		program.PolicyVersion = ReviewRebatePolicyVersion
	}
	program.MaxAmountPerUser = util.RoundMoney(program.MaxAmountPerUser)
	program.MinAgentConfidence = util.RoundMoney(program.MinAgentConfidence)
	if err := reviewRebateSession(db).Create(&program).Error; err != nil {
		return entity.ReviewRebateProgram{}, err
	}
	return program, nil
}

// ListReviewRebateRulesWithDB 返回规则表。
//
// programID <= 0 表示「不过滤活动」（管理页全量）；用户端一律传具体 programID。
// 排序：Priority 降序、ID 升序 —— 与 MatchReviewRebateRule 的决断顺序一致，
// 管理页看到的第一条就是会先生效的那条。
func ListReviewRebateRulesWithDB(db *gorm.DB, programID int32) ([]entity.ReviewRebateRule, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return nil, ErrReviewRebateSchemaUnavailable
	}
	rules := make([]entity.ReviewRebateRule, 0)
	query := reviewRebateSession(db).Model(&entity.ReviewRebateRule{})
	if programID > 0 {
		query = query.Where("program_id = ?", programID)
	}
	if err := query.Order("priority desc, id asc").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// CreateReviewRebateRuleWithDB 新建一条规则。
func CreateReviewRebateRuleWithDB(db *gorm.DB, rule entity.ReviewRebateRule) (entity.ReviewRebateRule, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateRule{}, ErrReviewRebateSchemaUnavailable
	}
	if err := validateReviewRebateRule(rule); err != nil {
		return entity.ReviewRebateRule{}, err
	}
	var programCount int64
	if err := reviewRebateSession(db).Model(&entity.ReviewRebateProgram{}).
		Where("id = ?", rule.ProgramID).Count(&programCount).Error; err != nil {
		return entity.ReviewRebateRule{}, err
	}
	if programCount == 0 {
		return entity.ReviewRebateRule{}, ErrReviewRebateProgramNotFound
	}
	rule.Remark = strings.TrimSpace(rule.Remark)
	rule.Amount = util.RoundMoney(rule.Amount)
	if err := reviewRebateSession(db).Create(&rule).Error; err != nil {
		return entity.ReviewRebateRule{}, err
	}
	return rule, nil
}

// ReviewRebateRuleUpdate 是规则的部分更新入参（指针语义：nil = 保持现值）。
type ReviewRebateRuleUpdate struct {
	ProgramID       *int32
	MinStars        *int
	MaxStars        *int
	MinContentScore *int
	MaxContentScore *int
	Amount          *float64
	Priority        *int
	Enabled         *bool
	Remark          *string
}

// UpdateReviewRebateRuleWithDB 部分更新一条规则。
//
// 合并后的整条规则必须通过校验（不只是改动的字段）：只改 MinStars 时，
// 也可能与既有的 MaxStars 形成「下限大于上限」，这种跨字段错误必须在写入前拦下。
func UpdateReviewRebateRuleWithDB(db *gorm.DB, id int32, update ReviewRebateRuleUpdate) (entity.ReviewRebateRule, error) {
	if !ReviewRebateSchemaAvailable(db) {
		return entity.ReviewRebateRule{}, ErrReviewRebateSchemaUnavailable
	}
	if id <= 0 {
		return entity.ReviewRebateRule{}, ErrReviewRebateRuleNotFound
	}
	var rule entity.ReviewRebateRule
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateRule{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&rule).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReviewRebateRuleNotFound
			}
			return err
		}
		merged := rule
		if update.ProgramID != nil {
			merged.ProgramID = *update.ProgramID
		}
		if update.MinStars != nil {
			merged.MinStars = *update.MinStars
		}
		if update.MaxStars != nil {
			merged.MaxStars = *update.MaxStars
		}
		if update.MinContentScore != nil {
			merged.MinContentScore = *update.MinContentScore
		}
		if update.MaxContentScore != nil {
			merged.MaxContentScore = *update.MaxContentScore
		}
		if update.Amount != nil {
			merged.Amount = *update.Amount
		}
		if update.Priority != nil {
			merged.Priority = *update.Priority
		}
		if update.Enabled != nil {
			merged.Enabled = *update.Enabled
		}
		if update.Remark != nil {
			merged.Remark = *update.Remark
		}
		if err := validateReviewRebateRule(merged); err != nil {
			return err
		}
		if err := reviewRebateSession(tx).Model(&entity.ReviewRebateRule{}).Where("id = ?", id).Updates(map[string]any{
			"program_id":        merged.ProgramID,
			"min_stars":         merged.MinStars,
			"max_stars":         merged.MaxStars,
			"min_content_score": merged.MinContentScore,
			"max_content_score": merged.MaxContentScore,
			"amount":            util.RoundMoney(merged.Amount),
			"priority":          merged.Priority,
			"enabled":           merged.Enabled,
			"remark":            strings.TrimSpace(merged.Remark),
		}).Error; err != nil {
			return err
		}
		return reviewRebateSession(tx).Model(&entity.ReviewRebateRule{}).Where("id = ?", id).Take(&rule).Error
	})
	if err != nil {
		return entity.ReviewRebateRule{}, err
	}
	return rule, nil
}

// DeleteReviewRebateRuleWithDB 删除一条规则。
//
// 真删而不是软删：规则是纯配置，历史 claim 已经把命中的 rule_id 与金额快照下来，
// 删掉规则不影响对账（MatchedRuleID 指向不存在的行是允许的）。
func DeleteReviewRebateRuleWithDB(db *gorm.DB, id int32) error {
	if !ReviewRebateSchemaAvailable(db) {
		return ErrReviewRebateSchemaUnavailable
	}
	if id <= 0 {
		return ErrReviewRebateRuleNotFound
	}
	result := reviewRebateSession(db).Where("id = ?", id).Delete(&entity.ReviewRebateRule{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrReviewRebateRuleNotFound
	}
	return nil
}
