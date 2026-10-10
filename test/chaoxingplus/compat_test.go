// Package chaoxingplus 用于核验 tikuAdapter 与 chaoxing_plus 客户端的真实对接。
//
// 这里不使用任何 stub：直接构建 /adapter-service 的完整路由（含中间件），
// 用 chaoxing_plus 实际发送的请求体与 Authorization 头打真实 HTTP 请求，
// 再按 chaoxing_plus 的 handler 逻辑校验响应字段。
// 放在独立包中是为了让 registry 的 sync.Once 初始化拿到本测试自己的临时数据库。
package chaoxingplus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/api"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/spf13/viper"
)

const chaoxingPlusAccessToken = "TK_CHAOXING_PLUS_COMPAT_2024"

// 与 chaoxing_plus 的 DEFAULT_TIKU_BASE_URL / key 配置对应的凭据。
var chaoxingPlusAuthorization = "Bearer " + chaoxingPlusAccessToken

const (
	singleChoiceQuestion   = "下面选项中,属于男性在青春期生理变化的内容有?()"
	multipleChoiceQuestion = "下面选项中,属于缓解性欲的常见途径有?()"
	judgementQuestion      = "十进制数(9)10比十六进制数(9)16小.()"
	readerQuestion         = "请阅读材料后回答：已知离散 LSI 系统的差分方程 y(n) = x(n) + (1/3)x(n-1)，求系统函数并指出零极点。"
)

var choiceOptions = []string{"自然缓解", "升华", "转移", "克制"}

// compatWorkDir 保存测试用的临时数据库文件；退出时统一清理，
// 避免 Windows 上 SQLite 文件句柄未释放导致 t.TempDir 清理失败。
var compatWorkDir string

var (
	managerOnce   sync.Once
	managerErr    error
	compatManager *manager.Manager
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)

	// 假 AI 上游跑在 httptest 的 127.0.0.1 上，必须放行环回地址，
	// 否则 pkg/util.NewSafeHTTPClient 会按 SSRF 防护直接拒绝出站。
	_ = os.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "true")

	workDir, err := os.MkdirTemp("", "tiku-chaoxingplus-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	compatWorkDir = workDir

	code := m.Run()

	// 先关掉数据库连接再删目录，否则 Windows 会因为文件占用而删除失败。
	if compatManager != nil {
		_ = compatManager.CloseManager()
	}
	_ = os.RemoveAll(workDir)
	os.Exit(code)
}

// aiUpstream 是一个 OpenAI 兼容的假上游：按题型返回对应的 JSON 答案。
type aiUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  int
}

func (u *aiUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func newAIUpstream(t *testing.T) *aiUpstream {
	t.Helper()

	upstream := &aiUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-chaoxing-plus-upstream" {
			http.Error(w, "unexpected upstream key: "+got, http.StatusUnauthorized)
			return
		}
		upstream.mu.Lock()
		upstream.calls++
		upstream.mu.Unlock()

		body, _ := io.ReadAll(r.Body)
		writeOpenAIStream(w, answerForRequest(t, body))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// answerForRequest 复刻真实上游的行为：按题干内容决定答案，而不是依赖题型标签。
func answerForRequest(t *testing.T, requestBody []byte) string {
	t.Helper()

	userText := extractUserText(t, requestBody)

	switch {
	case strings.Contains(userText, "缓解性欲"):
		return `{"answers":["自然缓解","升华"]}`
	case strings.Contains(userText, "青春期"):
		return `{"answers":["自然缓解"]}`
	case strings.Contains(userText, "十六进制"):
		return `{"answers":["正确"]}`
	case strings.Contains(userText, "最大的城市"):
		return `{"answers":["北京","上海"]}`
	default:
		return `{"answers":["H(z)=(1+(1/3)z^-1)，零点 z=-1/3，极点 z=0；收敛域 |z|>0；单位冲激响应 h(n)=δ(n)+(1/3)δ(n-1)"]}`
	}
}

func extractUserText(t *testing.T, requestBody []byte) string {
	t.Helper()

	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	if len(payload.Messages) < 2 {
		t.Fatalf("expected system and user messages, got %d", len(payload.Messages))
	}

	userText := ""
	if err := json.Unmarshal(payload.Messages[1].Content, &userText); err == nil && userText != "" {
		return userText
	}

	// content 可能是多模态数组，取其中的 text 片段。
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload.Messages[1].Content, &parts); err == nil {
		for _, part := range parts {
			userText += part.Text
		}
	}
	return userText
}

func writeOpenAIStream(w http.ResponseWriter, answerJSON string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	for _, chunk := range splitForStream(answerJSON) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", streamChunk(chunk))
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", usageChunk(2000, 300, 512))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// splitForStream 把答案拆成两个流式分片。
// 必须按 rune 切分：按字节切开多字节汉字会产生非法 UTF-8，
// json.Marshal 会把它替换成 U+FFFD，从而在测试里伪造出「乱码」假象。
func splitForStream(answer string) []string {
	runes := []rune(answer)
	if len(runes) < 2 {
		return []string{answer}
	}
	mid := len(runes) / 2
	return []string{string(runes[:mid]), string(runes[mid:])}
}

func streamChunk(content string) string {
	raw, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{"content": content}}},
	})
	return string(raw)
}

func usageChunk(prompt int, completion int, cached int) string {
	raw, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":         prompt,
			"completion_tokens":     completion,
			"total_tokens":          prompt + completion,
			"prompt_tokens_details": map[string]any{"cached_tokens": cached},
		},
	})
	return string(raw)
}

type compatHarness struct {
	router   *gin.Engine
	upstream *aiUpstream
	token    entity.AccessToken
	user     entity.User
}

// newCompatHarness 启动一套完整可用的 tikuAdapter，并配置好 chaoxing_plus 会使用的凭据。
//
// 这里走与 cmd/adapter-service 完全一致的初始化路径（manager.CreateManager），
// 这样限流等依赖全局 manager 的中间件才能正常工作。
func newCompatHarness(t *testing.T) *compatHarness {
	t.Helper()

	// 用 viper 指定临时 SQLite 路径，让 registry.Config() 读到它。
	viper.Set("database.path", filepath.Join(compatWorkDir, "chaoxing-plus-compat.db"))
	viper.Set("limit.enable", false)

	managerOnce.Do(func() {
		managerErr = func() error {
			mg, err := manager.CreateManager()
			if err != nil {
				return err
			}
			compatManager = mg
			return nil
		}()
	})
	if managerErr != nil {
		t.Fatalf("create manager: %v", managerErr)
	}
	if compatManager == nil {
		t.Fatal("expected a global manager")
	}

	db := compatManager.GetDB()
	if db == nil {
		t.Fatal("expected registry to return a database")
	}
	// 让 DAO 层也指向同一个库（AuthOrAccessToken 走的是 dao.User）。
	dao.SetDefault(db)

	upstream := newAIUpstream(t)

	// 1) 接入访问令牌所属的用户，并给出可用余额。
	//    CreateManager 只会初始化一次，同进程内多个测试共用同一个库，
	//    因此这里的写入必须是幂等的。
	var user entity.User
	if err := db.Where("id = ?", 4201).First(&user).Error; err != nil {
		user = entity.User{
			ID:             4201,
			Username:       "chaoxing-plus-user",
			Password:       "irrelevant",
			Role:           "user",
			Status:         "enabled",
			Balance:        50,
			RegisterSource: "public",
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	// 令牌值必须与 chaoxing_plus 配置里的 key 完全一致（Bearer 方案）。
	// 存储用的是 sha256(小写 trim 后的令牌值)，这里按同样规则生成。
	tokenDigest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(chaoxingPlusAccessToken))))
	tokenHash := hex.EncodeToString(tokenDigest[:])
	var token entity.AccessToken
	if err := db.Where("token_hash = ?", tokenHash).First(&token).Error; err != nil {
		token = entity.AccessToken{
			UserID:      user.ID,
			Name:        "chaoxing-plus",
			TokenHash:   tokenHash,
			TokenPrefix: chaoxingPlusAccessToken[:8],
			TokenValue:  chaoxingPlusAccessToken,
			Status:      "enabled",
		}
		if err := db.Create(&token).Error; err != nil {
			t.Fatalf("seed access token: %v", err)
		}
	}

	// 余额在计费测试中会被扣减，每轮重置回 50，保证用例互不影响。
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 50).Error; err != nil {
		t.Fatalf("reset user balance: %v", err)
	}
	user.Balance = 50

	// 3) 默认 AI 账号指向假上游，并使用价格表里已存在的模型名。
	if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: upstream.server.URL,
		APIKey:  "sk-chaoxing-plus-upstream",
		Model:   "GPT-5.6 Sol",
	}); err != nil {
		t.Fatalf("seed ai search config: %v", err)
	}

	// 4) 价格表必须已经初始化，否则会按 0 计费。
	if err := service.EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed model prices: %v", err)
	}
	if _, found, err := service.ResolveAIModelTokenPriceWithDB(db, "GPT-5.6 Sol"); err != nil || !found {
		t.Fatalf("expected GPT-5.6 Sol to be priced, found=%t err=%v", found, err)
	}

	router := gin.New()
	api.SetAPIRouter(router)

	return &compatHarness{router: router, upstream: upstream, token: token, user: user}
}

func (h *compatHarness) post(t *testing.T, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", chaoxingPlusAuthorization)
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h *compatHarness) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", chaoxingPlusAuthorization)
	h.router.ServeHTTP(recorder, request)
	return recorder
}

// chaoxingPlusSearchResponse 复刻 chaoxing_plus handler 读取的字段。
type chaoxingPlusSearchResponse struct {
	Plat     int      `json:"plat"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Type     int      `json:"type"`
	Answer   struct {
		AnswerKey     []string   `json:"answerKey"`
		AnswerKeyText string     `json:"answerKeyText"`
		AnswerIndex   []int      `json:"answerIndex"`
		AnswerText    string     `json:"answerText"`
		BestAnswer    []string   `json:"bestAnswer"`
		AllAnswer     [][]string `json:"allAnswer"`
	} `json:"answer"`
}

// resolveChaoxingPlusAnswer 复刻 chaoxing_plus `createTikuAdapterAnswererWrapper` 的 handler 逻辑，
// 用来证明客户端真的能从这个响应里取到答案。
func resolveChaoxingPlusAnswer(res chaoxingPlusSearchResponse) string {
	question := res.Question
	_ = question
	isChoiceType := res.Type == 0 || res.Type == 1

	if isChoiceType && res.Answer.AnswerKeyText != "" {
		return res.Answer.AnswerKeyText
	}
	answerIndex := make([]int, 0, len(res.Answer.AnswerIndex))
	for _, index := range res.Answer.AnswerIndex {
		if index >= 0 {
			answerIndex = append(answerIndex, index)
		}
	}
	if isChoiceType && len(answerIndex) > 0 {
		letters := ""
		for _, index := range answerIndex {
			letters += string(rune(index + 65))
		}
		return letters
	}
	if res.Answer.AnswerText != "" {
		return res.Answer.AnswerText
	}
	if len(res.Answer.BestAnswer) > 0 {
		joined := ""
		for index, item := range res.Answer.BestAnswer {
			if index > 0 {
				joined += "#"
			}
			joined += item
		}
		if joined != "" {
			return joined
		}
	}
	for _, group := range res.Answer.AllAnswer {
		if len(group) > 0 {
			joined := ""
			for index, item := range group {
				if index > 0 {
					joined += "#"
				}
				joined += item
			}
			return joined
		}
	}
	return ""
}

func decodeSearchResponse(t *testing.T, recorder *httptest.ResponseRecorder) chaoxingPlusSearchResponse {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	var response chaoxingPlusSearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode search response: %v (body %s)", err, recorder.Body.String())
	}
	return response
}

// buildSearchBody 复刻 chaoxing_plus 的 fetch 请求体（qid/plat 固定为 "" 与 -1）。
func buildSearchBody(t *testing.T, question string, questionType int, options []string) string {
	t.Helper()
	return buildSearchBodyWithExtra(t, question, questionType, options, "", "")
}

func buildSearchBodyWithExtra(t *testing.T, question string, questionType int, options []string, courseName string, extra string) string {
	t.Helper()

	if options == nil {
		options = []string{}
	}
	payload := map[string]any{
		"qid":        "",
		"plat":       -1,
		"question":   question,
		"options":    options,
		"type":       questionType,
		"courseName": courseName,
		"extra":      extra,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode search body: %v", err)
	}
	return string(raw)
}

// buildAIFallbackBody 复刻 chaoxing_plus `buildTikuAdapterAIFallbackPayload` 生成的请求体。
func buildAIFallbackBody(t *testing.T, question string, questionType int, options []string) string {
	t.Helper()

	if options == nil {
		options = []string{}
	}
	raw, err := json.Marshal(map[string]any{
		"question": question,
		"type":     questionType,
		"options":  options,
	})
	if err != nil {
		t.Fatalf("encode ai fallback body: %v", err)
	}
	return string(raw)
}

// assertBilled 校验价格表计费确实落到了余额与账单上。
//
// 上游固定返回 prompt=2000 / cached=512 / completion=300，
// GPT-5.6 Sol 单价为 输入 4 / 缓存 0.4 / 输出 20（元每百万 token）：
//
//	(2000-512)/1e6*4 + 512/1e6*0.4 + 300/1e6*20
//	= 0.005952 + 0.0002048 + 0.006 = 0.0121568 → 四舍五入入账 0.01
func assertBilled(t *testing.T, harness *compatHarness, promptTokens int, completionTokens int, cachedTokens int) {
	t.Helper()

	if harness.upstream.callCount() == 0 {
		t.Fatal("expected the AI upstream to be called before billing")
	}

	db := compatManager.GetDB()
	var user entity.User
	if err := db.Where("id = ?", harness.user.ID).First(&user).Error; err != nil {
		t.Fatalf("load billed user: %v", err)
	}
	if user.Balance != 49.99 {
		t.Fatalf("expected balance 50 -> 49.99 after price-table billing, got %v", user.Balance)
	}

	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND provider = ?", harness.user.ID, "ai").
		Order("id desc").First(&record).Error; err != nil {
		t.Fatalf("load ai billing record: %v", err)
	}
	if record.ModelName != "GPT-5.6 Sol" {
		t.Fatalf("expected model GPT-5.6 Sol, got %q", record.ModelName)
	}
	if record.InputTokens != int64(promptTokens) || record.CachedInputTokens != int64(cachedTokens) || record.OutputTokens != int64(completionTokens) {
		t.Fatalf("unexpected token accounting: %+v", record)
	}
	if record.Amount != 0.01 {
		t.Fatalf("expected price-table amount 0.01, got %v (snapshot %q)", record.Amount, record.UnitPriceSnapshot)
	}
	for _, part := range []string{"pricing=model_price_table", "inputPrice=4.0000", "cachedPrice=0.4000", "outputPrice=20.0000"} {
		if !strings.Contains(record.UnitPriceSnapshot, part) {
			t.Fatalf("expected billing snapshot %q to contain %q", record.UnitPriceSnapshot, part)
		}
	}
	if strings.Contains(record.UnitPriceSnapshot, "billingMultiplier") {
		t.Fatalf("expected multiplier billing to be absent, got %q", record.UnitPriceSnapshot)
	}
}

// chaoxing_plus 以 `Authorization: Bearer <key>` 调用 /adapter-service/search。
func TestChaoxingPlusBearerTokenIsAcceptedBySearch(t *testing.T) {
	harness := newCompatHarness(t)

	// 与 createTikuAdapterAnswererWrapper 的 data 模板一致的请求体。
	body := buildSearchBody(t, singleChoiceQuestion, 0, choiceOptions)
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	if response.Type != 0 {
		t.Fatalf("expected echoed type 0, got %d", response.Type)
	}
	if len(response.Options) != 4 {
		t.Fatalf("expected 4 echoed options, got %+v", response.Options)
	}

	answer := resolveChaoxingPlusAnswer(response)
	if answer == "" {
		t.Fatalf("chaoxing_plus would resolve an empty answer from %s", recorder.Body.String())
	}
	// 单选题必须能落到选项字母上，否则客户端无法勾选。
	if response.Answer.AnswerKeyText != "A" {
		t.Fatalf("expected answerKeyText A for single choice, got %q (body %s)", response.Answer.AnswerKeyText, recorder.Body.String())
	}
	if answer != response.Answer.AnswerKeyText {
		t.Fatalf("expected chaoxing_plus to pick answerKeyText %q, got %q", response.Answer.AnswerKeyText, answer)
	}
	if harness.upstream.callCount() == 0 {
		t.Fatal("expected the AI upstream to be called")
	}

	// 价格表计费必须真的扣到余额上（GPT-5.6 Sol：输入 4 / 缓存 0.4 / 输出 20 元每百万 token）。
	assertBilled(t, harness, 2000, 300, 512)
}

// 多选题：客户端要求至少两个选项才能通过 len(ans) > 1 的判断。
func TestChaoxingPlusMultipleChoiceYieldsMultipleKeys(t *testing.T) {
	harness := newCompatHarness(t)

	body := buildSearchBody(t, multipleChoiceQuestion, 1, choiceOptions)
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	answer := resolveChaoxingPlusAnswer(response)
	if response.Answer.AnswerKeyText != "AB" {
		t.Fatalf("expected answerKeyText AB for multiple choice, got %q (body %s)", response.Answer.AnswerKeyText, recorder.Body.String())
	}
	if answer != "AB" {
		t.Fatalf("expected chaoxing_plus to resolve AB, got %q", answer)
	}
	assertBilled(t, harness, 2000, 300, 512)
}

// 判断题（type=3）：客户端按非选择题处理，取 answerText。
func TestChaoxingPlusJudgementQuestionYieldsAnswerText(t *testing.T) {
	harness := newCompatHarness(t)

	body := buildSearchBody(t, judgementQuestion, 3, nil)
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	answer := resolveChaoxingPlusAnswer(response)
	if answer != "正确" {
		t.Fatalf("expected judgement answer 正确, got %q (body %s)", answer, recorder.Body.String())
	}
	if response.Answer.AnswerText != "正确" {
		t.Fatalf("expected answerText 正确 for judgement question, got %q (body %s)", response.Answer.AnswerText, recorder.Body.String())
	}
	assertBilled(t, harness, 2000, 300, 512)
}

// 填空题（type=2）：按空返回，客户端取 answerText。
func TestChaoxingPlusCompletionQuestionKeepsBlankOrder(t *testing.T) {
	harness := newCompatHarness(t)

	body := buildSearchBody(t, "中国的首都是____，最大的城市是____。", 2, nil)
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	if response.Answer.AnswerText != "北京#上海" {
		t.Fatalf("expected ordered fill answers 北京#上海, got %q (body %s)", response.Answer.AnswerText, recorder.Body.String())
	}
	assertBilled(t, harness, 2000, 300, 512)
}

// 阅读理解/问答题（type=4，chaoxing_plus 对 reader/unknown 都发送 4）：必须保留完整多小问作答。
func TestChaoxingPlusReaderQuestionKeepsFullAnswer(t *testing.T) {
	harness := newCompatHarness(t)

	body := buildSearchBodyWithExtra(t, readerQuestion, 4, nil, "信号与系统", "来自 chaoxing_plus OCR")
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	answer := resolveChaoxingPlusAnswer(response)
	if answer == "" {
		t.Fatalf("expected a reader answer, got body %s", recorder.Body.String())
	}
	for _, fragment := range []string{"极点", "收敛域", "h(n)="} {
		if !strings.Contains(answer, fragment) {
			t.Fatalf("expected full reader answer to contain %q, got %q", fragment, answer)
		}
	}
}

// 普通题库全部未命中时的 AI 兜底链路：
// chaoxing_plus 先 POST /ai-fallback/tasks，返回 pending 后轮询 /ai-fallback/status。
func TestChaoxingPlusAIFallbackTaskFlow(t *testing.T) {
	harness := newCompatHarness(t)

	recorder := harness.post(t, "/adapter-service/ai-fallback/tasks",
		buildAIFallbackBody(t, singleChoiceQuestion, 0, choiceOptions))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for async task, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var created struct {
		Success   bool   `json:"success"`
		Status    string `json:"status"`
		TaskID    string `json:"taskId"`
		StatusURL string `json:"statusUrl"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode task response: %v", err)
	}

	// chaoxing_plus 只有在 taskId/statusUrl 存在且 status 属于
	// pending/running/processing/queued/created 时才会进入轮询。
	if created.TaskID == "" || created.StatusURL == "" {
		t.Fatalf("expected taskId and statusUrl, got %s", recorder.Body.String())
	}
	if !isChaoxingPlusPendingStatus(created.Status) {
		t.Fatalf("chaoxing_plus would NOT poll this status: %q (body %s)", created.Status, recorder.Body.String())
	}

	// 轮询直到任务完成，并校验最终结果结构与 chaoxing_plus 的解析一致。
	final := pollUntilSettled(t, harness, created.StatusURL)
	if !final.Success {
		t.Fatalf("expected successful fallback result, got %s", final.RawBody)
	}
	if final.Result == nil || final.Result.Answer == "" {
		t.Fatalf("expected result.answer, got %s", final.RawBody)
	}
	if final.Result.Question != singleChoiceQuestion {
		t.Fatalf("expected echoed question, got %q", final.Result.Question)
	}
}

type fallbackPollResult struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	RawBody string
	Result  *struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// 同步兜底：chaoxing_plus 在 tasks 返回 404 时会回退到 POST /ai-fallback，
// 因此该端点的 JSON 结构必须保持稳定（result.answer / error.code）。
func TestChaoxingPlusSyncAIFallbackEndpointKeepsResponseShape(t *testing.T) {
	harness := newCompatHarness(t)

	recorder := harness.post(t, "/adapter-service/ai-fallback",
		buildAIFallbackBody(t, singleChoiceQuestion, 0, choiceOptions))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Success bool `json:"success"`
		Result  *struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode sync fallback response: %v", err)
	}
	if !response.Success {
		t.Fatalf("expected success, got %s", recorder.Body.String())
	}
	if response.Result == nil || response.Result.Answer == "" {
		t.Fatalf("expected result.answer, got %s", recorder.Body.String())
	}
	if response.Result.Question != singleChoiceQuestion {
		t.Fatalf("expected echoed question, got %q", response.Result.Question)
	}
}

// chaoxing_plus 的 data 模板会带上 skipCache（可能为 undefined，此时字段被省略）。
// 两种形态都必须被接受，且 skipCache 不能污染答案字段。
func TestChaoxingPlusSkipCachePayloadVariantsAreAccepted(t *testing.T) {
	harness := newCompatHarness(t)

	for _, skipCache := range []string{"", `,"skipCache":true`} {
		body := strings.TrimSuffix(buildSearchBody(t, singleChoiceQuestion, 0, choiceOptions), "}") + skipCache + "}"
		recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

		response := decodeSearchResponse(t, recorder)
		if response.Answer.AnswerKeyText != "A" {
			t.Fatalf("expected key A with skipCache=%q, got %q (body %s)", skipCache, response.Answer.AnswerKeyText, recorder.Body.String())
		}
	}
}

// type=unknown/reader 之外的未知题型：chaoxing_plus 会发送 4，服务端必须回退到大作业/文本作答路径。
func TestChaoxingPlusUnknownQuestionTypeFallsBackToTextAnswer(t *testing.T) {
	harness := newCompatHarness(t)

	body := buildSearchBody(t, readerQuestion, 4, nil)
	recorder := harness.post(t, "/adapter-service/search?use=ai&noRecord=1", body)

	response := decodeSearchResponse(t, recorder)
	if response.Type != 4 {
		t.Fatalf("expected echoed type 4, got %d", response.Type)
	}
	if len(response.Answer.AllAnswer) == 0 {
		t.Fatalf("expected at least one answer group, got body %s", recorder.Body.String())
	}

	// type=4 且只有一个答案组时必须整组保留，不能被裁剪成一句。
	group := response.Answer.AllAnswer[0]
	if len(group) == 0 {
		t.Fatalf("expected non-empty answer group, got body %s", recorder.Body.String())
	}
	if !strings.Contains(group[0], "极点") {
		t.Fatalf("expected full text answer, got %q", group[0])
	}
}

func isChaoxingPlusPendingStatus(status string) bool {
	switch status {
	case "pending", "running", "processing", "queued", "created":
		return true
	default:
		return false
	}
}

func pollUntilSettled(t *testing.T, harness *compatHarness, statusURL string) fallbackPollResult {
	t.Helper()

	var last fallbackPollResult
	for attempt := 0; attempt < 300; attempt++ {
		recorder := harness.get(t, statusURL)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected status 200 while polling, got %d with body %s", recorder.Code, recorder.Body.String())
		}
		last = fallbackPollResult{RawBody: recorder.Body.String()}
		if err := json.Unmarshal(recorder.Body.Bytes(), &last); err != nil {
			t.Fatalf("decode poll response: %v", err)
		}
		if !isChaoxingPlusPendingStatus(last.Status) {
			return last
		}
		// 与 chaoxing_plus 的 TIKU_ADAPTER_AI_FALLBACK_STATUS_POLL_INTERVAL_MS 一致的轮询节奏，
		// 但缩短到 10ms 以免测试变慢。
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task never settled, last response %s", last.RawBody)
	return last
}
