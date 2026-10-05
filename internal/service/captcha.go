package service

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"gorm.io/gorm"
)

const (
	CaptchaSceneLoginPassword   = "login_password"
	CaptchaSceneLoginSMS        = "login_sms"
	CaptchaSceneRegisterSubmit  = "register_submit"
	CaptchaSceneSMSSendLogin    = "sms_send_login"
	CaptchaSceneSMSSendRegister = "sms_send_register"

	CaptchaChallengeStatusPending  = "pending"
	CaptchaChallengeStatusVerified = "verified"
	CaptchaChallengeStatusExpired  = "expired"

	CaptchaTokenStatusActive   = "active"
	CaptchaTokenStatusConsumed = "consumed"
	CaptchaTokenStatusExpired  = "expired"
)

const captchaTTL = 2 * time.Minute

type CaptchaChallengeResult struct {
	ChallengeID string    `json:"challengeId"`
	ImageData   string    `json:"imageData"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type CaptchaTokenResult struct {
	Token     string    `json:"captchaToken"`
	Scene     string    `json:"scene"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type RemoteCaptchaVerifyInput struct {
	Scene      string
	CaptchaID  string
	RequestIP  string
	APIBaseURL string
	APIKey     string
	Client     *http.Client
}

type RemoteCaptchaProxyInput struct {
	DB          *gorm.DB
	APIBaseURL  string
	APIKey      string
	CaptchaType string
	Body        []byte
	RequestIP   string
	Client      *http.Client
}

var captchaNow = time.Now
var defaultCaptchaCodeGenerator = func() string {
	return randomAlphaNumeric(6)
}
var captchaCodeGenerator = defaultCaptchaCodeGenerator

const captchaAPIKeyHeader = "X-Captcha-Key"

const (
	captchaLogModule                  = "captcha"
	captchaLogActionRemoteGenerate    = int32(20)
	captchaLogActionRemoteCheck       = int32(21)
	captchaLogActionRemoteSecondCheck = int32(22)

	remoteCaptchaLogStepGenerate    = "remote_gen"
	remoteCaptchaLogStepCheck       = "remote_check"
	remoteCaptchaLogStepSecondCheck = "remote_check2"
)

type remoteCaptchaLogEvent struct {
	Action           int32
	Step             string
	Result           string
	HTTPStatus       int
	RemoteCode       string
	RemoteValid      string
	Scene            string
	CaptchaID        string
	CaptchaType      string
	HasKey           bool
	RequestIP        string
	Duration         time.Duration
	RequestSnapshot  string
	ResponseSnapshot string
}

func captchaChallengeDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.CaptchaChallenge{})
}

func captchaTokenDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.CaptchaToken{})
}

func randomAlphaNumeric(length int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, length)
	raw := make([]byte, length)
	_, _ = cryptorand.Read(raw)
	for i := range buf {
		buf[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(buf)
}

func randomHex(length int) string {
	buf := make([]byte, length)
	_, _ = cryptorand.Read(buf)
	return hex.EncodeToString(buf)
}

func truncateLogText(value string, limit int) string {
	text := strings.TrimSpace(value)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func boolLogValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func writeRemoteCaptchaLog(db *gorm.DB, event remoteCaptchaLogEvent) {
	if db == nil {
		return
	}
	result := strings.TrimSpace(event.Result)
	if result == "" {
		result = "failed"
	}
	step := strings.TrimSpace(event.Step)
	if step == "" {
		step = "unknown"
	}
	descriptionPrefix := "远程验证码"
	switch step {
	case remoteCaptchaLogStepGenerate:
		descriptionPrefix = "远程验证码生成"
	case remoteCaptchaLogStepCheck:
		descriptionPrefix = "远程验证码校验"
	case remoteCaptchaLogStepSecondCheck:
		descriptionPrefix = "远程验证码二验"
	}

	parts := []string{
		descriptionPrefix,
		fmt.Sprintf("step=%s", step),
		fmt.Sprintf("result=%s", result),
	}
	if event.HTTPStatus > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", event.HTTPStatus))
	}
	if value := strings.TrimSpace(event.RemoteCode); value != "" {
		parts = append(parts, fmt.Sprintf("remoteCode=%s", value))
	}
	if value := strings.TrimSpace(event.RemoteValid); value != "" {
		parts = append(parts, fmt.Sprintf("remoteValid=%s", value))
	}
	if value := strings.TrimSpace(event.Scene); value != "" {
		parts = append(parts, fmt.Sprintf("scene=%s", value))
	}
	if value := strings.TrimSpace(event.CaptchaID); value != "" {
		parts = append(parts, fmt.Sprintf("captchaId=%s", value))
	}
	if value := strings.TrimSpace(event.CaptchaType); value != "" {
		parts = append(parts, fmt.Sprintf("type=%s", value))
	}
	if event.Duration > 0 {
		parts = append(parts, fmt.Sprintf("durationMs=%d", event.Duration.Milliseconds()))
	}
	parts = append(parts, fmt.Sprintf("hasKey=%s", boolLogValue(event.HasKey)))

	entry := entity.Log{
		UserID:      0,
		Username:    "anonymous",
		Module:      captchaLogModule,
		Action:      event.Action,
		Description: truncateLogText(strings.Join(parts, " "), 255),
		ClientIP:    strings.TrimSpace(event.RequestIP),
		OldAnswer:   truncateLogText(event.RequestSnapshot, 4000),
		NewAnswer:   truncateLogText(event.ResponseSnapshot, 4000),
		CreateTime:  captchaNow(),
	}
	_ = db.Session(&gorm.Session{NewDB: true}).Create(&entry).Error
}

func extractRemoteCaptchaPayloadMetadata(body []byte) (captchaID string, remoteCode string, remoteValid string) {
	text := strings.TrimSpace(string(body))
	if text == "true" || text == "false" {
		return "", "", text
	}
	if text == "" {
		return "", "", ""
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", ""
	}
	remoteCode = jsonScalarToLogText(payload["code"])
	if value := jsonScalarToLogText(payload["success"]); value != "" {
		remoteValid = value
	}
	for _, key := range []string{"id", "captchaId", "captcha_id"} {
		if value := jsonScalarToLogText(payload[key]); value != "" {
			captchaID = value
			return captchaID, remoteCode, remoteValid
		}
	}
	if data, ok := payload["data"].(map[string]any); ok {
		for _, key := range []string{"id", "captchaId", "captcha_id"} {
			if value := jsonScalarToLogText(data[key]); value != "" {
				captchaID = value
				return captchaID, remoteCode, remoteValid
			}
		}
		if value := jsonScalarToLogText(data["success"]); value != "" && remoteValid == "" {
			remoteValid = value
		}
	}
	return captchaID, remoteCode, remoteValid
}

func jsonScalarToLogText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case bool:
		return boolLogValue(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%g", typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func extractRemoteCaptchaIDFromBody(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"id", "captchaId", "captcha_id"} {
		if value := jsonScalarToLogText(payload[key]); value != "" {
			return value
		}
	}
	if data, ok := payload["data"].(map[string]any); ok {
		for _, key := range []string{"id", "captchaId", "captcha_id"} {
			if value := jsonScalarToLogText(data[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func hashCaptchaValue(value string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}

func isSupportedCaptchaScene(scene string) bool {
	switch strings.TrimSpace(scene) {
	case CaptchaSceneLoginPassword, CaptchaSceneLoginSMS, CaptchaSceneRegisterSubmit, CaptchaSceneSMSSendLogin, CaptchaSceneSMSSendRegister:
		return true
	default:
		return false
	}
}

func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	buf := make([]byte, 1)
	if _, err := cryptorand.Read(buf); err != nil {
		return 0
	}
	return int(buf[0]) % max
}

func buildCaptchaImageData(code string) string {
	img := image.NewRGBA(image.Rect(0, 0, 160, 56))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{245, 247, 250, 255}}, image.Point{}, draw.Src)

	for x := 0; x < 160; x += 8 {
		for y := 0; y < 56; y += 8 {
			if (x/8+y/8)%2 == 0 {
				img.Set(x, y, color.RGBA{226, 232, 240, 255})
			}
		}
	}

	for i := 0; i < 10; i++ {
		img.Set(randomInt(160), randomInt(56), color.RGBA{148, 163, 184, 255})
	}

	normalizedCode := strings.ToUpper(strings.TrimSpace(code))
	baseX := 14
	for index, r := range normalizedCode {
		drawer := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.RGBA{30, 41, 59, 255}),
			Face: basicfont.Face7x13,
			Dot:  fixed.P(baseX+index*22+randomInt(7), 28+randomInt(14)),
		}
		drawer.DrawString(string(r))
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "data:image/png;base64,"
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func CreateCaptchaChallengeWithDB(db *gorm.DB, scene string, requestIP string) (CaptchaChallengeResult, error) {
	if !isSupportedCaptchaScene(scene) {
		return CaptchaChallengeResult{}, fmt.Errorf("人机验证场景不正确")
	}
	challengeDB := captchaChallengeDB(db)
	code := captchaCodeGenerator()
	now := captchaNow()
	challengeID := randomHex(16)
	record := entity.CaptchaChallenge{
		ChallengeID: challengeID,
		Scene:       strings.TrimSpace(scene),
		AnswerHash:  hashCaptchaValue(code),
		Status:      CaptchaChallengeStatusPending,
		RequestIP:   strings.TrimSpace(requestIP),
		ExpiresAt:   now.Add(captchaTTL),
	}
	if err := challengeDB.Create(&record).Error; err != nil {
		return CaptchaChallengeResult{}, err
	}
	return CaptchaChallengeResult{
		ChallengeID: challengeID,
		ImageData:   buildCaptchaImageData(code),
		ExpiresAt:   record.ExpiresAt,
	}, nil
}

func VerifyCaptchaChallengeWithDB(db *gorm.DB, challengeID string, scene string, captchaCode string, requestIP string) (CaptchaTokenResult, error) {
	challengeDB := captchaChallengeDB(db)
	tokenDB := captchaTokenDB(db)

	var record entity.CaptchaChallenge
	if err := challengeDB.Where("challenge_id = ? AND scene = ?", strings.TrimSpace(challengeID), strings.TrimSpace(scene)).First(&record).Error; err != nil {
		return CaptchaTokenResult{}, fmt.Errorf("人机验证不存在或已过期")
	}
	if record.Status != CaptchaChallengeStatusPending || captchaNow().After(record.ExpiresAt) {
		_ = captchaChallengeDB(db).Model(&record).Update("status", CaptchaChallengeStatusExpired).Error
		return CaptchaTokenResult{}, fmt.Errorf("人机验证不存在或已过期")
	}
	if record.AnswerHash != hashCaptchaValue(captchaCode) {
		return CaptchaTokenResult{}, fmt.Errorf("图形验证码错误")
	}

	now := captchaNow()
	verifiedAt := now
	if err := captchaChallengeDB(db).Model(&record).Updates(map[string]any{
		"status":      CaptchaChallengeStatusVerified,
		"verified_at": &verifiedAt,
	}).Error; err != nil {
		return CaptchaTokenResult{}, err
	}

	tokenValue := randomHex(24)
	token := entity.CaptchaToken{
		Token:       tokenValue,
		Scene:       record.Scene,
		ChallengeID: record.ChallengeID,
		RequestIP:   strings.TrimSpace(requestIP),
		Status:      CaptchaTokenStatusActive,
		ExpiresAt:   now.Add(captchaTTL),
	}
	if err := tokenDB.Create(&token).Error; err != nil {
		return CaptchaTokenResult{}, err
	}
	return CaptchaTokenResult{Token: tokenValue, Scene: token.Scene, ExpiresAt: token.ExpiresAt}, nil
}

func VerifyRemoteCaptchaWithDB(db *gorm.DB, input RemoteCaptchaVerifyInput) (CaptchaTokenResult, error) {
	scene := strings.TrimSpace(input.Scene)
	captchaID := strings.TrimSpace(input.CaptchaID)
	startedAt := captchaNow()
	logBase := remoteCaptchaLogEvent{
		Action:          captchaLogActionRemoteSecondCheck,
		Step:            remoteCaptchaLogStepSecondCheck,
		Scene:           scene,
		CaptchaID:       captchaID,
		HasKey:          strings.TrimSpace(input.APIKey) != "",
		RequestIP:       input.RequestIP,
		RequestSnapshot: buildRemoteCaptchaRequestSnapshot(http.MethodGet, "/check2", map[string]string{"id": captchaID}, nil, strings.TrimSpace(input.APIKey) != ""),
	}
	writeSecondCheckLog := func(result string, status int, remoteCode string, remoteValid string, responseSnapshot string) {
		event := logBase
		event.Result = result
		event.HTTPStatus = status
		event.RemoteCode = remoteCode
		event.RemoteValid = remoteValid
		event.Duration = captchaNow().Sub(startedAt)
		event.ResponseSnapshot = responseSnapshot
		writeRemoteCaptchaLog(db, event)
	}
	if !isSupportedCaptchaScene(scene) {
		writeSecondCheckLog("failed", 0, "", "", "localError=人机验证场景不正确")
		return CaptchaTokenResult{}, fmt.Errorf("人机验证场景不正确")
	}
	if captchaID == "" {
		writeSecondCheckLog("failed", 0, "", "", "localError=远程验证码 ID 不能为空")
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码 ID 不能为空")
	}
	client := input.Client
	if client == nil {
		client = util.NewSafeHTTPClient(10 * time.Second)
	}
	req, err := newRemoteCaptchaRequest(http.MethodGet, input.APIBaseURL, "/check2", map[string]string{"id": captchaID}, nil, input.APIKey)
	if err != nil {
		writeSecondCheckLog("failed", 0, "", "", "localError=远程验证码服务地址不正确")
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码服务地址不正确")
	}
	resp, err := client.Do(req)
	if err != nil {
		writeSecondCheckLog("failed", 0, "", "", "remoteError=远程验证码校验失败")
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码校验失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		writeSecondCheckLog("failed", resp.StatusCode, "", "", "localError=远程验证码响应解析失败")
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码响应解析失败")
	}
	_, remoteCode, remoteValid := extractRemoteCaptchaPayloadMetadata(body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		writeSecondCheckLog("failed", resp.StatusCode, remoteCode, remoteValid, string(body))
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码校验失败")
	}
	valid, message, err := parseRemoteCaptchaCheckResponse(body)
	if err != nil {
		writeSecondCheckLog("failed", resp.StatusCode, remoteCode, remoteValid, string(body))
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码响应解析失败")
	}
	if remoteValid == "" {
		remoteValid = boolLogValue(valid)
	}
	if !valid {
		if message == "" {
			message = "远程验证码校验失败"
		}
		writeSecondCheckLog("failed", resp.StatusCode, remoteCode, remoteValid, string(body))
		return CaptchaTokenResult{}, fmt.Errorf("%s", message)
	}

	now := captchaNow()
	tokenValue := randomHex(24)
	token := entity.CaptchaToken{
		Token:       tokenValue,
		Scene:       scene,
		ChallengeID: "remote:" + captchaID,
		RequestIP:   strings.TrimSpace(input.RequestIP),
		Status:      CaptchaTokenStatusActive,
		ExpiresAt:   now.Add(captchaTTL),
	}
	if err := captchaTokenDB(db).Create(&token).Error; err != nil {
		writeSecondCheckLog("failed", resp.StatusCode, remoteCode, remoteValid, string(body)+" localError="+err.Error())
		return CaptchaTokenResult{}, err
	}
	writeSecondCheckLog("success", resp.StatusCode, remoteCode, remoteValid, string(body))
	return CaptchaTokenResult{Token: tokenValue, Scene: token.Scene, ExpiresAt: token.ExpiresAt}, nil
}

func FetchRemoteCaptchaWithDB(input RemoteCaptchaProxyInput) ([]byte, string, error) {
	client := input.Client
	if client == nil {
		client = util.NewSafeHTTPClient(10 * time.Second)
	}
	captchaType := normalizeCaptchaType(input.CaptchaType)
	startedAt := captchaNow()
	logBase := remoteCaptchaLogEvent{
		Action:          captchaLogActionRemoteGenerate,
		Step:            remoteCaptchaLogStepGenerate,
		CaptchaType:     captchaType,
		HasKey:          strings.TrimSpace(input.APIKey) != "",
		RequestIP:       input.RequestIP,
		RequestSnapshot: buildRemoteCaptchaRequestSnapshot(http.MethodGet, "/gen", map[string]string{"type": captchaType}, nil, strings.TrimSpace(input.APIKey) != ""),
	}
	req, err := newRemoteCaptchaRequest(http.MethodGet, input.APIBaseURL, "/gen", map[string]string{
		"type": captchaType,
	}, nil, input.APIKey)
	if err != nil {
		logBase.Result = "failed"
		logBase.Duration = captchaNow().Sub(startedAt)
		logBase.ResponseSnapshot = "localError=远程验证码服务地址不正确"
		writeRemoteCaptchaLog(input.DB, logBase)
		return nil, "", fmt.Errorf("远程验证码服务地址不正确")
	}
	body, contentType, status, err := doRemoteCaptchaProxyRequest(client, req)
	captchaID, remoteCode, remoteValid := extractRemoteCaptchaPayloadMetadata(body)
	logBase.Result = "success"
	if err != nil {
		logBase.Result = "failed"
	}
	logBase.HTTPStatus = status
	logBase.CaptchaID = captchaID
	logBase.RemoteCode = remoteCode
	logBase.RemoteValid = remoteValid
	logBase.Duration = captchaNow().Sub(startedAt)
	logBase.ResponseSnapshot = string(body)
	if err != nil && len(body) == 0 {
		logBase.ResponseSnapshot = "remoteError=" + err.Error()
	}
	writeRemoteCaptchaLog(input.DB, logBase)
	return body, contentType, err
}

func CheckRemoteCaptchaWithDB(input RemoteCaptchaProxyInput) ([]byte, string, error) {
	client := input.Client
	if client == nil {
		client = util.NewSafeHTTPClient(10 * time.Second)
	}
	startedAt := captchaNow()
	requestCaptchaID := extractRemoteCaptchaIDFromBody(input.Body)
	logBase := remoteCaptchaLogEvent{
		Action:          captchaLogActionRemoteCheck,
		Step:            remoteCaptchaLogStepCheck,
		CaptchaID:       requestCaptchaID,
		HasKey:          strings.TrimSpace(input.APIKey) != "",
		RequestIP:       input.RequestIP,
		RequestSnapshot: buildRemoteCaptchaRequestSnapshot(http.MethodPost, "/check", nil, input.Body, strings.TrimSpace(input.APIKey) != ""),
	}
	req, err := newRemoteCaptchaRequest(http.MethodPost, input.APIBaseURL, "/check", nil, bytes.NewReader(input.Body), input.APIKey)
	if err != nil {
		logBase.Result = "failed"
		logBase.Duration = captchaNow().Sub(startedAt)
		logBase.ResponseSnapshot = "localError=远程验证码服务地址不正确"
		writeRemoteCaptchaLog(input.DB, logBase)
		return nil, "", fmt.Errorf("远程验证码服务地址不正确")
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	body, contentType, status, err := doRemoteCaptchaProxyRequest(client, req)
	responseCaptchaID, remoteCode, remoteValid := extractRemoteCaptchaPayloadMetadata(body)
	if responseCaptchaID != "" {
		logBase.CaptchaID = responseCaptchaID
	}
	logBase.Result = "success"
	if err != nil {
		logBase.Result = "failed"
	}
	logBase.HTTPStatus = status
	logBase.RemoteCode = remoteCode
	logBase.RemoteValid = remoteValid
	logBase.Duration = captchaNow().Sub(startedAt)
	logBase.ResponseSnapshot = string(body)
	if err != nil && len(body) == 0 {
		logBase.ResponseSnapshot = "remoteError=" + err.Error()
	}
	writeRemoteCaptchaLog(input.DB, logBase)
	return body, contentType, err
}

func newRemoteCaptchaRequest(method string, apiBaseURL string, path string, query map[string]string, body io.Reader, apiKey string) (*http.Request, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultCaptchaAPIBaseURL
	}
	parsed, err := url.Parse(baseURL + path)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid remote captcha url")
	}
	if len(query) > 0 {
		values := parsed.Query()
		for key, value := range query {
			values.Set(key, value)
		}
		parsed.RawQuery = values.Encode()
	}
	req, err := http.NewRequest(method, parsed.String(), body)
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set(captchaAPIKeyHeader, key)
	}
	return req, nil
}

func buildRemoteCaptchaRequestSnapshot(method string, path string, query map[string]string, body []byte, hasKey bool) string {
	parts := []string{
		"method=" + strings.TrimSpace(method),
		"path=" + strings.TrimSpace(path),
	}
	if len(query) > 0 {
		values := url.Values{}
		for key, value := range query {
			values.Set(key, value)
		}
		parts = append(parts, "query="+values.Encode())
	}
	if len(body) > 0 {
		parts = append(parts, "body="+truncateLogText(string(body), 2000))
	}
	parts = append(parts, "hasKey="+boolLogValue(hasKey))
	return strings.Join(parts, " ")
}

func doRemoteCaptchaProxyRequest(client *http.Client, req *http.Request) ([]byte, string, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("远程验证码请求失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, "", resp.StatusCode, fmt.Errorf("远程验证码响应解析失败")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return body, contentType, resp.StatusCode, fmt.Errorf("远程验证码请求失败")
	}
	return body, contentType, resp.StatusCode, nil
}

func parseRemoteCaptchaCheckResponse(body []byte) (bool, string, error) {
	text := strings.TrimSpace(string(body))
	switch text {
	case "true":
		return true, "", nil
	case "false":
		return false, "", nil
	case "":
		return false, "", fmt.Errorf("empty remote captcha response")
	}

	var payload struct {
		Code    int    `json:"code"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, "", err
	}
	if payload.Success || payload.Code == http.StatusOK {
		return true, "", nil
	}
	return false, strings.TrimSpace(payload.Message), nil
}

func ConsumeCaptchaTokenWithDB(db *gorm.DB, tokenValue string, scene string, requestIP string) error {
	tokenDB := captchaTokenDB(db)

	trimmedToken := strings.TrimSpace(tokenValue)
	if trimmedToken == "" {
		return fmt.Errorf("请先完成人机验证")
	}

	var token entity.CaptchaToken
	if err := tokenDB.Where("token = ?", trimmedToken).First(&token).Error; err != nil {
		return fmt.Errorf("请先完成人机验证")
	}
	if token.Scene != strings.TrimSpace(scene) {
		return fmt.Errorf("人机验证场景不匹配")
	}
	if token.Status != CaptchaTokenStatusActive {
		return fmt.Errorf("人机验证已过期，请重新验证")
	}
	if captchaNow().After(token.ExpiresAt) {
		_ = captchaTokenDB(db).Model(&token).Update("status", CaptchaTokenStatusExpired).Error
		return fmt.Errorf("人机验证已过期，请重新验证")
	}

	now := captchaNow()
	return captchaTokenDB(db).Model(&token).Updates(map[string]any{
		"status":      CaptchaTokenStatusConsumed,
		"consumed_at": &now,
	}).Error
}
