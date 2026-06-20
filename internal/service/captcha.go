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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
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
	Client     *http.Client
}

var captchaNow = time.Now
var defaultCaptchaCodeGenerator = func() string {
	return randomAlphaNumeric(6)
}
var captchaCodeGenerator = defaultCaptchaCodeGenerator

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
	if !isSupportedCaptchaScene(scene) {
		return CaptchaTokenResult{}, fmt.Errorf("人机验证场景不正确")
	}
	captchaID := strings.TrimSpace(input.CaptchaID)
	if captchaID == "" {
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码 ID 不能为空")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(input.APIBaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultCaptchaAPIBaseURL
	}
	parsed, err := url.Parse(baseURL + "/check2")
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码服务地址不正确")
	}
	query := parsed.Query()
	query.Set("id", captchaID)
	parsed.RawQuery = query.Encode()

	client := input.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码校验失败")
	}
	defer resp.Body.Close()
	var payload struct {
		Code    int    `json:"code"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return CaptchaTokenResult{}, fmt.Errorf("远程验证码响应解析失败")
	}
	if !payload.Success && payload.Code != 200 {
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = "远程验证码校验失败"
		}
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
		return CaptchaTokenResult{}, err
	}
	return CaptchaTokenResult{Token: tokenValue, Scene: token.Scene, ExpiresAt: token.ExpiresAt}, nil
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
