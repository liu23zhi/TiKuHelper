package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ── 测试替身 ─────────────────────────────────────────────────

// fakeReviewRebateCompleter 返回固定文本或固定错误，用于在不联网的前提下
// 钉住「模型输出 → 分数 → 状态」这条链路。
type fakeReviewRebateCompleter struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
	lastText string
	lastImgs int
	// block 让 Complete 阻塞到被取消，用于验证超时路径。
	block bool
}

func (f *fakeReviewRebateCompleter) Complete(
	ctx context.Context,
	systemPrompt string,
	userText string,
	images []ReviewRebateScoringImage,
) (string, error) {
	f.mu.Lock()
	f.calls++
	f.lastText = userText
	f.lastImgs = len(images)
	response, err, block := f.response, f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return response, err
}

func (f *fakeReviewRebateCompleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// writeReviewRebateTestScreenshot 在测试工作目录下写一张真实 PNG，返回 claim 用的相对路径。
//
// 打分器从 `data/review-rebate/...` 读图，所以必须先 t.Chdir(t.TempDir())。
func writeReviewRebateTestScreenshot(t *testing.T, userID int32, digest string) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 200, G: 30, B: 40, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	relativeDir := filepath.Join(ReviewRebateScreenshotDirName, fmt.Sprintf("%d", userID), digest[:2])
	absoluteDir := filepath.Join("data", relativeDir)
	if err := os.MkdirAll(absoluteDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filename := digest + ".png"
	if err := os.WriteFile(filepath.Join(absoluteDir, filename), buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	return filepath.Join(relativeDir, filename)
}

// newReviewRebateScorerTestDB 建一个带 AI 配置表的测试库。
func newReviewRebateScorerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newReviewRebateTestDB(t)
	if err := db.AutoMigrate(&entity.AISearchConfig{}); err != nil {
		t.Fatalf("migrate ai_search_configs: %v", err)
	}
	return db
}

// agentJSON 拼一段合法模型输出。
func agentJSON(stars int, contentScore int, confidence float64, rationale string) string {
	payload, _ := json.Marshal(map[string]any{
		"stars":        stars,
		"contentScore": contentScore,
		"confidence":   confidence,
		"rationale":    rationale,
	})
	return string(payload)
}

// ── 测试 1：未配置模型 → 走 stub，行为与改动前一致 ──────────────

// 未配置模型时 NewReviewRebateScorerWithDB 必须给出占位打分器，
// 且输出带 Scorer="stub" 标记（管理端据此知道这不是真实核验）。
func TestReviewRebateScorerFallsBackToStubWithoutModel(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)

	scorer := NewReviewRebateScorerWithDB(db)
	digest := reviewRebateFixtureDigest("a0")
	score, err := scorer.Score(entity.ReviewRebateClaim{ScreenshotSHA256: digest})
	if err != nil {
		t.Fatalf("stub scorer must not error: %v", err)
	}
	if score.Scorer != ReviewRebateScorerKindStub {
		t.Fatalf("unconfigured model must mark scorer=stub, got %q", score.Scorer)
	}
	// 与旧行为逐字段一致：确定性派生 + 置信度 1 + 范围合法。
	expected, err := deterministicReviewRebateScore(entity.ReviewRebateClaim{ScreenshotSHA256: digest})
	if err != nil {
		t.Fatalf("deterministic score: %v", err)
	}
	if score.Stars != expected.Stars || score.ContentScore != expected.ContentScore || score.Confidence != expected.Confidence {
		t.Fatalf("stub output drifted from the historical mapping: got %+v want %+v", score, expected)
	}
	if score.Stars < 1 || score.Stars > 5 || score.ContentScore < 1 || score.ContentScore > 10 {
		t.Fatalf("stub output out of range: %+v", score)
	}
	if !strings.Contains(score.Rationale, "占位打分器") {
		t.Fatalf("stub rationale must be explicit about being a placeholder: %q", score.Rationale)
	}
}

// AI 配置存在但被关闭 / 字段不全 → 仍然走 stub（fail-closed，不能假装有模型）。
func TestReviewRebateScorerStubWhenAIConfigUnusable(t *testing.T) {
	cases := []struct {
		name   string
		config entity.AISearchConfig
	}{
		{"disabled", entity.AISearchConfig{Provider: "ai", Enabled: false, BaseURL: "https://api.invalid/v1", APIKey: "sk-x", Model: "m"}},
		{"no-key", entity.AISearchConfig{Provider: "ai", Enabled: true, BaseURL: "https://api.invalid/v1", Model: "m"}},
		{"no-model", entity.AISearchConfig{Provider: "ai", Enabled: true, BaseURL: "https://api.invalid/v1", APIKey: "sk-x"}},
		{"no-base", entity.AISearchConfig{Provider: "ai", Enabled: true, APIKey: "sk-x", Model: "m"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := newReviewRebateScorerTestDB(t)
			config := testCase.config
			if err := db.Create(&config).Error; err != nil {
				t.Fatalf("seed config: %v", err)
			}
			score, err := NewReviewRebateScorerWithDB(db).Score(entity.ReviewRebateClaim{
				ScreenshotSHA256: reviewRebateFixtureDigest("a1"),
			})
			if err != nil {
				t.Fatalf("unusable config must degrade to stub, got error: %v", err)
			}
			if score.Scorer != ReviewRebateScorerKindStub {
				t.Fatalf("unusable config must mark scorer=stub, got %q", score.Scorer)
			}
		})
	}
}

// 配置可用 → 必须是真实打分器（而不是仍然用 stub）。
func TestReviewRebateScorerUsesAgentWhenConfigured(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)
	config := entity.AISearchConfig{
		Provider: "ai", Enabled: true,
		BaseURL: "https://api.invalid/v1", APIKey: "sk-fixture", Model: "gpt-fixture",
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if _, ok := NewReviewRebateScorerWithDB(db).(*agentReviewRebateScorer); !ok {
		t.Fatalf("usable config must produce the agent scorer, got %T", NewReviewRebateScorerWithDB(db))
	}
}

// ── 测试 6（第 1 条）：真实打分器读截图 + 解析模型输出 ──────────

func TestReviewRebateAgentScorerParsesModelOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("b1")
	claim := entity.ReviewRebateClaim{
		ID: 1, ProgramID: program.ID, UserID: 7,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, 7, digest),
		ScreenshotSHA256: digest,
		ClaimedStars:     5,
		ClaimedReviewURL: "https://example.invalid/review/1",
	}
	completer := &fakeReviewRebateCompleter{
		response: agentJSON(5, 9, 0.88, "截图清晰显示 5 星，正文写了具体使用场景，质量较高。"),
	}
	scorer := NewReviewRebateAgentScorer(db, completer)
	score, err := scorer.Score(claim)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if score.Stars != 5 || score.ContentScore != 9 || score.Confidence != 0.88 {
		t.Fatalf("unexpected parsed score: %+v", score)
	}
	if score.Scorer != ReviewRebateScorerKindAgent {
		t.Fatalf("agent scorer must mark scorer=agent, got %q", score.Scorer)
	}
	if !strings.Contains(score.Rationale, "5 星") {
		t.Fatalf("rationale must be readable, got %q", score.Rationale)
	}
	if completer.callCount() != 1 {
		t.Fatalf("expected exactly one model call, got %d", completer.callCount())
	}
	if completer.lastImgs != 1 {
		t.Fatalf("screenshot must be sent to the model, got %d images", completer.lastImgs)
	}
	// 提示词必须带上活动配置，且不得声称已核验归属。
	if !strings.Contains(completer.lastText, program.StoreURL) {
		t.Fatalf("user prompt must carry store URL: %q", completer.lastText)
	}
	if !strings.Contains(completer.lastText, "不可信") {
		t.Fatalf("user prompt must mark the user link as untrusted: %q", completer.lastText)
	}
	if strings.Contains(ReviewRebateAgentSystemPrompt, "已确认作者") {
		t.Fatal("system prompt must not assert authorship")
	}
}

// ── 测试 2：配置了模型但调用失败 → 不自动通过、不默认高分 ───────

func TestReviewRebateScoreFailsClosedWhenModelErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 71, "rr-agent-fail", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})
	digest := reviewRebateFixtureDigest("b2")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	completer := &fakeReviewRebateCompleter{err: errors.New("upstream 500")}
	scorer := NewReviewRebateAgentScorer(db, completer)
	_, err = ScoreReviewRebateClaimWithDB(db, claim.ID, scorer)
	if !errors.Is(err, ErrReviewRebateScoringFailed) {
		t.Fatalf("model failure must surface ErrReviewRebateScoringFailed, got %v", err)
	}

	// 关键断言：分数、金额、状态**一个都没动**（保持 submitted）。
	var stored entity.ReviewRebateClaim
	if err := db.Where("id = ?", claim.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload claim: %v", err)
	}
	if stored.Status != entity.ReviewRebateStatusSubmitted {
		t.Fatalf("failed scoring must keep submitted, got %q", stored.Status)
	}
	if stored.AgentStars != 0 || stored.AgentContentScore != 0 || stored.AgentConfidence != 0 {
		t.Fatalf("failed scoring must not write any score, got %+v", stored)
	}
	if stored.AwardAmount != 0 || stored.MatchedRuleID != 0 {
		t.Fatalf("failed scoring must not compute an amount, got %+v", stored)
	}
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Fatalf("failed scoring must never award, got %d", awards)
	}
}

// 模型返回不可解析的内容 / 越界分数 → 同样是打分失败，不得猜测合法值。
func TestReviewRebateScoreRejectsUnparseableModelOutput(t *testing.T) {
	bad := []struct {
		name     string
		response string
	}{
		{"empty", ""},
		{"not-json", "我认为这条评价不错，给 5 星。"},
		{"stars-out-of-range", agentJSON(9, 8, 0.9, "星数越界")},
		{"content-out-of-range", agentJSON(4, 99, 0.9, "内容分越界")},
	}
	for _, testCase := range bad {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			db := newReviewRebateScorerTestDB(t)
			program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
			digest := reviewRebateFixtureDigest("b3")
			claim := entity.ReviewRebateClaim{
				ID: 3, ProgramID: program.ID, UserID: 73,
				ScreenshotPath:   writeReviewRebateTestScreenshot(t, 73, digest),
				ScreenshotSHA256: digest,
			}
			scorer := NewReviewRebateAgentScorer(db, &fakeReviewRebateCompleter{response: testCase.response})
			if _, err := scorer.Score(claim); !errors.Is(err, ErrReviewRebateScoringFailed) {
				t.Fatalf("unparseable output must be a scoring failure, got %v", err)
			}
		})
	}
}

// 截图缺失/被替换 → 打分失败（不带着坏证据去问模型）。
func TestReviewRebateAgentScorerRejectsMissingScreenshot(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	completer := &fakeReviewRebateCompleter{response: agentJSON(5, 9, 0.9, "ok")}
	scorer := NewReviewRebateAgentScorer(db, completer)

	cases := []struct {
		name string
		path string
	}{
		{"empty-path", ""},
		{"absolute", filepath.Join(t.TempDir(), "x.png")},
		{"traversal", "../etc/passwd"},
		{"outside-dir", "other/user/a.png"},
		{"missing", filepath.Join(ReviewRebateScreenshotDirName, "9", "aa", "missing.png")},
	}
	for _, testCase := range cases {
		claim := entity.ReviewRebateClaim{ID: 4, ProgramID: program.ID, ScreenshotPath: testCase.path}
		if _, err := scorer.Score(claim); err == nil {
			t.Fatalf("%s: bad screenshot path must fail, got nil error", testCase.name)
		}
	}
	if completer.callCount() != 0 {
		t.Fatalf("model must not be called without a valid screenshot, got %d calls", completer.callCount())
	}
}

// ── 解析器细节：代码块包裹 / 百分制置信度 / 归属断言清洗 ────────

func TestParseReviewRebateAgentScoreToleratesRealModelOutput(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantStars  int
		wantScore  int
		wantConf   float64
		wantInText string
	}{
		{
			name:      "fenced json",
			raw:       "```json\n" + agentJSON(4, 7, 0.5, "截图显示 4 星，正文较短。") + "\n```",
			wantStars: 4, wantScore: 7, wantConf: 0.5,
		},
		{
			name:      "surrounded by prose",
			raw:       "我的判断如下：" + agentJSON(3, 5, 0.4, "星级可读，正文较空泛。") + " 以上。",
			wantStars: 3, wantScore: 5, wantConf: 0.4,
		},
		{
			name:      "percent confidence",
			raw:       agentJSON(5, 8, 85, "截图清晰。"),
			wantStars: 5, wantScore: 8, wantConf: 0.85,
		},
		{
			name:      "no stars visible",
			raw:       agentJSON(0, 0, 0.05, "截图里没有星级也没有正文，无法判断。"),
			wantStars: 0, wantScore: 0, wantConf: 0.05,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			score, err := ParseReviewRebateAgentScore(testCase.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if score.Stars != testCase.wantStars || score.ContentScore != testCase.wantScore {
				t.Fatalf("got %+v want stars=%d score=%d", score, testCase.wantStars, testCase.wantScore)
			}
			if diff := score.Confidence - testCase.wantConf; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("confidence = %v, want %v", score.Confidence, testCase.wantConf)
			}
		})
	}
}

// 模型若在理由里断言「这就是本人写的」，整段理由必须被作废换成固定说明。
func TestParseReviewRebateAgentScoreStripsAuthorshipClaims(t *testing.T) {
	score, err := ParseReviewRebateAgentScore(agentJSON(5, 9, 0.9, "已确认作者：这条评价是提交者本人所写。"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, phrase := range reviewRebateForbiddenAuthorshipPhrases {
		if strings.Contains(score.Rationale, phrase) {
			t.Fatalf("rationale still asserts authorship (%q): %q", phrase, score.Rationale)
		}
	}
	if !strings.Contains(score.Rationale, "不核验") {
		t.Fatalf("replaced rationale must state the boundary, got %q", score.Rationale)
	}
}

// ── 测试 3 + 4：confidence 低于门槛 → 强制 pending_review 且金额 0；归属恒 false ──

func TestReviewRebateScoreLowConfidenceFromAgentForcesPendingReview(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 81, "rr-agent-lowconf", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 9, MaxContentScore: 10,
		Amount: 1, Priority: 10, Enabled: true,
	})
	digest := reviewRebateFixtureDigest("c1")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	// 模型给了满分但明说没把握：这正是「宁可转人工」的场景。
	completer := &fakeReviewRebateCompleter{
		response: agentJSON(5, 10, 0.25, "截图上只能看到星级，正文被裁掉了，把握不足。"),
	}
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, NewReviewRebateAgentScorer(db, completer))
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusPendingReview {
		t.Fatalf("low confidence must force pending_review, got %q", scored.Status)
	}
	if scored.AwardAmount != 0 || scored.MatchedRuleID != 0 {
		t.Fatalf("low confidence must not compute a candidate amount, got %+v", scored)
	}
	if scored.AgentScorer != ReviewRebateScorerKindAgent {
		t.Fatalf("scorer provenance must be persisted, got %q", scored.AgentScorer)
	}
	if scored.AgentConfidence != 0.25 {
		t.Fatalf("confidence must be persisted as given, got %v", scored.AgentConfidence)
	}
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Fatalf("low confidence must never award, got %d", awards)
	}
}

// 置信度达标 + 规则命中 → scored，写候选金额但不发放；来源标记为 agent。
func TestReviewRebateAgentScoreWritesCandidateWithProvenance(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 82, "rr-agent-ok", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 8, MaxContentScore: 10,
		Amount: 0.8, Priority: 20, Enabled: true,
	})
	digest := reviewRebateFixtureDigest("c2")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	completer := &fakeReviewRebateCompleter{
		response: agentJSON(5, 9, 0.87, "截图清晰显示 5 星，正文写了具体使用体验与建议。"),
	}
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, NewReviewRebateAgentScorer(db, completer))
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusScored {
		t.Fatalf("status = %q, want scored", scored.Status)
	}
	if scored.AwardAmount != 0.8 || scored.MatchedRuleID == 0 {
		t.Fatalf("candidate amount/rule not written: %+v", scored)
	}
	if scored.AgentScorer != ReviewRebateScorerKindAgent {
		t.Fatalf("scorer provenance must be agent, got %q", scored.AgentScorer)
	}
	// 打分不发放。
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Fatalf("scoring must not create awards, got %d", awards)
	}
}

// ── 测试 4：authorshipVerified 恒 false ────────────────────────

func TestReviewRebateProgramViewNeverVerifiesAuthorship(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 91, "rr-authorship", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)

	view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view: %v", err)
	}
	if view.AuthorshipVerified {
		t.Fatal("authorshipVerified must always be false")
	}
	if strings.TrimSpace(view.Notice) == "" {
		t.Fatal("notice must explain the boundary")
	}
	if view.Program.ID != program.ID {
		t.Fatalf("program id = %d, want %d", view.Program.ID, program.ID)
	}
	// 活动开关必须下发：用户端靠它决定显示规则表还是「功能未开启」。
	if !view.Program.Enabled {
		t.Fatal("program.enabled must be exposed to the client")
	}

	// 序列化后的 JSON 里也只能是 false（不能被 omitempty 之类漏掉）。
	encoded, marshalErr := json.Marshal(view)
	if marshalErr != nil {
		t.Fatalf("marshal view: %v", marshalErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if value, exists := decoded["authorshipVerified"]; !exists || value != false {
		t.Fatalf("authorshipVerified must serialize as false, got %v (exists=%v)", value, exists)
	}
}

// ── 测试 5：字段契约（JSON 键名）───────────────────────────────

// 用户端 program 视图、claims 列表、usage 的键名必须与前端读取的名字一致。
func TestReviewRebateJSONContractKeys(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 95, "rr-contract", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})

	view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view: %v", err)
	}
	viewKeys := decodeReviewRebateJSON(t, view)
	assertReviewRebateKeys(t, "program view", viewKeys,
		"program", "rules", "usage", "authorshipVerified", "notice")
	assertReviewRebateKeys(t, "program summary", mapAt(t, viewKeys, "program"),
		"id", "enabled", "name", "storeUrl", "maxClaimsPerUser", "maxAmountPerUser",
		"minAgentConfidence", "policyVersion", "tutorialVersionId")
	assertReviewRebateKeys(t, "usage", mapAt(t, viewKeys, "usage"),
		"claimedCount", "approvedCount", "approvedAmount", "pendingCount",
		"rejectedCount", "remainingClaims", "remainingAmount")
	// 两个条数键必须同值，否则「已领次数」会出现两套数字。
	if mapAt(t, viewKeys, "usage")["claimedCount"] != mapAt(t, viewKeys, "usage")["approvedCount"] {
		t.Fatalf("claimedCount and approvedCount must match: %v", mapAt(t, viewKeys, "usage"))
	}

	// claims 列表：{data:[...], total, page, pageSize}，行内键名固定。
	result, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{UserID: user.ID}, 1, 20)
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	listKeys := decodeReviewRebateJSON(t, result)
	assertReviewRebateKeys(t, "claims envelope", listKeys, "data", "total", "page", "pageSize")
	if _, ok := listKeys["data"].([]any); !ok {
		t.Fatalf("claims data must serialize as an array, got %T", listKeys["data"])
	}
}

// 空列表必须是 []（不是 null）：前端对 null 与 [] 的处理不同。
func TestReviewRebateClaimListEmptyIsArray(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)

	result, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	if !strings.Contains(string(encoded), `"data":[]`) {
		t.Fatalf("empty claim list must serialize as [], got %s", encoded)
	}
}

// 管理员列表要带 agentRationale 与 screenshotUrl；用户列表不得下发这两项。
func TestReviewRebateClaimListScopesAdminOnlyFields(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 97, "rr-scope-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("d1")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	if err := db.Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).Updates(map[string]any{
		"agent_rationale": "截图清晰，正文具体。",
		"agent_scorer":    ReviewRebateScorerKindAgent,
	}).Error; err != nil {
		t.Fatalf("seed rationale: %v", err)
	}

	admin, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{AdminView: true}, 1, 20)
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(admin.Data) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(admin.Data))
	}
	if admin.Data[0].AgentRationaleText != "截图清晰，正文具体。" {
		t.Fatalf("admin list must expose the rationale, got %q", admin.Data[0].AgentRationaleText)
	}
	expectedURL := fmt.Sprintf("%s/admin/review-rebate/claims/%d/screenshot", ReviewRebateAdminAPIPrefix, claim.ID)
	if admin.Data[0].ScreenshotURL != expectedURL {
		t.Fatalf("screenshotUrl = %q, want %q", admin.Data[0].ScreenshotURL, expectedURL)
	}

	userList, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{UserID: user.ID}, 1, 20)
	if err != nil {
		t.Fatalf("user list: %v", err)
	}
	if userList.Data[0].AgentRationaleText != "" {
		t.Fatalf("user list must not expose the rationale, got %q", userList.Data[0].AgentRationaleText)
	}
	userJSON, marshalErr := json.Marshal(userList.Data[0])
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	if strings.Contains(string(userJSON), "agentRationale") {
		t.Fatalf("user list JSON must not contain agentRationale: %s", userJSON)
	}
}

// ── 打分器接口的兜底行为 ──────────────────────────────────────

// 已批准的行不允许重新打分（改已入账的金额会让账更乱）。
func TestReviewRebateScoreRejectsTerminalClaims(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 99, "rr-terminal", "user")
	admin := seedReviewRebateUser(t, db, 100, "rr-terminal-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})
	digest := reviewRebateFixtureDigest("d2")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, nil); err != nil {
		t.Fatalf("score: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// 已批准 → 再打分必须被拒。
	completer := &fakeReviewRebateCompleter{response: agentJSON(1, 1, 0.9, "想覆盖已入账金额。")}
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, NewReviewRebateAgentScorer(db, completer)); !errors.Is(err, ErrReviewRebateClaimState) {
		t.Fatalf("terminal claim must reject re-scoring, got %v", err)
	}
	if completer.callCount() != 0 {
		t.Fatalf("terminal claim must not even call the model, got %d calls", completer.callCount())
	}
}

// 打分器返回越界分数时必须报错，而不是把分数夹到边界（那等于替模型兜底）。
func TestReviewRebateScoreRejectsOutOfRangeScorerResult(t *testing.T) {
	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 101, "rr-range", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("d3"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	outOfRange := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 7, ContentScore: 12, Confidence: 0.9, Rationale: "越界"}, nil
	})
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, outOfRange); err == nil {
		t.Fatal("out-of-range scorer output must be rejected")
	}
	var stored entity.ReviewRebateClaim
	if err := db.Where("id = ?", claim.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != entity.ReviewRebateStatusSubmitted || stored.AwardAmount != 0 {
		t.Fatalf("rejected scoring must not change the claim, got %+v", stored)
	}
}

// ── 小工具 ───────────────────────────────────────────────────

func decodeReviewRebateJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded
}

func mapAt(t *testing.T, root map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := root[key].(map[string]any)
	if !ok {
		t.Fatalf("key %q is not an object: %T", key, root[key])
	}
	return value
}

// assertReviewRebateKeys 断言键集合**完全**等于期望集合。
//
// 用「完全相等」而不是「包含」：多余键同样会把前端搞乱（例如同一语义两个名字），
// 而漏键会让界面读到 undefined。两者都该在测试里立刻暴露。
func assertReviewRebateKeys(t *testing.T, label string, object map[string]any, expected ...string) {
	t.Helper()
	got := make(map[string]struct{}, len(object))
	for key := range object {
		got[key] = struct{}{}
	}
	for _, key := range expected {
		if _, ok := got[key]; !ok {
			t.Fatalf("%s missing key %q (have %v)", label, key, sortedReviewRebateKeys(got))
		}
		delete(got, key)
	}
	if len(got) > 0 {
		t.Fatalf("%s has unexpected keys %v", label, sortedReviewRebateKeys(got))
	}
}

func sortedReviewRebateKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// ── trace 内容契约：系统提示词全文 + attempt 顺序（task_doc/45 §0.26.1、77 §11.1）──

// resetReviewRebateAttemptCounters 清空进程内的打分次数计数器。
//
// 不同测试各自建库，claim 主键都从 1 开始，会与上一个测试的计数撞键；
// 清空后 attempt 断言才是确定的（计数器本身是纯内存 best-effort，见 trace.go）。
func resetReviewRebateAttemptCounters() {
	reviewRebateAttemptMu.Lock()
	reviewRebateAttemptSeq = map[int32]int{}
	reviewRebateAttemptMu.Unlock()
}

// readReviewRebateTraceRecords 读出 Tiku-review-rebate.txt 的所有 JSON Lines。
func readReviewRebateTraceRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "Tiku-review-rebate.txt"))
	if err != nil {
		t.Fatalf("read review-rebate trace: %v", err)
	}
	var records []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		start := strings.Index(line, "{")
		if start < 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line[start:]), &record); err != nil {
			t.Fatalf("unmarshal trace line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// 用户 2026-11-11 定案「都保留完整提示词，回复记录等」：
// 系统提示词必须记**全文**（不是 sha256），且同一 claim 的多次打分要能看出先后。
func TestReviewRebateTraceRecordsSystemPromptAndAttempt(t *testing.T) {
	t.Chdir(t.TempDir())
	oldLogDir := *logger.LogDir
	*logger.LogDir = t.TempDir()
	if err := logger.SetupGinLog(); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		*logger.LogDir = oldLogDir
	})
	logDir := *logger.LogDir

	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 83, "rr-trace", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 8, MaxContentScore: 10,
		Amount: 0.8, Priority: 20, Enabled: true,
	})
	digest := reviewRebateFixtureDigest("trace-1")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	resetReviewRebateAttemptCounters()
	completer := &fakeReviewRebateCompleter{
		response: agentJSON(5, 9, 0.87, "截图清晰显示 5 星，正文写了具体使用体验。"),
	}
	for i := 0; i < 2; i++ {
		if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, NewReviewRebateAgentScorer(db, completer)); err != nil {
			t.Fatalf("score #%d: %v", i+1, err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	records := readReviewRebateTraceRecords(t, logDir)
	if len(records) != 2 {
		t.Fatalf("trace records = %d, want 2 (first run + rerun)", len(records))
	}

	// ① attempt 必须能区分先后：首次 1、重跑 2。
	for index, want := range []float64{1, 2} {
		got, ok := records[index]["attempt"].(float64)
		if !ok {
			t.Fatalf("record #%d has no attempt field: %v", index+1, records[index])
		}
		if got != want {
			t.Fatalf("record #%d attempt = %v, want %v", index+1, got, want)
		}
	}

	// ② 系统提示词必须是**全文**，不是摘要，也不是 sha256。
	prompt, ok := records[0]["systemPrompt"].(string)
	if !ok || prompt == "" {
		t.Fatalf("record has no systemPrompt text: %v", records[0])
	}
	if prompt != strings.TrimSpace(ReviewRebateAgentSystemPrompt) {
		t.Fatalf("systemPrompt must be the full prompt text, got %q", prompt)
	}
	if !strings.Contains(prompt, "你绝对不做的事") {
		t.Fatalf("systemPrompt looks truncated: %q", prompt)
	}
	if digestValue, _ := records[0]["systemPromptSha256"].(string); len(digestValue) != 16 {
		t.Fatalf("systemPromptSha256 = %q, want the 16-char digest kept for version compare", digestValue)
	}
	if digestValue, _ := records[0]["systemPromptSha256"].(string); digestValue == prompt {
		t.Fatal("systemPromptSha256 must stay a digest, not the prompt text")
	}

	// ③ 脱敏仍然生效：完整提示词/模型输出不得带出密钥。
	raw, err := os.ReadFile(filepath.Join(logDir, "Tiku-review-rebate.txt"))
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	if strings.Contains(string(raw), "sk-") && !strings.Contains(string(raw), "sk-***") {
		t.Fatalf("trace leaked an api key: %s", string(raw))
	}
}

// trace payload 必须带 ts / sanitized / matchedRuleId / awardAmount 四个字段，
// 且四个值都必须有真实来源 —— 不能是占位常量。
//
// task_doc/73 §3.2.2 的规格表列了这四个；此前实现里它们全缺
// （grep '"ts"' / sanitized / matchedRuleId / awardAmount 在 trace 文件里 0 命中），
// 后果是「这条为什么给 0.8 元」这个最常被问的问题在日志里答不出来：
// 有分数、有理由，但没有「命中哪条规则、算出的候选金额是多少」。
//
// # 每个字段的判据都必须是「非默认值」
//
// 只断言字段存在，会漏掉「写死 0 / false / 空串」的实现 —— 那正是假钉子。
// 所以：
//   - ts 断言格式可解析且落在本次测试的时间窗内（不是 0001-01-01）；
//   - sanitized 断言为 true，且**换一条不含密钥的记录断言为 false**
//     （单侧断言无法区分「恒 true」「恒 false」与「真的算出来了」）；
//   - matchedRuleId 断言等于种子规则的 ID（非 0）；
//   - awardAmount 断言等于种子规则的金额 0.8（非 0）。
func TestReviewRebateTraceRecordsTsSanitizedRuleAndAmount(t *testing.T) {
	t.Chdir(t.TempDir())
	oldLogDir := *logger.LogDir
	*logger.LogDir = t.TempDir()
	if err := logger.SetupGinLog(); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		*logger.LogDir = oldLogDir
	})
	logDir := *logger.LogDir

	db := newReviewRebateScorerTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 84, "rr-trace-fields", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	rule := entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 8, MaxContentScore: 10,
		Amount: 0.8, Priority: 20, Enabled: true,
	}
	// 必须接返回值：Create 回填的自增 ID 在返回的结构体里，
	// 不看返回值常量那条 rule.ID 仍是 0，断言会退化成「等于 0」的假钉子。
	rule = seedReviewRebateRule(t, db, rule)
	if rule.ID == 0 {
		t.Fatal("seeded rule must carry a non-zero id")
	}
	digest := reviewRebateFixtureDigest("trace-fields-1")
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotPath:   writeReviewRebateTestScreenshot(t, user.ID, digest),
		ScreenshotSHA256: digest,
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	resetReviewRebateAttemptCounters()
	// 这一组响应故意**不含**任何密钥字样，因此 sanitized 必须是 false。
	before := time.Now().Add(-2 * time.Second)
	completer := &fakeReviewRebateCompleter{
		response: agentJSON(5, 9, 0.87, "截图清晰显示 5 星，正文写了具体使用体验。"),
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, NewReviewRebateAgentScorer(db, completer)); err != nil {
		t.Fatalf("score: %v", err)
	}
	after := time.Now().Add(2 * time.Second)
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	records := readReviewRebateTraceRecords(t, logDir)
	if len(records) == 0 {
		t.Fatal("no trace records written")
	}
	last := records[len(records)-1]

	// ① ts：格式与日志行首一致，且落在本次测试的时间窗内。
	tsValue, ok := last["ts"].(string)
	if !ok || tsValue == "" {
		t.Fatalf("record has no ts field: %v", last)
	}
	parsed, err := time.ParseInLocation("2006/01/02 - 15:04:05", tsValue, logger.ShanghaiLocation())
	if err != nil {
		t.Fatalf("ts %q is not in the log-line format: %v", tsValue, err)
	}
	if parsed.Before(before) || parsed.After(after) {
		t.Fatalf("ts %q outside this test's window [%v, %v]", tsValue, before, after)
	}

	// ② sanitized：这条记录不含密钥 → 必须是 false（真实判定，不是恒 true）。
	sanitized, ok := last["sanitized"].(bool)
	if !ok {
		t.Fatalf("record has no sanitized field: %v", last)
	}
	if sanitized {
		t.Fatalf("sanitized must be false for a record without secrets: %v", last)
	}

	// ③ matchedRuleId / ④ awardAmount：命中规则后的真实快照，非 0 且等于种子值。
	matched, ok := last["matchedRuleId"].(float64)
	if !ok {
		t.Fatalf("record has no matchedRuleId field: %v", last)
	}
	if int32(matched) != rule.ID {
		t.Fatalf("matchedRuleId = %v, want %d", matched, rule.ID)
	}
	amount, ok := last["awardAmount"].(float64)
	if !ok {
		t.Fatalf("record has no awardAmount field: %v", last)
	}
	if amount != 0.8 {
		t.Fatalf("awardAmount = %v, want 0.8", amount)
	}
}

// sanitized 必须是**真判定**：含密钥的记录为 true，不含的为 false。
//
// 这条与上一条分开写，是为了让「恒 true」和「恒 false」两种假实现分别变红：
// 上一条只断言 false 侧，这一条断言 true 侧。两条都过才说明它是算出来的。
func TestReviewRebateTraceSanitizedFlagReflectsRealRedaction(t *testing.T) {
	// 直接测判定函数：它是 sanitized 位的唯一来源。
	// 用 logger.RedactSecrets 认得的密钥形态，保证「含密钥」侧真的会被改写。
	const secretLike = "Authorization: Bearer sk-live-abcdefghijklmnop"
	if !reviewRebateTraceWasSanitized(`{"rawResponse":"` + secretLike + `"}`) {
		t.Fatal("text carrying a live-looking API key must be reported as sanitized")
	}
	if reviewRebateTraceWasSanitized(`{"rawResponse":"plain model output, no credentials here"}`) {
		t.Fatal("plain text must not be reported as sanitized")
	}
}
