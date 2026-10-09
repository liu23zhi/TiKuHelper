package service

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	WechatLoginStatusPending  = "pending"
	WechatLoginStatusScanned  = "scanned"
	WechatLoginStatusSuccess  = "success"
	WechatLoginStatusNeedBind = "need_bind"
	WechatLoginStatusExpired  = "expired"

	wechatBindTokenTTL = 5 * time.Minute
	maxWechatQRCodeTTL = 180 * time.Second
)

type WechatLoginClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

type WechatQRCodeResult struct {
	SceneID         string `json:"sceneId"`
	LoginToken      string `json:"loginToken"`
	QRCodeImageData string `json:"qrcodeImageData"`
	ExpireSeconds   int    `json:"expireSeconds"`
	ExpiresAtUnix   int64  `json:"expiresAtUnix"`
}

type WechatLoginStatusInput struct {
	Client       WechatLoginClient
	Store        *WechatBindTokenStore
	SessionStore *WechatLoginSessionStore
	LoginToken   string
	RequestIP    string
}

type WechatCodeLoginInput struct {
	Client    WechatLoginClient
	Store     *WechatBindTokenStore
	Code      string
	RequestIP string
}

type WechatCodeBindInput struct {
	Client WechatLoginClient
	Code   string
	UserID int32
}

type WechatScanProbeInput struct {
	Client     WechatLoginClient
	LoginToken string
}

type WechatScanProbeResult struct {
	Status string
}

type WechatScanIDResult struct {
	Status   string `json:"status"`
	WechatID string `json:"wechatId,omitempty"`
	Message  string `json:"message,omitempty"`
}

type wechatUpstreamStatus struct {
	Status   string
	WechatID string
	Message  string
}

type WechatLoginStatusResult struct {
	Status    string `json:"status"`
	JWT       string `json:"jwt,omitempty"`
	BindToken string `json:"bindToken,omitempty"`
	Message   string `json:"message,omitempty"`
}

type WechatBindInput struct {
	Store     *WechatBindTokenStore
	BindToken string
	Username  string
	Password  string
	RequestIP string
}

type WechatBindResult struct {
	JWT string `json:"jwt"`
}

type WechatBindTokenStore struct {
	mu     sync.Mutex
	tokens map[string]wechatBindTokenEntry
}

type WechatLoginSession struct {
	CreatedAt time.Time
	ExpiresAt time.Time
}

type WechatLoginSessionStore struct {
	mu       sync.Mutex
	sessions map[string]WechatLoginSession
}

type wechatBindTokenEntry struct {
	WechatID   string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

var defaultWechatBindTokenStore = NewWechatBindTokenStore()
var DefaultWechatLoginSessionStore = NewWechatLoginSessionStore()

func NewWechatLoginSessionStore() *WechatLoginSessionStore {
	return &WechatLoginSessionStore{sessions: map[string]WechatLoginSession{}}
}

func (s *WechatLoginSessionStore) Create(loginToken string, now time.Time, expiresAt time.Time) {
	if s == nil {
		s = DefaultWechatLoginSessionStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[strings.TrimSpace(loginToken)] = WechatLoginSession{
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
}

func (s *WechatLoginSessionStore) Resolve(loginToken string, now time.Time) (WechatLoginSession, error) {
	if s == nil {
		s = DefaultWechatLoginSessionStore
	}
	token := strings.TrimSpace(loginToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok || token == "" || !now.Before(session.ExpiresAt) {
		return WechatLoginSession{}, fmt.Errorf("微信二维码已过期，请刷新")
	}
	return session, nil
}

func normalizeWechatQRCodeTTL(expireSeconds int) time.Duration {
	ttl := time.Duration(expireSeconds) * time.Second
	if ttl <= 0 || ttl > maxWechatQRCodeTTL {
		return maxWechatQRCodeTTL
	}
	return ttl
}

func NewWechatBindTokenStore() *WechatBindTokenStore {
	return &WechatBindTokenStore{tokens: map[string]wechatBindTokenEntry{}}
}

func (s *WechatBindTokenStore) Create(wechatID string, expiresAt time.Time) string {
	if s == nil {
		s = defaultWechatBindTokenStore
	}
	token := randomTokenHex(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = wechatBindTokenEntry{
		WechatID:  strings.TrimSpace(wechatID),
		ExpiresAt: expiresAt,
	}
	return token
}

func (s *WechatBindTokenStore) Consume(token string, now time.Time) (string, error) {
	wechatID, err := s.Resolve(token, now)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.tokens[strings.TrimSpace(token)]
	consumedAt := now
	entry.ConsumedAt = &consumedAt
	s.tokens[strings.TrimSpace(token)] = entry
	return wechatID, nil
}

func (s *WechatBindTokenStore) Resolve(token string, now time.Time) (string, error) {
	if s == nil {
		s = defaultWechatBindTokenStore
	}
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return "", fmt.Errorf("绑定令牌无效或已过期")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tokens[trimmedToken]
	if !ok || entry.ConsumedAt != nil || now.After(entry.ExpiresAt) || strings.TrimSpace(entry.WechatID) == "" {
		return "", fmt.Errorf("绑定令牌无效或已过期")
	}
	return entry.WechatID, nil
}

func CreateWechatLoginQRCodeWithDB(db *gorm.DB, client WechatLoginClient) (WechatQRCodeResult, error) {
	return CreateWechatLoginQRCodeWithStore(db, client, nil)
}

func CreateWechatLoginQRCodeWithStore(db *gorm.DB, client WechatLoginClient, store *WechatLoginSessionStore) (WechatQRCodeResult, error) {
	_ = db
	httpClient := resolveWechatHTTPClient(client)
	apiURL, err := joinWechatURL(client.BaseURL, "/api/wechat/create_login_qrcode")
	if err != nil {
		return WechatQRCodeResult{}, err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, nil)
	if err != nil {
		return WechatQRCodeResult{}, err
	}
	applyWechatBearer(req, client.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return WechatQRCodeResult{}, fmt.Errorf("微信二维码创建失败")
	}
	defer resp.Body.Close()
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			SceneID       string `json:"scene_id"`
			QRCodeURL     string `json:"qrcode_url"`
			LoginToken    string `json:"login_token"`
			ExpireSeconds int    `json:"expire_seconds"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return WechatQRCodeResult{}, fmt.Errorf("微信二维码响应解析失败")
	}
	if !payload.Success || strings.TrimSpace(payload.Data.LoginToken) == "" || strings.TrimSpace(payload.Data.QRCodeURL) == "" {
		return WechatQRCodeResult{}, fmt.Errorf("%s", defaultWechatMessage(payload.Message, "微信二维码创建失败"))
	}

	imageData, err := downloadWechatQRCodeImage(httpClient, payload.Data.QRCodeURL)
	if err != nil {
		return WechatQRCodeResult{}, err
	}
	now := time.Now()
	ttl := normalizeWechatQRCodeTTL(payload.Data.ExpireSeconds)
	expiresAt := now.Add(ttl)
	loginToken := strings.TrimSpace(payload.Data.LoginToken)
	if store == nil {
		store = DefaultWechatLoginSessionStore
	}
	store.Create(loginToken, now, expiresAt)
	return WechatQRCodeResult{
		SceneID:         strings.TrimSpace(payload.Data.SceneID),
		LoginToken:      loginToken,
		QRCodeImageData: imageData,
		ExpireSeconds:   int(ttl.Seconds()),
		ExpiresAtUnix:   expiresAt.UnixMilli(),
	}, nil
}

func CheckWechatLoginStatusWithDB(db *gorm.DB, input WechatLoginStatusInput) (WechatLoginStatusResult, error) {
	sessionStore := input.SessionStore
	if sessionStore == nil {
		sessionStore = DefaultWechatLoginSessionStore
	}
	if _, err := sessionStore.Resolve(input.LoginToken, time.Now()); err != nil {
		return WechatLoginStatusResult{}, err
	}
	upstream, err := fetchWechatUpstreamStatus(input.Client, input.LoginToken)
	if err != nil {
		return WechatLoginStatusResult{}, err
	}
	switch upstream.Status {
	case WechatLoginStatusPending:
		return WechatLoginStatusResult{Status: WechatLoginStatusPending, Message: upstream.Message}, nil
	case WechatLoginStatusScanned:
		return WechatLoginStatusResult{Status: WechatLoginStatusScanned}, nil
	case WechatLoginStatusSuccess:
		return resolveWechatLoginResultByID(db, input.Store, upstream.WechatID, input.RequestIP)
	default:
		return WechatLoginStatusResult{Status: WechatLoginStatusPending}, nil
	}
}

func ProbeWechatScanStatusWithDB(db *gorm.DB, input WechatScanProbeInput) (WechatScanProbeResult, error) {
	upstream, err := fetchWechatUpstreamStatus(input.Client, input.LoginToken)
	if err != nil {
		return WechatScanProbeResult{}, err
	}
	if upstream.Status == WechatLoginStatusPending || upstream.Status == WechatLoginStatusScanned {
		return WechatScanProbeResult{Status: upstream.Status}, nil
	}
	if upstream.Status != WechatLoginStatusSuccess {
		return WechatScanProbeResult{Status: WechatLoginStatusPending}, nil
	}

	var user entity.User
	err = db.Where("wechat_id = ?", upstream.WechatID).First(&user).Error
	if err == nil {
		if !isServiceUserEnabled(&user) {
			return WechatScanProbeResult{}, fmt.Errorf("用户已被禁用")
		}
		return WechatScanProbeResult{Status: WechatLoginStatusSuccess}, nil
	}
	if err != gorm.ErrRecordNotFound {
		return WechatScanProbeResult{}, err
	}
	return WechatScanProbeResult{Status: WechatLoginStatusNeedBind}, nil
}

func ResolveWechatScanID(client WechatLoginClient, loginToken string) (WechatScanIDResult, error) {
	trimmedToken := strings.TrimSpace(loginToken)
	if trimmedToken == "" {
		return WechatScanIDResult{}, fmt.Errorf("微信登录令牌不能为空")
	}
	upstream, err := fetchWechatUpstreamStatus(client, trimmedToken)
	if err != nil {
		return WechatScanIDResult{}, err
	}
	if upstream.Status == WechatLoginStatusSuccess && strings.TrimSpace(upstream.WechatID) == "" {
		return WechatScanIDResult{}, fmt.Errorf("微信用户 ID 为空")
	}
	return WechatScanIDResult{
		Status:   upstream.Status,
		WechatID: strings.TrimSpace(upstream.WechatID),
		Message:  upstream.Message,
	}, nil
}

func fetchWechatUpstreamStatus(client WechatLoginClient, loginToken string) (wechatUpstreamStatus, error) {
	httpClient := resolveWechatHTTPClient(client)
	apiURL, err := joinWechatURL(client.BaseURL, "/api/wechat/login_status")
	if err != nil {
		return wechatUpstreamStatus{}, err
	}
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return wechatUpstreamStatus{}, err
	}
	query := parsed.Query()
	query.Set("login_token", strings.TrimSpace(loginToken))
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return wechatUpstreamStatus{}, err
	}
	applyWechatBearer(req, client.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return wechatUpstreamStatus{}, fmt.Errorf("微信登录状态查询失败")
	}
	defer resp.Body.Close()
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Status     string `json:"status"`
			WechatUser struct {
				OpenID string `json:"openid"`
			} `json:"wechat_user"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return wechatUpstreamStatus{}, fmt.Errorf("微信登录状态响应解析失败")
	}
	if !payload.Success {
		return wechatUpstreamStatus{Status: WechatLoginStatusPending, Message: strings.TrimSpace(payload.Message)}, nil
	}
	status := normalizeWechatUpstreamStatus(payload.Data.Status)
	wechatID := strings.TrimSpace(payload.Data.WechatUser.OpenID)
	if status == WechatLoginStatusSuccess && wechatID == "" {
		return wechatUpstreamStatus{}, fmt.Errorf("微信用户 ID 为空")
	}
	return wechatUpstreamStatus{
		Status:   status,
		WechatID: wechatID,
		Message:  strings.TrimSpace(payload.Message),
	}, nil
}

func normalizeWechatUpstreamStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case WechatLoginStatusSuccess, "completed", "authorized":
		return WechatLoginStatusSuccess
	case WechatLoginStatusScanned, "scan_success", "waiting_confirm", "confirmed":
		return WechatLoginStatusScanned
	default:
		return WechatLoginStatusPending
	}
}

func LoginWechatByCodeWithDB(db *gorm.DB, input WechatCodeLoginInput) (WechatLoginStatusResult, error) {
	wechatID, err := FetchWechatUserIDByCode(input.Client, input.Code)
	if err != nil {
		return WechatLoginStatusResult{}, err
	}
	if strings.TrimSpace(wechatID) == "" {
		return WechatLoginStatusResult{}, fmt.Errorf("微信验证码无效")
	}
	return resolveWechatLoginResultByID(db, input.Store, wechatID, input.RequestIP)
}

func resolveWechatLoginResultByID(db *gorm.DB, store *WechatBindTokenStore, wechatID string, requestIP string) (WechatLoginStatusResult, error) {
	wechatID = strings.TrimSpace(wechatID)
	if wechatID == "" {
		return WechatLoginStatusResult{}, fmt.Errorf("微信用户 ID 为空")
	}
	var user entity.User
	if err := db.Where("wechat_id = ?", wechatID).First(&user).Error; err == nil {
		if !isServiceUserEnabled(&user) {
			return WechatLoginStatusResult{}, fmt.Errorf("用户已被禁用")
		}
		jwt, err := issueWechatJWTWithDB(db, &user, requestIP)
		if err != nil {
			return WechatLoginStatusResult{}, err
		}
		return WechatLoginStatusResult{Status: WechatLoginStatusSuccess, JWT: jwt}, nil
	} else if err != gorm.ErrRecordNotFound {
		return WechatLoginStatusResult{}, err
	}

	if store == nil {
		store = defaultWechatBindTokenStore
	}
	bindToken := store.Create(wechatID, time.Now().Add(wechatBindTokenTTL))
	return WechatLoginStatusResult{Status: WechatLoginStatusNeedBind, BindToken: bindToken}, nil
}

func BindWechatLoginWithDB(db *gorm.DB, input WechatBindInput) (WechatBindResult, error) {
	store := input.Store
	if store == nil {
		store = defaultWechatBindTokenStore
	}
	now := time.Now()
	wechatID, err := store.Resolve(input.BindToken, now)
	if err != nil {
		return WechatBindResult{}, err
	}

	var user entity.User
	err = db.Where("username = ?", strings.TrimSpace(input.Username)).First(&user).Error
	if err != nil {
		return WechatBindResult{}, fmt.Errorf("用户名或密码错误")
	}
	passwordMatches, needsUpgrade := util.VerifyPassword(user.Password, strings.TrimSpace(input.Password))
	if !passwordMatches {
		return WechatBindResult{}, fmt.Errorf("用户名或密码错误")
	}
	if !isServiceUserEnabled(&user) {
		return WechatBindResult{}, fmt.Errorf("用户已被禁用")
	}
	if needsUpgrade {
		if hashedPassword, hashErr := util.HashPassword(strings.TrimSpace(input.Password)); hashErr == nil {
			if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("password", hashedPassword).Error; err != nil {
				return WechatBindResult{}, err
			}
			user.Password = hashedPassword
		}
	}

	var occupied entity.User
	if err := db.Where("wechat_id = ? AND id <> ?", wechatID, user.ID).First(&occupied).Error; err == nil {
		return WechatBindResult{}, fmt.Errorf("该微信已绑定其他账号")
	} else if err != gorm.ErrRecordNotFound {
		return WechatBindResult{}, err
	}

	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"wechat_id":       wechatID,
		"wechat_bound_at": now,
	}).Error; err != nil {
		return WechatBindResult{}, err
	}
	user.WechatID = &wechatID
	user.WechatBoundAt = &now
	if _, err := store.Consume(input.BindToken, now); err != nil {
		return WechatBindResult{}, err
	}

	jwt, err := issueWechatJWTWithDB(db, &user, input.RequestIP)
	if err != nil {
		return WechatBindResult{}, err
	}
	return WechatBindResult{JWT: jwt}, nil
}

func BindWechatToUserWithDB(db *gorm.DB, store *WechatBindTokenStore, bindToken string, userID int32) (entity.User, error) {
	if store == nil {
		store = defaultWechatBindTokenStore
	}
	now := time.Now()
	wechatID, err := store.Resolve(bindToken, now)
	if err != nil {
		return entity.User{}, err
	}

	user, err := bindWechatIDToUserWithDB(db, wechatID, userID, now)
	if err != nil {
		return entity.User{}, err
	}
	if _, err := store.Consume(bindToken, now); err != nil {
		return entity.User{}, err
	}
	return user, nil
}

func BindWechatCodeToUserWithDB(db *gorm.DB, client WechatLoginClient, code string, userID int32) (entity.User, error) {
	trimmedCode := strings.TrimSpace(code)
	if trimmedCode == "" {
		return entity.User{}, fmt.Errorf("微信验证码不能为空")
	}
	wechatID, err := FetchWechatUserIDByCode(client, trimmedCode)
	if err != nil {
		return entity.User{}, err
	}
	if strings.TrimSpace(wechatID) == "" {
		return entity.User{}, fmt.Errorf("微信验证码无效")
	}
	return bindWechatIDToUserWithDB(db, wechatID, userID, time.Now())
}

func bindWechatIDToUserWithDB(db *gorm.DB, wechatID string, userID int32, now time.Time) (entity.User, error) {
	var user entity.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		return entity.User{}, err
	}
	if !isServiceUserEnabled(&user) {
		return entity.User{}, fmt.Errorf("用户已被禁用")
	}
	if user.WechatID != nil && strings.TrimSpace(*user.WechatID) != "" {
		return entity.User{}, fmt.Errorf("当前账号已绑定微信，请先解绑")
	}

	var occupied entity.User
	if err := db.Where("wechat_id = ? AND id <> ?", wechatID, userID).First(&occupied).Error; err == nil {
		return entity.User{}, fmt.Errorf("该微信已绑定其他账号")
	} else if err != gorm.ErrRecordNotFound {
		return entity.User{}, err
	}

	if err := db.Model(&entity.User{}).Where("id = ?", userID).Updates(map[string]any{
		"wechat_id":       wechatID,
		"wechat_bound_at": now,
	}).Error; err != nil {
		return entity.User{}, err
	}
	user.WechatID = &wechatID
	user.WechatBoundAt = &now
	return user, nil
}

func UnbindWechatFromUserWithDB(db *gorm.DB, userID int32) (entity.User, error) {
	var user entity.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		return entity.User{}, err
	}
	if err := db.Model(&entity.User{}).Where("id = ?", userID).Updates(map[string]any{
		"wechat_id":       nil,
		"wechat_bound_at": nil,
	}).Error; err != nil {
		return entity.User{}, err
	}
	user.WechatID = nil
	user.WechatBoundAt = nil
	return user, nil
}

func FetchWechatUserIDByCode(client WechatLoginClient, code string) (string, error) {
	httpClient := resolveWechatHTTPClient(client)
	apiURL, err := joinWechatURL(client.BaseURL, "/api/wechat/user")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("code", strings.TrimSpace(code))
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	applyWechatBearer(req, client.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("微信用户查询失败")
	}
	defer resp.Body.Close()
	var payload struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("微信用户响应解析失败")
	}
	if !payload.Success {
		return "", fmt.Errorf("%s", defaultWechatMessage(payload.Message, "微信用户查询失败"))
	}
	return strings.TrimSpace(payload.Data), nil
}

func resolveWechatHTTPClient(client WechatLoginClient) *http.Client {
	if client.Client != nil {
		return client.Client
	}
	return util.NewSafeHTTPClient(10 * time.Second)
}

func applyWechatBearer(req *http.Request, token string) {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken != "" {
		req.Header.Set("Authorization", "Bearer "+trimmedToken)
	}
}

func joinWechatURL(baseURL string, path string) (string, error) {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase == "" {
		return "", fmt.Errorf("微信登录配置未完成")
	}
	parsed, err := url.Parse(trimmedBase)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("微信登录配置未完成")
	}
	return trimmedBase + path, nil
}

func downloadWechatQRCodeImage(client *http.Client, imageURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("微信二维码地址无效")
	}
	query := parsed.Query()
	query.Set("_", randomTokenHex(6))
	parsed.RawQuery = query.Encode()
	resp, err := client.Get(parsed.String())
	if err != nil {
		return "", fmt.Errorf("微信二维码下载失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("微信二维码读取失败")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" || strings.HasPrefix(contentType, "application/octet-stream") {
		contentType = "image/png"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

func issueWechatJWTWithDB(db *gorm.DB, user *entity.User, requestIP string) (string, error) {
	jwt := util.GenerateJwtForUser(user.ID, user.Password)
	if jwt == "" {
		return "", fmt.Errorf("jwt生成失败")
	}
	now := time.Now()
	clientIP := strings.TrimSpace(requestIP)
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"last_login_at": now,
		"last_login_ip": clientIP,
	}).Error; err != nil {
		return "", fmt.Errorf("登录信息更新失败")
	}
	user.LastLoginAt = &now
	user.LastLoginIP = clientIP
	return jwt, nil
}

func isServiceUserEnabled(user *entity.User) bool {
	if user == nil {
		return false
	}
	status := strings.TrimSpace(user.Status)
	return status == "" || strings.EqualFold(status, "enabled")
}

func randomTokenHex(byteLength int) string {
	if byteLength <= 0 {
		byteLength = 16
	}
	buf := make([]byte, byteLength)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func defaultWechatMessage(message string, fallback string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
