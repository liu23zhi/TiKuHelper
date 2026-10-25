package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const AccessTokenQuotaExceededCode = "ACCESS_TOKEN_QUOTA_EXCEEDED"

var ErrAccessTokenNameRequired = errors.New("令牌名称不能为空")
var ErrAccessTokenNameInvalid = errors.New("令牌名称只能使用中文、英文、数字、下划线和@，且长度不能超过128个字符")
var ErrAccessTokenNotFound = errors.New("访问令牌不存在")
var ErrAccessTokenForbidden = errors.New("无权限操作访问令牌")

const (
	accessTokenStatusEnabled  = "enabled"
	accessTokenStatusDisabled = "disabled"
)

type CreateAccessTokenInput struct {
	Name            string  `json:"name"`
	Remark          string  `json:"remark"`
	TotalQuotaLimit float64 `json:"totalQuotaLimit"`
}

type UpdateAccessTokenInput struct {
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Remark          string  `json:"remark"`
	TotalQuotaLimit float64 `json:"totalQuotaLimit"`
}

type CreatedAccessToken struct {
	AccessToken entity.AccessToken `json:"accessToken"`
	Token       string             `json:"token"`
}

type AccessTokenListItem struct {
	entity.AccessToken
	Username string `json:"username,omitempty"`
}

func ListAccessTokensWithDB(db *gorm.DB, operator entity.User) ([]AccessTokenListItem, error) {
	query := db.Table(entity.TableNameAccessToken+" AS access_tokens").
		Where("access_tokens.user_id = ?", operator.ID).
		Order("access_tokens.id desc")

	var tokens []AccessTokenListItem
	if err := query.Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

func ListAllAccessTokensWithDB(db *gorm.DB, operator entity.User) ([]AccessTokenListItem, error) {
	if !isAdminUser(operator) {
		return ListAccessTokensWithDB(db, operator)
	}

	var tokens []AccessTokenListItem
	if err := db.
		Table(entity.TableNameAccessToken + " AS access_tokens").
		Select("access_tokens.*, users.username AS username").
		Joins("LEFT JOIN " + entity.TableNameUser + " AS users ON users.id = access_tokens.user_id").
		Order("access_tokens.id desc").
		Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

func CreateAccessTokenWithDB(db *gorm.DB, operator entity.User, input CreateAccessTokenInput) (CreatedAccessToken, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreatedAccessToken{}, ErrAccessTokenNameRequired
	}
	if err := util.ValidateRestrictedName(name, util.AccessTokenNameMaxLength, "令牌名称"); err != nil {
		return CreatedAccessToken{}, ErrAccessTokenNameInvalid
	}

	plainToken := "tk_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	tokenHash := hashAccessTokenValue(plainToken)
	tokenPrefix := plainToken
	if len(tokenPrefix) > 12 {
		tokenPrefix = tokenPrefix[:12]
	}

	accessToken := entity.AccessToken{
		UserID:          operator.ID,
		Name:            name,
		TokenHash:       tokenHash,
		TokenPrefix:     tokenPrefix,
		TokenValue:      plainToken,
		Status:          accessTokenStatusEnabled,
		TotalQuotaLimit: normalizeQuotaLimit(input.TotalQuotaLimit),
		TotalAmountUsed: 0,
		Remark:          strings.TrimSpace(input.Remark),
	}
	if err := db.Create(&accessToken).Error; err != nil {
		return CreatedAccessToken{}, err
	}

	return CreatedAccessToken{
		AccessToken: accessToken,
		Token:       plainToken,
	}, nil
}

func UpdateAccessTokenWithDB(db *gorm.DB, operator entity.User, tokenID int32, input UpdateAccessTokenInput) (entity.AccessToken, error) {
	accessToken, err := loadAccessibleAccessToken(db, operator, tokenID)
	if err != nil {
		return entity.AccessToken{}, err
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		return entity.AccessToken{}, ErrAccessTokenNameRequired
	}
	if name != accessToken.Name {
		if err := util.ValidateRestrictedName(name, util.AccessTokenNameMaxLength, "令牌名称"); err != nil {
			return entity.AccessToken{}, ErrAccessTokenNameInvalid
		}
	}

	accessToken.Name = name
	accessToken.Status = normalizeAccessTokenStatus(input.Status)
	accessToken.Remark = strings.TrimSpace(input.Remark)
	accessToken.TotalQuotaLimit = normalizeQuotaLimit(input.TotalQuotaLimit)

	// Metadata edits must never write a stale usage/last-use snapshot over billing.
	updated := db.Model(&entity.AccessToken{}).Where("id = ? AND user_id = ?", accessToken.ID, accessToken.UserID).Updates(map[string]any{
		"name": accessToken.Name, "status": accessToken.Status,
		"remark": accessToken.Remark, "total_quota_limit": accessToken.TotalQuotaLimit,
	})
	if updated.Error != nil {
		return entity.AccessToken{}, updated.Error
	}
	if err := db.Where("id = ?", accessToken.ID).First(&accessToken).Error; err != nil {
		return entity.AccessToken{}, err
	}
	return accessToken, nil
}

func DeleteAccessTokenWithDB(db *gorm.DB, operator entity.User, tokenID int32) error {
	accessToken, err := loadAccessibleAccessToken(db, operator, tokenID)
	if err != nil {
		return err
	}
	return db.Delete(&accessToken).Error
}

func loadAccessibleAccessToken(db *gorm.DB, operator entity.User, tokenID int32) (entity.AccessToken, error) {
	var accessToken entity.AccessToken
	if err := db.Where("id = ?", tokenID).First(&accessToken).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AccessToken{}, ErrAccessTokenNotFound
		}
		return entity.AccessToken{}, err
	}
	if accessToken.UserID != operator.ID && !isAdminUser(operator) {
		return entity.AccessToken{}, ErrAccessTokenForbidden
	}
	return accessToken, nil
}

func isAdminUser(user entity.User) bool {
	return strings.EqualFold(strings.TrimSpace(user.Role), "admin")
}

func normalizeAccessTokenStatus(status string) string {
	if strings.EqualFold(strings.TrimSpace(status), accessTokenStatusDisabled) {
		return accessTokenStatusDisabled
	}
	return accessTokenStatusEnabled
}

func normalizeQuotaLimit(limit float64) float64 {
	if limit < 0 {
		return 0
	}
	return limit
}

func normalizeAccessTokenValue(token string) string {
	return strings.ToLower(strings.TrimSpace(token))
}

func hashAccessTokenValue(token string) string {
	hashed := sha256.Sum256([]byte(normalizeAccessTokenValue(token)))
	return hex.EncodeToString(hashed[:])
}
