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
	"gorm.io/gorm/clause"
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
	// Keep the initial admission read outside the write transaction: an independent
	// SQLite billing/delete operation may finish before we acquire fresh mutation state.
	initial, err := loadAccessibleAccessToken(db, operator, tokenID)
	if err != nil {
		return entity.AccessToken{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return entity.AccessToken{}, ErrAccessTokenNameRequired
	}
	if name != initial.Name {
		if err := util.ValidateRestrictedName(name, util.AccessTokenNameMaxLength, "令牌名称"); err != nil {
			return entity.AccessToken{}, ErrAccessTokenNameInvalid
		}
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		accessToken, err := loadAccessibleAccessToken(tx.Clauses(clause.Locking{Strength: "UPDATE"}), operator, tokenID)
		if err != nil {
			return err
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
			accessToken.Name = name
		}
		if status := normalizeAccessTokenStatus(input.Status); status != accessToken.Status {
			updates["status"] = status
			changedFields = append(changedFields, "status")
			accessToken.Status = status
		}
		if remark := strings.TrimSpace(input.Remark); remark != accessToken.Remark {
			updates["remark"] = remark
			changedFields = append(changedFields, "remark")
			accessToken.Remark = remark
		}
		if quota := normalizeQuotaLimit(input.TotalQuotaLimit); quota != accessToken.TotalQuotaLimit {
			updates["total_quota_limit"] = quota
			changedFields = append(changedFields, "totalQuotaLimit")
			accessToken.TotalQuotaLimit = quota
		}
		if len(updates) == 0 {
			return nil
		}
		// Write only editable fields with ownership pinned; never Save a stale usage,
		// last-use or credential snapshot over a concurrent billing update.
		result := tx.Model(&entity.AccessToken{}).Where("id = ? AND user_id = ?", accessToken.ID, accessToken.UserID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&entity.AccessToken{}).Where("id = ? AND user_id = ?", accessToken.ID, accessToken.UserID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrAccessTokenNotFound
			}
		}
		return writeAccessTokenAudit(tx, operator, accessToken, 1, "更新访问令牌成功", input.ClientIP, before, accessTokenAuditSnapshot(accessToken), changedFields)
	})
	if err != nil {
		return entity.AccessToken{}, err
	}
	// Readback is deliberately after commit, including no-op edits. A read error
	// must not pretend to roll back metadata+audit that already committed.
	return loadAccessibleAccessToken(db, operator, tokenID)
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

func normalizeAccessTokenValue(token string) string {
	return strings.ToLower(strings.TrimSpace(token))
}

func hashAccessTokenValue(token string) string {
	hashed := sha256.Sum256([]byte(normalizeAccessTokenValue(token)))
	return hex.EncodeToString(hashed[:])
}
