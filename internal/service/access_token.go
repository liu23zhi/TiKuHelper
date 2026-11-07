package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrAccessTokenNameRequired = errors.New("令牌名称不能为空")
var ErrAccessTokenNameInvalid = errors.New("令牌名称只能使用中文、英文、数字、下划线和@，且长度不能超过128个字符")
var ErrAccessTokenQuotaLimitInvalid = errors.New("总额度必须为非负且不超过数据库可存储范围")
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
	ClientIP        string  `json:"-"`
}

type UpdateAccessTokenInput struct {
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Remark          string  `json:"remark"`
	TotalQuotaLimit float64 `json:"totalQuotaLimit"`
	ClientIP        string  `json:"-"`
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
		Where("COALESCE(access_tokens.script_id, 0) = 0").
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
		Where("COALESCE(access_tokens.script_id, 0) = 0").
		Order("access_tokens.id desc").
		Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

func CreateAccessTokenWithDB(db *gorm.DB, operator entity.User, input CreateAccessTokenInput) (CreatedAccessToken, error) {
	if err := validateAccessTokenQuotaLimit(input.TotalQuotaLimit); err != nil {
		return CreatedAccessToken{}, err
	}
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
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&accessToken).Error; err != nil {
			return err
		}
		return writeAccessTokenAudit(tx, operator, accessToken, 0, "创建访问令牌成功", input.ClientIP, nil, accessTokenAuditSnapshot(accessToken), nil)
	}); err != nil {
		return CreatedAccessToken{}, err
	}

	return CreatedAccessToken{
		AccessToken: accessToken,
		Token:       plainToken,
	}, nil
}

func UpdateAccessTokenWithDB(db *gorm.DB, operator entity.User, tokenID int32, input UpdateAccessTokenInput) (entity.AccessToken, error) {
	if err := validateAccessTokenQuotaLimit(input.TotalQuotaLimit); err != nil {
		return entity.AccessToken{}, err
	}
	var accessToken entity.AccessToken
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		accessToken, err = loadAccessibleAccessToken(tx.Clauses(clause.Locking{Strength: "UPDATE"}), operator, tokenID)
		if err != nil {
			return err
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			return ErrAccessTokenNameRequired
		}
		if name != accessToken.Name {
			if err := util.ValidateRestrictedName(name, util.AccessTokenNameMaxLength, "令牌名称"); err != nil {
				return ErrAccessTokenNameInvalid
			}
		}

		before := accessTokenAuditSnapshot(accessToken)
		updates := make(map[string]any)
		changedFields := make([]string, 0, 4)
		if name != accessToken.Name {
			updates["name"] = name
			changedFields = append(changedFields, "name")
		}
		if status := normalizeAccessTokenStatus(input.Status); status != accessToken.Status {
			updates["status"] = status
			changedFields = append(changedFields, "status")
		}
		if remark := strings.TrimSpace(input.Remark); remark != accessToken.Remark {
			updates["remark"] = remark
			changedFields = append(changedFields, "remark")
		}
		if quota := normalizeQuotaLimit(input.TotalQuotaLimit); quota != accessToken.TotalQuotaLimit {
			updates["total_quota_limit"] = quota
			changedFields = append(changedFields, "totalQuotaLimit")
		}
		if len(updates) == 0 {
			return nil
		}
		// Only editable fields are written so usage and credentials cannot be overwritten.
		result := tx.Model(&entity.AccessToken{}).Where("id = ?", accessToken.ID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAccessTokenNotFound
		}
		if err := tx.First(&accessToken, accessToken.ID).Error; err != nil {
			return err
		}
		return writeAccessTokenAudit(tx, operator, accessToken, 1, "更新访问令牌成功", input.ClientIP, before, accessTokenAuditSnapshot(accessToken), changedFields)
	})
	if err != nil {
		return entity.AccessToken{}, err
	}
	return accessToken, nil
}

func DeleteAccessTokenWithDB(db *gorm.DB, operator entity.User, tokenID int32) error {
	return DeleteAccessTokenWithAuditWithDB(db, operator, tokenID, "")
}

func DeleteAccessTokenWithAuditWithDB(db *gorm.DB, operator entity.User, tokenID int32, clientIP string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		accessToken, err := loadAccessibleAccessToken(tx.Clauses(clause.Locking{Strength: "UPDATE"}), operator, tokenID)
		if err != nil {
			return err
		}
		result := tx.Delete(&accessToken)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAccessTokenNotFound
		}
		return writeAccessTokenAudit(tx, operator, accessToken, 2, "删除访问令牌成功", clientIP, accessTokenAuditSnapshot(accessToken), nil, nil)
	})
}

func accessTokenAuditSnapshot(accessToken entity.AccessToken) map[string]any {
	return map[string]any{
		"tokenId": accessToken.ID, "ownerId": accessToken.UserID,
		"name": accessToken.Name, "status": accessToken.Status,
		"totalQuotaLimit": accessToken.TotalQuotaLimit,
	}
}

func writeAccessTokenAudit(tx *gorm.DB, operator entity.User, accessToken entity.AccessToken, action int32, description, clientIP string, before, after map[string]any, changedFields []string) error {
	return WriteAccountAuditWithDB(tx, AccountAuditEvent{
		UserID: operator.ID, Username: operator.Username, Module: "access_token", Action: action,
		Description: description, ClientIP: strings.TrimSpace(clientIP),
		Request: map[string]any{"tokenId": accessToken.ID, "targetUserId": accessToken.UserID, "before": before},
		Result: map[string]any{
			"status": "success", "tokenId": accessToken.ID, "targetUserId": accessToken.UserID,
			"after": after, "changedFields": changedFields,
		},
	})
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

func validateAccessTokenQuotaLimit(limit float64) error {
	if math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 || limit >= entity.AccessTokenQuotaLimitMaxExclusive {
		return ErrAccessTokenQuotaLimitInvalid
	}
	return nil
}

func normalizeAccessTokenValue(token string) string {
	return strings.ToLower(strings.TrimSpace(token))
}

func hashAccessTokenValue(token string) string {
	hashed := sha256.Sum256([]byte(normalizeAccessTokenValue(token)))
	return hex.EncodeToString(hashed[:])
}
