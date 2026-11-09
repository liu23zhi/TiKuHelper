package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	// 注册 PNG/JPEG 解码器：image.DecodeConfig 只认已注册的格式。
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 打分器来源标记。
//
// 为什么要落库：管理端必须能一眼看出「这条分数是模型给的，还是占位实现按哈希算的」。
// 没配置模型时占位实现仍会给出一个确定性的分数（单测与历史行为都依赖它），
// 如果界面上分不清，运营会把它当成真实核验结果去批准 —— 那才是真正的风险。
const (
	// ReviewRebateScorerKindStub 表示未配置模型，分数由截图摘要派生（不作为核验依据）。
	ReviewRebateScorerKindStub = "stub"
	// ReviewRebateScorerKindAgent 表示由真实模型根据截图打分。
	ReviewRebateScorerKindAgent = "agent"
)

const (
	// reviewRebateScreenshotDataRoot 是截图私有目录的父目录（相对工作目录）。
	//
	// 与 controller 落盘路径必须一致：controller 用 filepath.Join("data", <相对路径>)，
	// 打分器反过来用同一个根读回文件。改这里等于改两端的契约，见 ReviewRebateScreenshotAbsolutePath。
	reviewRebateScreenshotDataRoot = "data"
	// ReviewRebateScreenshotDirName 是截图私有目录名（data/ 下）。
	ReviewRebateScreenshotDirName = "review-rebate"
	// ReviewRebateScreenshotMaxBytes 是单张截图上限（≤5 MB，16-*.md §1.1）。
	ReviewRebateScreenshotMaxBytes = int64(5 << 20)
	// ReviewRebateScoringTimeout 是单次打分的总预算。
	//
	// 打分是管理员触发的同步操作，最长等待 2 分钟：超时一律按「打分失败」处理，
	// 绝不降级成「给个高分先过」。模型客户端内部另有 180s 上限与自身重试预算，
	// 这里是更紧的外层约束。
	ReviewRebateScoringTimeout = 2 * time.Minute
)

// ErrReviewRebateScoringFailed 表示自动打分没有拿到可信结果（超时/网络/模型报错/输出不可解析）。
//
// 语义上它是**预期失败**：调用方必须保留 claim 原状态（submitted）并提示人工处理，
// 不能因为「打分失败了」就写入任何分数或金额。
var ErrReviewRebateScoringFailed = errors.New("评价返现自动打分失败")

// ReviewRebateScoringImage 是打分器提交给模型的图片（已解码校验过的原始字节）。
type ReviewRebateScoringImage struct {
	Name        string
	ContentType string
	Body        []byte
}

// ReviewRebateModelCompleter 是打分器对模型客户端的最小依赖面。
//
// 抽成接口的原因与 ReviewRebateScorer 相同：单测可以注入一个返回固定文本的假客户端，
// 从而在不联网的前提下钉住「模型输出 → 分数 → 状态」这条链路。
type ReviewRebateModelCompleter interface {
	Complete(ctx context.Context, systemPrompt string, userText string, images []ReviewRebateScoringImage) (string, error)
}

// chatGptReviewRebateCompleter 复用仓库现有的搜题模型客户端（internal/search）完成调用。
//
// 不新建 HTTP 客户端、不新建密钥管理：地址/密钥/模型/重试预算全部来自
// `ai_search_config` 那张表与 `BuildAISearchRequestConfigWithDB`，
// 与搜题、AI 兜底、OCR 诊断走同一套配置。
type chatGptReviewRebateCompleter struct {
	client *internalsearch.ChatGptClient
}

func (c chatGptReviewRebateCompleter) Complete(
	ctx context.Context,
	systemPrompt string,
	userText string,
	images []ReviewRebateScoringImage,
) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("%w: 模型客户端未初始化", ErrReviewRebateScoringFailed)
	}
	converted := make([]internalsearch.CompletionImage, 0, len(images))
	for _, image := range images {
		converted = append(converted, internalsearch.CompletionImage{
			Name:        image.Name,
			ContentType: image.ContentType,
			Body:        image.Body,
		})
	}
	content, _, err := c.client.CompleteWithImages(ctx, systemPrompt, userText, converted)
	return content, err
}

// agentReviewRebateScorer 是真实打分器：读截图 → 调模型 → 解析成 ReviewRebateScore。
//
// 它**只产出建议**：真正写库的是 ScoreReviewRebateClaimWithDB，真正发钱的是
// DecideReviewRebateClaimWithDB（人工复核）。本类型不接触钱包、不接触 claim 状态。
type agentReviewRebateScorer struct {
	db        *gorm.DB
	completer ReviewRebateModelCompleter
	// model 仅用于日志与 rationale 溯源，不参与判定。
	model string
}

// NewReviewRebateScorerWithDB 按当前配置选择打分器实现。
//
// 选择口径（fail-closed）：
//   - AI 配置不可用（未启用 / 缺地址 / 缺密钥 / 缺模型）→ 返回占位打分器，
//     分数带 Scorer=stub 标记，管理端能看出这不是真实核验；
//   - 配置可用 → 返回真实打分器。配置可用不代表调用一定成功：
//     运行期失败一律返回 ErrReviewRebateScoringFailed，绝不默认给分。
func NewReviewRebateScorerWithDB(db *gorm.DB) ReviewRebateScorer {
	if db == nil {
		return deterministicReviewRebateScorer{}
	}
	if !db.Migrator().HasTable(entity.TableNameAISearchConfig) {
		// 老库/最小测试库里可能还没有这张表：直接按「未配置」处理，
		// 不要走一遍注定报错的查询再靠日志兜底。
		return deterministicReviewRebateScorer{}
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, nil, true)
	if err != nil {
		// 读配置失败 = 不知道有没有模型 → 按「没有」处理（不是崩溃，也不是假装成功）。
		logger.SysError("评价返现读取 AI 配置失败，退化为占位打分器: " + logger.RedactSecrets(err.Error()))
		return deterministicReviewRebateScorer{}
	}
	config := resolved.Config
	if !IsAISearchConfigUsable(config) {
		return deterministicReviewRebateScorer{}
	}
	return &agentReviewRebateScorer{
		db: db,
		completer: chatGptReviewRebateCompleter{client: &internalsearch.ChatGptClient{
			Context:               context.Background(),
			Enable:                true,
			BaseURL:               config.BaseURL,
			APIKey:                config.APIKey,
			Model:                 config.Model,
			BackupModel:           config.BackupModel,
			Prompt:                config.Prompt,
			Temperature:           0,
			TopP:                  0,
			MaxAISearchModelCalls: 1,
			MaxAISearches:         0,
			MaxAIWebReads:         0,
			MaxAISearchRetries:    config.MaxAISearchRetries,
		}},
		model: strings.TrimSpace(config.Model),
	}
}

// NewReviewRebateAgentScorer 用给定客户端构造真实打分器（测试与自定义注入用）。
func NewReviewRebateAgentScorer(db *gorm.DB, completer ReviewRebateModelCompleter) ReviewRebateScorer {
	return &agentReviewRebateScorer{db: db, completer: completer}
}

func (s *agentReviewRebateScorer) Score(claim entity.ReviewRebateClaim) (ReviewRebateScore, error) {
	if s.db == nil {
		return ReviewRebateScore{}, fmt.Errorf("%w: 数据库不可用", ErrReviewRebateScoringFailed)
	}
	if s.completer == nil {
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型客户端未配置", ErrReviewRebateScoringFailed)
	}
	program, err := getReviewRebateProgramByID(s.db, claim.ProgramID)
	if err != nil {
		return ReviewRebateScore{}, fmt.Errorf("%w: 活动配置不可读", ErrReviewRebateScoringFailed)
	}
	image, err := loadReviewRebateScreenshot(claim.ScreenshotPath)
	if err != nil {
		// 截图读不出来就**不打分**：没有依据的分数会诱使运营直接批准。
		return ReviewRebateScore{}, err
	}

	userPrompt := buildReviewRebateAgentUserPrompt(program, claim)
	ctx, cancel := context.WithTimeout(context.Background(), ReviewRebateScoringTimeout)
	defer cancel()

	raw, err := s.completer.Complete(ctx, ReviewRebateAgentSystemPrompt, userPrompt, []ReviewRebateScoringImage{image})
	if err != nil {
		if errors.Is(err, ErrReviewRebateScoringFailed) {
			return ReviewRebateScore{}, err
		}
		// 上游错误只进私有日志（已脱敏），对外统一成「打分失败」，
		// 避免把供应商地址/密钥/内部错误原文带到接口响应里。
		logger.SysError(logger.RedactSecrets(fmt.Sprintf(
			"评价返现打分调用失败 claim=%d model=%s: %v", claim.ID, s.model, err)))
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型调用失败", ErrReviewRebateScoringFailed)
	}
	score, err := ParseReviewRebateAgentScore(raw)
	if err != nil {
		return ReviewRebateScore{}, err
	}
	score.Scorer = ReviewRebateScorerKindAgent
	return score, nil
}

// buildReviewRebateAgentUserPrompt 组装用户消息（活动配置 + 用户自填信息）。
//
// 评价链接只作为「不可信线索」写进提示词，**不做真实抓取**（没有可信数据源，
// StoreURL 同样只用于展示）。用户自填星数也一并交给模型做交叉对照。
func buildReviewRebateAgentUserPrompt(program entity.ReviewRebateProgram, claim entity.ReviewRebateClaim) string {
	claimedStars := "未填写"
	if claim.ClaimedStars > 0 {
		claimedStars = strconv.Itoa(claim.ClaimedStars)
	}
	reviewURL := strings.TrimSpace(claim.ClaimedReviewURL)
	if reviewURL == "" {
		reviewURL = "未填写"
	}
	storeURL := strings.TrimSpace(program.StoreURL)
	if storeURL == "" {
		storeURL = "未配置"
	}
	extensionID := strings.TrimSpace(program.ExtensionID)
	if extensionID == "" {
		extensionID = "未配置"
	}
	return fmt.Sprintf(ReviewRebateAgentUserPromptTemplate, storeURL, extensionID, claimedStars, reviewURL)
}

// reviewRebateAgentScorePayload 是模型输出的 JSON 结构（字段名即对外契约，不要改）。
type reviewRebateAgentScorePayload struct {
	Stars        int     `json:"stars"`
	ContentScore int     `json:"contentScore"`
	Confidence   float64 `json:"confidence"`
	Rationale    string  `json:"rationale"`
}

// ParseReviewRebateAgentScore 把模型输出解析成 ReviewRebateScore。
//
// 容错边界（都是真实模型会做的事）：
//   - 外面包了 ```json ... ``` 代码块；
//   - 前后带解释性文字（只取第一个 { 到最后一个 }）；
//   - confidence 给成 1 以上的百分比（1.5 / 85）→ 归一化到 [0,1]。
//
// 不做的事：**把越界分数改成合法值**。stars>5 或 contentScore>10 说明模型没按格式答，
// 这时返回错误（→ 打分失败 → 保持 submitted），而不是猜一个「接近的值」。
func ParseReviewRebateAgentScore(raw string) (ReviewRebateScore, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型返回空内容", ErrReviewRebateScoringFailed)
	}
	text = stripReviewRebateJSONFence(text)
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var payload reviewRebateAgentScorePayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型输出不是合法 JSON", ErrReviewRebateScoringFailed)
	}
	if payload.Stars < 0 || payload.Stars > reviewRebateStarsMax {
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型给出的星数越界", ErrReviewRebateScoringFailed)
	}
	if payload.ContentScore < 0 || payload.ContentScore > reviewRebateContentScoreMax {
		return ReviewRebateScore{}, fmt.Errorf("%w: 模型给出的内容分越界", ErrReviewRebateScoringFailed)
	}
	rationale := sanitizeReviewRebateAgentRationale(payload.Rationale, payload.Stars, payload.ContentScore, payload.Confidence)
	return ReviewRebateScore{
		Stars:        payload.Stars,
		ContentScore: payload.ContentScore,
		Confidence:   clampReviewRebateConfidence(payload.Confidence),
		Rationale:    rationale,
	}, nil
}

// stripReviewRebateJSONFence 去掉模型常见的 markdown 代码块包裹。
func stripReviewRebateJSONFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	trimmed := strings.TrimPrefix(text, "```")
	if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
		// 去掉 ```json 这类语言标注行。
		if !strings.Contains(trimmed[:newline], "{") {
			trimmed = trimmed[newline+1:]
		}
	}
	if end := strings.LastIndex(trimmed, "```"); end >= 0 {
		trimmed = trimmed[:end]
	}
	return strings.TrimSpace(trimmed)
}

// clampReviewRebateConfidence 把模型给的置信度收拢到 [0,1]。
//
// 模型偶尔会把置信度写成 1.5 或 85（当成百分制）；这里按「大于 1 且不超过 100 视为百分数」
// 归一化，其余一律截断到 [0,1]。注意**不会**因为「值看起来太自信」就调高它 ——
// 只做收拢，不做美化。
func clampReviewRebateConfidence(confidence float64) float64 {
	if confidence != confidence { // NaN
		return 0
	}
	if confidence < 0 {
		return 0
	}
	if confidence <= 1 {
		return confidence
	}
	if confidence <= 100 {
		return confidence / 100
	}
	return 1
}

// 模型可能编出的「归属已核验」类措辞。
//
// 我们不假装能识别所有变体，只拦最直接的断言：这几句一旦写进 rationale，
// 管理员就会误以为系统核验过作者身份。命中时整段理由作废并换成固定说明 ——
// 宁可让管理员去翻截图，也不能让他读到一句我们无法支撑的结论。
var reviewRebateForbiddenAuthorshipPhrases = []string{
	"本人所写", "是提交者本人", "本人发布", "账号归属已核验", "已确认作者",
	"同一账号发布", "归属已验证", "作者身份已验证",
}

// sanitizeReviewRebateAgentRationale 保证 rationale 是「给管理员看的可读解释」。
//
// 三件事：
//  1. 命中归属断言 → 整段替换成固定说明（不改写成别的判断，避免二次编造）；
//  2. 空/过短（<4 字）→ 用分数兜一句可读说明，不留空字符串；
//  3. 截断到 500 字（DB 是 text，但界面展示与日志都不该被超长文本拖累）。
func sanitizeReviewRebateAgentRationale(rationale string, stars int, contentScore int, confidence float64) string {
	trimmed := strings.TrimSpace(rationale)
	for _, phrase := range reviewRebateForbiddenAuthorshipPhrases {
		if strings.Contains(trimmed, phrase) {
			logger.SysLog("评价返现打分理由包含归属断言，已替换为固定说明")
			trimmed = ""
			break
		}
	}
	if len([]rune(trimmed)) < 4 {
		trimmed = fmt.Sprintf("模型给出 %d 星 / %d 分（置信度 %.2f）；系统不核验评价作者归属，请管理员结合截图自行判断。",
			stars, contentScore, clampReviewRebateConfidence(confidence))
	}
	runes := []rune(trimmed)
	if len(runes) > 500 {
		trimmed = string(runes[:500])
	}
	return trimmed
}

// ReviewRebateScreenshotAbsolutePath 把 claim 里的相对路径还原成磁盘路径。
//
// 安全口径与 controller 的 removeReviewRebateScreenshot 一致：
// 只允许 `review-rebate/...` 下的相对路径，绝对路径与 `..` 一律拒绝。
// 这里虽然是内部生成的路径，仍按不可信输入处理 —— 一旦有人能写 claim 表，
// 路径穿透就会变成任意文件读取。
func ReviewRebateScreenshotAbsolutePath(relativePath string) (string, error) {
	trimmed := strings.TrimSpace(relativePath)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 提交记录没有截图", ErrReviewRebateScoringFailed)
	}
	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("%w: 截图路径非法", ErrReviewRebateScoringFailed)
	}
	if !strings.HasPrefix(cleaned, ReviewRebateScreenshotDirName+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: 截图路径非法", ErrReviewRebateScoringFailed)
	}
	return filepath.Join(reviewRebateScreenshotDataRoot, cleaned), nil
}

// loadReviewRebateScreenshot 读取并校验截图，返回可提交给模型的图片。
//
// 用 image.DecodeConfig 真的解一次图（与上传入口同一口径）：文件可能已被清理、
// 被替换成文本，或超过上限。任何一种都按「打分失败」返回，不带着坏图去调模型。
func loadReviewRebateScreenshot(relativePath string) (ReviewRebateScoringImage, error) {
	absolute, err := ReviewRebateScreenshotAbsolutePath(relativePath)
	if err != nil {
		return ReviewRebateScoringImage{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图文件不可读", ErrReviewRebateScoringFailed)
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > ReviewRebateScreenshotMaxBytes {
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图文件大小异常", ErrReviewRebateScoringFailed)
	}
	body, err := os.ReadFile(absolute)
	if err != nil {
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图文件不可读", ErrReviewRebateScoringFailed)
	}
	if len(body) == 0 || int64(len(body)) > ReviewRebateScreenshotMaxBytes {
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图文件大小异常", ErrReviewRebateScoringFailed)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err == nil && (config.Width <= 0 || config.Height <= 0) {
		err = errors.New("invalid dimensions")
	}
	if err != nil {
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图不是有效的 PNG/JPEG", ErrReviewRebateScoringFailed)
	}
	contentType := ""
	switch strings.ToLower(format) {
	case "png":
		contentType = "image/png"
	case "jpeg":
		contentType = "image/jpeg"
	default:
		return ReviewRebateScoringImage{}, fmt.Errorf("%w: 截图不是有效的 PNG/JPEG", ErrReviewRebateScoringFailed)
	}
	return ReviewRebateScoringImage{
		Name:        filepath.Base(absolute),
		ContentType: contentType,
		Body:        body,
	}, nil
}
