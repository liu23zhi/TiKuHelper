package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScriptReservationFee         = 10.0
	ScriptExchangeTokenTTL       = 5 * time.Minute
	ScriptVersionMaxBytes  int64 = 64 << 20
)

var (
	ErrScriptForbidden           = errors.New("无权限操作脚本")
	ErrScriptNotFound            = errors.New("脚本不存在")
	ErrScriptVersionNotFound     = errors.New("脚本版本不存在")
	ErrScriptNameRequired        = errors.New("脚本名称不能为空")
	ErrScriptDomainRequired      = errors.New("脚本域名不能为空")
	ErrScriptVersionRequired     = errors.New("脚本版本不能为空")
	ErrScriptVersionInvalid      = errors.New("脚本版本无效")
	ErrScriptBalanceInsufficient = errors.New("余额不足，创建脚本需要预扣 ¥10")
	ErrScriptTokenInvalid        = errors.New("脚本兑换令牌无效或已过期")
	ErrScriptTokenRequired       = errors.New("脚本兑换令牌不能为空")
)

type CreateUserScriptInput struct {
	Name                string         `json:"name"`
	Instruction         string         `json:"instruction"`
	Command             string         `json:"command"`
	VersionID           int32          `json:"versionId"`
	Parameters          map[string]any `json:"parameters"`
	Description         string         `json:"description"`
	BaseDomain          string         `json:"baseDomain"`
	Scheme              string         `json:"scheme"`
	UseWildcardDomain   *bool          `json:"useWildcardDomain"`
	Enabled             *bool          `json:"enabled"`
	BillingEnabled      *bool          `json:"billingEnabled"`
	UseTikuPrice        *bool          `json:"useTikuPrice"`
	BillingUseTikuPrice *bool          `json:"billingUseTikuPrice"`
	PresetParams        map[string]any `json:"presetParams"`
	AnswerPrice         float64        `json:"answerPrice"`
}

type UpdateUserScriptInput struct {
	Name                string         `json:"name"`
	Instruction         string         `json:"instruction"`
	Command             string         `json:"command"`
	Parameters          map[string]any `json:"parameters"`
	PresetParams        map[string]any `json:"presetParams"`
	Description         string         `json:"description"`
	BaseDomain          string         `json:"baseDomain"`
	Scheme              string         `json:"scheme"`
	UseWildcardDomain   *bool          `json:"useWildcardDomain"`
	Enabled             *bool          `json:"enabled"`
	BillingEnabled      *bool          `json:"billingEnabled"`
	UseTikuPrice        *bool          `json:"useTikuPrice"`
	BillingUseTikuPrice *bool          `json:"billingUseTikuPrice"`
	AnswerPrice         *float64       `json:"answerPrice"`
}

type ScriptListItem struct {
	entity.UserScript
	Username      string `json:"username,omitempty"`
	Version       string `json:"version"`
	VersionSHA256 string `json:"versionSha256"`
	Command       string `json:"command"`
	DownloadURL   string `json:"downloadUrl"`
}

type ScriptExchangeGrant struct {
	ShortCode string    `json:"shortCode"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ScriptExchangeResult struct {
	ScriptID     int32          `json:"scriptId"`
	Name         string         `json:"name"`
	Instruction  string         `json:"instruction"`
	Parameters   map[string]any `json:"parameters"`
	Version      string         `json:"version"`
	Filename     string         `json:"filename"`
	DownloadURL  string         `json:"downloadUrl"`
	AccessKey    string         `json:"key"`
	CommandURL   string         `json:"commandUrl"`
	BootstrapURL string         `json:"bootstrapUrl"`
}

func ListScriptVersionsWithDB(db *gorm.DB) ([]entity.ScriptVersion, error) {
	var versions []entity.ScriptVersion
	err := db.Session(&gorm.Session{NewDB: true}).Select("id", "version", "filename", "sha256", "size_bytes", "is_current", "created_by", "created_at").Order("id desc").Find(&versions).Error
	return versions, err
}

func UploadScriptVersionWithDB(db *gorm.DB, admin entity.User, version, filename string, data []byte) (entity.ScriptVersion, error) {
	if !isAdminUser(admin) {
		return entity.ScriptVersion{}, ErrScriptForbidden
	}
	version = strings.TrimSpace(version)
	filename = strings.TrimSpace(filename)
	if version == "" {
		return entity.ScriptVersion{}, ErrScriptVersionRequired
	}
	if len(version) > 64 || strings.ContainsAny(version, "\\/\x00") {
		return entity.ScriptVersion{}, ErrScriptVersionInvalid
	}
	if !strings.EqualFold(filepathExt(filename), ".exe") || len(data) < 2 || data[0] != 'M' || data[1] != 'Z' || int64(len(data)) > ScriptVersionMaxBytes {
		return entity.ScriptVersion{}, ErrScriptVersionInvalid
	}
	sum := sha256.Sum256(data)
	result := entity.ScriptVersion{Version: version, Filename: safeScriptFilename(filename), SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), Data: append([]byte(nil), data...), IsCurrent: true, CreatedBy: admin.ID}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.ScriptVersion{}).Where("is_current = ?", true).Update("is_current", false).Error; err != nil {
			return err
		}
		return tx.Create(&result).Error
	})
	return result, err
}

func CreateUserScriptWithDB(db *gorm.DB, operator entity.User, input CreateUserScriptInput) (ScriptListItem, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ScriptListItem{}, ErrScriptNameRequired
	}
	baseDomain := normalizeScriptDomain(input.BaseDomain)
	if baseDomain == "" {
		return ScriptListItem{}, ErrScriptDomainRequired
	}
	if input.VersionID <= 0 {
		return ScriptListItem{}, ErrScriptVersionNotFound
	}
	if math.IsNaN(input.AnswerPrice) || math.IsInf(input.AnswerPrice, 0) || input.AnswerPrice < 0 {
		return ScriptListItem{}, errors.New("脚本答题单价必须是大于或等于 0 的有效金额")
	}
	if input.AnswerPrice >= 1_000_000_000_000 {
		return ScriptListItem{}, errors.New("脚本答题单价超出可存储范围")
	}
	parameterValues := input.Parameters
	if parameterValues == nil {
		parameterValues = input.PresetParams
	}
	if parameterValues == nil {
		parameterValues = map[string]any{}
	}
	parameters, err := json.Marshal(parameterValues)
	if err != nil {
		return ScriptListItem{}, err
	}
	var result ScriptListItem
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, operator.ID).Error; err != nil {
			return err
		}
		var version entity.ScriptVersion
		if err := tx.First(&version, input.VersionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptVersionNotFound
			}
			return err
		}
		before := util.RoundMoney(user.Balance)
		if before < ScriptReservationFee {
			return ErrScriptBalanceInsufficient
		}
		after := util.RoundMoney(before - ScriptReservationFee)
		updated := tx.Model(&entity.User{}).Where("id = ? AND balance = ?", user.ID, user.Balance).Update("balance", after)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("脚本预扣余额已变化，请重试")
		}
		key, err := randomScriptSecret(32)
		if err != nil {
			return err
		}
		shortCode, err := uniqueScriptShortCode(tx)
		if err != nil {
			return err
		}
		useWildcard := true
		if input.UseWildcardDomain != nil {
			useWildcard = *input.UseWildcardDomain
		}
		script := entity.UserScript{UserID: user.ID, VersionID: version.ID, Name: name, Parameters: string(parameters), Description: strings.TrimSpace(input.Description), BaseDomain: baseDomain, Scheme: normalizeScriptScheme(input.Scheme), UseWildcardDomain: useWildcard, Enabled: true, BillingEnabled: true, UseTikuPrice: true, AnswerPrice: util.RoundMoney(input.AnswerPrice), ShortCode: shortCode, AccessKey: key, ReservedFee: ScriptReservationFee}
		if input.Enabled != nil {
			script.Enabled = *input.Enabled
		}
		if input.BillingEnabled != nil {
			script.BillingEnabled = *input.BillingEnabled
		}
		if useTikuPrice := input.UseTikuPrice; useTikuPrice != nil {
			script.UseTikuPrice = *useTikuPrice
		} else if input.BillingUseTikuPrice != nil {
			script.UseTikuPrice = *input.BillingUseTikuPrice
		}
		script.Instruction = BuildScriptCommand(script)
		if err := tx.Create(&script).Error; err != nil {
			return err
		}
		plainToken := "tk_" + key
		tokenPrefix := plainToken
		if len(tokenPrefix) > 12 {
			tokenPrefix = tokenPrefix[:12]
		}
		accessToken := entity.AccessToken{UserID: user.ID, ScriptID: script.ID, Name: "script:" + fmt.Sprint(script.ID), TokenHash: hashScriptSecret(plainToken), TokenPrefix: tokenPrefix, TokenValue: plainToken, Status: "enabled", Remark: "脚本专用令牌"}
		if err := tx.Create(&accessToken).Error; err != nil {
			return err
		}
		if err := tx.Model(&script).Update("access_token_id", accessToken.ID).Error; err != nil {
			return err
		}
		script.AccessTokenID = accessToken.ID
		ledger := entity.BillingRecord{UserID: user.ID, Type: "script_reservation", Provider: "script", Amount: ScriptReservationFee, BalanceBefore: before, BalanceAfter: after, RequestID: fmt.Sprintf("script_create_%d", script.ID), Remark: "创建脚本预扣费用"}
		if err := tx.Create(&ledger).Error; err != nil {
			return err
		}
		result = decorateScriptItem(ScriptListItem{UserScript: script, Username: user.Username, Version: version.Version, VersionSHA256: version.SHA256})
		return nil
	})
	return result, err
}

func ListUserScriptsWithDB(db *gorm.DB, operator entity.User, all bool) ([]ScriptListItem, error) {
	query := db.Session(&gorm.Session{NewDB: true}).Table(entity.TableNameUserScript + " AS user_scripts").
		Select("user_scripts.*, users.username AS username, script_versions.version AS version, script_versions.sha256 AS version_sha256").
		Joins("LEFT JOIN users ON users.id = user_scripts.user_id").
		Joins("LEFT JOIN script_versions ON script_versions.id = user_scripts.version_id").
		Where("user_scripts.deleted_at IS NULL").Order("user_scripts.id desc")
	if !(all && isAdminUser(operator)) {
		query = query.Where("user_scripts.user_id = ?", operator.ID)
	}
	var result []ScriptListItem
	if err := query.Find(&result).Error; err != nil {
		return nil, err
	}
	for i := range result {
		result[i] = decorateScriptItem(result[i])
	}
	return result, nil
}

func UpdateUserScriptWithDB(db *gorm.DB, operator entity.User, scriptID int32, input UpdateUserScriptInput) (ScriptListItem, error) {
	var item ScriptListItem
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", scriptID)
		if !isAdminUser(operator) {
			query = query.Where("user_id = ?", operator.ID)
		}
		if err := query.First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		baseDomain := normalizeScriptDomain(input.BaseDomain)
		if baseDomain == "" {
			return ErrScriptDomainRequired
		}
		scheme := normalizeScriptScheme(input.Scheme)
		updates := map[string]any{"description": strings.TrimSpace(input.Description), "base_domain": baseDomain, "scheme": scheme}
		if value := strings.TrimSpace(input.Name); value != "" {
			updates["name"] = value
		}
		parameterValues := input.Parameters
		if parameterValues == nil {
			parameterValues = input.PresetParams
		}
		if parameterValues != nil {
			raw, err := json.Marshal(parameterValues)
			if err != nil {
				return err
			}
			updates["parameters"] = string(raw)
		}
		if input.UseWildcardDomain != nil {
			updates["use_wildcard_domain"] = *input.UseWildcardDomain
		}
		if input.Enabled != nil {
			updates["enabled"] = *input.Enabled
		}
		if input.BillingEnabled != nil {
			updates["billing_enabled"] = *input.BillingEnabled
		}
		if useTikuPrice := input.UseTikuPrice; useTikuPrice != nil {
			updates["use_tiku_price"] = *useTikuPrice
		} else if input.BillingUseTikuPrice != nil {
			updates["use_tiku_price"] = *input.BillingUseTikuPrice
		}
		if input.AnswerPrice != nil {
			if math.IsNaN(*input.AnswerPrice) || math.IsInf(*input.AnswerPrice, 0) || *input.AnswerPrice < 0 || *input.AnswerPrice >= 1_000_000_000_000 {
				return errors.New("脚本答题单价必须为有效的非负金额")
			}
			updates["answer_price"] = util.RoundMoney(*input.AnswerPrice)
		}
		updatedScript := script
		updatedScript.BaseDomain = baseDomain
		updatedScript.Scheme = scheme
		if input.UseWildcardDomain != nil {
			updatedScript.UseWildcardDomain = *input.UseWildcardDomain
		}
		updates["instruction"] = BuildScriptCommand(updatedScript)
		if err := tx.Model(&script).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Table(entity.TableNameUserScript+" AS user_scripts").Select("user_scripts.*, users.username AS username, script_versions.version AS version, script_versions.sha256 AS version_sha256").Joins("LEFT JOIN users ON users.id = user_scripts.user_id").Joins("LEFT JOIN script_versions ON script_versions.id = user_scripts.version_id").Where("user_scripts.id = ?", script.ID).First(&item).Error; err != nil {
			return err
		}
		item = decorateScriptItem(item)
		return nil
	})
	return item, err
}

func DeleteUserScriptWithDB(db *gorm.DB, operator entity.User, scriptID int32) error {
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", scriptID)
		if !isAdminUser(operator) {
			query = query.Where("user_id = ?", operator.ID)
		}
		if err := query.First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		if script.DeletedAt != nil {
			return nil
		}
		now := time.Now()
		if err := tx.Model(&script).Updates(map[string]any{"deleted_at": now}).Error; err != nil {
			return err
		}
		if script.AccessTokenID > 0 {
			if err := tx.Model(&entity.AccessToken{}).Where("id = ?", script.AccessTokenID).Update("status", "disabled").Error; err != nil {
				return err
			}
		}
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, script.UserID).Error; err != nil {
			return err
		}
		before := util.RoundMoney(user.Balance)
		after := util.RoundMoney(before + script.ReservedFee)
		if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", after).Error; err != nil {
			return err
		}
		return tx.Create(&entity.BillingRecord{UserID: user.ID, Type: "script_refund", Provider: "script", Amount: script.ReservedFee, BalanceBefore: before, BalanceAfter: after, RequestID: fmt.Sprintf("script_delete_%d", script.ID), Remark: "删除脚本释放预扣费用"}).Error
	})
}

func IssueScriptExchangeTokenWithDB(db *gorm.DB, operator entity.User, scriptID int32, ttl time.Duration) (ScriptExchangeGrant, error) {
	if ttl <= 0 || ttl > ScriptExchangeTokenTTL {
		ttl = ScriptExchangeTokenTTL
	}
	var script entity.UserScript
	query := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND deleted_at IS NULL", scriptID)
	if !isAdminUser(operator) {
		query = query.Where("user_id = ?", operator.ID)
	}
	if err := query.First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ScriptExchangeGrant{}, ErrScriptNotFound
		}
		return ScriptExchangeGrant{}, err
	}
	token, err := randomScriptSecret(24)
	if err != nil {
		return ScriptExchangeGrant{}, err
	}
	expires := time.Now().Add(ttl)
	if err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := deleteExpiredScriptExchangeTokens(tx, script.ID); err != nil {
			return err
		}
		return tx.Create(&entity.ScriptExchangeToken{ScriptID: script.ID, TokenHash: hashScriptSecret(token), ExpiresAt: expires}).Error
	}); err != nil {
		return ScriptExchangeGrant{}, err
	}
	return ScriptExchangeGrant{ShortCode: script.ShortCode, Token: token, ExpiresAt: expires}, nil
}

func ExchangeScriptTokenWithDB(db *gorm.DB, shortCode, token string) (ScriptExchangeResult, error) {
	shortCode, token = strings.TrimSpace(shortCode), strings.TrimSpace(token)
	if shortCode == "" || token == "" {
		return ScriptExchangeResult{}, ErrScriptTokenRequired
	}
	var result ScriptExchangeResult
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var grant entity.ScriptExchangeToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ?", hashScriptSecret(token)).First(&grant).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if grant.UsedAt != nil || time.Now().After(grant.ExpiresAt) {
			return ErrScriptTokenInvalid
		}
		var script entity.UserScript
		if err := tx.Where("id = ? AND short_code = ? AND deleted_at IS NULL AND enabled = ?", grant.ScriptID, shortCode, true).First(&script).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		var version entity.ScriptVersion
		// The exchange response only needs release metadata. Avoid loading a
		// potentially 64 MiB executable into memory before the actual download.
		if err := tx.Select("id", "version", "filename").First(&version, script.VersionID).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		now := time.Now()
		if err := tx.Model(&grant).Update("used_at", now).Error; err != nil {
			return err
		}
		var parameters map[string]any
		if strings.TrimSpace(script.Parameters) != "" && json.Unmarshal([]byte(script.Parameters), &parameters) != nil {
			return errors.New("脚本参数损坏")
		}
		accessKey := script.AccessKey
		if script.AccessTokenID > 0 {
			var accessToken entity.AccessToken
			if err := tx.Where("id = ? AND script_id = ? AND status = ?", script.AccessTokenID, script.ID, "enabled").First(&accessToken).Error; err != nil {
				return ErrScriptTokenInvalid
			}
			accessKey = accessToken.TokenValue
		}
		if parameters == nil {
			parameters = map[string]any{}
		}
		result = ScriptExchangeResult{ScriptID: script.ID, Name: script.Name, Instruction: script.Instruction, Parameters: parameters, Version: version.Version, Filename: version.Filename, DownloadURL: "/adapter-service/script-command/" + script.ShortCode + "/file", AccessKey: accessKey, CommandURL: BuildScriptCommandURL(script), BootstrapURL: "/adapter-service/script-command/" + script.ShortCode + "/bootstrap"}
		return nil
	})
	return result, err
}

func normalizeScriptDomain(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return ""
	}
	host := strings.TrimRight(strings.TrimSpace(parsed.Host), ".")
	host = strings.TrimPrefix(strings.ToLower(host), "*.")
	if host == "" || strings.ContainsAny(host, "'\"\\\r\n/?# \t") {
		return ""
	}
	return host
}
func normalizeScriptScheme(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "http" && value != "https" {
		return "https"
	}
	return value
}

// BuildScriptCommandURL returns the shortest public URL for a script.
// Wildcard mode uses a short host; path mode keeps the host unchanged and
// appends the short code as the only path segment.
func BuildScriptCommandURL(script entity.UserScript) string {
	domain := normalizeScriptDomain(script.BaseDomain)
	if domain == "" {
		return "/adapter-service/script-command/" + url.PathEscape(strings.TrimSpace(script.ShortCode)) + "/bootstrap"
	}
	shortCode := strings.TrimSpace(script.ShortCode)
	if shortCode == "" {
		return ""
	}
	scheme := normalizeScriptScheme(script.Scheme)
	host := shortCode + "." + domain
	if script.UseWildcardDomain {
		return scheme + "://" + host
	}
	return scheme + "://" + host + "/" + shortCode
}

// BuildScriptCommand emits a PowerShell 2 compatible one-liner. The URL is
// deliberately the only value in the command; the exchange endpoint injects
// the user's saved settings and short-lived access key later.
func BuildScriptCommand(script entity.UserScript) string {
	commandURL := BuildScriptCommandURL(script)
	if commandURL == "" {
		return ""
	}
	commandURL = strings.ReplaceAll(commandURL, "'", "''")
	return `powershell -c "iex (New-Object Net.WebClient).DownloadString('` + commandURL + `')"`
}

func decorateScriptItem(item ScriptListItem) ScriptListItem {
	item.Command = BuildScriptCommand(item.UserScript)
	item.DownloadURL = BuildScriptCommandURL(item.UserScript)
	item.Instruction = item.Command
	return item
}

// ResolvePublicScriptWithDB maps a request host/path to an enabled script.
// It intentionally compares normalized values in Go so wildcard and regular
// domain behavior remains identical on SQLite and MySQL.
func ResolvePublicScriptWithDB(db *gorm.DB, requestHost, requestPath string) (entity.UserScript, error) {
	host := normalizePublicScriptHost(requestHost)
	path := strings.TrimSpace(requestPath)
	if path == "/" {
		path = ""
	}
	shortCode, wildcard := "", false
	if path == "" {
		separator := strings.IndexByte(host, '.')
		if separator <= 0 || separator == len(host)-1 {
			return entity.UserScript{}, ErrScriptNotFound
		}
		shortCode, wildcard = host[:separator], true
	} else {
		if !strings.HasPrefix(path, "/") || strings.Count(path, "/") != 1 {
			return entity.UserScript{}, ErrScriptNotFound
		}
		shortCode = strings.TrimPrefix(path, "/")
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND use_wildcard_domain = ? AND deleted_at IS NULL AND enabled = ?", shortCode, wildcard, true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.UserScript{}, ErrScriptNotFound
		}
		return entity.UserScript{}, err
	}
	domain := normalizeScriptDomain(script.BaseDomain)
	scriptHost := strings.ToLower(script.ShortCode) + "." + domain
	if domain == "" || !strings.EqualFold(host, scriptHost) {
		return entity.UserScript{}, ErrScriptNotFound
	}
	return script, nil
}

func normalizePublicScriptHost(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else {
		value = strings.TrimPrefix(value, "[")
		value = strings.TrimSuffix(value, "]")
	}
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), ".")
}

// IssuePublicScriptExchangeTokenWithDB is used by the short public launcher
// entrypoint. It never returns a long-term access key and caps the token TTL.
func IssuePublicScriptExchangeTokenWithDB(db *gorm.DB, shortCode string, ttl time.Duration) (ScriptExchangeGrant, error) {
	if ttl <= 0 || ttl > ScriptExchangeTokenTTL {
		ttl = ScriptExchangeTokenTTL
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND deleted_at IS NULL AND enabled = ?", strings.TrimSpace(shortCode), true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ScriptExchangeGrant{}, ErrScriptNotFound
		}
		return ScriptExchangeGrant{}, err
	}
	token, err := randomScriptSecret(24)
	if err != nil {
		return ScriptExchangeGrant{}, err
	}
	expires := time.Now().Add(ttl)
	if err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := deleteExpiredScriptExchangeTokens(tx, script.ID); err != nil {
			return err
		}
		return tx.Create(&entity.ScriptExchangeToken{ScriptID: script.ID, TokenHash: hashScriptSecret(token), ExpiresAt: expires}).Error
	}); err != nil {
		return ScriptExchangeGrant{}, err
	}
	return ScriptExchangeGrant{ShortCode: script.ShortCode, Token: token, ExpiresAt: expires}, nil
}

func deleteExpiredScriptExchangeTokens(tx *gorm.DB, scriptID int32) error {
	return tx.Where("script_id = ? AND (used_at IS NOT NULL OR expires_at < ?)", scriptID, time.Now()).Delete(&entity.ScriptExchangeToken{}).Error
}

func DownloadScriptVersionWithDB(db *gorm.DB, id int32) (entity.ScriptVersion, error) {
	var version entity.ScriptVersion
	err := db.Session(&gorm.Session{NewDB: true}).First(&version, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.ScriptVersion{}, ErrScriptVersionNotFound
	}
	return version, err
}

// DownloadPublicScriptVersionWithDB validates the dedicated script key before
// returning the executable bound to the active script version.
func DownloadPublicScriptVersionWithDB(db *gorm.DB, shortCode, accessKey string) (entity.ScriptVersion, error) {
	shortCode = strings.TrimSpace(shortCode)
	accessKey = strings.TrimSpace(accessKey)
	if shortCode == "" || accessKey == "" {
		return entity.ScriptVersion{}, ErrScriptTokenRequired
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND deleted_at IS NULL AND enabled = ?", shortCode, true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ScriptVersion{}, ErrScriptNotFound
		}
		return entity.ScriptVersion{}, err
	}
	if script.AccessTokenID <= 0 {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	var accessToken entity.AccessToken
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND script_id = ? AND status = ? AND token_hash = ?", script.AccessTokenID, script.ID, "enabled", hashScriptSecret(accessKey)).First(&accessToken).Error; err != nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	return DownloadScriptVersionWithDB(db, script.VersionID)
}

func randomScriptSecret(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashScriptSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func uniqueScriptShortCode(tx *gorm.DB) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for length, total := 1, 36; length <= 3; length, total = length+1, total*36 {
		for index := 0; index < total; index++ {
			code := shortCodeFromIndex(index, length, alphabet)
			var count int64
			if err := tx.Model(&entity.UserScript{}).Where("short_code = ?", code).Count(&count).Error; err != nil {
				return "", err
			}
			if count == 0 {
				return code, nil
			}
		}
	}
	for i := 0; i < 8; i++ {
		code, err := randomScriptSecret(6)
		if err != nil {
			return "", err
		}
		code = code[:12]
		var count int64
		if err := tx.Model(&entity.UserScript{}).Where("short_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", errors.New("生成脚本短地址失败")
}

func shortCodeFromIndex(index, length int, alphabet string) string {
	result := make([]byte, length)
	for position := length - 1; position >= 0; position-- {
		result[position] = alphabet[index%len(alphabet)]
		index /= len(alphabet)
	}
	return string(result)
}

func filepathExt(name string) string {
	name = strings.TrimSpace(name)
	idx := strings.LastIndexByte(name, '.')
	if idx < 0 {
		return ""
	}
	return name[idx:]
}

func safeScriptFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "script.exe"
	}
	return name
}
